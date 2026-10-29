package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzqjlcn/xsh/internal/webstore"
)

func TestHostLifecycle(t *testing.T) {
	const alias = "xsh-webui-host-lifecycle-test"
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	createTestVault(t, "test-password")
	user, key, err := webstore.Authenticate("admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest("POST", "http://127.0.0.1/api/login", strings.NewReader(`{"username":"admin","password":"test-password"}`)))
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	var auth struct{ Token string }
	json.Unmarshal(login.Body.Bytes(), &auth)
	request := func(method, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1/api/hosts", strings.NewReader(body))
		r.AddCookie(login.Result().Cookies()[0])
		r.Header.Set("X-XSH-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"host":{"name":"` + alias + `","hostname":"127.0.0.1","user":"tester","port":22,"auth_type":"password","last_login":123},"password":"saved-password"}`
	if w := request("POST", body, ""); w.Code != 403 {
		t.Fatalf("missing token: %d", w.Code)
	}
	if w := request("POST", body, auth.Token); w.Code != 204 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	host, err := webstore.FindHost(user.ID, alias)
	if err != nil || !host.SavePassword || host.LastLogin != 0 {
		t.Fatalf("host: %+v %v", host, err)
	}
	secret, err := webstore.ReadSecret(user.ID, key, alias)
	if err != nil || secret.Password != "saved-password" {
		t.Fatal("saved password mismatch")
	}
	if w := request("POST", body, auth.Token); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	update := `{"original":"` + alias + `","host":{"name":"renamed","hostname":"example.test","user":"tester","port":2222,"auth_type":"password"}}`
	if w := request("PUT", update, auth.Token); w.Code != 204 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	host, err = webstore.FindHost(user.ID, "renamed")
	if err != nil || host.Hostname != "example.test" || !host.SavePassword {
		t.Fatal("update lost profile")
	}
	if _, err := webstore.FindHost(user.ID, alias); err != webstore.ErrNotFound {
		t.Fatal("old name still exists")
	}
	secret, err = webstore.ReadSecret(user.ID, key, "renamed")
	if err != nil || secret.Password != "saved-password" {
		t.Fatal("rename lost password")
	}
	if w := request("DELETE", `{"original":"renamed"}`, auth.Token); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if _, err := webstore.FindHost(user.ID, "renamed"); err != webstore.ErrNotFound {
		t.Fatal("host not deleted")
	}
	if w := request("PUT", update, auth.Token); w.Code != 404 {
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
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
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

func TestFirstLoginCreatesAdministrator(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest("GET", "http://127.0.0.1/api/auth/status", nil))
	if !strings.Contains(status.Body.String(), `"setup_required":true`) {
		t.Fatal(status.Body.String())
	}
	code, err := webstore.SetupCode()
	if err != nil {
		t.Fatal(err)
	}
	login := func(create bool, code string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"username": "admin", "password": "new-password", "create": create, "setup_code": code})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1/api/login", strings.NewReader(string(body))))
		return w
	}
	if w := login(false, code); w.Code != 409 {
		t.Fatalf("setup: %d", w.Code)
	}
	if w := login(true, "wrong"); w.Code != 403 {
		t.Fatalf("wrong setup code: %d", w.Code)
	}
	if w := login(true, code); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	user, _, err := webstore.Authenticate("admin", "new-password")
	if err != nil || !user.Admin {
		t.Fatal("administrator not created")
	}
	if w := login(true, code); w.Code != 409 {
		t.Fatal("second bootstrap allowed")
	}
}

func createTestVault(t *testing.T, password string) {
	t.Helper()
	if _, _, err := webstore.CreateUser("admin", password, true, nil); err != nil {
		t.Fatal(err)
	}
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
