package copyhistory

import (
	"fmt"
	"testing"
	"time"
)

func TestRecordKeepsNewestTenEntries(t *testing.T) {
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	for index := 0; index < 12; index++ {
		entry := Entry{
			Source:      fmt.Sprintf("source-%d", index),
			Destination: "destination",
			CompletedAt: time.Unix(int64(index), 0),
		}
		if err := Record(entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != Limit {
		t.Fatalf("Load() returned %d entries, want %d", len(entries), Limit)
	}
	if entries[0].Source != "source-11" || entries[9].Source != "source-2" {
		t.Fatalf("unexpected history order: %#v", entries)
	}
}
