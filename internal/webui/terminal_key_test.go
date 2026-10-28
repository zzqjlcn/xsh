package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
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
	"golang.org/x/crypto/ssh"
)

func TestTemporaryPrivateKeyAuthentication(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "test key")
	if err != nil {
		t.Fatal(err)
	}
	keyText := string(pem.EncodeToMemory(block))
	for _, username := range []string{"owner", "alternate"} {
		t.Run(username, func(t *testing.T) {
			t.Setenv("XSH_CONFIG_HOME", t.TempDir())
			s := &server{rootPath: "/"}
			actor := testActor(t, s, "admin", true)
			address, stop := startTestSSHServerWithWorkingDirectory(t, username, nil, signer.PublicKey())
			defer stop()
			hostname, portString, _ := net.SplitHostPort(address)
			port, _ := strconv.Atoi(portString)
			host := config.SSHHost{Name: "host", Hostname: hostname, Port: port, User: "owner", AuthType: config.AuthPassword}
			if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{Password: "saved-password"}, false, 0); err != nil {
				t.Fatal(err)
			}
			web := httptest.NewServer(http.HandlerFunc(s.serve))
			defer web.Close()
			header := http.Header{"Origin": {web.URL}, "Cookie": {sessionCookie + "=" + actor.ID}}
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/ws/terminal?token="+actor.Token, header)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(terminalMessage{Type: "connect", Host: host.Name, User: username, AuthType: config.AuthPrivateKey, PrivateKey: keyText}); err != nil {
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
				}
				if message.Type == "error" {
					t.Fatal(message.Message)
				}
				if message.Type == "connected" {
					if message.User != username {
						t.Fatalf("wrong user: %q", message.User)
					}
					break
				}
			}
			stored, err := webstore.FindHost(actor.User.ID, host.Name)
			if err != nil || stored.User != "owner" || stored.AuthType != config.AuthPassword {
				t.Fatalf("temporary key changed profile: %+v, %v", stored, err)
			}
			secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, host.Name)
			if err != nil || secret.Password != "saved-password" || secret.PrivateKey != "" {
				t.Fatal("temporary key was saved or replaced original credentials")
			}
		})
	}
}

func TestTemporaryCredentialsCannotBorrowAnotherUsersKey(t *testing.T) {
	original := config.SSHHost{User: "owner", AuthType: config.AuthPrivateKey, SavePassword: true}
	for _, input := range []terminalMessage{
		{User: "alternate", AuthType: config.AuthPrivateKey},
		{User: "alternate", AuthType: config.AuthPassword},
		{User: "alternate", AuthType: config.AuthPrivateKey, PrivateKey: "invalid"},
		{User: "alternate", AuthType: config.AuthPrivateKey, PrivateKey: "/" + strings.Repeat("x", 64<<10)},
		{User: "owner", AuthType: config.AuthPassword, PrivateKey: "unexpected key"},
		{User: "owner", AuthType: config.AuthPrivateKey, Password: "unexpected password"},
	} {
		host := original
		if err := applyTerminalCredentials(&host, input); err == nil {
			t.Fatalf("accepted invalid credentials for %q", input.User)
		}
		if !reflect.DeepEqual(host, original) {
			t.Fatal("failed validation modified profile")
		}
	}
	host := original
	if err := applyTerminalCredentials(&host, terminalMessage{User: "owner", AuthType: config.AuthPrivateKey}); err != nil {
		t.Fatalf("configured user cannot reuse own saved key: %v", err)
	}
}
