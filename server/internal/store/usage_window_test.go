package store

import (
	"context"
	"path/filepath"
	"testing"
)

// §B5-per-request usage rows: a credit-paid turn splits into several chat rows
// sharing one message_id; the cost remainder can land a row at 0 credits. The
// free-quota window (UsageInWindow) must decide free-vs-paid PER TURN (sum of a
// message_id's credits), not per row — otherwise the 0-credit split row leaks a
// fully credit-paid turn into the free window's count AND cost.
func TestUsageInWindowPerTurnCreditAggregation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "usagewin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@x.com','h','user')`)
	mustLog := func(u UsageLog) {
		if err := LogUsage(ctx, db, u); err != nil {
			t.Fatalf("log: %v", err)
		}
	}

	// Turn A — a FREE turn split across two requests (both credits 0).
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", MessageID: "mA", Purpose: "chat", Cost: 0.10})
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", MessageID: "mA", Purpose: "chat", Cost: 0.05})
	// Turn B — a CREDIT-PAID turn split so the remainder row lands at 0 credits.
	// Its total credits > 0, so the whole turn must be EXCLUDED from the free
	// window — including the 0-credit row's cost.
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", MessageID: "mB", Purpose: "chat", Cost: 0.20, Credits: 3.0})
	mustLog(UsageLog{UserID: "u1", ModelID: "m1", MessageID: "mB", Purpose: "chat", Cost: 0.30, Credits: 0})

	cost, count, err := UsageInWindow(ctx, db, "u1", "m1", 0)
	if err != nil {
		t.Fatalf("UsageInWindow: %v", err)
	}
	// Only turn A is free: 1 turn, cost 0.15. Turn B (0-credit split row included)
	// must contribute NOTHING.
	if count != 1 {
		t.Errorf("count = %d, want 1 (only the free turn; the credit-paid split turn excluded)", count)
	}
	if cost < 0.149 || cost > 0.151 {
		t.Errorf("cost = %.4f, want 0.15 (credit-paid turn's 0-credit row must not leak into free cost)", cost)
	}
}
