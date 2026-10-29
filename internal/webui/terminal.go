package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/sshclient"
	"github.com/zzqjlcn/xsh/internal/webstore"
)

type terminalMessage struct {
	Type        string          `json:"type"`
	Code        string          `json:"code,omitempty"`
	Host        string          `json:"host,omitempty"`
	User        string          `json:"user,omitempty"`
	Password    string          `json:"password,omitempty"`
	AuthType    config.AuthType `json:"auth_type,omitempty"`
	PrivateKey  string          `json:"private_key,omitempty"`
	Data        string          `json:"data,omitempty"`
	Message     string          `json:"message,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	KeyType     string          `json:"key_type,omitempty"`
	Cols        int             `json:"cols,omitempty"`
	Rows        int             `json:"rows,omitempty"`
	Trust       bool            `json:"trust,omitempty"`
	Session     string          `json:"session,omitempty"`
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
	actor := requestSession(r)
	if actor == nil || !subtleToken(r.URL.Query().Get("token"), actor.Token) {
		http.Error(w, "invalid request token", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		return sameOrigin(request)
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-actor.Context.Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	stopSession := context.AfterFunc(actor.Context, func() { _ = conn.Close() })
	defer stopSession()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var start terminalMessage
	if err := conn.ReadJSON(&start); err != nil || start.Type != "connect" || start.Host == "" {
		_ = conn.WriteJSON(terminalMessage{Type: "error", Message: "invalid connection request"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	writer := &socketWriter{conn: conn}
	hosts, err := webstore.ListHosts(actor.User.ID)
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	resolve := func(name string) (*config.SSHHost, bool) {
		for _, item := range hosts {
			if strings.EqualFold(item.Name, name) {
				copy := item.SSHHost
				return &copy, true
			}
		}
		return nil, false
	}
	host, ok := resolve(start.Host)
	if !ok {
		writer.send(terminalMessage{Type: "error", Message: "host not found"})
		return
	}
	password := start.Password
	if err := applyTerminalCredentials(host, start); err != nil {
		writer.send(terminalMessage{Type: "error", Code: "authentication_failed", Message: err.Error()})
		return
	}
	temporaryKey := []byte(start.PrivateKey)
	start.PrivateKey = ""
	start.Password = ""
	defer func() { clear(temporaryKey) }()
	if password == "" && host.SavePassword {
		secret, readErr := webstore.ReadSecret(actor.User.ID, actor.Key, host.Name)
		if readErr != nil {
			writer.send(terminalMessage{Type: "error", Message: "无法读取主机凭据"})
			return
		}
		password = secret.Password
	}
	passwordResolver := func(name string) (string, error) {
		host, ok := resolve(name)
		if !ok {
			return "", webstore.ErrNotFound
		}
		if !host.SavePassword {
			return "", nil
		}
		secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, name)
		return secret.Password, err
	}
	keys := func(name string) ([]byte, error) {
		if name == host.Name && len(temporaryKey) > 0 {
			return temporaryKey, nil
		}
		secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, name)
		return []byte(secret.PrivateKey), err
	}
	callback, err := browserHostKeyCallback(conn, writer, actor.User.ID)
	if err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	connection, err := sshclient.DialWithPrivateKeys(*host, password, resolve, passwordResolver, callback, keys)
	clear(temporaryKey)
	temporaryKey = nil
	password = ""
	if err != nil {
		writer.send(terminalMessage{Type: "error", Code: terminalDialErrorCode(err), Message: err.Error()})
		return
	}
	defer connection.Close()
	stopConnection := context.AfterFunc(actor.Context, func() { _ = connection.Close() })
	defer stopConnection()
	if actor.Context.Err() != nil {
		return
	}
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
	_ = webstore.RecordLogin(actor.User.ID, host.Name)
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		writer.send(terminalMessage{Type: "error", Message: err.Error()})
		return
	}
	connectionID := hex.EncodeToString(secret[:])
	s.uploads.Store(connectionID, &webConnection{UserID: actor.User.ID, SessionID: actor.ID, Client: connection.Client})
	s.passwordTargets.Store(connectionID, &passwordTarget{host: *host, client: connection.Client, userID: actor.User.ID, sessionID: actor.ID})
	defer s.passwordTargets.Delete(connectionID)
	defer s.uploads.Delete(connectionID)
	writer.send(terminalMessage{Type: "connected", Message: "connected", Session: connectionID, User: host.User})
	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()
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
		case <-keepAlive.C:
			// No reply is required; unsupported keepalive requests must not end a session.
			if _, _, err := connection.Client.SendRequest("keepalive@openssh.com", false, nil); err != nil {
				return
			}
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
				if s.sessionLocked(actor) {
					continue
				}
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

func terminalDialErrorCode(err error) string {
	if err == nil || strings.HasPrefix(err.Error(), "ProxyJump ") {
		return ""
	}
	if strings.Contains(err.Error(), "ssh: unable to authenticate") || err.Error() == "no usable authentication method configured" {
		return "authentication_failed"
	}
	return ""
}

func applyTerminalCredentials(host *config.SSHHost, input terminalMessage) error {
	if input.AuthType == "" {
		if input.PrivateKey != "" {
			return errors.New("使用私钥时请选择私钥认证")
		}
		return applyTerminalUser(host, input.User, input.Password)
	}
	if input.AuthType != config.AuthPassword && input.AuthType != config.AuthPrivateKey {
		return errors.New("请选择密码或私钥认证")
	}
	username := strings.TrimSpace(input.User)
	if username == "" {
		username = host.User
	}
	if username == "" || len(username) > 256 || strings.ContainsAny(username, " \t\r\n\x00") {
		return errors.New("SSH 用户名格式无效")
	}
	changed := username != host.User
	if input.AuthType == config.AuthPrivateKey {
		if input.Password != "" {
			return errors.New("私钥认证不能同时提交密码")
		}
		if input.PrivateKey == "" && (changed || host.AuthType != config.AuthPrivateKey) {
			return errors.New("请选择私钥文件或粘贴私钥内容")
		}
		if input.PrivateKey != "" {
			if len(input.PrivateKey) > 64<<10 {
				return errors.New("私钥文件不能超过 64 KB")
			}
			if _, err := ssh.ParsePrivateKey([]byte(input.PrivateKey)); err != nil {
				return errors.New("私钥格式无效，暂不支持带口令的私钥")
			}
		}
		host.SavePassword = false
	} else {
		if input.PrivateKey != "" {
			return errors.New("密码认证不能同时提交私钥")
		}
		if changed && input.Password == "" {
			return errors.New("使用其他用户登录时，请填写该用户的密码")
		}
		if changed {
			host.SavePassword = false
		}
	}
	host.User = username
	host.AuthType = input.AuthType
	host.IdentityFile = ""
	return nil
}

// A temporary login never borrows the configured user's password or private key.
func applyTerminalUser(host *config.SSHHost, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		if password != "" {
			host.AuthType = config.AuthPassword
			host.IdentityFile = ""
		}
		return nil
	}
	if len(username) > 256 || strings.ContainsAny(username, " \t\r\n\x00") {
		return errors.New("SSH 用户名格式无效")
	}
	if username == host.User {
		if password != "" {
			host.AuthType = config.AuthPassword
			host.IdentityFile = ""
		}
		return nil
	}
	if password == "" {
		return errors.New("使用其他用户登录时，请填写该用户的密码")
	}
	host.User = username
	host.SavePassword = false
	host.AuthType = config.AuthPassword
	host.IdentityFile = ""
	return nil
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

func browserHostKeyCallback(conn *websocket.Conn, writer *socketWriter, userID int64) (ssh.HostKeyCallback, error) {
	base, err := config.Dir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "web-known-hosts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, strconv.FormatInt(userID, 10))
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
