package copyhistory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
)

const Limit = 10

type Entry struct {
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	CompletedAt time.Time `json:"completed_at"`
}

type historyFile struct {
	Entries []Entry `json:"entries"`
}

func Load() ([]Entry, error) {
	path, err := filePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var history historyFile
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	if len(history.Entries) > Limit {
		history.Entries = history.Entries[:Limit]
	}
	return history.Entries, nil
}

func Record(entry Entry) error {
	entries, err := Load()
	if err != nil {
		return err
	}
	if entry.CompletedAt.IsZero() {
		entry.CompletedAt = time.Now()
	}
	entries = append([]Entry{entry}, entries...)
	if len(entries) > Limit {
		entries = entries[:Limit]
	}
	data, err := json.MarshalIndent(historyFile{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	path, err := filePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func filePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "copy-history.json"), nil
}
