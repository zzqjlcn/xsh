//go:build !windows

package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenNewDatabasePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", dir)
	db, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(filepath.Join(dir, "xsh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new database permissions: %o, want 600", info.Mode().Perm())
	}
}

func TestOpenPreservesExistingDatabasePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XSH_CONFIG_HOME", dir)
	filename := filepath.Join(dir, "xsh.db")
	if err := os.WriteFile(filename, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, 0660); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO hosts(name,data) VALUES('test','{}')`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0660 {
			t.Fatalf("existing database permissions changed to %o", info.Mode().Perm())
		}
	}
}
