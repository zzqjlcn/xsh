package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyMigrationIsAtomicAndRunsOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", dir)
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("hosts.json", `{"hosts":[{"name":"dev","hostname":"example.test"}]}`)
	write("copy-history.json", `{"entries":[{"source":"new"},{"source":"old"}]}`)
	write("secrets.db", `{"version":1,"ciphertext":"encrypted-only"}`)
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM hosts`).Scan(&name); err != nil || name != "dev" {
		t.Fatalf("host: %s %v", name, err)
	}
	var data string
	if err := db.QueryRow(`SELECT data FROM copy_history ORDER BY id DESC LIMIT 1`).Scan(&data); err != nil || data != `{"source":"new"}` {
		t.Fatalf("history: %s %v", data, err)
	}
	if _, err := db.Exec(`DELETE FROM hosts`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hosts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reimported: %d %v", count, err)
	}
	for _, name := range []string{"hosts.json", "copy-history.json", "secrets.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	vault, err := ReadVault()
	if err != nil || string(vault) != `{"version":1,"ciphertext":"encrypted-only"}` {
		t.Fatalf("vault: %s %v", vault, err)
	}
}

func TestMalformedMigrationCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", dir)
	path := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(path, []byte(`broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(); err == nil {
		db.Close()
		t.Fatal("invalid legacy data accepted")
	}
	if err := os.WriteFile(path, []byte(`{"hosts":[{"name":"fixed"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hosts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry: %d %v", count, err)
	}
}
