package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/store"
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

func buildHistoryRange(start, end int) []store.Message {
	out := make([]store.Message, 0, end-start)
	for i := start; i < end; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: fmt.Sprintf("message %d content", i)}})
		out = append(out, store.Message{ID: fmt.Sprintf("m%d", i), Role: role, Blocks: b})
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
	keep1, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(16), 0, "u1")
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
	keep2, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18), 0, "u1")
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
	_, blocks3, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18), 0, "u1")
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
		buildHistory(16), 0, "u1")
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

	_, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(16), 0, "u1")
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
	keep2, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, buildHistory(18), 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks2) != 1 {
		t.Fatalf("cut shrink created a duplicate summary block: got %d, want 1", len(blocks2))
	}
	if len(keep2) == 0 || keep2[0].ID != "m4" {
		start := "<empty>"
		if len(keep2) > 0 {
			start = keep2[0].ID
		}
		t.Fatalf("inline tail starts at %s, want m4 (after existing summary anchor)", start)
	}
}

func TestFilterBlocksForPathDropsContainedCrossBranchOverlap(t *testing.T) {
	shared := buildHistory(10) // m0..m9 shared prefix
	textBlock, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "branch turn"}})
	histA := append(append([]store.Message{}, shared...),
		store.Message{ID: "a10", Role: "user", Blocks: textBlock},
		store.Message{ID: "a11", Role: "assistant", Blocks: textBlock},
	)
	histB := append(append([]store.Message{}, shared...),
		store.Message{ID: "b10", Role: "user", Blocks: textBlock},
		store.Message{ID: "b11", Role: "assistant", Blocks: textBlock},
	)
	blocks := []SummaryBlock{
		{FromMessageID: "m0", AnchorMessageID: "a11", Text: "A branch recap"},
		{FromMessageID: "m0", AnchorMessageID: "m9", Text: "shared prefix recap from B"},
	}

	gotA := filterBlocksForPath(blocks, histA)
	if len(gotA) != 1 || gotA[0].Text != "A branch recap" {
		t.Fatalf("A path blocks = %+v, want only the containing A-branch recap", gotA)
	}
	gotB := filterBlocksForPath(blocks, histB)
	if len(gotB) != 1 || gotB[0].Text != "shared prefix recap from B" {
		t.Fatalf("B path blocks = %+v, want only the shared-prefix recap", gotB)
	}
}

func TestMaybeCompactSkipsWriteWhenSummarizedMessagesDeleted(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, blocks TEXT, raw TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1','[]')`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	hist := buildHistory(16) // normally summarises m0..m3
	for _, m := range hist {
		if m.ID == "m1" {
			continue // deleted after the compaction snapshot was taken
		}
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, blocks, raw) VALUES(?, 'c1', ?, ?)`, m.ID, string(m.Blocks), string(m.Raw)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, nil,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("deleted snapshot row was summarised: got blocks %+v, want none", blocks)
	}
	var raw string
	if err := db.QueryRow(`SELECT summary_blocks FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "[]" {
		t.Fatalf("summary_blocks persisted deleted content: %s", raw)
	}
}

func TestMaybeCompactSkipsWriteWhenSummarizedMessagesEdited(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, blocks TEXT, raw TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1','[]')`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	hist := buildHistory(16) // normally summarises m0..m3
	editedBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "edited after compaction snapshot"}})
	for _, m := range hist {
		blocks := string(m.Blocks)
		if m.ID == "m1" {
			blocks = string(editedBlocks) // edited while task-model summary was in flight
		}
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, blocks, raw) VALUES(?, 'c1', ?, ?)`, m.ID, blocks, string(m.Raw)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, nil,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("edited snapshot row was summarised: got blocks %+v, want none", blocks)
	}
	var raw string
	if err := db.QueryRow(`SELECT summary_blocks FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "[]" {
		t.Fatalf("summary_blocks persisted stale edited content: %s", raw)
	}
}

