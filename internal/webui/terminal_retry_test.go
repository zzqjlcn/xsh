package webui

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
)

func TestTerminalDialErrorCode(t *testing.T) {
	for _, test := range []struct {
		message string
		want    string
	}{
		{"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain", "authentication_failed"},
		{"no usable authentication method configured", "authentication_failed"},
		{"ProxyJump gateway: ssh: handshake failed: ssh: unable to authenticate", ""},
		{"ssh: handshake failed: knownhosts: key mismatch", ""},
		{"dial tcp: connection refused", ""},
	} {
		if got := terminalDialErrorCode(errors.New(test.message)); got != test.want {
			t.Fatalf("%s: got %q, want %q", test.message, got, test.want)
		}
	}
}

func TestTerminalPasswordRetryDoesNotChangeSavedCredentials(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	address, stop := startTestSSHServer(t)
	defer stop()
	hostname, portString, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portString)
	host := config.SSHHost{Name: "retry-host", Hostname: hostname, Port: port, User: "tester", AuthType: config.AuthPassword}
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{Password: "incorrect-saved-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(http.HandlerFunc(s.serve))
	defer web.Close()
	connect := func(password string) terminalMessage {
		t.Helper()
		header := http.Header{"Origin": {web.URL}, "Cookie": {sessionCookie + "=" + actor.ID}}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/ws/terminal?token="+actor.Token, header)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(terminalMessage{Type: "connect", Host: host.Name, User: host.User, Password: password}); err != nil {
			t.Fatal(err)
		}
		for {
			var message terminalMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatal(err)
			}
			if message.Type == "trust" {
				if err := conn.WriteJSON(terminalMessage{Type: "trust", Trust: true}); err != nil {
					t.Fatal(err)
				}
			} else if message.Type == "error" || message.Type == "connected" {
				return message
			}
		}
	}
	failed := connect("")
	if failed.Type != "error" || failed.Code != "authentication_failed" {
		t.Fatalf("missing authentication failure code: %+v", failed)
	}
	if retried := connect("secret"); retried.Type != "connected" || retried.User != "tester" {
		t.Fatalf("retry failed: %+v", retried)
	}
	secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, host.Name)
	if err != nil || secret.Password != "incorrect-saved-password" {
		t.Fatalf("retry modified saved credentials: %+v, %v", secret, err)
	}
}
