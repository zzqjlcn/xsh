package webui

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
	"github.com/zzqjlcn/xsh/internal/sshclient"
)

type terminalMessage struct {
	Type        string `json:"type"`
	Host        string `json:"host,omitempty"`
	Password    string `json:"password,omitempty"`
	Data        string `json:"data,omitempty"`
	Message     string `json:"message,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	KeyType     string `json:"key_type,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	Trust       bool   `json:"trust,omitempty"`
}

type socketWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *socketWriter) send(message terminalMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(message)
}

func (w *socketWriter) Write(data []byte) (int, error) {
	if err := w.send(terminalMessage{Type: "output", Data: string(data)}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *server) terminal(w http.ResponseWriter, r *http.Request) {
	if subtleToken(r.URL.Query().Get("token"), s.token) == false {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		return request.Header.Get("Origin") == "http://"+request.Host
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var start terminalMessage
	if err := conn.ReadJSON(&start); err != nil || start.Type != "connect" || start.Host == "" {
		_ = conn.WriteJSON(terminalMessage{Type: "error", Message: "invalid connection request"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	writer := &socketWriter{conn: conn}
	store, err := config.Load()
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	host := store.Find(start.Host)
	if host == nil {
		writer.send(terminalMessage{Type: "error", Message: "host not found"})
		return
	}
	password := start.Password
	if password == "" && host.SavePassword {
		password, err = credential.GetPassword(host.Name, nil)
		if err != nil {
			writer.send(terminalMessage{Type: "error", Message: err.Error()})
			return
		}
	}
	resolver := func(name string) (*config.SSHHost, bool) {
		found := store.Find(name)
		if found == nil {
			return nil, false
		}
		copy := *found
		return &copy, true
	}
	passwordResolver := func(name string) (string, error) {
		jump, ok := resolver(name)
		if !ok {
			return "", fmt.Errorf("host %q not found", name)
		}
		if jump.SavePassword {
			return credential.GetPassword(name, nil)
		}
		return "", fmt.Errorf("jump host %q needs a saved password or private key", name)
	}
	callback, err := browserHostKeyCallback(conn, writer)
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	connection, err := sshclient.DialWithHostKeyCallback(*host, password, resolver, passwordResolver, callback)
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	defer connection.Close()
	session, err := connection.Client.NewSession()
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	session.Stdout, session.Stderr = writer, writer
	cols, rows := terminalSize(start.Cols, start.Rows)
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	_ = session.Setenv("COLORTERM", "truecolor")
	_ = session.Setenv("TERM_PROGRAM", "xsh-web")
	if err := session.Shell(); err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	_ = store.RecordLogin(host.Name)
	writer.send(terminalMessage{Type: "connected", Message: "connected"})
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	messages := make(chan terminalMessage, 16)
	readErrors := make(chan error, 1)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		for {
			var message terminalMessage
			if err := conn.ReadJSON(&message); err != nil {
				readErrors <- err
				return
			}
			select {
			case messages <- message:
			case <-readerDone:
				return
			}
		}
	}()
	for {
		select {
		case err := <-done:
			message := "连接已关闭。"
			if err != nil && !errors.Is(err, io.EOF) {
				message = err.Error()
			}
			writer.send(terminalMessage{Type: "closed", Message: message})
			return
		case <-readErrors:
			_ = session.Close()
			return
		case message := <-messages:
			switch message.Type {
			case "input":
				_, _ = io.WriteString(stdin, message.Data)
			case "resize":
				cols, rows = terminalSize(message.Cols, message.Rows)
				_ = session.WindowChange(rows, cols)
			case "close":
				_ = session.Close()
				return
			}
		}
	}
}

func terminalSize(cols, rows int) (int, int) {
	if cols < 20 || cols > 500 {
		cols = 120
	}
	if rows < 5 || rows > 200 {
		rows = 40
	}
	return cols, rows
}

func browserHostKeyCallback(conn *websocket.Conn, writer *socketWriter) (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "known_hosts")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return nil, err
		}
	}
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}
		for _, expected := range keyErr.Want {
			if expected.Key.Type() == key.Type() {
				return fmt.Errorf("host key changed for %s (expected %s, received %s); connection refused", hostname, ssh.FingerprintSHA256(expected.Key), ssh.FingerprintSHA256(key))
			}
		}
		if err := writer.send(terminalMessage{Type: "trust", Message: hostname, Fingerprint: ssh.FingerprintSHA256(key), KeyType: key.Type()}); err != nil {
			return err
		}
		var response terminalMessage
		if err := conn.ReadJSON(&response); err != nil {
			return err
		}
		if response.Type != "trust" || !response.Trust {
			return errors.New("host key was not trusted")
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.WriteString(line)
		return err
	}, nil
}

func subtleToken(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
