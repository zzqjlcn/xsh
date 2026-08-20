package config

import (
	"os"
	"testing"
)

func TestSSHHostValidate(t *testing.T) {
	tests := []struct {
		name    string
		host    SSHHost
		wantErr bool
	}{
		{name: "valid", host: SSHHost{Name: "dev", Hostname: "localhost", User: "root", Port: 22, AuthType: AuthAuto}},
		{name: "missing name", host: SSHHost{Hostname: "localhost", User: "root", Port: 22, AuthType: AuthAuto}, wantErr: true},
		{name: "bad port", host: SSHHost{Name: "dev", Hostname: "localhost", User: "root", Port: 70000, AuthType: AuthAuto}, wantErr: true},
		{name: "key without file", host: SSHHost{Name: "dev", Hostname: "localhost", User: "root", Port: 22, AuthType: AuthPrivateKey}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.Validate(); (got != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	host := SSHHost{Name: "dev", Hostname: "127.0.0.1", User: "root", Port: 22, AuthType: AuthAuto}
	if err := store.Add(host); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(host); err == nil {
		t.Fatal("duplicate host was accepted")
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Find("DEV"); got == nil || got.Hostname != host.Hostname {
		t.Fatalf("loaded host = %#v", got)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	host.Port = 2222
	if err := loaded.Update("dev", host); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Delete("dev"); err != nil {
		t.Fatal(err)
	}
}
