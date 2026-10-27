package webstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"golang.org/x/crypto/ssh"
	"os"
	"strings"
	"testing"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/database"
)

func testUsers(t *testing.T) (User, []byte, User, []byte) {
	t.Helper()
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	a, ak, err := CreateUser("alice", "alice-password", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, bk, err := CreateUser("bobby", "bobby-password", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, ak, b, bk
}
func sampleHost() config.SSHHost {
	return config.SSHHost{Name: "prod", Hostname: "server.test", Port: 22, User: "owner-login", AuthType: config.AuthPassword, Tags: []string{"web"}}
}

func TestBatchShareRecipientsAndRollback(t *testing.T) {
	a, ak, b, _ := testUsers(t)
	c, _, err := CreateUser("carol", "carol-password", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	disabled, _, err := CreateUser("disabled", "disabled-password", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetDisabled(disabled.ID, true); err != nil {
		t.Fatal(err)
	}
	recipients, err := ShareRecipients(a.ID)
	if err != nil || len(recipients) != 2 || recipients[0].Username != b.Username || recipients[1].Username != c.Username {
		t.Fatalf("recipients: %+v %v", recipients, err)
	}
	host := sampleHost()
	if err := SaveHost(a.ID, ak, host, Secret{Password: "private"}, false, 0); err != nil {
		t.Fatal(err)
	}
	for _, selection := range [][]string{nil, {b.Username, "missing"}, {b.Username, a.Username}, {b.Username, disabled.Username}, {b.Username, ""}, make([]string, 101)} {
		if err := ShareHost(a.ID, host.Name, selection); err == nil {
			t.Fatalf("accepted invalid selection: %v", selection)
		}
		if inbox, err := Shares(b.ID); err != nil || len(inbox) != 0 {
			t.Fatalf("partial delivery: %+v %v", inbox, err)
		}
	}
	if err := ShareHost(a.ID, host.Name, []string{b.Username, " BOBBY ", c.Username}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{b.ID, c.ID} {
		if inbox, err := Shares(id); err != nil || len(inbox) != 1 {
			t.Fatalf("batch delivery: %+v %v", inbox, err)
		}
	}
	db, err := database.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO web_shares(sender_id,recipient_id,data,created_at) SELECT sender_id,recipient_id,data,created_at FROM web_shares WHERE recipient_id=?`, c.ID); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < 100; i++ {
		if _, err := db.Exec(`INSERT INTO web_shares(sender_id,recipient_id,data,created_at) VALUES(?,?,?,0)`, a.ID, c.ID, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := ShareHost(a.ID, host.Name, []string{b.Username, c.Username}); err == nil {
		t.Fatal("accepted full inbox")
	}
	if inbox, err := Shares(b.ID); err != nil || len(inbox) != 1 {
		t.Fatalf("full inbox caused partial delivery: %+v %v", inbox, err)
	}
}

func TestHostAndCredentialIsolation(t *testing.T) {
	a, ak, b, bk := testUsers(t)
	host := sampleHost()
	if err := SaveHost(a.ID, ak, host, Secret{Password: "alice-ssh-secret"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := FindHost(b.ID, host.Name); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign host visible")
	}
	if _, err := ReadSecret(b.ID, bk, host.Name); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign credential visible")
	}
	if err := DeleteHost(b.ID, host.Name); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign host deleted")
	}
	if err := SaveHost(b.ID, bk, host, Secret{Password: "bobby-ssh-secret"}, false, 0); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		user User
		key  []byte
		want string
	}{{a, ak, "alice-ssh-secret"}, {b, bk, "bobby-ssh-secret"}} {
		secret, err := ReadSecret(item.user.ID, item.key, host.Name)
		if err != nil || secret.Password != item.want {
			t.Fatalf("credential mismatch: %v", err)
		}
	}
	if _, err := ReadSecret(a.ID, bk, host.Name); err == nil {
		t.Fatal("another user's key decrypted credential")
	}
	path, _ := database.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"alice-password", "bobby-password", "alice-ssh-secret", "bobby-ssh-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext secret persisted")
		}
	}
}

func TestSharingOnlyConfigurationAndAcceptingAtomically(t *testing.T) {
	a, ak, b, bk := testUsers(t)
	host := sampleHost()
	host.ProxyJump = "private-bastion"
	if err := SaveHost(a.ID, ak, host, Secret{Password: "never-share-password"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := ShareHost(a.ID, host.Name, []string{b.Username}); err != nil {
		t.Fatal(err)
	}
	inbox, err := Shares(b.ID)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox: %v", err)
	}
	raw, _ := json.Marshal(inbox)
	for _, field := range []string{"owner-login", "never-share-password", "private-bastion", "auth_type", "identity_file", "private_key", "save_password", "last_login"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("shared forbidden data: %s", field)
		}
	}
	if own, err := Shares(a.ID); err != nil || len(own) != 0 {
		t.Fatal("foreign inbox visible")
	}
	if err := DeleteShare(a.ID, inbox[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign share deleted")
	}
	imported := config.SSHHost{Name: inbox[0].Config.Name, Hostname: inbox[0].Config.Hostname, Port: inbox[0].Config.Port, User: "bobby-login", AuthType: config.AuthAuto}
	if err := SaveHost(a.ID, ak, config.SSHHost{Name: "foreign-import", Hostname: host.Hostname, Port: 22, User: "attacker", AuthType: config.AuthAuto}, Secret{}, false, inbox[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign share accepted")
	}
	if err := SaveHost(b.ID, bk, imported, Secret{}, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := SaveHost(b.ID, bk, imported, Secret{}, false, inbox[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate accepted")
	}
	if pending, _ := Shares(b.ID); len(pending) != 1 {
		t.Fatal("failed import consumed share")
	}
	imported.Name = "prod-copy"
	if err := SaveHost(b.ID, bk, imported, Secret{}, false, inbox[0].ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := Shares(b.ID); len(pending) != 0 {
		t.Fatal("share not consumed")
	}
	got, _ := FindHost(b.ID, imported.Name)
	secret, err := ReadSecret(b.ID, bk, imported.Name)
	if err != nil || got.User != "bobby-login" || got.SavePassword || got.HasPrivateKey || secret.Password != "" || secret.PrivateKey != "" {
		t.Fatal("import inherited credentials")
	}
	if err := DeleteHost(a.ID, host.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := FindHost(b.ID, imported.Name); err != nil {
		t.Fatal("copy depended on sender")
	}
}

func TestLoginPasswordRewrapPreservesCredentials(t *testing.T) {
	a, ak, _, _ := testUsers(t)
	host := sampleHost()
	if err := SaveHost(a.ID, ak, host, Secret{Password: "ssh-secret"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := ChangeLoginPassword(a, "wrong-password", "next-password", ak); !errors.Is(err, ErrLogin) {
		t.Fatal("wrong login password accepted")
	}
	if err := ChangeLoginPassword(a, "alice-password", "next-password", ak); err != nil {
		t.Fatal(err)
	}
	if SessionValid(a) {
		t.Fatal("old authentication snapshot remains valid")
	}
	if _, _, err := Authenticate("alice", "alice-password"); !errors.Is(err, ErrLogin) {
		t.Fatal("old password accepted")
	}
	_, key, err := Authenticate("ALICE", "next-password")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(a.ID, key, host.Name)
	if err != nil || secret.Password != "ssh-secret" {
		t.Fatal("password change lost credentials")
	}
}

func TestCLIStorageRemainsSeparate(t *testing.T) {
	a, ak, _, _ := testUsers(t)
	legacy, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	host := sampleHost()
	if err := legacy.Add(host); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := ListHosts(a.ID); len(hosts) != 0 {
		t.Fatal("CLI list exposed to Web")
	}
	if err := SaveHost(a.ID, ak, host, Secret{Password: "web-only"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := DeleteHost(a.ID, host.Name); err != nil {
		t.Fatal(err)
	}
	legacy, err = config.Load()
	if err != nil || legacy.Find(host.Name) == nil {
		t.Fatal("Web modified CLI list")
	}
}

func TestFirstAdminAndDisabledUser(t *testing.T) {
	a, _, b, _ := testUsers(t)
	if _, _, err := CreateUser("second-admin", "test-password", true, nil); err == nil {
		t.Fatal("second bootstrap allowed")
	}
	if err := SetDisabled(a.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("administrator disabled")
	}
	if err := SetDisabled(b.ID, true); err != nil {
		t.Fatal(err)
	}
	if Active(b.ID) {
		t.Fatal("disabled user active")
	}
	if _, _, err := Authenticate(b.Username, "bobby-password"); !errors.Is(err, ErrLogin) {
		t.Fatal("disabled login allowed")
	}
}

func TestPrivateKeysAreOwnedEncryptedAndNeverShared(t *testing.T) {
	a, ak, b, _ := testUsers(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(block))
	host := sampleHost()
	host.AuthType = config.AuthPrivateKey
	host.IdentityFile = "/server/private/key"
	if err := SaveHost(a.ID, ak, host, Secret{PrivateKey: key}, false, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := FindHost(a.ID, host.Name)
	if err != nil || !stored.HasPrivateKey || stored.IdentityFile != "" || stored.SavePassword {
		t.Fatal("private-key metadata invalid")
	}
	secret, err := ReadSecret(a.ID, ak, host.Name)
	if err != nil || secret.PrivateKey != key {
		t.Fatal("private key lost")
	}
	metadata, _ := ListHosts(a.ID)
	raw, _ := json.Marshal(metadata)
	if strings.Contains(string(raw), "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatal("private key exposed in host list")
	}
	if err := ShareHost(a.ID, host.Name, []string{b.Username}); err != nil {
		t.Fatal(err)
	}
	shares, _ := Shares(b.ID)
	raw, _ = json.Marshal(shares)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "identity_file") || strings.Contains(string(raw), "has_private_key") {
		t.Fatal("shared private-key data")
	}
	path, _ := database.Path()
	raw, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatal("plaintext private key persisted")
	}
}

func TestConcurrentBootstrapCreatesOnlyOneAdministrator(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	results := make(chan error, 2)
	for _, name := range []string{"first-admin", "second-admin"} {
		go func(name string) {
			_, key, err := CreateUser(name, "test-password", true, nil)
			clear(key)
			results <- err
		}(name)
	}
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("bootstrap successes=%d", successes)
	}
	users, err := Users()
	if err != nil || len(users) != 1 || !users[0].Admin {
		t.Fatal("bootstrap created inconsistent users")
	}
}
