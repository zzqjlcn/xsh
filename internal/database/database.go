package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func Dir() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XSH_CONFIG_HOME"))
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "xsh")
	}
	return dir, os.MkdirAll(dir, 0700)
}

func Path() (string, error) { dir, err := Dir(); return filepath.Join(dir, "xsh.db"), err }

// Open imports legacy files once, in the same transaction as the schema setup.
// Legacy files are retained as backups; credentials remain encrypted.
func Open() (*sql.DB, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	// Restrict newly created files, but preserve permissions on existing mounts.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS hosts (name TEXT PRIMARY KEY COLLATE NOCASE, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS copy_history (id INTEGER PRIMARY KEY AUTOINCREMENT, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS vault (id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS migrations (name TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS web_users (
 id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE,
 admin INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0,
 salt BLOB NOT NULL, verifier BLOB NOT NULL, wrapped_key BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS web_hosts (
 user_id INTEGER NOT NULL, name TEXT NOT NULL COLLATE NOCASE,
 data TEXT NOT NULL, secret BLOB NOT NULL, PRIMARY KEY(user_id,name));
 CREATE TABLE IF NOT EXISTS web_shares (
 id INTEGER PRIMARY KEY, sender_id INTEGER NOT NULL, recipient_id INTEGER NOT NULL,
 data TEXT NOT NULL, created_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS web_user_security (
 user_id INTEGER PRIMARY KEY, session_hours INTEGER NOT NULL);`)
	if err == nil {
		err = migrate(tx, filepath.Dir(path))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(tx *sql.Tx, dir string) error {
	var done string
	err := tx.QueryRow(`SELECT name FROM migrations WHERE name='legacy-json'`).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, name := range []string{"hosts.json", "copy-history.json", "secrets.db"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		switch name {
		case "hosts.json":
			var old struct {
				Hosts []json.RawMessage `json:"hosts"`
			}
			if err := json.Unmarshal(data, &old); err != nil {
				return err
			}
			for _, raw := range old.Hosts {
				var host struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(raw, &host); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO hosts(name,data) VALUES(?,?)`, host.Name, string(raw)); err != nil {
					return err
				}
			}
		case "copy-history.json":
			var old struct {
				Entries []json.RawMessage `json:"entries"`
			}
			if err := json.Unmarshal(data, &old); err != nil {
				return err
			}
			if len(old.Entries) > 10 {
				old.Entries = old.Entries[:10]
			}
			for i := len(old.Entries) - 1; i >= 0; i-- {
				if _, err := tx.Exec(`INSERT INTO copy_history(data) VALUES(?)`, string(old.Entries[i])); err != nil {
					return err
				}
			}
		case "secrets.db":
			if !json.Valid(data) {
				return errors.New("invalid legacy encrypted vault")
			}
			if _, err := tx.Exec(`INSERT INTO vault(id,data) VALUES(1,?)`, data); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`INSERT INTO migrations(name) VALUES('legacy-json')`)
	return err
}

func ReadVault() ([]byte, error) {
	db, err := Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var data []byte
	err = db.QueryRow(`SELECT data FROM vault WHERE id=1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		err = os.ErrNotExist
	}
	return data, err
}

func WriteVault(data []byte) error {
	db, err := Open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO vault(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, data)
	return err
}
