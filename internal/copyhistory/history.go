package copyhistory

import (
	"encoding/json"
	"time"

	"github.com/zzqjlcn/xsh/internal/database"
)

const Limit = 10

type Entry struct {
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	CompletedAt time.Time `json:"completed_at"`
}

func Load() ([]Entry, error) {
	db, err := database.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM copy_history ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var entry Entry
		if err := json.Unmarshal([]byte(data), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func Record(entry Entry) error {
	if entry.CompletedAt.IsZero() {
		entry.CompletedAt = time.Now()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO copy_history(data) VALUES(?)`, string(data)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM copy_history WHERE id NOT IN (SELECT id FROM copy_history ORDER BY id DESC LIMIT 10)`); err != nil {
		return err
	}
	return tx.Commit()
}
