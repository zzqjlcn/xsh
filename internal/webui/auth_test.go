package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/credential"
	"github.com/zzqjlcn/xsh/internal/webstore"
)

func testActor(t *testing.T, s *server, username string, first bool) *webSession {
	t.Helper()
	user, key, err := webstore.CreateUser(username, "test-password", first, nil)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := s.newSession(user, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(actor.cancel)
	return actor
}

func sessionRequest(t *testing.T, s *server, actor *webSession, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	if actor != nil {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: actor.ID})
		r.Header.Set("X-XSH-Token", actor.Token)
	}
	w := httptest.NewRecorder()
	s.serve(w, r)
	return w
}

func TestRecipientDirectoryAndBatchSharing(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	admin := testActor(t, s, "admin", true)
	alice := testActor(t, s, "alice", false)
	bob := testActor(t, s, "bobby", false)
	disabled := testActor(t, s, "disabled", false)
	if err := webstore.SetDisabled(disabled.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, nil, "GET", "/api/share-recipients", ""); w.Code != 401 {
		t.Fatal("directory is public")
	}
	w := sessionRequest(t, s, alice, "GET", "/api/share-recipients", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `[{"username":"admin"},{"username":"bobby"}]` {
		t.Fatalf("directory: %d %s", w.Code, w.Body.String())
	}
	if w := sessionRequest(t, s, alice, "POST", "/api/share-recipients", `{}`); w.Code != 405 {
		t.Fatal("directory accepted mutation")
	}
	if w := sessionRequest(t, s, alice, "GET", "/api/users", ""); w.Code != 403 {
		t.Fatal("directory granted admin access")
	}
	if err := webstore.SaveHost(alice.User.ID, alice.Key, config.SSHHost{Name: "prod", Hostname: "shared.test", User: "secret-user", Port: 22, AuthType: config.AuthAuto}, webstore.Secret{}, false, 0); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, alice, "POST", "/api/shares", `{"name":"prod","recipients":["admin","bobby"]}`); w.Code != 204 {
		t.Fatalf("batch: %d %s", w.Code, w.Body.String())
	}
	for _, actor := range []*webSession{admin, bob} {
		inbox, err := webstore.Shares(actor.User.ID)
		if err != nil || len(inbox) != 1 {
			t.Fatalf("batch inbox: %+v %v", inbox, err)
		}
	}
	if w := sessionRequest(t, s, alice, "POST", "/api/lock", `{}`); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if w := sessionRequest(t, s, alice, "GET", "/api/share-recipients", ""); w.Code != 423 {
		t.Fatal("locked directory remained accessible")
	}
}

