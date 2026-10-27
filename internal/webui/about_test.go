package webui

import (
	"encoding/json"
	"runtime"
	"testing"
)

func TestAboutAccessAndMetadata(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	admin := testActor(t, s, "admin", true)
	member := testActor(t, s, "member", false)
	if w := sessionRequest(t, s, nil, "GET", "/api/about", ""); w.Code != 401 {
		t.Fatalf("unauthenticated about: %d", w.Code)
	}
	for _, actor := range []*webSession{admin, member} {
		w := sessionRequest(t, s, actor, "GET", "/api/about", "")
		var info appInfo
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil {
			t.Fatalf("about: %d %s", w.Code, w.Body.String())
		}
		if info.Version != Version || info.GoVersion != runtime.Version() || info.Platform != runtime.GOOS+"/"+runtime.GOARCH {
			t.Fatalf("unexpected build metadata: %+v", info)
		}
		if w := sessionRequest(t, s, actor, "POST", "/api/about", `{}`); w.Code != 405 {
			t.Fatalf("about accepted mutation: %d", w.Code)
		}
	}
	if w := sessionRequest(t, s, member, "POST", "/api/lock", `{}`); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if w := sessionRequest(t, s, member, "GET", "/api/about", ""); w.Code != 423 {
		t.Fatalf("locked about: %d", w.Code)
	}
}
