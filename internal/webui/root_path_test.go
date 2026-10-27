package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeRootPath(t *testing.T) {
	for input, want := range map[string]string{"": "/", "/": "/", "/xsh": "/xsh/", "/tools/xsh/": "/tools/xsh/"} {
		got, err := NormalizeRootPath(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"xsh", "https://example.com/xsh", "//example.com", "/x?a", "/x#b", "/x%2fy", "/x\\y", "/x;y", "/x\ny"} {
		if _, err := NewForHostAndRootPath("127.0.0.1", input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestMountedRoutesAndAssets(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	handler, err := NewForHostAndRootPath("0.0.0.0", "/tools/xsh/")
	if err != nil {
		t.Fatal(err)
	}
	for target, code := range map[string]int{
		"/tools/xsh/?q=1": 200, "/tools/xsh/index.html": 200,
		"/tools/xsh/api/auth/status": 200, "/tools/xsh/api/hosts": 401,
		"/tools/xsh/ws/terminal": 401, "/tools/xsh/api/files": 401,
		"/tools/xsh/api/download": 401, "/tools/xsh/api/upload": 401,
		"/": 404, "/api/auth/status": 404, "/tools/xsh-other/app.js": 404,
		"/tools/xsh?foo=bar": 308,
	} {
		r := httptest.NewRequest("GET", "https://example.com"+target, nil)
		r.Header.Set("Origin", "https://example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != code {
			t.Errorf("%s: got %d, want %d", target, w.Code, code)
		}
		if code == 308 && w.Header().Get("Location") != "/tools/xsh/?foo=bar" {
			t.Errorf("redirect = %q", w.Header().Get("Location"))
		}
		if target != "/tools/xsh/?q=1" && target != "/tools/xsh/index.html" {
			continue
		}
		for _, match := range regexp.MustCompile(`<(?:script|link|img)\b[^>]*\b(?:src|href)="([^"]+)"`).FindAllStringSubmatch(w.Body.String(), -1) {
			ref, err := url.Parse(match[1])
			if err != nil {
				t.Fatal(err)
			}
			asset := r.URL.ResolveReference(ref)
			if !strings.HasPrefix(asset.Path, "/tools/xsh/") {
				t.Fatalf("asset escaped mount path: %s", asset)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", asset.String(), nil))
			if response.Code != 200 {
				t.Errorf("asset %s: %d", asset, response.Code)
			}
		}
	}
	r := httptest.NewRequest("GET", "https://example.com/tools/xsh/api/auth/status", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin request: %d", w.Code)
	}
}

func TestMountedSessionCookie(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	createTestVault(t, "test-password")
	handler, err := NewForHostAndRootPath("127.0.0.1", "/xsh")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "http://127.0.0.1:8080/xsh/api/login", strings.NewReader(`{"username":"admin","password":"test-password"}`)))
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Path != "/xsh/" {
		t.Fatalf("login cookie path = %q", cookie.Path)
	}
	var login struct{ Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/xsh/api/logout", nil)
	r.AddCookie(cookie)
	r.Header.Set("X-XSH-Token", login.Token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	cookie = w.Result().Cookies()[0]
	if cookie.Path != "/xsh/" || cookie.MaxAge != -1 {
		t.Fatalf("logout cookie: %+v", cookie)
	}
}
