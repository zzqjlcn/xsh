package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
)

func TestHostLifecycle(t *testing.T) {
	const alias = "xsh-webui-host-lifecycle-test"
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	createTestVault(t, "test-password")
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/login", strings.NewReader(`{"password":"test-password"}`))
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	request := func(method, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8080/api/hosts", strings.NewReader(body))
		r.Header.Set("X-XSH-Token", token)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := loginResponse
	var initial struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	body := `{"host":{"name":"` + alias + `","hostname":"127.0.0.1","user":"tester","port":22,"auth_type":"password","save_password":true,"last_login":123},"password":"saved-password"}`
	if w := request("POST", body, ""); w.Code != 403 {
		t.Fatalf("missing token: %d", w.Code)
	}
	if w := request("POST", body, initial.Token); w.Code != 204 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	store, _ := config.Load()
	if host := store.Find(alias); host == nil || !host.SavePassword || host.LastLogin != 0 {
		t.Fatalf("invalid created host: %+v", host)
	}
	if password, err := credential.GetPassword(alias, nil); err != nil || password != "saved-password" {
		t.Fatalf("saved password = %q, err=%v", password, err)
	}
	if w := request("POST", body, initial.Token); w.Code != 400 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	store.Hosts[0].LastLogin = 456
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	update := `{"original":"` + alias + `","host":{"name":"renamed","hostname":"example.test","user":"tester","port":2222,"auth_type":"password","save_password":true}}`
	if w := request("PUT", update, initial.Token); w.Code != 204 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	store, _ = config.Load()
	host := store.Find(alias)
	if host == nil || host.Hostname != "example.test" || !host.SavePassword || host.LastLogin != 456 {
		t.Fatalf("invalid updated host: %+v", host)
	}
	if w := request("DELETE", `{"original":"`+alias+`"}`, initial.Token); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	store, _ = config.Load()
	if len(store.Hosts) != 0 {
		t.Fatal("host was not deleted")
	}
	if w := request("PUT", update, initial.Token); w.Code != 404 {
		t.Fatalf("missing host: %d", w.Code)
	}
}

func TestRequestBoundaries(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, origin, method string
		code                 int
	}{
		{"http://127.0.0.1:8080/", "", "GET", 200},
		{"http://127.0.0.1:8080/app.js", "", "GET", 200},
		{"http://127.0.0.1:8080/style.css", "", "GET", 200},
		{"http://127.0.0.1:8080/logo.png", "", "GET", 200},
		{"http://127.0.0.1:8080/terminal.css", "", "GET", 200},
		{"http://127.0.0.1:8080/vendor/xterm.js", "", "GET", 200},
		{"http://127.0.0.1:8080/api/auth/status", "", "GET", 200},
		{"http://127.0.0.1:8080/api/hosts", "", "GET", 401},
		{"http://evil.test:8080/api/hosts", "", "GET", 403},
		{"http://127.0.0.1:8080/api/hosts", "https://evil.test", "GET", 403},
		{"http://127.0.0.1:8080/missing", "", "GET", 404},
		{"http://127.0.0.1:8080/", "", "POST", 405},
	} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Errorf("%s %s: got %d want %d", test.method, test.path, w.Code, test.code)
		}
	}
}

func TestRemoteHostAccess(t *testing.T) {
	handler, err := NewForHost("0.0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://192.168.1.20:23456/", "http://server.example:23456/"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: got %d want %d", target, w.Code, http.StatusOK)
		}
	}
}

func TestLogoContentType(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/logo.png", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("logo content type = %q", got)
	}
}

func TestTerminalStylesAllowedByCSP(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	policy := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("terminal dynamic styles are blocked: %s", policy)
	}
	if !strings.Contains(policy, "script-src 'self'") {
		t.Fatalf("script policy was weakened: %s", policy)
	}
}

func TestMasterPasswordLogin(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	createTestVault(t, "correct-password")
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	login := func(password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/login", strings.NewReader(`{"password":"`+password+`"}`))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := login("wrong-password"); w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w := login("correct-password")
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].HttpOnly {
		t.Fatalf("successful login: %d, cookies=%v", w.Code, w.Result().Cookies())
	}
}

func TestFirstLoginCreatesPasswordVault(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	credential.LockVault()
	t.Cleanup(credential.LockVault)
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/auth/status", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	var status struct {
		SetupRequired bool `json:"setup_required"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil || !status.SetupRequired {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	login := func(create bool) *httptest.ResponseRecorder {
		body := `{"password":"new-password","create":` + map[bool]string{true: "true", false: "false"}[create] + `}`
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/login", strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := login(false); w.Code != http.StatusConflict {
		t.Fatalf("setup without create: %d", w.Code)
	}
	if w := login(true); w.Code != http.StatusOK {
		t.Fatalf("create vault: %d %s", w.Code, w.Body.String())
	}
	credential.LockVault()
	if err := credential.UnlockVault("new-password"); err != nil {
		t.Fatalf("created vault cannot be unlocked: %v", err)
	}
}

func createTestVault(t *testing.T, password string) {
	t.Helper()
	credential.LockVault()
	t.Cleanup(credential.LockVault)
	if err := credential.SetVaultPassword("test-host", "saved-password", func(string) (string, error) { return password, nil }); err != nil {
		t.Fatal(err)
	}
	credential.LockVault()
}

func TestTerminalBoundaries(t *testing.T) {
	if cols, rows := terminalSize(0, 0); cols != 120 || rows != 40 {
		t.Fatalf("defaults = %d x %d", cols, rows)
	}
	if cols, rows := terminalSize(160, 50); cols != 160 || rows != 50 {
		t.Fatalf("size = %d x %d", cols, rows)
	}
	if !subtleToken("same", "same") || subtleToken("wrong", "same") || subtleToken("short", "longer") {
		t.Fatal("token comparison failed")
	}
}