func TestMaybeCompactBridgesPrunedSummaryGap(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, blocks TEXT, raw TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	hist := append(buildHistory(4), buildHistoryRange(6, 22)...)
	for _, m := range hist {
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, blocks, raw) VALUES(?, 'c1', ?, ?)`, m.ID, string(m.Blocks), string(m.Raw)); err != nil {
			t.Fatal(err)
		}
	}
	existing := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m3", Text: "prefix recap", Tokens: 10},
		// Simulates a later block that survived after DeleteRound pruned the middle
		// block covering m4..m7. This block must not let the frontier jump past m6/m7.
		{Level: 1, FromMessageID: "m8", AnchorMessageID: "m9", Text: "later recap", Tokens: 10},
	}
	raw, _ := json.Marshal(existing)
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1', ?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: raw}

	path := filterBlocksForPath(existing, hist)
	if len(path) != 1 || path[0].AnchorMessageID != "m3" {
		t.Fatalf("path filter kept disconnected later block: %+v", path)
	}
	keep, blocks, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks after bridging gap = %+v, want prefix + new bridge block", blocks)
	}
	if blocks[1].FromMessageID != "m6" || blocks[1].AnchorMessageID != "m9" {
		t.Fatalf("bridge range = %s..%s, want m6..m9", blocks[1].FromMessageID, blocks[1].AnchorMessageID)
	}
	if len(keep) == 0 || keep[0].ID != "m10" {
		t.Fatalf("keep starts at %+v, want m10 after bridging m6..m9", keep)
	}
}

func TestMergeOldestBlocksFoldsAtLeastTwo(t *testing.T) {
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("alpha ", 80), Tokens: 120},
		{Level: 7, FromMessageID: "m2", AnchorMessageID: "m3", Text: strings.Repeat("beta ", 80), Tokens: 120},
		{Level: 1, FromMessageID: "m4", AnchorMessageID: "m5", Text: "tail", Tokens: 5},
	}
	got2 := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", blocks[:2], 256)
	if len(got2) != 1 || got2[0].FromMessageID != "m0" || got2[0].AnchorMessageID != "m3" {
		t.Fatalf("2-block merge = %+v, want one coarse block covering m0..m3", got2)
	}
	if got2[0].Level != 8 {
		t.Fatalf("coarse level = %d, want max+1 = 8", got2[0].Level)
	}
	got3 := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", blocks, 256)
	if len(got3) != 2 || got3[0].AnchorMessageID != "m3" || got3[1].AnchorMessageID != "m5" {
		t.Fatalf("3-block merge = %+v, want first two folded and tail preserved", got3)
	}
}

func TestCJKFallbacksClipByTokens(t *testing.T) {
	cjk := strings.Repeat("汉", 1000)
	msgBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: cjk}})
	clipped := clipOlder([]store.Message{{Blocks: msgBlocks}}, 300)
	if estimateTokens(clipped) > 300 {
		t.Fatalf("clipOlder CJK estimate = %d, want <= 300", estimateTokens(clipped))
	}
	if len([]rune(clipped)) >= len([]rune(cjk)) || !strings.Contains(clipped, "…") {
		t.Fatalf("clipOlder did not visibly truncate CJK text: len=%d", len([]rune(clipped)))
	}

	merged := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: cjk, Tokens: estimateTokens(cjk)},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "m3", Text: cjk, Tokens: estimateTokens(cjk)},
	}, 256)
	if len(merged) != 1 {
		t.Fatalf("merge fallback produced %d blocks, want 1", len(merged))
	}
	if estimateTokens(merged[0].Text) > 128 {
		t.Fatalf("merge fallback CJK estimate = %d, want <= 128", estimateTokens(merged[0].Text))
	}
}

func TestCompactionDisabledReturnsNoSummaryBlocks(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer func() { _ = store.SetSetting(db, "compaction_enabled", true) }()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", false); err != nil {
		t.Fatal(err)
	}
	hist := buildHistory(4)
	raw, _ := json.Marshal([]SummaryBlock{{
		Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: "old recap", Tokens: 9,
	}})
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: raw}

	keep, blocks, action := PlanCompaction(db, conv, hist, 0)
	if action != compactNone || len(keep) != len(hist) || len(blocks) != 0 {
		t.Fatalf("PlanCompaction disabled: action=%d keep=%d blocks=%d, want none/full/0", action, len(keep), len(blocks))
	}
	keep, blocks, err = MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != len(hist) || len(blocks) != 0 {
		t.Fatalf("MaybeCompact disabled: keep=%d blocks=%d, want full/0", len(keep), len(blocks))
	}
}

func mustSet(t *testing.T, db *sql.DB, key, jsonVal string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, key, jsonVal); err != nil {
		t.Fatal(err)
	}
}

// TestMaybeCompactConcurrentNoDuplicate locks in the lost-update fix: two turns
// that both read the SAME stale (empty) summary_blocks snapshot — the race from
// a double-send / regenerate-while-streaming — must not append a duplicate
// summary for the same message range. The CAS re-read + coverage guard catches it.
func TestMaybeCompactConcurrentNoDuplicate(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1','[]')`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	hist := buildHistory(16) // cut=4 → summarise m0..m3

	convA := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	if _, b1, err := MaybeCompact(context.Background(), db, nil, convA, hist, 0, "u1"); err != nil || len(b1) != 1 {
		t.Fatalf("first compaction: blocks=%v err=%v", b1, err)
	}
	// convB read the empty snapshot BEFORE convA wrote — the stale concurrent turn.
	convB := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	_, b2, err := MaybeCompact(context.Background(), db, nil, convB, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(b2) != 1 {
		t.Fatalf("stale second compaction duplicated the range: got %d blocks, want 1", len(b2))
	}
	var raw string
	if err := db.QueryRow(`SELECT summary_blocks FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var blks []SummaryBlock
	_ = json.Unmarshal([]byte(raw), &blks)
	if len(blks) != 1 {
		t.Fatalf("conversations.summary_blocks has %d blocks, want 1 (lost-update not prevented)", len(blks))
	}
}

// TestMaybeCompactConcurrentDeeperCutNoOverlap locks in the overlap fix: a stale
// concurrent turn that computes a DEEPER cut (it saw more history) than the turn
// that already wrote must NOT append an OVERLAPPING block (the same early rounds
// summarised twice). The range-aware coverage check adopts the current blocks and
// keeps the uncovered tail verbatim instead.
func TestMaybeCompactConcurrentDeeperCutNoOverlap(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1','[]')`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	// Turn A: 16 messages → cut=4 → summarise m0..m3, persisted to the DB.
	convA := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	if _, b1, err := MaybeCompact(context.Background(), db, nil, convA, buildHistory(16), 0, "u1"); err != nil || len(b1) != 1 {
		t.Fatalf("turn A: blocks=%v err=%v", b1, err)
	}
	// Turn B read the empty snapshot but sees 18 messages → a DEEPER cut (m0..m5).
	convB := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	keepB, b2, err := MaybeCompact(context.Background(), db, nil, convB, buildHistory(18), 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(b2) != 1 {
		t.Fatalf("deeper concurrent cut created overlapping blocks: got %d, want 1", len(b2))
	}
	if b2[0].FromMessageID != "m0" || b2[0].AnchorMessageID != "m3" {
		t.Fatalf("block range = %s..%s, want m0..m3 (not the deeper m0..m5)", b2[0].FromMessageID, b2[0].AnchorMessageID)
	}
	// The uncovered tail (m4, m5) must be kept VERBATIM, not silently dropped.
	if len(keepB) == 0 || keepB[0].ID != "m4" {
		t.Fatalf("uncovered tail not kept verbatim: keep starts at %+v", keepB)
	}
	var raw string
	if err := db.QueryRow(`SELECT summary_blocks FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var blks []SummaryBlock
	_ = json.Unmarshal([]byte(raw), &blks)
	if len(blks) != 1 {
		t.Fatalf("DB has %d blocks, want 1 (overlap persisted)", len(blks))
	}
}

// TestPlanCompactionHotPath verifies the synchronous planner makes NO task-model
// call: it keeps the recent tail verbatim and only signals how to advance.
func TestPlanCompactionHotPath(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	// Short conversation (< keepMsgs=12) → nothing to summarise.
	keep, blocks, action := PlanCompaction(db, conv, buildHistory(8), 0)
	if action != compactNone || len(keep) != 8 || len(blocks) != 0 {
		t.Fatalf("short conv: action=%d keep=%d blocks=%d, want none/8/0", action, len(keep), len(blocks))
	}
	// Overflow (> 12, ≤ 36) → advance asynchronously, keep all verbatim this turn.
	keep2, _, action2 := PlanCompaction(db, conv, buildHistory(20), 0)
	if action2 != compactAsync || len(keep2) != 20 {
		t.Fatalf("overflow conv: action=%d keep=%d, want async/20", action2, len(keep2))
	}
	// Large cold-start backlog (> 36) → summarise inline to bound the prompt.
	if _, _, action3 := PlanCompaction(db, conv, buildHistory(40), 0); action3 != compactInline {
		t.Fatalf("large backlog: action=%d, want inline", action3)
	}
}

// setLastAssistantInput stamps the newest assistant message with a real recorded
// prompt size so contextTokens reports it as `exact`.
func setLastAssistantInput(h []store.Message, n int) {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "assistant" {
			h[i].InputTokens = n
			return
		}
	}
}

// TestPlanCompactionInlineOnBigTokenOverflow locks in the token-magnitude inline
// path: a message-LIGHT history (tail ≤ keepRounds*2*3, so the backlog gate stays
// quiet) whose last turn recorded a REAL prompt well past 1.25× the trigger is
// summarised INLINE this turn — otherwise a few huge code/plot turns overflow on
// tokens but not on message count and make the turn pay one full-price spike
// before the async pass. Mild and estimate-only overflows still go async.
func TestPlanCompactionInlineOnBigTokenOverflow(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Pin the settings we depend on — GetSetting has a PROCESS-WIDE cache, so a
	// sibling test's `compaction_token_trigger=0` would otherwise leak in and
	// disable the token path (SetSetting refreshes the cache for this db).
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 32000); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	// 14 msgs: tail=14 > keepRounds*2 (12) so it overflows, but ≤ keepRounds*2*3 (36)
	// so the message-count backlog gate does NOT fire — inline must come from tokens.
	big := buildHistory(14)
	setLastAssistantInput(big, 50000) // real prompt 50000 > 1.25×32000 = 40000
	if _, _, action := PlanCompaction(db, conv, big, 0); action != compactInline {
		t.Fatalf("real ctx 50000 (>1.25×trigger), 14 msgs: action=%d, want inline", action)
	}

	// Mild overflow: real prompt over the trigger but under the 1.25× inline bar →
	// stay async so a task-model round-trip isn't added to first token every turn.
	mild := buildHistory(14)
	setLastAssistantInput(mild, 33000) // 32000 < 33000 < 40000
	if _, _, action := PlanCompaction(db, conv, mild, 0); action != compactAsync {
		t.Fatalf("real ctx 33000 (<1.25×trigger): action=%d, want async", action)
	}

	// Estimate-only overflow (no recorded usage → exact=false) must NOT inline: we
	// never stall first token on a shaky estimate. Small blocks keep the estimate
	// tiny, so this stays async via the round-budget overflow.
	est := buildHistory(14) // no InputTokens anywhere → exact=false
	if _, _, action := PlanCompaction(db, conv, est, 0); action != compactAsync {
		t.Fatalf("estimate-only, no real count: action=%d, want async", action)
	}
}

// TestEstimateMsgTokensConcurrent exercises the memo under concurrent access so
// `go test -race` proves the size-bound reset no longer races Load/Store (the
// previous build reassigned a sync.Map under a bare Load — a data race).
func TestEstimateMsgTokensConcurrent(t *testing.T) {
	msgs := make([]store.Message, 64)
	for i := range msgs {
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: fmt.Sprintf("content %d %d", i, i*7)}})
		msgs[i] = store.Message{ID: fmt.Sprintf("cm%d", i), Role: "user", Blocks: b}
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < 500; k++ {
				_ = estimateMsgTokens(msgs[(g+k)%len(msgs)])
			}
		}(g)
	}
	wg.Wait()
}

// TestEstimateTokensNonLatin guards the estimator against the catastrophic
// undercount of whitespace-free non-Latin runs (emoji, CJK Ext-B, punctuation).
func TestEstimateTokensNonLatin(t *testing.T) {
	if got := estimateTokens(strings.Repeat("😀", 50)); got < 20 {
		t.Fatalf("50 emoji estimated %d tokens, want ≥20 (was ~2 before the fix)", got)
	}
	if got := estimateTokens("、。「」"); got < 4 {
		t.Fatalf("CJK punctuation estimated %d, want ≥4", got)
	}
	if got := estimateTokens(strings.Repeat("\U00020000", 40)); got < 30 {
		t.Fatalf("40 CJK Ext-B ideographs estimated %d, want ≥30", got)
	}
}

// TestContextTokensCountsInjectedOverhead locks in the §4.7 first-turn fix:
// freshly-injected RAG/uploaded-file content (injectedOverhead) — which is NOT
// yet in the message history — must count toward the compaction trigger size, so
// the first turn after an upload isn't blind to the file. It must count both on
// the heuristic fallback (no prior recorded usage) and as a floor over the real
// last-turn provider count (a file injected THIS turn the previous turn lacked).
func TestContextTokensCountsInjectedOverhead(t *testing.T) {
	// Fallback path: no assistant row has input_tokens yet.
	hist := []store.Message{
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`)},
	}
	base, exact := contextTokens(hist, nil, 0)
	if exact {
		t.Fatal("expected fallback (no prior input_tokens) to report exact=false")
	}
	if withFile, _ := contextTokens(hist, nil, 5000); withFile != base+5000 {
		t.Fatalf("injected overhead not counted on fallback: base=%d withFile=%d (want %d)", base, withFile, base+5000)
	}

	// Exact path: a prior assistant turn recorded only 1000 input tokens, but THIS
	// turn injects 5000 of new file content → the larger estimate must win so the
	// trigger doesn't lag a turn behind the upload.
	hist2 := []store.Message{
		{Role: "assistant", InputTokens: 1000},
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`)},
	}
	got, exact2 := contextTokens(hist2, nil, 5000)
	if !exact2 {
		t.Fatal("expected exact=true when a prior assistant input_tokens exists")
	}
	if got < 5000 {
		t.Fatalf("injected overhead ignored on exact path: got=%d, want ≥5000", got)
	}

	// And when the real last-turn count already dominates, it wins unchanged.
	hist3 := []store.Message{
		{Role: "assistant", InputTokens: 80000, CacheReadTokens: 0},
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`)},
	}
	if got, _ := contextTokens(hist3, nil, 500); got != 80000 {
		t.Fatalf("real last-turn count should dominate a small overhead: got=%d, want 80000", got)
	}
}

