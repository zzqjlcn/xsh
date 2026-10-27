package webui

import (
	"encoding/json"
	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordRequestBoundaries(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	createTestVault(t, "test-password")
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest("POST", "http://127.0.0.1/api/login", strings.NewReader(`{"username":"admin","password":"test-password"}`)))
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	var auth struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, body string
		cookie, token      bool
		want               int
	}{
		{"unauthenticated", "POST", `{}`, false, false, 401},
		{"csrf", "POST", `{}`, true, false, 403},
		{"method", "GET", ``, true, true, 405},
		{"mismatch", "POST", `{"password":"new-password","confirm":"different"}`, true, true, 400},
		{"short", "POST", `{"password":"short","confirm":"short"}`, true, true, 400},
		{"missing session", "POST", `{"password":"new-password","confirm":"new-password","session":"missing"}`, true, true, 409},
		{"unknown field", "POST", `{"password":"new-password","confirm":"new-password","host":"other"}`, true, true, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "http://127.0.0.1/api/password", strings.NewReader(test.body))
			if test.cookie {
				req.AddCookie(login.Result().Cookies()[0])
			}
			if test.token {
				req.Header.Set("X-XSH-Token", auth.Token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.want {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCurrentPasswordAccess(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "admin", true)
	host := config.SSHHost{Name: "test", Hostname: "original.test", User: "tester", Port: 22, AuthType: config.AuthPassword, SavePassword: true}
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{Password: "saved-current-secret"}, false, 0); err != nil {
		t.Fatal(err)
	}
	s.passwordTargets.Store("connected", &passwordTarget{host: host, userID: actor.User.ID, sessionID: actor.ID})
	request := func(method, body, origin string, cookie, token bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1/api/password/current", strings.NewReader(body))
		if cookie {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: actor.ID})
		}
		if token {
			r.Header.Set("X-XSH-Token", actor.Token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.serve(w, r)
		return w
	}
	for _, test := range []struct {
		name, method, body, origin string
		cookie, token              bool
		want                       int
	}{
		{"no login", "POST", `{"session":"connected"}`, "", false, true, 401},
		{"no token", "POST", `{"session":"connected"}`, "", true, false, 403},
		{"cross origin", "POST", `{"session":"connected"}`, "https://other.test", true, true, 403},
		{"get forbidden", "GET", ``, "", true, true, 405},
		{"invalid body", "POST", `{`, "", true, true, 400},
		{"unknown field", "POST", `{"session":"connected","host":"other"}`, "", true, true, 400},
		{"trailing body", "POST", `{"session":"connected"}{}`, "", true, true, 400},
		{"closed connection", "POST", `{"session":"closed"}`, "", true, true, 409},
		{"success", "POST", `{"session":"connected"}`, "", true, true, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := request(test.method, test.body, test.origin, test.cookie, test.token)
			if w.Code != test.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential response may be cached")
			}
			if w.Code == 200 {
				var data struct {
					Password string `json:"password"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.Password != "saved-current-secret" {
					t.Fatal("saved credential not returned")
				}
			} else if strings.Contains(w.Body.String(), "saved-current-secret") {
				t.Fatal("credential leaked on failure")
			}
		})
	}
	target, _ := s.passwordTargets.Load("connected")
	target.(*passwordTarget).mu.Lock()
	if w := request("POST", `{"session":"connected"}`, "", true, true); w.Code != 409 {
		t.Fatalf("concurrent change: %d", w.Code)
	}
	target.(*passwordTarget).mu.Unlock()
	host.Hostname = "different.test"
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{}, true, 0); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", `{"session":"connected"}`, "", true, true); w.Code != 404 || strings.Contains(w.Body.String(), "saved-current-secret") {
		t.Fatalf("changed identity: %d", w.Code)
	}
	host.Hostname = "original.test"
	if err := webstore.SaveHost(actor.User.ID, actor.Key, host, webstore.Secret{}, true, 0); err != nil {
		t.Fatal(err)
	}
	actor.Key = make([]byte, 32)
	if w := request("POST", `{"session":"connected"}`, "", true, true); w.Code != 400 {
		t.Fatalf("locked vault: %d", w.Code)
	}
}
