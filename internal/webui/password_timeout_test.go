package webui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/sshclient"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"golang.org/x/crypto/ssh"
)

// The fixture either leaves channel opening unanswered, or keeps passwd alive
// after a prompt, as a real interactive process would do while waiting for input.
func stalledPasswordClient(t *testing.T, prompt string) *ssh.Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		server, channels, requests, err := ssh.NewServerConn(connection, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for request := range channels {
			if prompt == "" {
				continue
			} // Do not acknowledge channel-open.
			channel, requests, err := request.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					switch request.Type {
					case "pty-req":
						_ = request.Reply(true, nil)
					case "exec":
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, prompt)
						_, _ = io.Copy(io.Discard, channel)
						return
					default:
						_ = request.Reply(false, nil)
					}
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "tester", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestPasswordTimeoutCoversChannelOpening(t *testing.T) {
	client := stalledPasswordClient(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := sshclient.ChangePassword(ctx, client, "old-secret", "new-secret")
	if !errors.Is(err, sshclient.ErrPasswordChangeUncertain) || time.Since(started) > time.Second {
		t.Fatalf("unbounded channel opening: %v after %s", err, time.Since(started))
	}
}

func TestPasswordRejectedPromptClosesRemoteProcess(t *testing.T) {
	for _, test := range []struct {
		name, prompt string
		want         error
	}{
		{"missing current password", "Current password: ", sshclient.ErrCurrentPasswordRequired},
		{"unsupported prompt", "OTP code: ", sshclient.ErrPasswordChange},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := stalledPasswordClient(t, test.prompt)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := sshclient.ChangePassword(ctx, client, "", "new-secret"); !errors.Is(err, test.want) {
				t.Fatalf("process remained blocked instead of returning prompt error: %v", err)
			}
		})
	}
}

func TestPasswordTimeoutReturnsHTTPResponse(t *testing.T) {
	client := stalledPasswordClient(t, "")
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{}
	actor := testActor(t, s, "admin", true)
	s.passwordTargets.Store("test", &passwordTarget{host: config.SSHHost{User: "tester"}, client: client, userID: actor.User.ID, sessionID: actor.ID})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 80*time.Millisecond)
		defer cancel()
		s.changePassword(w, actorRequest(r.WithContext(ctx), actor))
	}))
	// Reproduce the previous deadline collision without a 30-second test.
	server.Config.WriteTimeout = 80 * time.Millisecond
	server.Start()
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(`{"session":"test","current_password":"old-secret","password":"new-secret","confirm":"new-secret"}`))
	if err != nil {
		t.Fatalf("timeout discarded the HTTP response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(body), "结果尚未确认") {
		t.Fatalf("timeout response: %d %s %v", response.StatusCode, body, err)
	}
}

func TestPasswordMissingCurrentDoesNotStartSSH(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{}
	actor := testActor(t, s, "admin", true)
	// A nil SSH client ensures validation cannot reach SSH.
	s.passwordTargets.Store("test", &passwordTarget{host: config.SSHHost{User: "tester"}, userID: actor.User.ID, sessionID: actor.ID})
	request := httptest.NewRequest(http.MethodPost, "/api/password", strings.NewReader(`{"session":"test","password":"new-secret","confirm":"new-secret"}`))
	response := httptest.NewRecorder()
	s.changePassword(response, actorRequest(request, actor))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "当前用户的密码") {
		t.Fatalf("missing password: %d %s", response.Code, response.Body.String())
	}
}

func TestPasswordSavedCredentialBoundaries(t *testing.T) {
	for _, name := range []string{"same password", "host changed", "user changed", "port changed", "profile deleted", "saving disabled", "vault locked"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XSH_CONFIG_HOME", t.TempDir())
			s := &server{}
			actor := testActor(t, s, "admin", true)
			original := config.SSHHost{Name: "test", Hostname: "original.test", User: "tester", Port: 22, AuthType: config.AuthPassword, SavePassword: true}
			profile := original
			switch name {
			case "host changed":
				profile.Hostname = "another.test"
			case "user changed":
				profile.User = "another-user"
			case "port changed":
				profile.Port = 2222
			case "saving disabled":
				profile.SavePassword = false
			}
			if name != "profile deleted" {
				if err := webstore.SaveHost(actor.User.ID, actor.Key, profile, webstore.Secret{Password: "saved-secret"}, false, 0); err != nil {
					t.Fatal(err)
				}
			}
			if name == "saving disabled" {
				profile.AuthType = config.AuthAuto
				if err := webstore.SaveHost(actor.User.ID, actor.Key, profile, webstore.Secret{}, true, 0); err != nil {
					t.Fatal(err)
				}
			}
			if name == "vault locked" {
				actor.Key = make([]byte, 32)
			}
			s.passwordTargets.Store("test", &passwordTarget{host: original, userID: actor.User.ID, sessionID: actor.ID})
			// A nil SSH client ensures no credential is sent on a rejected request.
			request := httptest.NewRequest(http.MethodPost, "/api/password", strings.NewReader(`{"session":"test","password":"saved-secret","confirm":"saved-secret"}`))
			response := httptest.NewRecorder()
			s.changePassword(response, actorRequest(request, actor))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
			expected := "没有可用的已保存密码"
			if name == "same password" {
				expected = "新密码不能与当前密码相同"
			}
			if name == "vault locked" {
				expected = "无法读取已保存的密码"
			}
			if !strings.Contains(response.Body.String(), expected) {
				t.Fatalf("wrong rejection: %s", response.Body.String())
			}
		})
	}
}
