package webui

import (
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
)

func TestTemporaryTerminalUserCredentials(t *testing.T) {
	original := config.SSHHost{Name: "host", User: "default", AuthType: config.AuthPrivateKey, IdentityFile: "private-key", SavePassword: true}
	host := original
	if err := applyTerminalUser(&host, "alternate", ""); err == nil || !reflect.DeepEqual(host, original) {
		t.Fatal("missing password accepted or changed profile")
	}
	if err := applyTerminalUser(&host, "alternate", "temporary-secret"); err != nil {
		t.Fatal(err)
	}
	if host.User != "alternate" || host.SavePassword || host.AuthType != config.AuthPassword || host.IdentityFile != "" {
		t.Fatal("borrowed original credentials")
	}
	for _, name := range []string{"bad\nuser", "bad user", strings.Repeat("a", 257)} {
		host = original
		if err := applyTerminalUser(&host, name, "secret"); err == nil {
			t.Fatal("invalid user accepted")
		}
	}
	for _, name := range []string{"", "default"} {
		host = original
		if err := applyTerminalUser(&host, name, ""); err != nil || !reflect.DeepEqual(host, original) {
			t.Fatal("default login changed")
		}
		host = original
		if err := applyTerminalUser(&host, name, "retry-password"); err != nil || host.AuthType != config.AuthPassword || host.IdentityFile != "" || host.User != original.User {
			t.Fatalf("explicit password did not override private key: %+v, %v", host, err)
		}
	}
}

func TestTemporaryTerminalUserSSHLogin(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	address, stop := startTestSSHServerForUser(t, "alternate")
	defer stop()
	hostname, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	host := config.SSHHost{Name: "saved", Hostname: hostname, Port: port, User: "default", AuthType: config.AuthPassword}
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{Password: "original-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(s.serve))
	defer httpServer.Close()
	dial := func(password string) *websocket.Conn {
		t.Helper()
		header := http.Header{}
		header.Set("Origin", httpServer.URL)
		header.Set("Cookie", sessionCookie+"="+actor.ID)
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/terminal?token="+actor.Token, header)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(terminalMessage{Type: "connect", Host: host.Name, User: "alternate", Password: password}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	missing := dial("")
	var message terminalMessage
	if err := missing.ReadJSON(&message); err != nil || message.Type != "error" || !strings.Contains(message.Message, "请填写") {
		t.Fatalf("saved password reused for alternate user: %+v %v", message, err)
	}
	missing.Close()
	conn := dial("secret")
	for {
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == "trust" {
			if err := conn.WriteJSON(terminalMessage{Type: "trust", Trust: true}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if message.Type == "error" {
			t.Fatal(message.Message)
		}
		if message.Type == "connected" {
			break
		}
	}
	if message.User != "alternate" {
		t.Fatalf("reported incorrect login user: %q", message.User)
	}
	value, ok := s.passwordTargets.Load(message.Session)
	if !ok {
		t.Fatal("missing password target")
	}
	target := value.(*passwordTarget)
	if target.host.User != "alternate" || target.host.SavePassword {
		t.Fatal("password change target used default user")
	}
	if password, err := s.savedPasswordForHost(actor, target.host); err != nil || password != "" {
		t.Fatal("default password disclosed to alternate login")
	}
	stored, err := webstore.FindHost(actor.User.ID, host.Name)
	if err != nil || stored.User != "default" {
		t.Fatal("temporary user persisted")
	}
	secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, host.Name)
	if err != nil || secret.Password != "original-password" {
		t.Fatal("temporary password persisted")
	}
	conn.Close()
}
