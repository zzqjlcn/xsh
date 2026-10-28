package webui

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	"github.com/zzqjlcn/xsh/internal/webstore"
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
	actor, key, err := webstore.CreateUser("admin", "test-master", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	sshAddress, stopSSH := startTestSSHServer(t)
	defer stopSSH()
	hostname, portText, err := net.SplitHostPort(sshAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	if err := webstore.SaveHost(actor.ID, key, config.SSHHost{Name: "test", Hostname: hostname, Port: port, User: "tester", AuthType: config.AuthPassword}, webstore.Secret{Password: "secret"}, false, 0); err != nil {
		t.Fatal(err)
	}

	handler, err := NewForHostAndRootPath("127.0.0.1", root)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	loginRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+root+"/api/login", strings.NewReader(`{"username":"admin","password":"test-master"}`))
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
	screenAction := func(path, password string, want int) {
		t.Helper()
		body := ""
		if password != "" {
			raw, _ := json.Marshal(map[string]string{"password": password})
			body = string(raw)
		}
		request, _ := http.NewRequest("POST", httpServer.URL+root+path, strings.NewReader(body))
		request.AddCookie(loginResponse.Cookies()[0])
		request.Header.Set("X-XSH-Token", login.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s: %d %s", path, response.StatusCode, data)
		}
	}
	screenAction("/api/lock", "", 204)
	screenAction("/api/unlock", "wrong-password", 403)
	screenAction("/api/unlock", "test-master", 204)
	// Transfers and terminal echo below reuse the very same SSH and WebSocket
	// connections established before locking the screen.
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
	remoteRequest := func(endpoint, filePath string, authenticated bool, want int, currentDirectory ...bool) ([]byte, http.Header) {
		t.Helper()
		query := url.Values{"session": {uploadSession}, "path": {filepath.ToSlash(filePath)}}
		if len(currentDirectory) > 0 && currentDirectory[0] {
			query.Set("cwd", "1")
		}
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
	fallback, _ := remoteRequest("/api/files", ".", true, http.StatusOK, true)
	var fallbackInfo struct {
		CWDUnavailable bool `json:"cwd_unavailable"`
	}
	if err := json.Unmarshal(fallback, &fallbackInfo); err != nil || !fallbackInfo.CWDUnavailable {
		t.Fatalf("missing working-directory fallback: %s, %v", fallback, err)
	}
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
	failedBody := `{"session":"` + uploadSession + `","current_password":"wrong-current","password":"New-password123!","confirm":"New-password123!"}`
	failedRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+root+"/api/password", strings.NewReader(failedBody))
	failedRequest.AddCookie(loginResponse.Cookies()[0])
	failedRequest.Header.Set("X-XSH-Token", login.Token)
	failedResponse, err := http.DefaultClient.Do(failedRequest)
	if err != nil {
		t.Fatal(err)
	}
	failedData, _ := io.ReadAll(failedResponse.Body)
	failedResponse.Body.Close()
	if failedResponse.StatusCode != http.StatusBadGateway {
		t.Fatalf("rejected password: %d %s", failedResponse.StatusCode, failedData)
	}
	if secret, err := webstore.ReadSecret(actor.ID, key, "test"); err != nil || secret.Password != "secret" {
		t.Fatal("failed change modified saved password")
	}
	changeBody := `{"session":"` + uploadSession + `","password":"New-password123!","confirm":"New-password123!"}`
	changeRequest, err := http.NewRequest(http.MethodPost, httpServer.URL+root+"/api/password", strings.NewReader(changeBody))
	if err != nil {
		t.Fatal(err)
	}
	changeRequest.AddCookie(loginResponse.Cookies()[0])
	changeRequest.Header.Set("X-XSH-Token", login.Token)
	changeResponse, err := http.DefaultClient.Do(changeRequest)
	if err != nil {
		t.Fatal(err)
	}
	changeData, _ := io.ReadAll(changeResponse.Body)
	changeResponse.Body.Close()
	if changeResponse.StatusCode != http.StatusOK || !strings.Contains(string(changeData), `"changed":true`) {
		t.Fatalf("change password: %d %s", changeResponse.StatusCode, changeData)
	}
	if secret, err := webstore.ReadSecret(actor.ID, key, "test"); err != nil || secret.Password != "New-password123!" {
		t.Fatalf("updated credential: %v", err)
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
	logoutRequest, _ := http.NewRequest("POST", httpServer.URL+root+"/api/logout", nil)
	logoutRequest.AddCookie(loginResponse.Cookies()[0])
	logoutRequest.Header.Set("X-XSH-Token", login.Token)
	logoutResponse, err := http.DefaultClient.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	logoutResponse.Body.Close()
	if logoutResponse.StatusCode != 204 {
		t.Fatal("logout failed")
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var afterLogout terminalMessage
	for {
		err := conn.ReadJSON(&afterLogout)
		if err == nil {
			continue
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Fatal("logout did not close the existing terminal")
		}
		break
	}
	if _, err := os.Stat(filepath.Join(configDir, "web-known-hosts", strconv.FormatInt(actor.ID, 10))); err != nil {
		t.Fatal("account-specific known_hosts missing")
	}
}

func startTestSSHServer(t *testing.T) (string, func()) {
	return startTestSSHServerForUser(t, "")
}
func startTestSSHServerForUser(t *testing.T, expectedUser string) (string, func()) {
	return startTestSSHServerWithWorkingDirectory(t, expectedUser, nil)
}

func startTestSSHServerWithWorkingDirectory(t *testing.T, expectedUser string, cwd func() string, publicKeys ...ssh.PublicKey) (string, func()) {
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
		if string(password) != "secret" || expectedUser != "" && metadata.User() != expectedUser {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	if len(publicKeys) > 0 {
		serverConfig.PublicKeyCallback = func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() != expectedUser || !bytes.Equal(key.Marshal(), publicKeys[0].Marshal()) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, nil
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handle := func(connection net.Conn) {
		defer connection.Close()
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
					case "exec":
						var payload struct{ Command string }
						if ssh.Unmarshal(request.Payload, &payload) == nil && payload.Command == workingDirectoryCommand && cwd != nil {
							_ = request.Reply(true, nil)
							_, _ = io.WriteString(channel, cwd()+"\n")
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
						if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != "LC_ALL=C passwd" {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						reader := bufio.NewReader(channel)
						for index, prompt := range []string{"Current password: ", "New password: ", "Retype new password: "} {
							_, _ = io.WriteString(channel, prompt)
							value, err := reader.ReadString('\n')
							expected := "New-password123!"
							if index == 0 {
								expected = "secret"
							}
							if err != nil || strings.TrimSuffix(value, "\n") != expected {
								_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
								return
							}
						}
						_, _ = io.WriteString(channel, "password updated successfully\r\n")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
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
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(connection)
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}
