package webstore

import (
	"github.com/zzqjlcn/xsh/internal/config"
	"testing"
)

func TestPasswordCanBeEnteredOnlyWhenConnecting(t *testing.T) {
	user, key, _, _ := testUsers(t)
	host := config.SSHHost{Name: "prompt-password", Hostname: "example.test", Port: 22, User: "owner", AuthType: config.AuthPassword}
	if err := SaveHost(user.ID, key, host, Secret{}, false, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := FindHost(user.ID, host.Name)
	if err != nil || stored.SavePassword || stored.AuthType != config.AuthPassword {
		t.Fatalf("unexpected password-less profile: %+v, %v", stored, err)
	}
	if err := SaveHost(user.ID, key, host, Secret{Password: "saved"}, true, 0); err != nil {
		t.Fatal(err)
	}
	if err := SaveHost(user.ID, key, host, Secret{}, true, 0); err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecret(user.ID, key, host.Name)
	if err != nil || secret.Password != "saved" {
		t.Fatal("blank edit cleared saved password")
	}
}