// TestContextTokensFrontierAware locks in the exact-mislabeling fix: rows
// already rolled into summary blocks must NOT inflate the estimate. A previous
// build estimated the FULL history, so on a compacted conversation est exceeded
// the provider's real count forever, was returned as exact=true, and forced the
// bigTokenOverflow INLINE path (a task-model call before first token) on every
// subsequent turn. Frontier-aware, the estimate is tail+summaries+injection and
// the real count dominates again.
func TestContextTokensFrontierAware(t *testing.T) {
	// A small kept tail (what will actually be sent) + a summary block; the last
	// assistant recorded a real prompt of 3000 tokens.
	fat, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("word ", 200)}})
	kept := []store.Message{
		{ID: "k0", Role: "user", Blocks: fat},
		{ID: "k1", Role: "assistant", Blocks: fat, InputTokens: 3000},
		{ID: "k2", Role: "user", Blocks: fat},
	}
	blocks := []SummaryBlock{{AnchorMessageID: "old9", FromMessageID: "old0", Text: "recap", Tokens: 60}}
	got, exact := contextTokens(kept, blocks, 0)
	if !exact {
		t.Fatal("expected exact=true with a recorded last-turn count")
	}
	// tail ≈ 3×270 + summary 60 ≪ 3000 → the REAL count must win. The old
	// full-history estimate (with dozens of summarised fat rows) would have
	// exceeded it and been returned instead.
	if got != 3000 {
		t.Fatalf("frontier-aware estimate should let the real count dominate: got=%d, want 3000", got)
	}
}

