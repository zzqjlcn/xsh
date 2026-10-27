package webui

import (
	"fmt"
	"testing"
)

func TestDeleteUserPermissionsAndSessionRevocation(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	admin := testActor(t, s, "admin", true)
	member := testActor(t, s, "member", false)
	body := fmt.Sprintf(`{"id":%d}`, member.User.ID)
	for _, actor := range []*webSession{nil, member} {
		want := 403
		if actor == nil {
			want = 401
		}
		if w := sessionRequest(t, s, actor, "DELETE", "/api/users", body); w.Code != want {
			t.Fatalf("unauthorized deletion: %d %s", w.Code, w.Body.String())
		}
	}
	if w := sessionRequest(t, s, admin, "DELETE", "/api/users", fmt.Sprintf(`{"id":%d}`, admin.User.ID)); w.Code != 400 {
		t.Fatalf("administrator deletion: %d", w.Code)
	}
	if w := sessionRequest(t, s, admin, "DELETE", "/api/users", `{}`); w.Code != 400 {
		t.Fatalf("missing account ID: %d", w.Code)
	}
	if w := sessionRequest(t, s, admin, "DELETE", "/api/users", body); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if member.Context.Err() == nil {
		t.Fatal("deleted account connections were not canceled")
	}
	if w := sessionRequest(t, s, member, "GET", "/api/hosts", ""); w.Code != 401 {
		t.Fatalf("deleted session retained access: %d", w.Code)
	}
	if w := sessionRequest(t, s, admin, "GET", "/api/users", ""); w.Code != 200 {
		t.Fatalf("administrator session affected: %d", w.Code)
	}
}
