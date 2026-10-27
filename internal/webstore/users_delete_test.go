package webstore

import (
	"errors"
	"testing"

	"github.com/zzqjlcn/xsh/internal/database"
)

func TestDeleteUserCleansOwnedData(t *testing.T) {
	admin, adminKey, member, memberKey := testUsers(t)
	for _, item := range []struct {
		user User
		key  []byte
	}{{admin, adminKey}, {member, memberKey}} {
		if err := SaveHost(item.user.ID, item.key, sampleHost(), Secret{Password: "saved-secret"}, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := ShareHost(admin.ID, "prod", []string{member.Username}); err != nil {
		t.Fatal(err)
	}
	if err := ShareHost(member.ID, "prod", []string{admin.Username}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("administrator deletion: %v", err)
	}
	if err := SetDisabled(member.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(member.ID); err != nil {
		t.Fatal(err)
	}
	if SessionValid(member) {
		t.Fatal("deleted account still valid")
	}
	if _, _, err := Authenticate(member.Username, "bobby-password"); !errors.Is(err, ErrLogin) {
		t.Fatalf("deleted account can log in: %v", err)
	}
	db, err := database.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{`SELECT count(*) FROM web_hosts WHERE user_id=?`, `SELECT count(*) FROM web_shares WHERE sender_id=? OR recipient_id=?`} {
		args := []any{member.ID}
		if query == `SELECT count(*) FROM web_shares WHERE sender_id=? OR recipient_id=?` {
			args = append(args, member.ID)
		}
		var count int
		if err := db.QueryRow(query, args...).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphaned account data: count=%d err=%v", count, err)
		}
	}
	if _, err := ReadSecret(admin.ID, adminKey, "prod"); err != nil {
		t.Fatalf("other account data affected: %v", err)
	}
	if err := DeleteUser(member.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat deletion: %v", err)
	}
}