func TestUserIsolationAcrossWebEndpoints(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	alice := testActor(t, s, "alice", true)
	bob := testActor(t, s, "bobby", false)
	host := config.SSHHost{Name: "alice-only", Hostname: "private.test", User: "private-user", Port: 22, AuthType: config.AuthPassword}
	if err := webstore.SaveHost(alice.User.ID, alice.Key, host, webstore.Secret{Password: "private-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	host.SavePassword = true
	s.passwordTargets.Store("alice-connection", &passwordTarget{host: host, userID: alice.User.ID, sessionID: alice.ID})
	s.uploads.Store("alice-connection", &webConnection{UserID: alice.User.ID, SessionID: alice.ID})
	if w := sessionRequest(t, s, bob, "GET", "/api/hosts?user_id=1", ""); w.Code != 200 || strings.Contains(w.Body.String(), "alice-only") {
		t.Fatal("foreign list leaked")
	}
	for _, item := range []struct {
		method, path, body string
		want               int
	}{
		{"PUT", "/api/hosts", `{"original":"alice-only","host":{"hostname":"evil.test","user":"attacker","auth_type":"auto"}}`, 404},
		{"DELETE", "/api/hosts", `{"original":"alice-only"}`, 404},
		{"POST", "/api/shares", `{"name":"alice-only","recipients":["alice"]}`, 404},
		{"POST", "/api/password/current", `{"session":"alice-connection"}`, 404},
		{"POST", "/api/password", `{"session":"alice-connection","current_password":"x","password":"new-password","confirm":"new-password"}`, 404},
		{"GET", "/api/files?session=alice-connection", "", 404},
		{"GET", "/api/download?session=alice-connection", "", 404},
		{"POST", "/api/upload?session=alice-connection&name=x", "", 404},
		{"GET", "/api/users", "", 403},
		{"POST", "/api/users", `{"username":"evil-admin","password":"test-password"}`, 403},
	} {
		w := sessionRequest(t, s, bob, item.method, item.path, item.body)
		if w.Code != item.want {
			t.Errorf("%s %s: %d %s", item.method, item.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-password") {
			t.Fatal("password leaked")
		}
	}
	second, err := s.newSession(alice.User, alice.Key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.cancel)
	if w := sessionRequest(t, s, second, "POST", "/api/password/current", `{"session":"alice-connection"}`); w.Code != 404 {
		t.Fatal("another session borrowed connection")
	}
	// A valid cookie cannot use another session's CSRF token.
	r := httptest.NewRequest("DELETE", "http://127.0.0.1/api/hosts", strings.NewReader(`{"original":"alice-only"}`))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: alice.ID})
	r.Header.Set("X-XSH-Token", bob.Token)
	w := httptest.NewRecorder()
	s.serve(w, r)
	if w.Code != 403 {
		t.Fatal("cross-session CSRF accepted")
	}
}

func TestAdminCreationSharingAndAccountChanges(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	admin := testActor(t, s, "admin", true)
	w := sessionRequest(t, s, admin, "POST", "/api/users", `{"username":"bobby","password":"bobby-password"}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	user, key, err := webstore.Authenticate("bobby", "bobby-password")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.newSession(user, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.cancel)
	host := config.SSHHost{Name: "prod", Hostname: "shared.test", User: "do-not-share-user", Port: 5016, AuthType: config.AuthPassword, ProxyJump: "private-jump"}
	if err := webstore.SaveHost(admin.User.ID, admin.Key, host, webstore.Secret{Password: "do-not-share-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if w := sessionRequest(t, s, admin, "POST", "/api/shares", `{"name":"prod","recipients":["bobby"]}`); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	w = sessionRequest(t, s, bob, "GET", "/api/shares", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "do-not-share") || strings.Contains(w.Body.String(), "private-jump") {
		t.Fatal("share leaked login data")
	}
	var shares []webstore.Share
	if err := json.Unmarshal(w.Body.Bytes(), &shares); err != nil || len(shares) != 1 {
		t.Fatal("missing incoming share")
	}
	body, _ := json.Marshal(map[string]any{"host": config.SSHHost{Name: "prod", Hostname: "shared.test", Port: 5016, User: "own-user", AuthType: config.AuthAuto}, "share_id": shares[0].ID})
	if w := sessionRequest(t, s, bob, "POST", "/api/hosts", string(body)); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	copied, err := webstore.FindHost(user.ID, "prod")
	if err != nil || copied.User != "own-user" || copied.SavePassword {
		t.Fatal("import inherited credentials")
	}
	if w := sessionRequest(t, s, bob, "POST", "/api/account/password", `{"current_password":"bobby-password","password":"bobby-new-password","confirm":"bobby-new-password"}`); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if bob.Context.Err() == nil {
		t.Fatal("password change left session active")
	}
	if w := sessionRequest(t, s, bob, "GET", "/api/hosts", ""); w.Code != 401 {
		t.Fatal("old session accepted")
	}
	user, key, err = webstore.Authenticate("bobby", "bobby-new-password")
	if err != nil {
		t.Fatal(err)
	}
	bob, err = s.newSession(user, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.cancel)
	disable, _ := json.Marshal(map[string]any{"id": user.ID, "disabled": true})
	if w := sessionRequest(t, s, admin, "PUT", "/api/users", string(disable)); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if bob.Context.Err() == nil {
		t.Fatal("disabled session survived")
	}
	if w := sessionRequest(t, s, admin, "GET", "/api/hosts", ""); w.Code != 200 {
		t.Fatal("another user affected admin session")
	}
}

func TestWebLogoutDoesNotLockCLIVaultOrOtherUsers(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	credential.LockVault()
	t.Cleanup(credential.LockVault)
	if err := credential.CreateVault("legacy-master-password"); err != nil {
		t.Fatal(err)
	}
	if err := credential.SetVaultPassword("cli-host", "cli-secret", nil); err != nil {
		t.Fatal(err)
	}
	s := &server{rootPath: "/"}
	a := testActor(t, s, "alice", true)
	b := testActor(t, s, "bobby", false)
	if w := sessionRequest(t, s, a, "POST", "/api/logout", ""); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if a.Context.Err() == nil || b.Context.Err() != nil {
		t.Fatal("logout crossed session boundary")
	}
	if value, err := credential.GetPassword("cli-host", nil); err != nil || value != "cli-secret" {
		t.Fatal("Web logout changed CLI vault")
	}
	if w := sessionRequest(t, s, b, "GET", "/api/hosts", ""); w.Code != 200 {
		t.Fatal("other user logged out")
	}
}

func TestScreenLockPreservesSessionAndConnections(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	s := &server{rootPath: "/"}
	actor := testActor(t, s, "alice", true)
	other := testActor(t, s, "bobby", false)
	s.uploads.Store("existing", &webConnection{UserID: actor.User.ID, SessionID: actor.ID})
	if w := sessionRequest(t, s, actor, "POST", "/api/lock", ""); w.Code != 204 {
		t.Fatalf("lock: %d %s", w.Code, w.Body.String())
	}
	if actor.Context.Err() != nil {
		t.Fatal("screen lock revoked login")
	}
	if _, ok := s.uploads.Load("existing"); !ok {
		t.Fatal("screen lock removed existing connection")
	}
	for _, path := range []string{"/api/hosts", "/api/users", "/api/files?session=existing", "/ws/terminal?token=" + actor.Token} {
		if w := sessionRequest(t, s, actor, "GET", path, ""); w.Code != 423 {
			t.Errorf("locked %s: %d", path, w.Code)
		}
	}
	w := sessionRequest(t, s, actor, "GET", "/api/auth/status", "")
	var status struct {
		Authenticated, Locked bool
		Token                 string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !status.Authenticated || !status.Locked || status.Token != actor.Token {
		t.Fatal("locked state lost login identity")
	}
	if w := sessionRequest(t, s, other, "GET", "/api/hosts", ""); w.Code != 200 {
		t.Fatal("another user's session locked")
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/unlock", `{"password":"wrong-password"}`); w.Code != 403 {
		t.Fatalf("wrong unlock: %d", w.Code)
	}
	if !s.sessionLocked(actor) || actor.Context.Err() != nil {
		t.Fatal("wrong password changed login or unlocked screen")
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/unlock", `{"password":"test-password"}`); w.Code != 204 {
		t.Fatalf("unlock: %d %s", w.Code, w.Body.String())
	}
	if s.sessionLocked(actor) || actor.Context.Err() != nil {
		t.Fatal("unlock replaced login")
	}
	if w := sessionRequest(t, s, actor, "GET", "/api/hosts", ""); w.Code != 200 {
		t.Fatal("API not restored")
	}
	if _, ok := s.uploads.Load("existing"); !ok {
		t.Fatal("unlock replaced connection")
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/lock", ""); w.Code != 204 {
		t.Fatal("relock failed")
	}
	if w := sessionRequest(t, s, actor, "POST", "/api/logout", ""); w.Code != 204 || actor.Context.Err() == nil {
		t.Fatal("logout from lock screen did not revoke session")
	}
}

func TestLegacyImportRequiresMasterAndPreservesCLIData(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	credential.LockVault()
	t.Cleanup(credential.LockVault)
	if err := credential.CreateVault("legacy-master-password"); err != nil {
		t.Fatal(err)
	}
	if err := credential.SetVaultPassword("legacy", "legacy-ssh-secret", nil); err != nil {
		t.Fatal(err)
	}
	credential.LockVault()
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	host := config.SSHHost{Name: "legacy", Hostname: "legacy.test", User: "old-user", Port: 22, AuthType: config.AuthPassword, SavePassword: true}
	if err := store.Add(host); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyWebHosts("wrong-password"); err == nil {
		t.Fatal("import without master password")
	}
	initial, err := legacyWebHosts("legacy-master-password")
	if err != nil {
		t.Fatal(err)
	}
	user, key, err := webstore.CreateUser("admin", "new-login-password", true, initial)
	if err != nil {
		t.Fatal(err)
	}
	if secret, err := webstore.ReadSecret(user.ID, key, "legacy"); err != nil || secret.Password != "legacy-ssh-secret" {
		t.Fatal("legacy credential not imported")
	}
	store, err = config.Load()
	if err != nil || store.Find("legacy") == nil {
		t.Fatal("CLI profile removed")
	}
	if err := credential.UnlockVault("legacy-master-password"); err != nil {
		t.Fatal("CLI vault changed")
	}
}
func actorRequest(r *http.Request, actor *webSession) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, actor))
}
