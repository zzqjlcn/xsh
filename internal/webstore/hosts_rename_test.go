package webstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/zzqjlcn/xsh/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestRenameHostPreservesSecretsAndJumpReferences(t *testing.T) {
	for _, auth := range []config.AuthType{config.AuthPassword, config.AuthPrivateKey} {
		t.Run(string(auth), func(t *testing.T) {
			user, key, other, otherKey := testUsers(t)
			host := sampleHost()
			host.AuthType = auth
			secret := Secret{Password: "saved-password"}
			if auth == config.AuthPrivateKey {
				_, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				block, err := ssh.MarshalPrivateKey(private, "")
				if err != nil {
					t.Fatal(err)
				}
				secret = Secret{PrivateKey: string(pem.EncodeToMemory(block))}
			}
			if err := SaveHost(user.ID, key, host, secret, false, 0); err != nil {
				t.Fatal(err)
			}
			if err := RecordLogin(user.ID, host.Name); err != nil {
				t.Fatal(err)
			}
			before, _ := FindHost(user.ID, host.Name)
			child := sampleHost()
			child.Name = "child"
			child.ProxyJump = host.Name
			if err := SaveHost(user.ID, key, child, Secret{}, false, 0); err != nil {
				t.Fatal(err)
			}
			if err := SaveHost(other.ID, otherKey, child, Secret{}, false, 0); err != nil {
				t.Fatal(err)
			}
			original := host.Name
			host.Name = "renamed"
			if err := UpdateHost(user.ID, key, original, host, Secret{}); err != nil {
				t.Fatal(err)
			}
			if _, err := FindHost(user.ID, original); !errors.Is(err, ErrNotFound) {
				t.Fatal("old name exists")
			}
			got, err := ReadSecret(user.ID, key, host.Name)
			if err != nil || got != secret {
				t.Fatalf("secret lost: %v", err)
			}
			stored, _ := FindHost(user.ID, host.Name)
			if stored.LastLogin != before.LastLogin || stored.HasPrivateKey != (auth == config.AuthPrivateKey) {
				t.Fatal("metadata lost")
			}
			storedChild, _ := FindHost(user.ID, child.Name)
			if storedChild.ProxyJump != host.Name {
				t.Fatal("jump reference not renamed")
			}
			otherChild, _ := FindHost(other.ID, child.Name)
			if otherChild.ProxyJump != original {
				t.Fatal("other account modified")
			}
			host.Name = "RENAMED"
			if err := UpdateHost(user.ID, key, "renamed", host, Secret{}); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadSecret(user.ID, key, "RENAMED"); err != nil {
				t.Fatal(err)
			}
			host.Name = "CHILD"
			if err := UpdateHost(user.ID, key, "RENAMED", host, Secret{}); !errors.Is(err, ErrConflict) {
				t.Fatalf("collision accepted: %v", err)
			}
			if got, err := ReadSecret(user.ID, key, "RENAMED"); err != nil || got != secret {
				t.Fatal("collision damaged original")
			}
			if err := UpdateHost(user.ID, key, "missing", host, Secret{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing update: %v", err)
			}
		})
	}
}