// TestPlanCompactionNoInlineOnSummarizedBulk is the end-to-end regression for
// the permanent-inline bug: a LONG conversation whose bulk is already covered
// by a summary block — so the real prompt is small — must NOT trip the
// bigTokenOverflow inline path just because the raw history estimate is large.
func TestPlanCompactionNoInlineOnSummarizedBulk(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 3000); err != nil {
		t.Fatal(err)
	}

	// 40 fat messages (~250 estimated tokens each → full-history estimate ≈ 10k,
	// well past 1.25×3000) with everything up to m27 already summarised. The kept
	// tail (m28..m39, 12 msgs ≈ 3k... trimmed to stay under) — use modest text so
	// tail+summary stays under the 3000 trigger.
	hist := make([]store.Message, 40)
	for i := range hist {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("tok ", 150)}})
		hist[i] = store.Message{ID: fmt.Sprintf("m%d", i), Role: role, Blocks: b}
	}
	// Last assistant's REAL recorded prompt: comfortably under the trigger.
	setLastAssistantInput(hist, 2500)
	blocks, _ := json.Marshal([]SummaryBlock{{
		Level: 1, FromMessageID: "m0", AnchorMessageID: "m27", Text: "recap", Tokens: 80,
	}})
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: blocks}

	keep, _, action := PlanCompaction(db, conv, hist, 0)
	if action == compactInline {
		t.Fatalf("summarised bulk must not force the inline path (real prompt is small); got compactInline")
	}
	// Sanity: the verbatim tail starts after the summarised frontier.
	if len(keep) != 12 || keep[0].ID != "m28" {
		t.Fatalf("keep = %d msgs starting %s, want 12 starting m28", len(keep), keep[0].ID)
	}
}

