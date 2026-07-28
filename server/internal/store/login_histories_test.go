package store

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginHistoryStorePaginationAndValidation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "login-history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.test','h','User')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	first, err := RecordLoginHistory(ctx, db, "u1", LoginMethodPassword, SessionMeta{
		IP: " 203.0.113.7 ", Location: " Paris, FR ", UserAgent: strings.Repeat("界", 1100),
	})
	if err != nil {
		t.Fatalf("record password login: %v", err)
	}
	second, err := RecordLoginHistory(ctx, db, "u1", LoginMethodOAuth2FA, SessionMeta{
		IP: "198.51.100.9", Location: "Tokyo, JP", UserAgent: "Test Browser",
	})
	if err != nil {
		t.Fatalf("record oauth+2fa login: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE login_histories SET login_at=100 WHERE id=?`, first.ID); err != nil {
		t.Fatalf("set first timestamp: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE login_histories SET login_at=200 WHERE id=?`, second.ID); err != nil {
		t.Fatalf("set second timestamp: %v", err)
	}

	rows, err := ListLoginHistoriesForUser(ctx, db, "u1", 1, 0)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != second.ID || rows[0].Method != LoginMethodOAuth2FA {
		t.Fatalf("first page = %+v, want newest OAuth+2FA row", rows)
	}
	rows, err = ListLoginHistoriesForUser(ctx, db, "u1", 1, 1)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != first.ID || rows[0].IP != "203.0.113.7" || rows[0].Location != "Paris, FR" {
		t.Fatalf("second page = %+v, want normalized password row", rows)
	}
	if got := len([]rune(rows[0].UserAgent)); got != 1024 {
		t.Fatalf("stored user-agent runes = %d, want 1024", got)
	}
	if count, err := CountLoginHistoriesForUser(ctx, db, "u1"); err != nil || count != 2 {
		t.Fatalf("count = %d, err=%v, want 2", count, err)
	}
	if _, err := RecordLoginHistory(ctx, db, "u1", "refresh", SessionMeta{}); err == nil {
		t.Fatal("invalid login method was accepted")
	}
}

func TestLoginHistoryBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	source, err := Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer source.Close()
	if err := Migrate(source); err != nil {
		t.Fatalf("migrate source: %v", err)
	}
	if _, err := source.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.test','h','User')`); err != nil {
		t.Fatalf("seed source user: %v", err)
	}
	original, err := RecordLoginHistory(ctx, source, "u1", LoginMethodOAuth, SessionMeta{
		IP: "203.0.113.8", Location: "Lyon, FR", UserAgent: "Backup Browser",
	})
	if err != nil {
		t.Fatalf("record source history: %v", err)
	}
	var dump bytes.Buffer
	if count, err := ExportTable(ctx, source, "login_histories", &dump); err != nil || count != 1 {
		t.Fatalf("export count=%d err=%v", count, err)
	}

	destination, err := Open(filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatalf("open destination: %v", err)
	}
	defer destination.Close()
	if err := Migrate(destination); err != nil {
		t.Fatalf("migrate destination: %v", err)
	}
	if _, err := destination.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.test','h','User')`); err != nil {
		t.Fatalf("seed destination user: %v", err)
	}
	if count, err := RestoreTable(ctx, destination, "login_histories", bytes.NewReader(dump.Bytes())); err != nil || count != 1 {
		t.Fatalf("restore count=%d err=%v", count, err)
	}
	rows, err := ListLoginHistoriesForUser(ctx, destination, "u1", 10, 0)
	if err != nil {
		t.Fatalf("list restored history: %v", err)
	}
	if len(rows) != 1 || rows[0] != *original {
		t.Fatalf("restored history = %+v, want %+v", rows, original)
	}
}
