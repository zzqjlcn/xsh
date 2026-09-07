package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
)

func TestBrowserTerminalSession(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", configDir)
	t.Setenv("USERPROFILE", t.TempDir())
	credential.LockVault()
	if err := credential.SetVaultPassword("test", "secret", func(string) (string, error) { return "test-master", nil }); err != nil {
		t.Fatal(err)
	}
	credential.LockVault()
	t.Cleanup(credential.LockVault)
	sshAddress, stopSSH := startTestSSHServer(t)
	defer stopSSH()
	hostname, portText, err := net.SplitHostPort(sshAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(config.SSHHost{Name: "test", Hostname: hostname, Port: port, User: "tester", AuthType: config.AuthPassword, SavePassword: true}); err != nil {
		t.Fatal(err)
	}

	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	loginRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/login", strings.NewReader(`{"password":"test-master"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := http.DefaultClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	var login struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/terminal?token=" + login.Token
	header := http.Header{"Origin": []string{httpServer.URL}, "Cookie": []string{loginResponse.Cookies()[0].String()}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(terminalMessage{Type: "connect", Host: "test", Cols: 90, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	connected, ready := false, false
	for !connected || !ready {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		switch message.Type {
		case "trust":
			if message.Fingerprint == "" {
				t.Fatal("missing fingerprint")
			}
			if err := conn.WriteJSON(terminalMessage{Type: "trust", Trust: true}); err != nil {
				t.Fatal(err)
			}
		case "connected":
			connected = true
		case "output":
			ready = ready || strings.Contains(message.Data, "ready")
		case "error":
			t.Fatal(message.Message)
		}
	}
	if err := conn.WriteJSON(terminalMessage{Type: "input", Data: "hello\n"}); err != nil {
		t.Fatal(err)
	}
	for {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == "output" && strings.Contains(message.Data, "hello") {
			break
		}
	}
}

func startTestSSHServer(t *testing.T) (string, func()) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) != "secret" {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for channelRequest := range channels {
			if channelRequest.ChannelType() != "session" {
				_ = channelRequest.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			channel, requests, err := channelRequest.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					switch request.Type {
					case "pty-req", "window-change":
						_ = request.Reply(true, nil)
					case "shell":
						_ = request.Reply(true, nil)
						_, _ = channel.Write([]byte("ready\r\n"))
						_, _ = io.Copy(channel, channel)
						return
					default:
						_ = request.Reply(false, nil)
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}