// TestMaybeCompactStaleRealCountNoOverDeepening locks in the overhead-baseline
// choice: the newest recorded provider count can be STALE — measured on the turn
// BEFORE a compaction advanced the frontier, when the prompt still contained the
// now-summarised rows. If overhead were baselined on the frontier TAIL estimate,
// that staleness would inflate it by everything already summarised and the
// deepening loop would swallow the fresh recent rounds (a new block past the
// existing anchor). Baselined on the FULL history it cancels out: nothing new to
// summarise, the whole 12-message tail stays verbatim.
func TestMaybeCompactStaleRealCountNoOverDeepening(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id, summary_blocks) VALUES('c1','[]')`); err != nil {
		t.Fatal(err)
	}

	// 40 fat messages; m0..m27 already summarised (frontier=28, tail=12 ≈ 2.4k
	// estimated, under the 3000 trigger). The newest assistant recorded 3500 —
	// the pre-compaction prompt, over the trigger but stale.
	hist := make([]store.Message, 40)
	for i := range hist {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("tok ", 150)}})
		hist[i] = store.Message{ID: fmt.Sprintf("m%d", i), Role: role, Blocks: b}
	}
	setLastAssistantInput(hist, 3500)
	blocks, _ := json.Marshal([]SummaryBlock{{
		Level: 1, FromMessageID: "m0", AnchorMessageID: "m27", Text: "recap", Tokens: 80,
	}})
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: blocks}

	keep, out, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("stale real count over-deepened: %d blocks (new block past m27), want the 1 existing", len(out))
	}
	if len(keep) != 12 || keep[0].ID != "m28" {
		t.Fatalf("keep = %d msgs starting %s, want the full 12-message tail from m28", len(keep), keep[0].ID)
	}
}

// TestMaybeCompactSkipsInflightAssistant locks in the §workspaces fix: an
// assistant row that is still GENERATING (status="streaming", blocks empty until
// FinishMessage) must never be rolled into a summary — the anchor would cover
// its index and the finished answer, written into the same row later, would be
// permanently invisible to every future prompt. The cut is clamped so the whole
// in-flight round (its question included) stays verbatim.
func TestMaybeCompactSkipsInflightAssistant(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No settings table → defaults (keepRounds=6 → keepMsgs=12). 16 messages →
	// the cut would normally be 4, summarising m0..m3.
	hist := buildHistory(16)
	// m3 (assistant) is still in flight: another member's answer mid-stream.
	hist[3].Status = "streaming"
	hist[3].Blocks = json.RawMessage("[]")
	hist[3].CreatedAt = time.Now().Unix()
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	keep, blocks, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d summary blocks, want 1", len(blocks))
	}
	// The clamp stops the cut at m3, and the snap-to-user pulls it to m2 — so the
	// summary covers only m0..m1 and the in-flight round (m2, m3) stays verbatim.
	if blocks[0].AnchorMessageID != "m1" {
		t.Fatalf("anchor = %s, want m1 (in-flight m3 must stay uncovered)", blocks[0].AnchorMessageID)
	}
	if len(keep) == 0 || keep[0].ID != "m2" {
		t.Fatalf("keep starts at %s, want m2 (in-flight round kept verbatim)", keep[0].ID)
	}
}

// TestMaybeCompactZombieStreamingNotProtected: a row stuck in status="streaming"
// far beyond the generation time cap is a crash leftover that will never gain
// content — it must NOT clamp the cut, otherwise one zombie row freezes
// compaction (and prompt growth) forever.
func TestMaybeCompactZombieStreamingNotProtected(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hist := buildHistory(16)
	hist[3].Status = "streaming"
	hist[3].Blocks = json.RawMessage("[]")
	hist[3].CreatedAt = time.Now().Add(-time.Hour).Unix() // dead for an hour
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}

	_, blocks, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AnchorMessageID != "m3" {
		t.Fatalf("zombie row wrongly protected: blocks=%+v, want one block anchored at m3", blocks)
	}
}
