package webui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func passwordTestSSHServer(t *testing.T) (config.SSHHost, ssh.PublicKey, *atomic.Int32) {
	return passwordProbeSSHServer(t, false)
}
func passwordProbeSSHServer(t *testing.T, forwarding bool) (config.SSHHost, ssh.PublicKey, *atomic.Int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() == "tester" && string(password) == "correct-password" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	channels := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				server, requests, global, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(global)
				for request := range requests {
					channels.Add(1)
					if forwarding && request.ChannelType() == "direct-tcpip" {
						var destination struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(request.ExtraData(), &destination) != nil {
							_ = request.Reject(ssh.Prohibited, "invalid destination")
							continue
						}
						remote, err := net.DialTimeout("tcp", net.JoinHostPort(destination.Host, strconv.Itoa(int(destination.Port))), time.Second)
						if err != nil {
							_ = request.Reject(ssh.ConnectionFailed, "unreachable")
							continue
						}
						channel, requests, err := request.Accept()
						if err != nil {
							remote.Close()
							continue
						}
						go ssh.DiscardRequests(requests)
						go func() {
							defer channel.Close()
							defer remote.Close()
							go func() { _, _ = io.Copy(channel, remote); channel.Close() }()
							_, _ = io.Copy(remote, channel)
						}()
						continue
					}
					_ = request.Reject(ssh.Prohibited, "test must not open channels")
				}
			}()
		}
	}()
	address, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return config.SSHHost{Name: "draft", Hostname: address, Port: number, User: "tester", AuthType: config.AuthPassword}, signer.PublicKey(), channels
}

func TestPasswordConnectionProbe(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	other := testActor(t, s, "other", false)
	host, key, channels := passwordTestSSHServer(t)
	body := func(original, password string, trust map[string]string) string {
		raw, _ := json.Marshal(map[string]any{"original": original, "host": host, "password": password, "trust": trust})
		return string(raw)
	}
	if w := sessionRequest(t, s, nil, "POST", "/api/hosts/test", body("", "correct-password", nil)); w.Code != 401 {
		t.Fatal("unauthenticated probe")
	}
	request := httptest.NewRequest("POST", "http://127.0.0.1/api/hosts/test", strings.NewReader(body("", "correct-password", nil)))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: actor.ID})
	response := httptest.NewRecorder()
	s.serve(response, request)
	if response.Code != 403 {
		t.Fatal("probe accepted missing CSRF token")
	}
	if w := sessionRequest(t, s, actor, "GET", "/api/hosts/test", ""); w.Code != 405 {
		t.Fatal("wrong method accepted")
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "", nil)); w.Code != 400 {
		t.Fatal("empty password accepted")
	}
	w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "correct-password", nil))
	var challenge testHostTrust
	if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &challenge) != nil || challenge.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("trust challenge: %d %s", w.Code, w.Body.String())
	}
	trust := map[string]string{challenge.Address: challenge.Fingerprint}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "wrong-password", trust)); w.Code != 400 || !strings.Contains(w.Body.String(), "密码认证失败") {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body.String())
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "correct-password", trust)); w.Code != 204 {
		t.Fatalf("valid password: %d %s", w.Code, w.Body.String())
	}
	if items, err := webstore.ListHosts(actor.User.ID); err != nil || len(items) != 0 {
		t.Fatal("probe saved draft")
	}
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{Password: "correct-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body(host.Name, "", trust)); w.Code != 204 {
		t.Fatalf("saved password: %d %s", w.Code, w.Body.String())
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body(host.Name, "override-wrong", trust)); w.Code != 400 {
		t.Fatal("input password ignored")
	}
	if w := sessionRequest(t, s, other, "POST", "/api/hosts/test", body(host.Name, "", trust)); w.Code != 404 {
		t.Fatal("foreign password used")
	}
	host.ProxyJump = "draft"
	if w := sessionRequest(t, s, other, "POST", "/api/hosts/test", body("", "correct-password", trust)); w.Code != 400 || !strings.Contains(w.Body.String(), "跳板主机不存在") {
		t.Fatal("foreign jump used")
	}
	host.ProxyJump = ""
	base, _ := config.Dir()
	dir := filepath.Join(base, "web-known-hosts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, strconv.FormatInt(actor.User.ID, 10))
	line := knownhosts.Line([]string{knownhosts.Normalize(challenge.Address)}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "correct-password", nil)); w.Code != 204 {
		t.Fatalf("known host: %d %s", w.Code, w.Body.String())
	}
	_, differentKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	differentSigner, _ := ssh.NewSignerFromKey(differentKey)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{knownhosts.Normalize(challenge.Address)}, differentSigner.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", body("", "correct-password", trust)); w.Code != 400 || !strings.Contains(w.Body.String(), "指纹已变化") {
		t.Fatal("changed host key bypassed")
	}
	if channels.Load() != 0 {
		t.Fatal("probe opened terminal channel")
	}
	if stored, err := webstore.FindHost(actor.User.ID, host.Name); err != nil || stored.LastLogin != 0 {
		t.Fatal("probe updated history")
	}
}

func TestPasswordProbeThroughJump(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	target, targetKey, targetChannels := passwordTestSSHServer(t)
	jump, jumpKey, jumpChannels := passwordProbeSSHServer(t, true)
	jump.Name = "jump"
	if err := webstore.SaveHost(actor.User.ID, actor.Key, jump, webstore.Secret{Password: "correct-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	target.ProxyJump = jump.Name
	trusted := map[string]string{net.JoinHostPort(jump.Hostname, strconv.Itoa(jump.Port)): ssh.FingerprintSHA256(jumpKey), net.JoinHostPort(target.Hostname, strconv.Itoa(target.Port)): ssh.FingerprintSHA256(targetKey)}
	raw, _ := json.Marshal(map[string]any{"host": target, "password": "correct-password", "trust": trusted})
	w := sessionRequest(t, s, actor, "POST", "/api/hosts/test", string(raw))
	if w.Code != 204 {
		t.Fatalf("jump probe: %d %s", w.Code, w.Body.String())
	}
	if targetChannels.Load() != 0 || jumpChannels.Load() != 1 {
		t.Fatalf("unexpected channels target=%d jump=%d", targetChannels.Load(), jumpChannels.Load())
	}
}

func TestPasswordProbeHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	hostname, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = testSSHLogin(ctx, config.SSHHost{Hostname: hostname, Port: number, User: "tester"}, []ssh.AuthMethod{ssh.Password("test")}, ssh.InsecureIgnoreHostKey(), nil)
	if err == nil {
		t.Fatal("stalled handshake succeeded")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled handshake leaked connection")
	}
}
