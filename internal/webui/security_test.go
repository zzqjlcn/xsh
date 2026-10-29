package webui

import (
	"encoding/json"
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

func TestAccountSessionDuration(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/xsh/"}
	a := testActor(t, s, "alice", true)
	b := testActor(t, s, "bobby", false)
	r := sessionRequest(t, s, a, "GET", "/api/account/security", "")
	var settings struct {
		SessionHours int `json:"session_hours"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &settings) != nil || settings.SessionHours != 12 {
		t.Fatal(r.Body.String())
	}
	if r := sessionRequest(t, s, a, "PUT", "/api/account/security", `{"session_hours":720}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	} else {
		cookie := r.Result().Cookies()[0]
		if cookie.MaxAge != 720*3600 || cookie.Path != "/xsh/" || !cookie.HttpOnly {
			t.Fatal("cookie duration/scope not updated")
		}
	}
	if a.Context.Err() != nil {
		t.Fatal("changing duration canceled session")
	}
	fresh, err := s.newSession(a.User, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.revokeSession(fresh)
	s.auth.Lock()
	remaining := time.Until(fresh.expiresAt)
	otherRemaining := time.Until(b.expiresAt)
	s.auth.Unlock()
	if remaining < 719*time.Hour || otherRemaining > 12*time.Hour || otherRemaining < 11*time.Hour {
		t.Fatal("duration not persisted or crossed account boundary")
	}
	if r := sessionRequest(t, s, a, "PUT", "/api/account/security", `{"session_hours":0}`); r.Code != 400 {
		t.Fatal("invalid duration accepted")
	}
	if r := sessionRequest(t, s, a, "POST", "/api/account/security", `{"session_hours":1}`); r.Code != 405 {
		t.Fatal("wrong method accepted")
	}
	if r := sessionRequest(t, s, nil, "GET", "/api/account/security", ""); r.Code != 401 {
		t.Fatal("anonymous access accepted")
	}
	s.auth.Lock()
	s.resetSessionExpiryLocked(a, 20*time.Millisecond)
	s.resetSessionExpiryLocked(a, time.Hour)
	s.auth.Unlock()
	select {
	case <-a.Context.Done():
		t.Fatal("old expiry canceled renewed session")
	case <-time.After(50 * time.Millisecond):
	}
	s.auth.Lock()
	s.resetSessionExpiryLocked(a, 20*time.Millisecond)
	s.auth.Unlock()
	select {
	case <-a.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("session did not expire")
	}
	if r := sessionRequest(t, s, a, "GET", "/api/hosts", ""); r.Code != 401 {
		t.Fatal("expired session accepted")
	}
	if r := sessionRequest(t, s, b, "GET", "/api/hosts", ""); r.Code != 200 {
		t.Fatal("other session revoked")
	}
}

func TestLockedTerminalSurvivesUntilLoginExpires(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	address, stop := startTestSSHServer(t)
	defer stop()
	hostname, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	if err := webstore.SaveHost(actor.User.ID, actor.Key, config.SSHHost{Name: "test", Hostname: hostname, Port: port, User: "tester", AuthType: config.AuthPassword}, webstore.Secret{Password: "secret"}, false, 0); err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(http.HandlerFunc(s.serve))
	defer web.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/ws/terminal?token="+actor.Token, http.Header{"Origin": {web.URL}, "Cookie": {sessionCookie + "=" + actor.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteJSON(terminalMessage{Type: "connect", Host: "test"}); err != nil {
		t.Fatal(err)
	}
	for {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == "trust" {
			_ = conn.WriteJSON(terminalMessage{Type: "trust", Trust: true})
		}
		if message.Type == "error" {
			t.Fatal(message.Message)
		}
		if message.Type == "connected" {
			break
		}
	}
	if r := sessionRequest(t, s, actor, "POST", "/api/lock", ""); r.Code != 204 {
		t.Fatal(r.Body.String())
	}
	select {
	case <-actor.Context.Done():
		t.Fatal("lock expired login")
	case <-time.After(50 * time.Millisecond):
	}
	if r := sessionRequest(t, s, actor, "PUT", "/api/account/security", `{"session_hours":1}`); r.Code != 423 {
		t.Fatal("locked settings were editable")
	}
	if r := sessionRequest(t, s, actor, "POST", "/api/unlock", `{"password":"test-password"}`); r.Code != 204 {
		t.Fatal(r.Body.String())
	}
	if err := conn.WriteJSON(terminalMessage{Type: "input", Data: "still-alive\n"}); err != nil {
		t.Fatal(err)
	}
	for {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal("terminal closed while login valid", err)
		}
		if message.Type == "output" && strings.Contains(message.Data, "still-alive") {
			break
		}
	}
	s.auth.Lock()
	s.resetSessionExpiryLocked(actor, 20*time.Millisecond)
	s.auth.Unlock()
	for {
		var message terminalMessage
		if err := conn.ReadJSON(&message); err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				t.Fatal("expired terminal did not close")
			}
			break
		}
	}
	if actor.Context.Err() == nil {
		t.Fatal("terminal closed before login expired")
	}
}
