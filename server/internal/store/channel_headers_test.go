package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLegacyChannelHeadersMigrationIsIdempotent(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE channels (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL,
		api_format TEXT NOT NULL DEFAULT '', base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
	); INSERT INTO channels(id,name,type,api_key) VALUES('legacy','Legacy','openai','preserved-key')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		channel, err := GetChannel(context.Background(), db, "legacy")
		if err != nil || channel.Headers == nil || len(channel.Headers) != 0 || channel.APIKey != "preserved-key" {
			t.Fatalf("migration %d: channel=%v err=%v", i, channel, err)
		}
	}
}
