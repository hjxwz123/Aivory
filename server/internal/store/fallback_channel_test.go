package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestModelFallbackChannelRoundTrip verifies the new models.fallback_channel_id
// column persists through create/update/get (§fallback channel).
func TestModelFallbackChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "fb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	primary, err := CreateChannel(ctx, db, "Primary", "openai", "chat", "https://a.example", "ka")
	if err != nil {
		t.Fatalf("primary channel: %v", err)
	}
	backup, err := CreateChannel(ctx, db, "Backup", "openai", "chat", "https://b.example", "kb")
	if err != nil {
		t.Fatalf("backup channel: %v", err)
	}

	m, err := CreateModel(ctx, db, Model{ChannelID: primary.ID, RequestID: "gpt-x", Label: "GPT-X", FallbackChannelID: backup.ID})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if m.FallbackChannelID != backup.ID {
		t.Fatalf("create did not persist fallback: %q", m.FallbackChannelID)
	}
	got, err := GetModel(ctx, db, m.ID)
	if err != nil || got.FallbackChannelID != backup.ID {
		t.Fatalf("get fallback = %q (err %v), want %q", got.FallbackChannelID, err, backup.ID)
	}
	// Clearing it back to none.
	got.FallbackChannelID = ""
	if _, err := UpdateModel(ctx, db, got.ID, *got); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := GetModel(ctx, db, m.ID)
	if after.FallbackChannelID != "" {
		t.Fatalf("update did not clear fallback: %q", after.FallbackChannelID)
	}
}

// TestUsageChannelFallbackStatus verifies usage rows persist channel_id/fallback/
// status, that AdminUsageRecords surfaces them (with the channel-name join and a
// status=error filter), and that error rows are EXCLUDED from the quota reseed
// (UsageInWindow) — a failed request must not burn a count quota (§usage errors).
func TestUsageChannelFallbackStatus(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "usagefb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@x.com','h','user')`)
	primary, _ := CreateChannel(ctx, db, "Primary", "openai", "chat", "https://a", "ka")
	backup, _ := CreateChannel(ctx, db, "Backup", "openai", "chat", "https://b", "kb")

	// An OK primary row, an OK fallback-served row, and a failed (error) row.
	mustLog := func(u UsageLog) {
		if err := LogUsage(ctx, db, u); err != nil {
			t.Fatalf("log usage: %v", err)
		}
	}
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", Purpose: "chat", Cost: 0.1, Currency: "USD", ChannelID: primary.ID})
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", Purpose: "chat", Cost: 0.2, Currency: "USD", ChannelID: backup.ID, Fallback: true})
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", Purpose: "chat", Currency: "USD", ChannelID: backup.ID, Fallback: true, Status: "error", Error: "openai 500: rate limited"})

	rows, err := AdminUsageRecords(ctx, db, UsageFilter{}, 50, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("records = %d (err %v), want 3", len(rows), err)
	}
	// Locate the fallback OK row and assert channel-name join + flags.
	var okFallback, errRow *AdminUsageRecord
	for i := range rows {
		switch {
		case rows[i].Status == "error":
			errRow = &rows[i]
		case rows[i].Fallback:
			okFallback = &rows[i]
		}
	}
	if okFallback == nil || okFallback.ChannelName != "Backup" || okFallback.ChannelID != backup.ID {
		t.Fatalf("fallback row channel join wrong: %+v", okFallback)
	}
	if errRow == nil || !errRow.Fallback || errRow.Status != "error" {
		t.Fatalf("error row wrong: %+v", errRow)
	}
	// The upstream failure detail is surfaced to the admin list (§usage errors).
	if errRow.Error != "openai 500: rate limited" {
		t.Fatalf("error detail not surfaced: %q", errRow.Error)
	}

	// status=error filter returns only the failed row.
	only, _ := AdminUsageRecords(ctx, db, UsageFilter{Status: "error"}, 50, 0)
	if len(only) != 1 || only[0].Status != "error" {
		t.Fatalf("status filter = %+v, want 1 error row", only)
	}

	// Quota reseed counts the 2 OK chat rows but NOT the error row.
	cost, count, err := UsageInWindow(ctx, db, "u1", "m1", 0)
	if err != nil {
		t.Fatalf("UsageInWindow: %v", err)
	}
	if count != 2 {
		t.Errorf("UsageInWindow count = %d, want 2 (error row excluded)", count)
	}
	if cost < 0.29 || cost > 0.31 {
		t.Errorf("UsageInWindow cost = %.4f, want ~0.30", cost)
	}

	// The user-facing message count and the admin analytics totals both exclude
	// the error row so a failed request isn't counted as a delivered message /
	// distorts the dashboard (Calls stays consistent with cost).
	if _, mcount, _ := SumUsageByUser(ctx, db, "u1", 1); mcount != 2 {
		t.Errorf("SumUsageByUser count = %d, want 2 (error excluded)", mcount)
	}
	if totals, terr := AdminUsageTotals(ctx, db, 1); terr != nil || totals.Calls != 2 {
		t.Errorf("AdminUsageTotals Calls = %d (err %v), want 2 (error excluded)", totals.Calls, terr)
	}
}
