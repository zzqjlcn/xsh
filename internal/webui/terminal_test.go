package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
)

func TestBrowserTerminalSession(t *testing.T) {
	for _, root := range []string{"", "/tools/xsh"} {
		t.Run("root="+root, func(t *testing.T) {
			testBrowserTerminalSession(t, root)
		})
	}
}

func testBrowserTerminalSession(t *testing.T, root string) {
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

	handler, err := NewForHostAndRootPath("127.0.0.1", root)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	loginRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+root+"/api/login", strings.NewReader(`{"password":"test-master"}`))
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
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + root + "/ws/terminal?token=" + login.Token
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
	var uploadSession string
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
			uploadSession = message.Session
		case "output":
			ready = ready || strings.Contains(message.Data, "ready")
		case "error":
			t.Fatal(message.Message)
		}
	}
	remoteDir := t.TempDir()
	content := strings.Repeat("文件内容\x00\xff", 10000)
	upload := func(name, session, requestToken string, authenticated bool, want int) {
		t.Helper()
		query := url.Values{"session": {session}, "directory": {filepath.ToSlash(remoteDir)}, "name": {name}}
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+root+"/api/upload?"+query.Encode(), strings.NewReader(content))
		req.Header.Set("X-XSH-Token", requestToken)
		if authenticated {
			req.AddCookie(loginResponse.Cookies()[0])
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("upload %q: %d %s", name, response.StatusCode, body)
		}
	}
	if uploadSession == "" {
		t.Fatal("missing upload session")
	}
	upload("测试.bin", uploadSession, login.Token, false, http.StatusUnauthorized)
	upload("测试.bin", uploadSession, "", true, http.StatusForbidden)
	upload("测试.bin", "missing", login.Token, true, http.StatusConflict)
	upload("../escape", uploadSession, login.Token, true, http.StatusBadRequest)
	upload("测试.bin", uploadSession, login.Token, true, http.StatusNoContent)
	content = "replacement"
	upload("测试.bin", uploadSession, login.Token, true, http.StatusConflict)
	data, err := os.ReadFile(filepath.Join(remoteDir, "测试.bin"))
	if err != nil || string(data) != strings.Repeat("文件内容\x00\xff", 10000) {
		t.Fatalf("uploaded content mismatch: %v", err)
	}
	content = ""
	upload("empty.txt", uploadSession, login.Token, true, http.StatusNoContent)
	if err := os.Mkdir(filepath.Join(remoteDir, "子目录"), 0700); err != nil {
		t.Fatal(err)
	}
	remoteRequest := func(endpoint, filePath string, authenticated bool, want int) ([]byte, http.Header) {
		t.Helper()
		query := url.Values{"session": {uploadSession}, "path": {filepath.ToSlash(filePath)}}
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+root+endpoint+"?"+query.Encode(), nil)
		if authenticated {
			req.AddCookie(loginResponse.Cookies()[0])
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s: %d %s", endpoint, response.StatusCode, body)
		}
		return body, response.Header
	}
	remoteRequest("/api/files", remoteDir, false, http.StatusUnauthorized)
	remoteRequest("/api/download", filepath.Join(remoteDir, "测试.bin"), false, http.StatusUnauthorized)
	listing, _ := remoteRequest("/api/files", remoteDir, true, http.StatusOK)
	var files struct {
		Path    string        `json:"path"`
		Entries []remoteEntry `json:"entries"`
	}
	if err := json.Unmarshal(listing, &files); err != nil {
		t.Fatal(err)
	}
	if files.Path == "" || len(files.Entries) != 3 || files.Entries[0].Name != "子目录" || !files.Entries[0].Directory {
		t.Fatalf("unexpected listing: %s", listing)
	}
	for _, entry := range files.Entries {
		if entry.Name == "测试.bin" && (!entry.Regular || entry.Size != int64(len(data))) {
			t.Fatalf("invalid metadata: %+v", entry)
		}
	}
	listing, _ = remoteRequest("/api/files", filepath.Join(remoteDir, "子目录"), true, http.StatusOK)
	if err := json.Unmarshal(listing, &files); err != nil || len(files.Entries) != 0 {
		t.Fatalf("empty directory: %s", listing)
	}
	downloaded, headers := remoteRequest("/api/download", filepath.Join(remoteDir, "测试.bin"), true, http.StatusOK)
	_, parameters, err := mime.ParseMediaType(headers.Get("Content-Disposition"))
	if err != nil || parameters["filename"] != "测试.bin" || string(downloaded) != string(data) {
		t.Fatalf("download mismatch: %v %v", parameters, err)
	}
	empty, _ := remoteRequest("/api/download", filepath.Join(remoteDir, "empty.txt"), true, http.StatusOK)
	if len(empty) != 0 {
		t.Fatal("empty download was not empty")
	}
	remoteRequest("/api/download", filepath.Join(remoteDir, "子目录"), true, http.StatusBadRequest)
	remoteRequest("/api/download", filepath.Join(remoteDir, "missing"), true, http.StatusNotFound)
	remoteRequest("/api/files", filepath.Join(remoteDir, "测试.bin"), true, http.StatusBadRequest)
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
					case "subsystem":
						var payload struct{ Name string }
						if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						server, err := sftp.NewServer(channel)
						if err != nil {
							return
						}
						defer server.Close()
						_ = server.Serve()
						return
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
