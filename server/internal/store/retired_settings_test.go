package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateDeletesAndBackupOmitsRetiredCompactionSettings(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "retired-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"summary_target_percent":   "30",
		"summary_merge_max_tokens": "8192",
		"summary_max_tokens":       "2048",
	} {
		if _, err := db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}

	var exported bytes.Buffer
	if _, err := ExportTable(context.Background(), db, "settings", &exported); err != nil {
		t.Fatal(err)
	}
	if got := exported.String(); strings.Contains(got, "summary_target_percent") || strings.Contains(got, "summary_merge_max_tokens") {
		t.Fatalf("backup exported retired settings: %s", got)
	}
	if !strings.Contains(exported.String(), "summary_max_tokens") {
		t.Fatalf("backup lost current summary setting: %s", exported.String())
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"summary_target_percent", "summary_merge_max_tokens"} {
		var value string
		err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&value)
		if err != sql.ErrNoRows {
			t.Fatalf("retired setting %q still exists: value=%q err=%v", key, value, err)
		}
	}
}
