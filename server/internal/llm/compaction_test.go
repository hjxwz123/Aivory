package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"aurelia/server/internal/store"
)

// buildHistory makes n alternating user/assistant messages with small text
// blocks and stable ids m0..m{n-1}.
func buildHistory(n int) []store.Message {
	out := make([]store.Message, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: fmt.Sprintf("message %d content", i)}})
		out[i] = store.Message{ID: fmt.Sprintf("m%d", i), Role: role, Blocks: b}
	}
	return out
}

// TestMaybeCompactNoDoubleCompaction locks in §4.7's core guarantee: once a
// range is summarised it is NEVER summarised again. A later compaction only
// rolls up the messages after the previous summary's anchor (high-water mark),
// and earlier summary blocks stay byte-identical (stable prefix for the cache).
func TestMaybeCompactNoDoubleCompaction(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No settings table → GetSetting errors → defaults apply (keepRounds=6 →
	// keepMsgs=12, compaction enabled). task=nil → deterministic clip fallback.
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	// Pass 1: 16 messages → keep last 12, summarise m0..m3 (cut = 16-12 = 4).
	keep1, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(16))
	if err != nil {
		t.Fatal(err)
	}
	if len(keep1) != 12 {
		t.Fatalf("pass1 kept %d, want 12", len(keep1))
	}
	if len(blocks1) != 1 {
		t.Fatalf("pass1 got %d summary blocks, want 1", len(blocks1))
	}
	if blocks1[0].FromMessageID != "m0" || blocks1[0].AnchorMessageID != "m3" {
		t.Fatalf("pass1 block range = %s..%s, want m0..m3", blocks1[0].FromMessageID, blocks1[0].AnchorMessageID)
	}
	if blocks1[0].Text == "" {
		t.Fatal("pass1 summary text empty")
	}

	// Pass 2: history grew to 18; feed the prior summary back in.
	bjson, _ := json.Marshal(blocks1)
	conv.SummaryBlocks = bjson
	keep2, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18))
	if err != nil {
		t.Fatal(err)
	}
	if len(keep2) != 12 {
		t.Fatalf("pass2 kept %d, want 12", len(keep2))
	}
	if len(blocks2) != 2 {
		t.Fatalf("pass2 got %d summary blocks, want 2", len(blocks2))
	}
	// The first block must be UNCHANGED — not re-summarised.
	if blocks2[0].FromMessageID != "m0" || blocks2[0].AnchorMessageID != "m3" || blocks2[0].Text != blocks1[0].Text {
		t.Fatalf("pass2 re-summarised the old range: %+v", blocks2[0])
	}
	// The second block must cover ONLY the new range m4..m5.
	if blocks2[1].FromMessageID != "m4" || blocks2[1].AnchorMessageID != "m5" {
		t.Fatalf("pass2 new block range = %s..%s, want m4..m5", blocks2[1].FromMessageID, blocks2[1].AnchorMessageID)
	}

	// Pass 3: no growth → nothing new past the anchor → no extra block.
	bjson2, _ := json.Marshal(blocks2)
	conv.SummaryBlocks = bjson2
	_, blocks3, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks3) != 2 {
		t.Fatalf("pass3 re-compacted with no new messages: %d blocks", len(blocks3))
	}
}

// TestMaybeCompactTokenTriggerDeepens verifies the token budget compacts MORE
// aggressively than the round budget: with a tiny token trigger, the kept tail
// is reduced below keepMsgs (but never below the final round).
func TestMaybeCompactTokenTriggerDeepens(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Settings table present so we can force a tiny token trigger.
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	// keepRounds large (so the ROUND budget alone would keep everything), but a
	// tiny token trigger that the history easily exceeds.
	mustSet(t, db, "keep_recent_rounds", "100")
	mustSet(t, db, "compaction_token_trigger", "20")

	keep, blocks, err := MaybeCompact(context.Background(), db, nil,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		buildHistory(16))
	if err != nil {
		t.Fatal(err)
	}
	// Round budget (100 rounds) would keep all 16; the token trigger must force a
	// deeper cut, so the kept tail is smaller and a summary block is produced.
	if len(keep) >= 16 {
		t.Fatalf("token trigger did not deepen the cut: kept %d of 16", len(keep))
	}
	if len(keep) < 2 {
		t.Fatalf("token trigger compacted away the final round: kept %d", len(keep))
	}
	if len(blocks) == 0 {
		t.Fatal("token trigger produced no summary block")
	}
}

// TestMaybeCompactCutShrinkNoDuplicate covers the edge where the cut shrinks
// below a prior summary's anchor (e.g. keep_recent_rounds was raised). The
// already-summarised range must NOT be rolled up again into a duplicate block.
func TestMaybeCompactCutShrinkNoDuplicate(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// Disable the token trigger (also clears any cross-test cached value) and
	// start with keepRounds=6 (keepMsgs=12).
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	_, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(16))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks1) != 1 || blocks1[0].AnchorMessageID != "m3" {
		t.Fatalf("pass1 unexpected blocks: %+v", blocks1)
	}
	bjson, _ := json.Marshal(blocks1)
	conv.SummaryBlocks = bjson

	// Raise keep_recent_rounds → keepMsgs=16; with 18 messages the cut is 2,
	// which is BELOW the prior anchor (m3). Must not duplicate.
	if err := store.SetSetting(db, "keep_recent_rounds", 8); err != nil {
		t.Fatal(err)
	}
	_, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks2) != 1 {
		t.Fatalf("cut shrink created a duplicate summary block: got %d, want 1", len(blocks2))
	}
}

func mustSet(t *testing.T, db *sql.DB, key, jsonVal string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, key, jsonVal); err != nil {
		t.Fatal(err)
	}
}
