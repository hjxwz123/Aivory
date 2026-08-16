package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/generationcfg"
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

// persistCompactionFixture gives behavior-focused tests the same durability
// preconditions as production: the conversation and every message snapshot
// exist before a summary is allowed to affect the current request.
func persistCompactionFixture(t *testing.T, db *sql.DB, conv *store.Conversation, history []store.Message) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS conversations (id TEXT PRIMARY KEY, summary_blocks TEXT, active_leaf_id TEXT, updated_at INTEGER)`,
		`CREATE TABLE IF NOT EXISTS messages (id TEXT PRIMARY KEY, conversation_id TEXT, parent_id TEXT, role TEXT, status TEXT, created_at INTEGER, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	raw := compactionSnapshotJSON(conv.SummaryBlocks)
	leafID := conv.ActiveLeafID
	if leafID == "" && len(history) > 0 {
		leafID = history[len(history)-1].ID
		conv.ActiveLeafID = leafID
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO conversations(id, summary_blocks, active_leaf_id) VALUES(?, ?, ?)`, conv.ID, raw, leafID); err != nil {
		t.Fatal(err)
	}
	for i := range history {
		parentID := ""
		if i > 0 {
			parentID = history[i-1].ID
		}
		history[i].ConversationID = conv.ID
		history[i].ParentID = parentID
		message := history[i]
		if _, err := db.Exec(`INSERT OR REPLACE INTO messages(id, conversation_id, parent_id, role, status, created_at, blocks, raw, attachments, citations) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			message.ID, conv.ID, parentID, message.Role, message.Status, message.CreatedAt, string(message.Blocks), string(message.Raw),
			compactionSnapshotJSON(message.Attachments), compactionSnapshotJSON(message.Citations)); err != nil {
			t.Fatal(err)
		}
	}
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
	history := buildHistory(18)
	persistCompactionFixture(t, db, conv, history)

	// Pass 1: 16 messages → keep last 12, summarise m0..m3 (cut = 16-12 = 4).
	keep1, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, history[:16], 0, "u1")
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
	keep2, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, history, 0, "u1")
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
	_, blocks3, err := MaybeCompact(context.Background(), db, nil, conv, history, 0, "u1")
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

	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	history := buildHistory(16)
	persistCompactionFixture(t, db, conv, history)
	keep, blocks, err := MaybeCompact(context.Background(), db, nil, conv, history, 0, "u1")
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
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
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
	history := buildHistory(18)
	persistCompactionFixture(t, db, conv, history)

	_, blocks1, err := MaybeCompact(context.Background(), db, nil, conv, history[:16], 0, "u1")
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
	keep2, blocks2, err := MaybeCompact(context.Background(), db, nil, conv, history, 0, "u1")
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

func TestFilterBlocksForPathRendersConnectedBlocksInConversationOrder(t *testing.T) {
	history := buildHistory(6)
	blocks := []SummaryBlock{
		{FromMessageID: "m0", AnchorMessageID: "m1", Text: "first"},
		{FromMessageID: "m4", AnchorMessageID: "m5", Text: "third"},
		{FromMessageID: "m2", AnchorMessageID: "m3", Text: "second"},
	}

	ordered := filterBlocksForPath(blocks, history)
	if len(ordered) != 3 {
		t.Fatalf("ordered blocks = %+v, want all three connected blocks", ordered)
	}
	if ordered[0].Text != "first" || ordered[1].Text != "second" || ordered[2].Text != "third" {
		t.Fatalf("summary order = %q, %q, %q; want first, second, third",
			ordered[0].Text, ordered[1].Text, ordered[2].Text)
	}
	if rendered := ApplySummaryBlocks(ordered); strings.Index(rendered, "first") > strings.Index(rendered, "second") ||
		strings.Index(rendered, "second") > strings.Index(rendered, "third") {
		t.Fatalf("rendered summary order is not chronological: %s", rendered)
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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
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
	for i := range hist {
		if i > 0 {
			hist[i].ParentID = hist[i-1].ID
		}
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, parent_id, blocks, raw) VALUES(?, 'c1', NULLIF(?,''), ?, ?)`, hist[i].ID, hist[i].ParentID, string(hist[i].Blocks), string(hist[i].Raw)); err != nil {
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
	got2, err := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", blocks[:2], 256)
	if err != nil {
		t.Fatalf("merge 2 blocks: %v", err)
	}
	if len(got2) != 1 || got2[0].FromMessageID != "m0" || got2[0].AnchorMessageID != "m3" {
		t.Fatalf("2-block merge = %+v, want one coarse block covering m0..m3", got2)
	}
	if got2[0].Level != 8 {
		t.Fatalf("coarse level = %d, want max+1 = 8", got2[0].Level)
	}
	got3, err := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", blocks, 256)
	if err != nil {
		t.Fatalf("merge 3 blocks: %v", err)
	}
	if len(got3) != 2 || got3[0].AnchorMessageID != "m3" || got3[1].AnchorMessageID != "m5" {
		t.Fatalf("3-block merge = %+v, want first two folded and tail preserved", got3)
	}
}

func TestMergeIfOverPreservesSiblingFrontierAndMakesProgress(t *testing.T) {
	// Shared prefix m0..m3 forks into A and B. The oldest shared block ends at m1;
	// A's coarse replacement ends at a1, so B still needs that shared block. The
	// second folded input is A-private and must be removed.
	parents := map[string]string{
		"m0": "", "m1": "m0", "m2": "m1", "m3": "m2",
		"a0": "m3", "a1": "a0", "a2": "a1", "a3": "a2", "a4": "a3", "a5": "a4", "a6": "a5", "a7": "a6",
		"b0": "m3", "b1": "b0", "b2": "b1", "b3": "b2",
	}
	pathFor := func(leafID string) []store.Message {
		t.Helper()
		tree, err := newCompactionMessageTree(parents, nil)
		if err != nil {
			t.Fatal(err)
		}
		path, ok := tree.pathTo(leafID)
		if !ok {
			t.Fatalf("invalid path to %s", leafID)
		}
		return path
	}
	historyA := pathFor("a7")
	historyB := pathFor("b3")
	tree, err := newCompactionMessageTree(parents, historyA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("shared ", 120), Tokens: 120},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "a1", Text: strings.Repeat("a-one ", 120), Tokens: 120},
		{Level: 1, FromMessageID: "a2", AnchorMessageID: "a3", Text: strings.Repeat("a-two ", 120), Tokens: 120},
		{Level: 1, FromMessageID: "a4", AnchorMessageID: "a5", Text: strings.Repeat("a-three ", 120), Tokens: 120},
		{Level: 1, FromMessageID: "a6", AnchorMessageID: "a7", Text: strings.Repeat("a-four ", 120), Tokens: 120},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "b1", Text: "B branch recap", Tokens: 20},
	}
	beforeA := filterBlocksForPath(blocks, historyA)
	beforeB := filterBlocksForPath(blocks, historyB)
	beforeFrontierB := summarizedFrontier(beforeB, historyB)
	beforeTokensA := summaryTokens(beforeA)

	merged, err := mergeIfOver(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", "", blocks, historyA, tree, 300)
	if err != nil {
		t.Fatal(err)
	}
	afterA := filterBlocksForPath(merged, historyA)
	afterB := filterBlocksForPath(merged, historyB)
	if len(merged) >= len(blocks) {
		t.Fatalf("stored blocks did not shrink: before=%d after=%d", len(blocks), len(merged))
	}
	if len(afterA) >= len(beforeA) {
		t.Fatalf("A path blocks did not shrink: before=%d after=%d; blocks=%+v", len(beforeA), len(afterA), merged)
	}
	if summaryTokens(afterA) >= beforeTokensA {
		t.Fatalf("A path tokens did not shrink: before=%d after=%d", beforeTokensA, summaryTokens(afterA))
	}
	if got := summarizedFrontier(afterB, historyB); got != beforeFrontierB {
		t.Fatalf("B frontier changed: before=%d after=%d; blocks=%+v", beforeFrontierB, got, merged)
	}
	sharedCount := 0
	for _, block := range merged {
		if block.FromMessageID == "m0" && block.AnchorMessageID == "m1" {
			sharedCount++
		}
	}
	if sharedCount != 1 {
		t.Fatalf("shared block count = %d, want exactly one: %+v", sharedCount, merged)
	}

	// Re-running over the already-folded state must never grow storage or create
	// another block with the same covered range.
	again, err := mergeIfOver(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", "", merged, historyA, tree, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) > len(merged) {
		t.Fatalf("repeated merge grew storage: first=%d second=%d", len(merged), len(again))
	}
	ranges := make(map[string]bool)
	for _, block := range again {
		key := summaryBlockRangeKey(block)
		if ranges[key] {
			t.Fatalf("repeated merge produced duplicate coverage %q: %+v", key, again)
		}
		ranges[key] = true
	}
	if got := summarizedFrontier(filterBlocksForPath(again, historyB), historyB); got != beforeFrontierB {
		t.Fatalf("repeated merge changed B frontier: before=%d after=%d", beforeFrontierB, got)
	}
}

func TestMergeIfOverUsesActualBranchAncestryNotUnrelatedOffPathBlock(t *testing.T) {
	parents := map[string]string{
		"m0": "", "m1": "m0", "m2": "m1", "m3": "m2", "m4": "m3", "m5": "m4", "m6": "m5", "m7": "m6",
		"x0": "", "x1": "x0",
	}
	treeWithoutPath, err := newCompactionMessageTree(parents, nil)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := treeWithoutPath.pathTo("m7")
	if !ok {
		t.Fatal("invalid active path")
	}
	tree, err := newCompactionMessageTree(parents, history)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("one ", 100), Tokens: 100},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "m3", Text: strings.Repeat("two ", 100), Tokens: 100},
		{Level: 1, FromMessageID: "m4", AnchorMessageID: "m5", Text: strings.Repeat("three ", 100), Tokens: 100},
		{Level: 1, FromMessageID: "m6", AnchorMessageID: "m7", Text: strings.Repeat("four ", 100), Tokens: 100},
		{Level: 1, FromMessageID: "x0", AnchorMessageID: "x1", Text: "unrelated root recap", Tokens: 10},
	}
	merged, err := mergeIfOver(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", "", blocks, history, tree, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) >= len(blocks) {
		t.Fatalf("unrelated off-path block prevented progress: before=%d after=%d; blocks=%+v", len(blocks), len(merged), merged)
	}
	if got := len(filterBlocksForPath(merged, history)); got >= 4 {
		t.Fatalf("active path did not fold: got %d blocks; all=%+v", got, merged)
	}
	unrelated := 0
	for _, block := range merged {
		if block.AnchorMessageID == "x1" {
			unrelated++
		}
	}
	if unrelated != 1 {
		t.Fatalf("unrelated branch block count = %d, want one: %+v", unrelated, merged)
	}
}

func TestCJKFallbacksClipByTokens(t *testing.T) {
	cjk := strings.Repeat("汉", 1000)
	msgBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: cjk}})
	clipped := clipOlder([]store.Message{{Blocks: msgBlocks}}, 300)
	if estimateTokens(clipped) > 300 {
		t.Fatalf("clipOlder CJK estimate = %d, want <= 300", estimateTokens(clipped))
	}
	if len([]rune(clipped)) >= len([]rune(cjk)) || !strings.HasSuffix(clipped, "...") {
		t.Fatalf("clipOlder did not visibly truncate CJK text: len=%d", len([]rune(clipped)))
	}

	merged, err := mergeOldestBlocks(context.Background(), nil, &store.Conversation{ID: "c1"}, "u1", []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: cjk, Tokens: estimateTokens(cjk)},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "m3", Text: cjk, Tokens: estimateTokens(cjk)},
	}, 256)
	if err != nil {
		t.Fatalf("merge CJK blocks: %v", err)
	}
	if len(merged) != 1 {
		t.Fatalf("merge fallback produced %d blocks, want 1", len(merged))
	}
	if estimateTokens(merged[0].Text) > 256 {
		t.Fatalf("merge fallback CJK estimate = %d, want <= 256", estimateTokens(merged[0].Text))
	}
}

func TestMergeOldestBlocksRetriesMateriallyShortSummary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "short-merge-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{texts: []string{
		"Too brief.",
		strings.Repeat("Retained earlier requirement and outcome. ", 140),
	}}
	task := newCompactionTask(t, db, provider)
	longA := strings.Repeat("alpha requirement decision path result ", 700)
	longB := strings.Repeat("beta requirement decision path result ", 700)
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: longA, Tokens: estimateTokens(longA)},
		{Level: 2, FromMessageID: "m2", AnchorMessageID: "m3", Text: longB, Tokens: estimateTokens(longB)},
	}
	merged, err := mergeOldestBlocks(context.Background(), task, &store.Conversation{ID: "c1"}, "", blocks, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.reqs) != 2 {
		t.Fatalf("merge requests = %d, want 2", len(provider.reqs))
	}
	if len(merged) != 1 || merged[0].Text != strings.TrimSpace(provider.texts[1]) {
		t.Fatalf("merged blocks = %+v, want revised summary", merged)
	}
	retryPrompt := provider.reqs[1].History[0].Blocks[0].Text
	for _, want := range []string{"ORIGINAL CONVERSATION SOURCE", "[partial summary 1/2]", "alpha requirement", "beta requirement"} {
		if !strings.Contains(retryPrompt, want) {
			t.Fatalf("retry prompt omitted %q", want)
		}
	}
}

func TestMergeOldestBlocksMapReducesOversizedImportedSummaries(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bounded-imported-merge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "compaction_request_max_tokens", minimumCompactionRequestMaxTokens)
	provider := &compactionTestProvider{text: strings.Repeat("merged retained detail ", 100)}
	task := newCompactionTask(t, db, provider)
	longA := strings.Repeat("alpha imported requirement decision ", 5000)
	longB := strings.Repeat("beta imported requirement outcome ", 5000)
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: longA, Tokens: 1},
		{Level: 2, FromMessageID: "m2", AnchorMessageID: "m3", Text: longB, Tokens: 1},
	}
	merged, err := mergeOldestBlocks(context.Background(), task, &store.Conversation{ID: "c1"}, "", blocks, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || len(provider.reqs) < 3 {
		t.Fatalf("merged=%+v requests=%d, want bounded multi-call fold", merged, len(provider.reqs))
	}
	for i, req := range provider.reqs {
		if got := estimateRequestTokens(req) + req.MaxOutputTokens; got > minimumCompactionRequestMaxTokens {
			t.Fatalf("merge request %d total=%d, want <=%d", i+1, got, minimumCompactionRequestMaxTokens)
		}
	}
}

func TestMergeOldestBlocksUnacceptableReducePreservesSourceBlocks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "merge-short-fail-closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{texts: []string{"brief", "still too brief"}}
	task := newCompactionTask(t, db, provider)
	longA := strings.Repeat("alpha requirement decision path result ", 700)
	longB := strings.Repeat("beta requirement decision path result ", 700)
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: longA, Tokens: estimateTokens(longA)},
		{Level: 2, FromMessageID: "m2", AnchorMessageID: "m3", Text: longB, Tokens: estimateTokens(longB)},
	}
	merged, err := mergeOldestBlocks(context.Background(), task, &store.Conversation{ID: "c1"}, "", blocks, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged, blocks) {
		t.Fatalf("unacceptable reduce replaced source blocks: got=%+v want=%+v", merged, blocks)
	}
}

func TestMergeOldestBlocksPropagatesCancellationFromSuccessfulRetry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "canceled-short-merge-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &compactionTestProvider{
		texts: []string{
			"Too brief.",
			strings.Repeat("Retained earlier requirement and outcome. ", 140),
		},
		cancel: cancel, cancelOnCall: 2,
	}
	task := newCompactionTask(t, db, provider)
	longA := strings.Repeat("alpha requirement decision path result ", 700)
	longB := strings.Repeat("beta requirement decision path result ", 700)
	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: longA, Tokens: estimateTokens(longA)},
		{Level: 2, FromMessageID: "m2", AnchorMessageID: "m3", Text: longB, Tokens: estimateTokens(longB)},
	}
	merged, err := mergeOldestBlocks(ctx, task, &store.Conversation{ID: "c1"}, "", blocks, 2048)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("successful retry after cancellation err=%v, want context.Canceled", err)
	}
	if len(provider.reqs) != 2 {
		t.Fatalf("merge requests = %d, want 2", len(provider.reqs))
	}
	if !reflect.DeepEqual(merged, blocks) {
		t.Fatalf("canceled retry replaced source blocks: got=%+v want=%+v", merged, blocks)
	}
}

func TestCompactionSummaryTargetScalesWithSourceAndHonorsCap(t *testing.T) {
	short := buildHistory(2)
	if got := compactionSummaryTarget(short, 8192, 30); got != summaryTargetMinTokens {
		t.Fatalf("short source target = %d, want floor %d", got, summaryTargetMinTokens)
	}

	fatBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("detail ", 6000)}})
	fat := []store.Message{
		{ID: "u1", Role: "user", Blocks: fatBlocks},
		{ID: "a1", Role: "assistant", Blocks: fatBlocks},
	}
	got := compactionSummaryTarget(fat, 8192, 30)
	if got <= summaryTargetMinTokens {
		t.Fatalf("large source target = %d, want above floor %d", got, summaryTargetMinTokens)
	}
	if capped := compactionSummaryTarget(fat, 700, 30); capped != 700 {
		t.Fatalf("large source target under admin cap = %d, want 700", capped)
	}
	if outputCap := compactionSummaryOutputCap(got, 8192); outputCap < got || outputCap > 8192 {
		t.Fatalf("output cap = %d, want target<=cap<=8192 (target=%d)", outputCap, got)
	}
}

func TestAppendCompactionSourceIncludesToolInputsOutputsAndReferences(t *testing.T) {
	blocks, _ := json.Marshal([]UnifiedBlock{
		{Kind: "text", Text: "visible answer"},
		{Kind: "tool_call", ToolName: "lookup", Input: json.RawMessage(`{"query":"invoice 42"}`), Summary: "searched"},
		{Kind: "tool_output", ToolID: "call-1", Text: "total=123.45", Summary: "found invoice"},
		{Kind: "citation", Title: "Invoice", URL: "https://example.test/42", Text: "reference text"},
		{Kind: "artifact", Title: "report.csv", FileRef: "artifact-7", Summary: "generated report"},
	})
	var prompt strings.Builder
	appendCompactionSource(&prompt, []store.Message{{Role: "assistant", Blocks: blocks}})
	got := prompt.String()
	for _, want := range []string{
		"[assistant]", "visible answer", `name="lookup"`, `invoice 42`,
		"[tool_output", "total=123.45", "found invoice", "https://example.test/42",
		"report.csv", "artifact-7", "generated report",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction source omitted %q:\n%s", want, got)
		}
	}
}

func TestAppendCompactionSourceBoundsToolCallInputAndEstimate(t *testing.T) {
	messageWithInput := func(repetitions int) store.Message {
		t.Helper()
		input, err := json.Marshal(map[string]string{
			"query": strings.Repeat("bounded-tool-input ", repetitions) + "UNBOUNDED_TOOL_INPUT_SUFFIX",
		})
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := json.Marshal([]UnifiedBlock{{
			Kind: "tool_call", ToolName: "lookup", Input: input, Summary: "search request",
		}})
		if err != nil {
			t.Fatal(err)
		}
		return store.Message{Role: "assistant", Blocks: blocks}
	}

	bounded := []store.Message{messageWithInput(5_000)}
	muchLarger := []store.Message{messageWithInput(50_000)}
	var prompt strings.Builder
	appendCompactionSource(&prompt, bounded)
	got := prompt.String()
	if !strings.Contains(got, `input={"query":"bounded-tool-input`) {
		t.Fatalf("bounded compaction input lost useful prefix:\n%s", got)
	}
	if strings.Contains(got, "UNBOUNDED_TOOL_INPUT_SUFFIX") {
		t.Fatal("bounded compaction input retained the oversized suffix")
	}
	if tokens := estimateTokens(got); tokens > defaultCompactionToolInputTokens+128 {
		t.Fatalf("bounded tool-call projection = %d tokens, want at most %d plus framing", tokens, defaultCompactionToolInputTokens)
	}

	baseTokens := estimateCompactionSourceTokens(bounded)
	largeTokens := estimateCompactionSourceTokens(muchLarger)
	if largeTokens != baseTokens {
		t.Fatalf("source estimate grew after the tool-input cap: base=%d much-larger=%d", baseTokens, largeTokens)
	}
}

func TestCompactionToolOutputKeepsHeadAndTail(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	output := "tool started query=论文检索 " + strings.Repeat("intermediate row ", 120) + "FINAL_CONCLUSION total=42"
	block := canonicalToolOutputBlock("paper_lookup", "call-1", output, "complete")
	if !strings.Contains(block.Text, "tool started query=论文检索") {
		t.Fatalf("tool output lost head evidence: %q", block.Text)
	}
	if !strings.Contains(block.Text, "FINAL_CONCLUSION total=42") {
		t.Fatalf("tool output lost tail conclusion: %q", block.Text)
	}
	if estimateTokens(block.Text) > compactionToolOutputLimit() {
		t.Fatalf("tool output exceeded cap: tokens=%d cap=%d", estimateTokens(block.Text), compactionToolOutputLimit())
	}
}

func TestAppendCompactionSourceRecoversRawToolOutputTail(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	blocks, err := json.Marshal([]UnifiedBlock{{
		Kind: "tool_output", ToolName: "paper_lookup", ToolID: "call-raw", Text: "short preview",
	}})
	if err != nil {
		t.Fatal(err)
	}
	rawOutput := "tool started " + strings.Repeat("intermediate row ", 120) + "RAW_FINAL_CONCLUSION PMID=123"
	raw, err := json.Marshal([]map[string]any{{
		"role": "tool", "tool_call_id": "call-raw", "content": rawOutput,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	if err := appendCompactionSourceChecked(&prompt, []store.Message{{
		ID: "raw-tool", Role: "assistant", Blocks: blocks, Raw: raw,
	}}); err != nil {
		t.Fatal(err)
	}
	got := prompt.String()
	if !strings.Contains(got, "RAW_FINAL_CONCLUSION PMID=123") {
		t.Fatalf("raw tool result tail was not recovered:\n%s", got)
	}
}

func TestSplitCompactionSourcePreservesRawToolOutputMiddleEvidence(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	const middleEvidence = "MIDDLE_EVIDENCE accession=PMC987654 decision=approved"
	rawOutput := "tool result head " + strings.Repeat("prefix filler ", 800) +
		middleEvidence + strings.Repeat(" suffix filler", 800) + " tool result tail"
	blocks, err := json.Marshal([]UnifiedBlock{{
		Kind: "tool_output", ToolName: "paper_lookup", ToolID: "call-middle", Text: "short UI preview",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]map[string]any{{
		"role": "tool", "tool_call_id": "call-middle", "content": rawOutput,
	}})
	if err != nil {
		t.Fatal(err)
	}
	msg := store.Message{
		ID: "raw-tool-middle", Role: "assistant", Blocks: blocks, Raw: raw,
		Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"),
	}

	rendered, err := renderCompactionSource([]store.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, middleEvidence) {
		t.Fatalf("recognized Raw tool result lost middle evidence before splitting")
	}
	if strings.Contains(rendered, "[middle omitted]") {
		t.Fatal("recognized Raw tool result was projected through the bounded UI preview")
	}

	parts, err := splitCompactionSource([]store.Message{msg}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("parts=%d, want the oversized Raw result to enter map/reduce splitting", len(parts))
	}
	var rebuilt strings.Builder
	foundMiddle := false
	for _, part := range parts {
		if got := estimateTokens(part.Text); got > 256 {
			t.Fatalf("part tokens=%d, want <=256", got)
		}
		if strings.Contains(part.Text, middleEvidence) {
			foundMiddle = true
		}
		newline := strings.IndexByte(part.Text, '\n')
		if newline < 0 {
			t.Fatalf("missing oversized-source label: %q", part.Text)
		}
		rebuilt.WriteString(part.Text[newline+1:])
	}
	if !foundMiddle {
		t.Fatal("no map/reduce source part contained the middle tool evidence")
	}
	if rebuilt.String() != rendered {
		t.Fatalf("Raw tool result was not partitioned losslessly: got=%d bytes want=%d", rebuilt.Len(), len(rendered))
	}
}

func TestAppendCompactionSourceRecoversRawOnlyToolOutput(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "tool_call", ToolName: "paper_lookup", ToolID: "call-legacy"}})
	raw, _ := json.Marshal([]map[string]any{{
		"type": "function_call_output", "call_id": "call-legacy",
		"output": "legacy head " + strings.Repeat("row ", 120) + "LEGACY_FINAL_CONCLUSION",
	}})
	var prompt strings.Builder
	if err := appendCompactionSourceChecked(&prompt, []store.Message{{
		ID: "legacy-tool", Role: "assistant", Blocks: blocks, Raw: raw,
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.String(), "LEGACY_FINAL_CONCLUSION") {
		t.Fatalf("raw-only tool result tail was not preserved:\n%s", prompt.String())
	}
}

func TestAppendCompactionSourceRecoversPromptToolEnvelope(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	blocks, err := json.Marshal([]UnifiedBlock{
		{Kind: "tool_output", ToolName: "paper_lookup", ToolID: "pt_0", Text: "short prompt preview"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const middleEvidence = "PROMPT_MIDDLE_EVIDENCE accession=PMC123456 decision=approved"
	complete := "prompt result head " + strings.Repeat("intermediate row ", 700) + middleEvidence +
		strings.Repeat(" trailing detail", 700) + " prompt result tail"
	raw := marshalPromptToolRawEnvelope([]promptToolRawOutput{{
		Name: "paper_lookup", ID: "pt_0", Output: complete, Status: "complete",
	}})
	if len(raw) == 0 {
		t.Fatal("marshalPromptToolRawEnvelope returned empty Raw")
	}
	var prompt strings.Builder
	if err := appendCompactionSourceChecked(&prompt, []store.Message{{
		ID: "prompt-tool", Role: "assistant", Blocks: blocks, Raw: raw,
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.String(), middleEvidence) || !strings.Contains(prompt.String(), "prompt result tail") {
		t.Fatalf("prompt tool envelope was not recovered in full: %s", prompt.String())
	}
	if strings.Contains(prompt.String(), "[middle omitted]") {
		t.Fatal("prompt tool envelope was incorrectly reduced to the canonical preview")
	}
}

func TestAppendCompactionSourceRecoversAnthropicHostedToolResult(t *testing.T) {
	previous := compactionToolOutputTokens
	compactionToolOutputTokens = 64
	t.Cleanup(func() { compactionToolOutputTokens = previous })

	blocks, _ := json.Marshal([]UnifiedBlock{{
		Kind: "tool_output", ToolName: "web_search", ToolID: "srv-1", Text: "hosted preview",
	}})
	raw, _ := json.Marshal([]map[string]any{{
		"type": "web_search_tool_result", "tool_use_id": "srv-1",
		"content": []any{map[string]any{"type": "web_search_result", "title": "Paper", "url": "https://example.test", "snippet": "tail conclusion"}},
	}})
	var prompt strings.Builder
	if err := appendCompactionSourceChecked(&prompt, []store.Message{{
		ID: "anthropic-hosted", Role: "assistant", Blocks: blocks, Raw: raw,
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.String(), "tail conclusion") || !strings.Contains(prompt.String(), "https://example.test") {
		t.Fatalf("Anthropic hosted tool result was not projected into compaction source: %s", prompt.String())
	}
}

func TestAppendCompactionSourcePreservesMetadataAndReferenceCollections(t *testing.T) {
	oversized := strings.Repeat("metadata-value ", 2_000) + "UNBOUNDED_METADATA_SUFFIX"
	oversizedToolOutput := strings.Repeat("tool-output-value ", 2_000) + "TOOL_OUTPUT_TAIL"
	research, err := json.Marshal(map[string]any{
		"title":  oversized,
		"rounds": 2,
		"tasks": []map[string]any{{
			"question": oversized, "status": oversized, "round": 1,
		}},
		"sources": []map[string]any{{
			"title": oversized, "url": oversized, "domain": oversized,
			"status": oversized, "verdict": oversized,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := json.Marshal([]UnifiedBlock{
		{Kind: "tool_call", ToolName: oversized, Input: json.RawMessage(`{"query":"kept"}`), Summary: oversized},
		{Kind: "tool_output", ToolName: oversized, ToolID: oversized, Text: oversizedToolOutput, Summary: oversized},
		{Kind: "citation", Title: oversized, URL: oversized, Text: oversized},
		{Kind: "artifact", Title: oversized, FileRef: oversized, MimeType: oversized, URL: oversized, Summary: oversized},
		{Kind: "research", Text: string(research)},
	})
	if err != nil {
		t.Fatal(err)
	}

	messageWithReferences := func(count int) store.Message {
		attachments := make([]Attachment, 0, count)
		citations := make([]Citation, 0, count)
		for i := 0; i < count; i++ {
			value := fmt.Sprintf("attachment-%03d", i)
			citationValue := fmt.Sprintf("citation-%03d", i)
			attachment := Attachment{
				ID: value, DocumentID: value, Filename: value, MimeType: "text/plain", Kind: "file", URL: value,
			}
			citation := Citation{
				Index: i + 1, Title: citationValue, URL: citationValue, Source: "web", Snippet: citationValue,
			}
			if i == 0 {
				attachment = Attachment{
					ID: oversized, DocumentID: oversized, Filename: oversized,
					MimeType: oversized, Kind: oversized, URL: oversized,
				}
				citation = Citation{
					Index: 1, Title: oversized, URL: oversized, Source: oversized, Snippet: oversized,
				}
			}
			attachments = append(attachments, attachment)
			citations = append(citations, citation)
		}
		attachmentJSON, marshalErr := json.Marshal(attachments)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		citationJSON, marshalErr := json.Marshal(citations)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return store.Message{
			Role: "assistant", Blocks: blocks, Attachments: attachmentJSON, Citations: citationJSON,
		}
	}

	// Reference collections are not silently capped at an arbitrary item count.
	// The per-field metadata cap and the lossless map/reduce request budget remain
	// the boundaries for large collections.
	largeCount := 250
	largeMessage := messageWithReferences(largeCount)
	var prompt strings.Builder
	appendCompactionSource(&prompt, []store.Message{largeMessage})
	got := prompt.String()
	for _, want := range []string{
		"metadata-value", "attachment-023", "citation-023",
		"attachment-024", "citation-024", "attachment-249", "citation-249",
		`input={"query":"kept"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded metadata projection omitted %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"UNBOUNDED_METADATA_SUFFIX", "[attachments omitted=", "[citations omitted="} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("bounded metadata projection retained %q", forbidden)
		}
	}

	justOverLimit := []store.Message{messageWithReferences(25)}
	muchLarger := []store.Message{largeMessage}
	baseTokens := estimateCompactionSourceTokens(justOverLimit)
	largeTokens := estimateCompactionSourceTokens(muchLarger)
	if largeTokens <= baseTokens {
		t.Fatalf("source estimate did not include additional reference entries: base=%d much-larger=%d", baseTokens, largeTokens)
	}
}

func TestQuotedCompactionMetadataBoundsEscapedRepresentation(t *testing.T) {
	value := strings.Repeat("\\\"\n", 20_000) + "UNBOUNDED_ESCAPED_SUFFIX"
	quoted := quotedCompactionMetadata(value, defaultCompactionMetadataTokens)
	if !strings.HasPrefix(quoted, `"\\\"\n`) {
		t.Fatalf("quoted metadata lost its useful escaped prefix: %.80s", quoted)
	}
	if strings.Contains(quoted, "UNBOUNDED_ESCAPED_SUFFIX") {
		t.Fatal("quoted metadata retained its oversized suffix")
	}
	if tokens := estimateTokens(quoted); tokens > defaultCompactionMetadataTokens {
		t.Fatalf("quoted metadata = %d tokens, want at most %d", tokens, defaultCompactionMetadataTokens)
	}
	var decoded string
	if err := json.Unmarshal([]byte(quoted), &decoded); err != nil {
		t.Fatalf("quoted metadata is not a valid JSON string literal: %v", err)
	}
}

func TestCompactionPromptBoundsLegacyOversizedSetting(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	legacyPrompt := strings.Repeat("preserve concrete detail ", 20_000) + "UNBOUNDED_CUSTOM_PROMPT_SUFFIX"
	if err := store.SetSetting(db, "context_compaction_prompt", legacyPrompt); err != nil {
		t.Fatal(err)
	}

	got := compactionPrompt(db)
	if !strings.HasPrefix(got, "preserve concrete detail") {
		t.Fatalf("bounded custom prompt lost its useful prefix: %q", got)
	}
	if strings.Contains(got, "UNBOUNDED_CUSTOM_PROMPT_SUFFIX") {
		t.Fatal("bounded custom prompt retained the oversized suffix")
	}
	if tokens := estimateTokens(got); tokens > defaultCompactionPromptTokens {
		t.Fatalf("bounded custom prompt = %d tokens, want at most %d", tokens, defaultCompactionPromptTokens)
	}
}

func TestAppendCompactionSourceBoundsResearchState(t *testing.T) {
	state := map[string]any{
		"title":  "Quarterly launch investigation",
		"rounds": 3,
		"tasks":  []map[string]any{{"question": "Compare launch options", "status": "done", "round": 2}},
		"sources": []map[string]any{{
			"title": "Planning memo", "url": "https://example.test/memo", "domain": "example.test", "status": "kept", "verdict": "primary source",
		}},
		"private_payload": strings.Repeat("must not be replayed ", 1000),
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := json.Marshal([]UnifiedBlock{
		{Kind: "research", Text: string(raw)},
		{Kind: "thinking", Text: "hidden reasoning must stay omitted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	appendCompactionSource(&prompt, []store.Message{{Role: "assistant", Blocks: blocks}})
	got := prompt.String()
	for _, want := range []string{
		"Quarterly launch investigation", "rounds=3", "Compare launch options", "Planning memo", "https://example.test/memo", "primary source",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction research source omitted %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"private_payload", "must not be replayed", "hidden reasoning"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("compaction research source leaked %q:\n%s", forbidden, got)
		}
	}
}

func TestSplitCompactionSourcePreservesAllOversizedText(t *testing.T) {
	text := strings.Repeat("alpha beta gamma delta ", 5000)
	blocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: text}})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []store.Message{{ID: "m1", Role: "user", Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]")}}
	original, err := renderCompactionSource(msgs)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := splitCompactionSource(msgs, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("parts=%d, want oversized source split", len(parts))
	}
	var rebuilt strings.Builder
	for _, part := range parts {
		if got := estimateTokens(part.Text); got > 512 {
			t.Fatalf("part tokens=%d, want <=512", got)
		}
		newline := strings.IndexByte(part.Text, '\n')
		if newline < 0 {
			t.Fatalf("missing part label: %q", part.Text)
		}
		rebuilt.WriteString(part.Text[newline+1:])
	}
	if rebuilt.String() != original {
		t.Fatalf("oversized source was not partitioned losslessly: got=%d bytes want=%d", rebuilt.Len(), len(original))
	}
}

func TestSplitCompactionSourceRejectsMalformedMessageJSON(t *testing.T) {
	msgs := []store.Message{{ID: "bad", Role: "user", Blocks: json.RawMessage(`{"broken"`), Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]")}}
	if _, err := splitCompactionSource(msgs, 512); err == nil {
		t.Fatal("malformed message blocks were silently treated as summarized")
	}
}

func TestLoadSummaryBlocksRecalculatesUntrustedTokensAndDropsEmptyRanges(t *testing.T) {
	raw, err := json.Marshal([]SummaryBlock{
		{Level: -4, FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("actual summary text ", 100), Tokens: -1},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "m3", Text: "  ", Tokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := LoadSummaryBlocks(raw)
	if len(blocks) != 1 {
		t.Fatalf("normalized blocks=%+v, want one non-empty range", blocks)
	}
	if blocks[0].Level != 1 || blocks[0].Tokens != estimateTokens(blocks[0].Text) {
		t.Fatalf("normalized block=%+v", blocks[0])
	}
}

func TestSummaryTokensIgnoresForgedDerivedCounts(t *testing.T) {
	text := strings.Repeat("durable summary detail ", 200)
	blocks := []SummaryBlock{{Text: text, Tokens: -10}, {Text: "  ", Tokens: 1_000_000}}
	if got, want := summaryTokens(blocks), estimateTokens(text); got != want {
		t.Fatalf("summaryTokens=%d, want text-derived %d", got, want)
	}
}

func TestLoadSummaryBlocksForRequestRejectsOversizedCoverageAndDependentSuffix(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "compaction_request_max_tokens", minimumCompactionRequestMaxTokens); err != nil {
		t.Fatal(err)
	}

	blocks := []SummaryBlock{
		{Level: 1, FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("oversized imported summary ", 12_000), Tokens: 1},
		{Level: 1, FromMessageID: "m2", AnchorMessageID: "m3", Text: "later dependent summary", Tokens: 1},
		{Level: 1, Text: strings.Repeat("oversized anchorless legacy ", 12_000), Tokens: 1},
	}
	raw, _ := json.Marshal(blocks)
	loaded := loadSummaryBlocksForRequest(db, raw)
	if len(loaded) != 1 || loaded[0].AnchorMessageID != "m3" {
		t.Fatalf("request-safe decoded blocks = %+v, want only bounded dependent block before path filtering", loaded)
	}
	history := buildHistory(4)
	if path := filterBlocksForPath(loaded, history); len(path) != 0 {
		t.Fatalf("dependent block crossed rejected prefix gap: %+v", path)
	}
}

func TestMaybeCompactMapReduceBoundsEveryRequestAndCoversFullRange(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bounded-map-reduce.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"keep_recent_rounds":            1,
		"summary_max_tokens":            512,
		"compaction_request_max_tokens": minimumCompactionRequestMaxTokens,
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	provider := &compactionTestProvider{text: strings.Repeat("durable summary detail ", 80)}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','bounded@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Bounded"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(8)
	longBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("source fact decision path number ", 5000)}})
	for i := range history {
		history[i].ConversationID = conv.ID
		if i < 6 {
			history[i].Blocks = longBlocks
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.reqs) < 3 {
		t.Fatalf("requests=%d, want map-reduce calls", len(provider.reqs))
	}
	for i, req := range provider.reqs {
		if total := estimateRequestTokens(req) + req.MaxOutputTokens; total > minimumCompactionRequestMaxTokens {
			t.Fatalf("request %d total estimate=%d, want <=%d", i+1, total, minimumCompactionRequestMaxTokens)
		}
	}
	if len(blocks) != 1 || blocks[0].FromMessageID != "m0" || blocks[0].AnchorMessageID != "m5" {
		t.Fatalf("blocks=%+v, want one block covering the complete replaced range", blocks)
	}
}

func TestMaybeCompactBudgetIncludesResolvedModelExtraParams(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bounded-extra-params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "keep_recent_rounds", 1)
	_ = store.SetSetting(db, "summary_max_tokens", 1024)
	_ = store.SetSetting(db, "compaction_request_max_tokens", minimumCompactionRequestMaxTokens)
	provider := &compactionTestProvider{text: strings.Repeat("durable summary detail ", 120)}
	task := newCompactionTask(t, db, provider)
	var modelID string
	if err := db.QueryRow(`SELECT id FROM models LIMIT 1`).Scan(&modelID); err != nil {
		t.Fatal(err)
	}
	extraJSON, _ := json.Marshal(map[string]any{"metadata": strings.Repeat("extra context parameter ", 600)})
	if _, err := db.Exec(`UPDATE models SET extra_params=? WHERE id=?`, string(extraJSON), modelID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','extra@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, _ := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Extra params"})
	history := buildHistory(8)
	longBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("source fact decision ", 1800)}})
	for i := range history {
		history[i].ConversationID = conv.ID
		if i < 6 {
			history[i].Blocks = longBlocks
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || len(provider.reqs) < 2 {
		t.Fatalf("blocks=%+v requests=%d, want bounded map/reduce", blocks, len(provider.reqs))
	}
	for i, req := range provider.reqs {
		if got := estimateRequestTokens(req) + req.MaxOutputTokens; got > minimumCompactionRequestMaxTokens {
			t.Fatalf("request %d including extra params = %d tokens, want <= %d", i+1, got, minimumCompactionRequestMaxTokens)
		}
	}
}

func TestResolvedCompactionExtraParamsUsesLargestFallbackCandidate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "fallback-extra-params.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	channel, err := store.CreateChannel(ctx, db, "Compaction", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	dedicated, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "dedicated-small-extra", Label: "Dedicated", Enabled: true,
		ExtraParams: json.RawMessage(`{"temperature":0.1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	largeValue := strings.Repeat("fallback request metadata ", 700)
	largeExtra, _ := json.Marshal(map[string]any{"metadata": largeValue})
	conversationModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "conversation-large-extra", Label: "Conversation", Enabled: true,
		ExtraParams: largeExtra,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "context_compaction_model_id", dedicated.ID); err != nil {
		t.Fatal(err)
	}

	got, err := resolvedCompactionExtraParams(ctx, db, conversationModel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if estimateTokens(string(got)) < estimateTokens(string(largeExtra)) {
		t.Fatalf("resolved extra params = %d tokens, want fallback candidate budget >= %d", estimateTokens(string(got)), estimateTokens(string(largeExtra)))
	}
}

func TestMaybeCompactClampsOutputToRequestBudget(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "output-request-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "keep_recent_rounds", 1)
	_ = store.SetSetting(db, "summary_max_tokens", minimumCompactionRequestMaxTokens)
	_ = store.SetSetting(db, "compaction_request_max_tokens", minimumCompactionRequestMaxTokens)
	provider := &compactionTestProvider{text: strings.Repeat("retained detail ", 500)}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','cap@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, _ := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Cap"})
	history := buildHistory(8)
	for i := range history {
		history[i].ConversationID = conv.ID
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || len(provider.reqs) == 0 {
		t.Fatalf("blocks=%+v requests=%d", blocks, len(provider.reqs))
	}
	wantCap := effectiveCompactionOutputCap(minimumCompactionRequestMaxTokens, minimumCompactionRequestMaxTokens)
	for i, req := range provider.reqs {
		if req.MaxOutputTokens > wantCap {
			t.Fatalf("request %d output cap=%d, want <=%d", i+1, req.MaxOutputTokens, wantCap)
		}
		if got := estimateRequestTokens(req) + req.MaxOutputTokens; got > minimumCompactionRequestMaxTokens {
			t.Fatalf("request %d total=%d, want <=%d", i+1, got, minimumCompactionRequestMaxTokens)
		}
	}
}

func TestMaybeCompactMapFailureDoesNotAdvanceFrontier(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "bounded-map-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "keep_recent_rounds", 1)
	_ = store.SetSetting(db, "summary_max_tokens", 512)
	_ = store.SetSetting(db, "compaction_request_max_tokens", minimumCompactionRequestMaxTokens)
	provider := &compactionTestProvider{text: strings.Repeat("partial summary detail ", 80), failOnCall: 2}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','failure@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Failure"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(8)
	longBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("source remains verbatim ", 5000)}})
	for i := range history {
		history[i].ConversationID = conv.ID
		if i < 6 {
			history[i].Blocks = longBlocks
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	keep, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("failed map advanced context: keep=%d blocks=%+v", len(keep), blocks)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("failed map persisted summary blocks: %+v", got)
	}
}

func TestMaybeCompactProviderFailureKeepsExistingSummaryContextCoherent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "existing-prefix-provider-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_retention_percentage", 10); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{failOnCall: 1}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','existing-failure@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{
		ID: "c-existing-failure", UserID: "u1", Title: "Existing failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(8)
	for i := range history {
		history[i].ConversationID = conv.ID
		if i > 0 {
			history[i].ParentID = history[i-1].ID
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULLIF(?,''),?,?, '[]','[]','complete',?)`,
			history[i].ID, conv.ID, history[i].ParentID, history[i].Role, string(history[i].Blocks), i+1); err != nil {
			t.Fatal(err)
		}
	}
	conv.ActiveLeafID = history[len(history)-1].ID
	if _, err := db.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, conv.ActiveLeafID, conv.ID); err != nil {
		t.Fatal(err)
	}
	existing := []SummaryBlock{{
		Level: 1, FromMessageID: history[0].ID, AnchorMessageID: history[1].ID,
		Text: "durable summary of the first round",
	}}
	existing[0].Tokens = estimateTokens(existing[0].Text)
	raw, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET summary_blocks=? WHERE id=?`, string(raw), conv.ID); err != nil {
		t.Fatal(err)
	}
	conv.SummaryBlocks = raw

	keep, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AnchorMessageID != history[1].ID {
		t.Fatalf("provider failure lost existing summary: %+v", blocks)
	}
	if len(keep) != len(history)-2 || keep[0].ID != history[2].ID {
		t.Fatalf("provider failure duplicated summarized prefix: keep=%+v", keep)
	}
}

func TestMaybeCompactNoCompactableRoundsKeepsExistingSummaryContextCoherent(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 100); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 20); err != nil {
		t.Fatal(err)
	}
	history := buildHistory(4)
	existing := []SummaryBlock{{
		Level: 1, FromMessageID: history[0].ID, AnchorMessageID: history[1].ID,
		Text: "durable summary of the first round",
	}}
	raw, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ID: "c-no-compactable", UserID: "u1", SummaryBlocks: raw}
	persistCompactionFixture(t, db, conv, history)

	keep, blocks, err := MaybeCompact(context.Background(), db, nil, conv, history, 10_000, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AnchorMessageID != history[1].ID {
		t.Fatalf("no-compactable fallback lost existing summary: %+v", blocks)
	}
	if len(keep) != 2 || keep[0].ID != history[2].ID {
		t.Fatalf("no-compactable fallback duplicated summarized prefix: keep=%+v", keep)
	}
}

func TestMaybeCompactShortRetryStillTooShortDoesNotAdvanceFrontier(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "short-retry-fail-closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "keep_recent_rounds", 1)
	_ = store.SetSetting(db, "summary_max_tokens", 1024)
	provider := &compactionTestProvider{texts: []string{"brief", "slightly longer but still materially short"}}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','short@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Short"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(8)
	longBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("source requirement decision path ", 600)}})
	for i := range history {
		history[i].ConversationID = conv.ID
		if i < 6 {
			history[i].Blocks = longBlocks
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	keep, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.reqs) != 2 {
		t.Fatalf("requests=%d, want initial plus one retry", len(provider.reqs))
	}
	if len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("unacceptable retry advanced context: keep=%d blocks=%+v", len(keep), blocks)
	}
}

func TestMaybeCompactRetryFailureDoesNotUseRejectedDraft(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "short-retry-provider-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	_ = store.SetSetting(db, "keep_recent_rounds", 1)
	_ = store.SetSetting(db, "summary_max_tokens", 1024)
	provider := &compactionTestProvider{text: "brief", failOnCall: 2}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','retryfail@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, _ := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Retry failure"})
	history := buildHistory(8)
	longBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("source requirement decision path ", 600)}})
	for i := range history {
		history[i].ConversationID = conv.ID
		if i < 6 {
			history[i].Blocks = longBlocks
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	keep, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("failed retry persisted rejected draft: keep=%d blocks=%+v", len(keep), blocks)
	}
}

type compactionTestProvider struct {
	text         string
	texts        []string
	req          UnifiedChatRequest
	reqs         []UnifiedChatRequest
	cancel       context.CancelFunc
	cancelOnCall int
	failOnCall   int
}

func (p *compactionTestProvider) ID() string { return "openai" }

func (p *compactionTestProvider) Stream(_ context.Context, req UnifiedChatRequest, _ ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	p.req = req
	p.reqs = append(p.reqs, req)
	if p.failOnCall == len(p.reqs) {
		return nil, errors.New("injected compaction provider failure")
	}
	responseText := p.text
	if len(p.texts) > 0 {
		responseText = p.texts[min(len(p.reqs)-1, len(p.texts)-1)]
	}
	if responseText != "" {
		onEvent(SseEvent{Type: "text_delta", Text: responseText})
	}
	if p.cancel != nil && p.cancelOnCall == len(p.reqs) {
		p.cancel()
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: responseText}},
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: 100, OutputTokens: estimateTokens(responseText)},
	}, nil
}

type snapshotBlockingCompactionProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *snapshotBlockingCompactionProvider) ID() string { return "openai" }

func (p *snapshotBlockingCompactionProvider) Stream(ctx context.Context, _ UnifiedChatRequest, _ ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	select {
	case p.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	const text = "summary generated from the original message snapshot"
	onEvent(SseEvent{Type: "text_delta", Text: text})
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: text}},
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: 100, OutputTokens: estimateTokens(text)},
	}, nil
}

func newSnapshotBlockingCompactionTask(t *testing.T, db *sql.DB, provider *snapshotBlockingCompactionProvider) *TaskLLM {
	t.Helper()
	channel, err := store.CreateChannel(context.Background(), db, "Compaction", provider.ID(), "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "compaction-model", Label: "Compaction", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "task_model_id", model.ID); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(nil)
	reg.Register(provider)
	return NewTaskLLM(db, reg, nil)
}

func newCompactionTask(t *testing.T, db *sql.DB, provider *compactionTestProvider) *TaskLLM {
	t.Helper()
	channel, err := store.CreateChannel(context.Background(), db, "Compaction", provider.ID(), "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "compaction-model", Label: "Compaction", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "task_model_id", model.ID); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(nil)
	reg.Register(provider)
	return NewTaskLLM(db, reg, nil)
}

func TestMaybeCompactDoesNotReadSettingsInsideSingleConnectionTransaction(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-cold-settings-cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"compaction_enabled":            true,
		"compaction_token_trigger":      0,
		"keep_recent_rounds":            6,
		"compaction_request_max_tokens": defaultCompactionRequestMaxTokens,
		"context_compaction_prompt":     "Preserve concrete facts and decisions.",
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','cold-cache@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Cold settings cache"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(16)
	for i := range history {
		history[i].ConversationID = conv.ID
		if i > 0 {
			history[i].ParentID = history[i-1].ID
		}
		var parent any
		if history[i].ParentID != "" {
			parent = history[i].ParentID
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,?,?,?,'[]','[]','complete',?)`,
			history[i].ID, conv.ID, parent, history[i].Role, string(history[i].Blocks), i+1); err != nil {
			t.Fatal(err)
		}
	}

	provider := &snapshotBlockingCompactionProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	task := newSnapshotBlockingCompactionTask(t, db, provider)
	type result struct {
		blocks []SummaryBlock
		err    error
	}
	done := make(chan result, 1)
	go func() {
		_, blocks, compactErr := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
		done <- result{blocks: blocks, err: compactErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction model did not start")
	}

	// Simulate a summary call lasting beyond the settings-cache TTL. The old
	// implementation then began a write transaction and tried to refill this
	// cache through *sql.DB, deadlocking on SQLite's sole connection.
	store.InvalidateConfig()
	close(provider.release)
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		// Let an affected implementation unwind instead of leaving the test process
		// with a goroutine permanently waiting for the transaction's own connection.
		db.SetMaxOpenConns(2)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("compaction deadlocked after its settings cache expired")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.blocks) != 1 {
		t.Fatalf("compaction returned %d summary blocks, want 1", len(got.blocks))
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 1 {
		t.Fatalf("persisted summary blocks = %+v, want one durable block", blocks)
	}
}

func TestMaybeCompactSkipsWriteWhenPromptMetadataChangesDuringSummary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column string
		before json.RawMessage
		after  string
	}{
		{
			name:   "attachments",
			column: "attachments",
			before: json.RawMessage(`[{"id":"file-old","name":"old.pdf"}]`),
			after:  `[{"id":"file-new","name":"new.pdf"}]`,
		},
		{
			name:   "citations",
			column: "citations",
			before: json.RawMessage(`[{"title":"old source","url":"https://old.example"}]`),
			after:  `[{"title":"new source","url":"https://new.example"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.InvalidateConfig()
			t.Cleanup(store.InvalidateConfig)
			db, err := store.Open(filepath.Join(t.TempDir(), "metadata-snapshot.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := store.Migrate(db); err != nil {
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
			if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','admin')`); err != nil {
				t.Fatal(err)
			}
			conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Snapshot"})
			if err != nil {
				t.Fatal(err)
			}
			history := buildHistory(16)
			for i := range history {
				history[i].ConversationID = conv.ID
				history[i].Attachments = json.RawMessage("[]")
				history[i].Citations = json.RawMessage("[]")
				if i == 1 {
					if tc.column == "attachments" {
						history[i].Attachments = tc.before
					} else {
						history[i].Citations = tc.before
					}
				}
				if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?,?,?, 'complete',?)`,
					history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks), string(history[i].Attachments), string(history[i].Citations), i+1); err != nil {
					t.Fatal(err)
				}
			}

			provider := &snapshotBlockingCompactionProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
			task := newSnapshotBlockingCompactionTask(t, db, provider)
			done := make(chan error, 1)
			go func() {
				_, _, compactErr := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
				done <- compactErr
			}()
			select {
			case <-provider.started:
			case <-time.After(5 * time.Second):
				t.Fatal("compaction model did not start")
			}
			if _, err := db.Exec(`UPDATE messages SET `+tc.column+`=? WHERE id='m1'`, tc.after); err != nil {
				t.Fatal(err)
			}
			close(provider.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("compaction did not finish")
			}
			var raw string
			if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
				t.Fatalf("summary persisted after %s changed during model call: %+v", tc.column, got)
			}
		})
	}
}

func TestMaybeCompactSkipsWriteWhenParentChangesDuringSummary(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "parent-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"compaction_enabled":       true,
		"compaction_token_trigger": 0,
		"keep_recent_rounds":       6,
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Parent snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(16)
	for i := range history {
		history[i].ConversationID = conv.ID
		if i > 0 {
			history[i].ParentID = history[i-1].ID
		}
		var parent any
		if history[i].ParentID != "" {
			parent = history[i].ParentID
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,?,?,?, '[]','[]','complete',?)`,
			history[i].ID, conv.ID, parent, history[i].Role, string(history[i].Blocks), i+1); err != nil {
			t.Fatal(err)
		}
	}

	provider := &snapshotBlockingCompactionProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	task := newSnapshotBlockingCompactionTask(t, db, provider)
	done := make(chan error, 1)
	go func() {
		_, _, compactErr := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
		done <- compactErr
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction model did not start")
	}
	if _, err := db.Exec(`UPDATE messages SET parent_id=NULL WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not finish")
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("summary persisted after parent changed during model call: %+v", got)
	}
}

func TestMaybeCompactUsesAdaptiveTargetInPromptAndOutputCap(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "adaptive-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "summary_max_tokens", 8192); err != nil {
		t.Fatal(err)
	}

	provider := &compactionTestProvider{text: "Detailed durable summary"}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Adaptive"})
	if err != nil {
		t.Fatal(err)
	}
	hist := buildHistory(16)
	for _, m := range hist {
		m.ConversationID = conv.ID
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, m.ID, conv.ID, m.Role, string(m.Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	target := compactionSummaryTarget(hist[:4], 8192, 30)
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != provider.text {
		t.Fatalf("summary blocks = %+v", blocks)
	}
	if provider.req.MaxOutputTokens != compactionSummaryOutputCap(target, 8192) {
		t.Fatalf("max output tokens = %d, want %d", provider.req.MaxOutputTokens, compactionSummaryOutputCap(target, 8192))
	}
	if !strings.Contains(provider.req.History[0].Blocks[0].Text, fmt.Sprintf("Aim for about %d tokens", target)) {
		t.Fatalf("adaptive target missing from prompt: %s", provider.req.History[0].Blocks[0].Text)
	}
}

func TestMaybeCompactRetriesMateriallyShortSummaryOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "short-summary-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "summary_max_tokens", 8192); err != nil {
		t.Fatal(err)
	}

	firstDraft := "Too brief."
	revisedDraft := strings.Repeat("Preserved concrete fact and decision. ", 240)
	provider := &compactionTestProvider{texts: []string{firstDraft, revisedDraft}}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Short retry"})
	if err != nil {
		t.Fatal(err)
	}
	hist := buildHistory(16)
	longSource, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("requirement decision path value ", 1800)}})
	for i := range hist {
		hist[i].ConversationID = conv.ID
		if i < 4 {
			hist[i].Blocks = longSource
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`, hist[i].ID, conv.ID, hist[i].Role, string(hist[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}

	target := compactionSummaryTarget(hist[:4], 8192, 30)
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.reqs) < 2 {
		t.Fatalf("compaction requests = %d, want at least one summary plus retry/reduce", len(provider.reqs))
	}
	if len(blocks) != 1 || blocks[0].Text != strings.TrimSpace(revisedDraft) {
		t.Fatalf("summary blocks = %+v, want revised draft", blocks)
	}
	for i, req := range provider.reqs {
		if req.MaxOutputTokens <= 0 || req.MaxOutputTokens > compactionSummaryOutputCap(target, 8192) {
			t.Fatalf("request %d max output tokens = %d, want in bounded range", i+1, req.MaxOutputTokens)
		}
		if got := estimateRequestTokens(req) + req.MaxOutputTokens; got > defaultCompactionRequestMaxTokens {
			t.Fatalf("request %d total estimate = %d, want <= %d", i+1, got, defaultCompactionRequestMaxTokens)
		}
	}
	retryPrompt := provider.reqs[1].History[0].Blocks[0].Text
	for _, want := range []string{"ORIGINAL CONVERSATION SOURCE", "about"} {
		if !strings.Contains(retryPrompt, want) {
			t.Fatalf("retry prompt omitted %q: %s", want, retryPrompt)
		}
	}
}

func TestMaybeCompactLocallyCapsProviderSummary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hard-summary-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "summary_max_tokens", 256); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{text: strings.Repeat("provider ignored cap ", 3000)}
	task := newCompactionTask(t, db, provider)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{UserID: "u1", Title: "Hard cap"})
	if err != nil {
		t.Fatal(err)
	}
	history := buildHistory(16)
	for i := range history {
		history[i].ConversationID = conv.ID
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,role,blocks,attachments,citations,status,created_at) VALUES(?,?,NULL,?,?, '[]','[]','complete',1)`,
			history[i].ID, conv.ID, history[i].Role, string(history[i].Blocks)); err != nil {
			t.Fatal(err)
		}
	}
	_, blocks, err := MaybeCompact(context.Background(), db, task, conv, history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("summary blocks = %+v", blocks)
	}
	if got, cap := estimateTokens(blocks[0].Text), provider.req.MaxOutputTokens; got > cap {
		t.Fatalf("persisted summary tokens = %d, provider cap = %d", got, cap)
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

func TestMaybeCompactDoesNotExposeUnpersistedSummary(t *testing.T) {
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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
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
	history := buildHistory(16)
	for _, message := range history {
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,blocks,raw,attachments,citations) VALUES(?,'c1',?,?,?,?)`,
			message.ID, string(message.Blocks), string(message.Raw), "[]", "[]"); err != nil {
			t.Fatal(err)
		}
	}

	previousAttempts := summaryBlockCASAttempts
	summaryBlockCASAttempts = 0
	t.Cleanup(func() { summaryBlockCASAttempts = previousAttempts })
	keep, blocks, err := MaybeCompact(context.Background(), db, nil,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		history, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("unpersisted candidate leaked to caller: keep=%d blocks=%+v", len(keep), blocks)
	}
	var raw string
	if err := db.QueryRow(`SELECT summary_blocks FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "[]" {
		t.Fatalf("summary_blocks changed with zero CAS attempts: %s", raw)
	}
}

func TestMaybeCompactPropagatesCancellationWhenPersistenceCannotAdvance(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	history := buildHistory(16)
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	persistCompactionFixture(t, db, conv, history)

	previousAttempts := summaryBlockCASAttempts
	summaryBlockCASAttempts = 0
	t.Cleanup(func() { summaryBlockCASAttempts = previousAttempts })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	keep, blocks, err := MaybeCompact(ctx, db, nil, conv, history, 0, "u1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("automatic compaction with canceled persistence context err=%v, want context.Canceled", err)
	}
	if len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("canceled automatic compaction exposed candidate: keep=%d blocks=%+v", len(keep), blocks)
	}
}

func TestReadSummaryRawAfterPersistenceIgnoresCanceledRequestContext(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, summary_blocks TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,summary_blocks) VALUES('c1','[{"text":"durable"}]')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := readSummaryRawAfterPersistence(ctx, db, "c1")
	if err != nil {
		t.Fatalf("detached persistence verification failed: %v", err)
	}
	if !strings.Contains(raw, "durable") {
		t.Fatalf("detached persistence verification read %q", raw)
	}
}

func TestCompactionRatioTunablesFailClosedOnInvalidDenominators(t *testing.T) {
	previousHeadroomNum, previousHeadroomDen := summaryTargetHeadroomNum, summaryTargetHeadroomDen
	previousOverflowNum, previousOverflowDen := bigTokenOverflowNum, bigTokenOverflowDen
	t.Cleanup(func() {
		summaryTargetHeadroomNum, summaryTargetHeadroomDen = previousHeadroomNum, previousHeadroomDen
		bigTokenOverflowNum, bigTokenOverflowDen = previousOverflowNum, previousOverflowDen
	})

	summaryTargetHeadroomNum, summaryTargetHeadroomDen = 5, 0
	if got := compactionSummaryOutputCap(400, 1000); got != 400 {
		t.Fatalf("invalid headroom ratio output cap = %d, want conservative target 400", got)
	}

	bigTokenOverflowNum, bigTokenOverflowDen = 5, 0
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	// Keep one round so this case still has a real summary candidate. The test is
	// about the invalid inline-overflow ratio falling back to async, not about a
	// request-only overflow with no old messages to compact.
	mustSet(t, db, "keep_recent_rounds", "1")
	mustSet(t, db, "compaction_token_trigger", "10")
	_, _, action := PlanCompactionForRequest(db,
		&store.Conversation{SummaryBlocks: json.RawMessage("[]")}, buildHistory(4), 1000, 0)
	if action != compactAsync {
		t.Fatalf("invalid overflow ratio action = %d, want conservative async action", action)
	}
}

func TestTerminalCompactionTaskErrorPreservesBillingAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	billingErr := fmt.Errorf("%w: durable usage write failed", ErrTaskBillingRecord)

	got := terminalCompactionTaskError(ctx, billingErr)
	if !errors.Is(got, ErrTaskBillingRecord) {
		t.Fatalf("terminal error = %v, want ErrTaskBillingRecord", got)
	}
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("terminal error = %v, want context.Canceled", got)
	}
}

func TestCompactionLimitsFailClosedOnInvalidTunables(t *testing.T) {
	previousToolTokens := compactionToolOutputTokens
	previousToolInputTokens := compactionToolInputTokens
	previousMetadataTokens := compactionMetadataTokens
	previousBacklogFactor := inlineCompactionBacklogFactor
	previousMsgOverhead := msgStructuralOverhead
	previousClampFloor := summaryTokensClampFloor
	previousMediaAllowance := imageDocumentFlatTokenAllowance
	previousCacheBound := messageTokenMemoCacheBound
	previousPerRound := summaryTargetPerRoundTokens
	previousMinimum := summaryTargetMinTokens
	previousMergeIter := summaryMergeFoldIterCap
	t.Cleanup(func() {
		compactionToolOutputTokens = previousToolTokens
		compactionToolInputTokens = previousToolInputTokens
		compactionMetadataTokens = previousMetadataTokens
		inlineCompactionBacklogFactor = previousBacklogFactor
		msgStructuralOverhead = previousMsgOverhead
		summaryTokensClampFloor = previousClampFloor
		imageDocumentFlatTokenAllowance = previousMediaAllowance
		messageTokenMemoCacheBound = previousCacheBound
		summaryTargetPerRoundTokens = previousPerRound
		summaryTargetMinTokens = previousMinimum
		summaryMergeFoldIterCap = previousMergeIter
	})

	compactionToolOutputTokens = -1
	compactionToolInputTokens = 0
	compactionMetadataTokens = 0
	if got := compactionToolOutputLimit(); got != defaultCompactionToolOutputTokens {
		t.Fatalf("invalid tool output limit = %d, want default %d", got, defaultCompactionToolOutputTokens)
	}
	if got := compactionMetadataLimit(); got != defaultCompactionMetadataTokens {
		t.Fatalf("invalid metadata limit = %d, want default %d", got, defaultCompactionMetadataTokens)
	}
	if got := compactionToolInputLimit(); got != defaultCompactionToolInputTokens {
		t.Fatalf("invalid tool input limit = %d, want default %d", got, defaultCompactionToolInputTokens)
	}

	longOutput := strings.Repeat("tool-result ", defaultCompactionToolOutputTokens*2)
	block := canonicalToolOutputBlock("lookup", "call-1", longOutput, "complete")
	if tokens := estimateTokens(block.Text); tokens > defaultCompactionToolOutputTokens+1 {
		t.Fatalf("invalid env disabled tool-output clipping: got about %d tokens", tokens)
	}

	inlineCompactionBacklogFactor = 0
	if got := effectiveInlineBacklogFactor(); got != defaultInlineBacklogFactor {
		t.Fatalf("invalid inline factor = %d, want default %d", got, defaultInlineBacklogFactor)
	}

	msgStructuralOverhead = -1
	summaryTokensClampFloor = 0
	imageDocumentFlatTokenAllowance = -1
	messageTokenMemoCacheBound = -1
	msgTokenMemoMu.Lock()
	msgTokenMemo = make(map[string]int)
	msgTokenMemoMu.Unlock()
	message := buildHistory(1)[0]
	if got := estimateMsgTokens(message); got < defaultMessageStructuralOverhead {
		t.Fatalf("invalid structural overhead produced %d tokens", got)
	}
	request := UnifiedChatRequest{History: []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "image"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "answer"}}},
	}}
	wantAtLeast := 2*defaultMessageStructuralOverhead + defaultImageDocumentFlatTokenAllowance
	if got := estimateRequestTokens(request); got < wantAtLeast {
		t.Fatalf("invalid request-estimate tunables produced %d tokens, want at least %d", got, wantAtLeast)
	}
	if got := effectiveSummaryTokensClampFloor(); got != defaultSummaryTokensClampFloor {
		t.Fatalf("invalid summary clamp floor = %d, want default %d", got, defaultSummaryTokensClampFloor)
	}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	mustSet(t, db, "summary_max_tokens", "0")
	mustSet(t, db, "summary_merge_max_tokens", "0")
	_, _, _, _, summaryMax, _, mergeMax := compactionSettings(db)
	if summaryMax != defaultSummaryMaxTokens || mergeMax != defaultSummaryMergeBudget {
		t.Fatalf("invalid summary budgets bypassed defaults: max=%d merge=%d", summaryMax, mergeMax)
	}

	summaryTargetPerRoundTokens = -1
	summaryTargetMinTokens = 0
	if got := compactionSummaryTarget(buildHistory(2), 4096, 30); got < defaultSummaryTargetMinTokens {
		t.Fatalf("invalid target tunables produced a %d-token target", got)
	}

	summaryMergeFoldIterCap = 0
	tree, err := newCompactionMessageTree(map[string]string{"m0": "", "m1": "m0", "m2": "m1", "m3": "m2"}, buildHistory(4))
	if err != nil {
		t.Fatal(err)
	}
	blocks := []SummaryBlock{
		{FromMessageID: "m0", AnchorMessageID: "m1", Text: strings.Repeat("alpha ", 100), Tokens: 100},
		{FromMessageID: "m2", AnchorMessageID: "m3", Text: strings.Repeat("beta ", 100), Tokens: 100},
	}
	merged, err := mergeIfOver(context.Background(), nil, &store.Conversation{}, "u1", "", blocks, buildHistory(4), tree, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) >= len(blocks) {
		t.Fatalf("invalid merge iteration cap disabled folding: %+v", merged)
	}
}

func TestMessagesStillCurrentRejectsNonPositiveChunkSize(t *testing.T) {
	// The helper reads the environment on each call. Exercise both values in a
	// subprocess-style test process through t.Setenv; completing proves the loop
	// made progress instead of hanging on start += 0.
	t.Setenv("AIVORY_LLM_CHUNK_SIZE", "0")
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
		t.Fatal(err)
	}
	history := buildHistory(2)
	for i := range history {
		if i > 0 {
			history[i].ParentID = history[i-1].ID
		}
		if _, err := db.Exec(`INSERT INTO messages(id,conversation_id,parent_id,blocks,raw,attachments,citations) VALUES(?,'c1',NULLIF(?,''),?,?, '[]','[]')`,
			history[i].ID, history[i].ParentID, string(history[i].Blocks), string(history[i].Raw)); err != nil {
			t.Fatal(err)
		}
	}
	current, err := messagesStillCurrentTx(context.Background(), db, "c1", history)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("valid snapshot rejected with non-positive chunk size")
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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range hist {
		if i > 0 {
			hist[i].ParentID = hist[i-1].ID
		}
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, parent_id, blocks, raw) VALUES(?, 'c1', NULLIF(?,''), ?, ?)`, hist[i].ID, hist[i].ParentID, string(hist[i].Blocks), string(hist[i].Raw)); err != nil {
			t.Fatal(err)
		}
	}

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
	if _, err := db.Exec(`CREATE TABLE messages (id TEXT, conversation_id TEXT, parent_id TEXT, blocks TEXT, raw TEXT, attachments TEXT, citations TEXT)`); err != nil {
		t.Fatal(err)
	}
	fullHistory := buildHistory(18)
	for i := range fullHistory {
		if i > 0 {
			fullHistory[i].ParentID = fullHistory[i-1].ID
		}
		if _, err := db.Exec(`INSERT INTO messages(id, conversation_id, parent_id, blocks, raw) VALUES(?, 'c1', NULLIF(?,''), ?, ?)`, fullHistory[i].ID, fullHistory[i].ParentID, string(fullHistory[i].Blocks), string(fullHistory[i].Raw)); err != nil {
			t.Fatal(err)
		}
	}

	// Turn A: 16 messages → cut=4 → summarise m0..m3, persisted to the DB.
	convA := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	if _, b1, err := MaybeCompact(context.Background(), db, nil, convA, fullHistory[:16], 0, "u1"); err != nil || len(b1) != 1 {
		t.Fatalf("turn A: blocks=%v err=%v", b1, err)
	}
	// Turn B read the empty snapshot but sees 18 messages → a DEEPER cut (m0..m5).
	convB := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	keepB, b2, err := MaybeCompact(context.Background(), db, nil, convB, fullHistory, 0, "u1")
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
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 32000); err != nil {
		t.Fatal(err)
	}
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

func TestPlanCompactionSkipsTokenOverflowWithoutAnOldRound(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, "compaction_enabled", "true")
	mustSet(t, db, "keep_recent_rounds", "6")
	mustSet(t, db, "compaction_token_trigger", "10")

	// The complete request is deliberately over the trigger, but the history is
	// only one complete turn. There is no older round that a summary can remove.
	history := buildHistory(2)
	keep, blocks, action := PlanCompactionForRequest(
		db,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		history,
		1000,
		0,
	)
	if action != compactNone || len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("token-only overflow without an old round: action=%d keep=%d blocks=%d, want none/%d/0", action, len(keep), len(blocks), len(history))
	}
}

func TestPlanCompactionSkipsUnavoidablePerTurnOverflow(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, "compaction_enabled", "true")
	mustSet(t, db, "keep_recent_rounds", "6")
	mustSet(t, db, "compaction_token_trigger", "100")

	// There are old rounds that could technically be summarized, but fixed
	// request context still exceeds the trigger even after the deepest safe cut.
	// Retrying token compaction on every new turn cannot solve that condition.
	history := buildHistory(8)
	keep, blocks, action := PlanCompactionForRequest(
		db,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		history,
		1000,
		0,
		800,
	)
	if action != compactNone || len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("unavoidable request overflow: action=%d keep=%d blocks=%d, want none/%d/0", action, len(keep), len(blocks), len(history))
	}
	_, _, action = PlanCompactionForRequest(
		db,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		history,
		1000,
		0,
		50,
	)
	if action != compactInline {
		t.Fatalf("reducible request overflow action=%d, want inline", action)
	}
	_, _, action = PlanCompactionForRequest(
		db,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		buildHistory(20),
		1000,
		0,
		800,
	)
	if action != compactAsync {
		t.Fatalf("round overflow with unavoidable request action=%d, want async", action)
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

func TestEstimateMsgTokensInvalidatesEqualLengthEdits(t *testing.T) {
	msgTokenMemoMu.Lock()
	msgTokenMemo = make(map[string]int)
	msgTokenMemoMu.Unlock()

	shortTokens := strings.Repeat("a", 64)
	longTokens := strings.Repeat("a ", 32)
	shortBlocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: shortTokens}})
	if err != nil {
		t.Fatal(err)
	}
	longBlocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: longTokens}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shortBlocks) != len(longBlocks) {
		t.Fatalf("test payloads must have equal encoded length: short=%d long=%d", len(shortBlocks), len(longBlocks))
	}

	message := store.Message{ID: "equal-length-edit", Role: "user", Blocks: shortBlocks}
	before := estimateMsgTokens(message)
	message.Blocks = longBlocks
	after := estimateMsgTokens(message)
	if after <= before {
		t.Fatalf("equal-length edit reused stale token estimate: before=%d after=%d", before, after)
	}
}

func TestEstimateMsgTokensCountsAttachmentAndCitationMetadata(t *testing.T) {
	msgTokenMemoMu.Lock()
	msgTokenMemo = make(map[string]int)
	msgTokenMemoMu.Unlock()

	blocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "short answer"}})
	if err != nil {
		t.Fatal(err)
	}
	message := store.Message{
		ID:          "metadata-estimate",
		Role:        "assistant",
		Blocks:      blocks,
		Attachments: json.RawMessage("[]"),
		Citations:   json.RawMessage("[]"),
	}
	base := estimateMsgTokens(message)

	message.Attachments, err = json.Marshal([]Attachment{{
		ID: "file-1", DocumentID: "doc-1", Filename: strings.Repeat("attachment ", 80),
		MimeType: "application/pdf", Kind: "document", URL: "/api/files/file-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	withAttachment := estimateMsgTokens(message)
	if withAttachment <= base {
		t.Fatalf("attachment metadata was ignored: base=%d with_attachment=%d", base, withAttachment)
	}

	message.Citations, err = json.Marshal([]Citation{{
		Index: 1, Title: strings.Repeat("source ", 80), URL: "https://example.test/source",
		Snippet: strings.Repeat("decisive cited evidence ", 80), Source: "web",
	}})
	if err != nil {
		t.Fatal(err)
	}
	withCitation := estimateMsgTokens(message)
	if withCitation <= withAttachment {
		t.Fatalf("citation metadata was ignored: attachment=%d with_citation=%d", withAttachment, withCitation)
	}
}

func TestEstimateMsgTokensInvalidatesEqualLengthAttachmentAndCitationEdits(t *testing.T) {
	msgTokenMemoMu.Lock()
	msgTokenMemo = make(map[string]int)
	msgTokenMemoMu.Unlock()

	blocks, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "stable"}})
	if err != nil {
		t.Fatal(err)
	}
	shortAttachment, err := json.Marshal([]Attachment{{ID: "f1", Filename: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	longAttachment, err := json.Marshal([]Attachment{{ID: "f1", Filename: strings.Repeat("a ", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shortAttachment) != len(longAttachment) {
		t.Fatalf("attachment fixtures must have equal encoded length: %d != %d", len(shortAttachment), len(longAttachment))
	}
	message := store.Message{ID: "equal-metadata-edit", Role: "user", Blocks: blocks, Attachments: shortAttachment}
	attachmentBefore := estimateMsgTokens(message)
	message.Attachments = longAttachment
	attachmentAfter := estimateMsgTokens(message)
	if attachmentAfter <= attachmentBefore {
		t.Fatalf("equal-length attachment edit reused stale estimate: before=%d after=%d", attachmentBefore, attachmentAfter)
	}

	shortCitation, err := json.Marshal([]Citation{{Index: 1, Snippet: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	longCitation, err := json.Marshal([]Citation{{Index: 1, Snippet: strings.Repeat("b ", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shortCitation) != len(longCitation) {
		t.Fatalf("citation fixtures must have equal encoded length: %d != %d", len(shortCitation), len(longCitation))
	}
	message.Attachments = nil
	message.Citations = shortCitation
	citationBefore := estimateMsgTokens(message)
	message.Citations = longCitation
	citationAfter := estimateMsgTokens(message)
	if citationAfter <= citationBefore {
		t.Fatalf("equal-length citation edit reused stale estimate: before=%d after=%d", citationBefore, citationAfter)
	}
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

func TestEstimateTokensWhitespaceFreeASCII(t *testing.T) {
	compactJSON := `{"payload":"` + strings.Repeat("a", 4000) + `"}`
	if got := estimateTokens(compactJSON); got < 900 {
		t.Fatalf("whitespace-free ASCII estimated %d tokens, want a byte-based floor", got)
	}
	clipped := clipToTokens(compactJSON, 128)
	if got := estimateTokens(clipped); got > 128 {
		t.Fatalf("clipped whitespace-free ASCII = %d tokens, want <= 128", got)
	}
	if len(clipped) >= len(compactJSON) {
		t.Fatal("whitespace-free ASCII bypassed the compaction token cap")
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
	base, exact, _ := contextTokens(hist, nil, 0)
	if exact {
		t.Fatal("expected fallback (no prior input_tokens) to report exact=false")
	}
	if withFile, _, _ := contextTokens(hist, nil, 5000); withFile != base+5000 {
		t.Fatalf("injected overhead not counted on fallback: base=%d withFile=%d (want %d)", base, withFile, base+5000)
	}

	// A prior assistant turn recorded only 1000 input tokens, but THIS turn injects
	// 5000 estimated file tokens. The larger estimate must win so the trigger does
	// not lag a turn behind the upload. It is no longer marked exact because only
	// the newly assembled request estimate can safely drive the inline path.
	hist2 := []store.Message{
		{Role: "assistant", InputTokens: 1000},
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`)},
	}
	got, exact2, _ := contextTokens(hist2, nil, 5000)
	if exact2 {
		t.Fatal("estimated current-turn overhead must not be reported as an exact full request")
	}
	if got < 5000 {
		t.Fatalf("injected overhead ignored on exact path: got=%d, want ≥5000", got)
	}

	// And when the real last-turn count already dominates, it wins unchanged.
	hist3 := []store.Message{
		{Role: "assistant", InputTokens: 80000, CacheReadTokens: 0},
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"hi"}]`)},
	}
	if got, _, _ := contextTokens(hist3, nil, 500); got != 80000 {
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
	got, exact, _ := contextTokens(kept, blocks, 0)
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

func TestEffectiveCompactionTokenTriggerUsesModelOverrideAndGlobalCap(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		globalTrigger, cap, model int
		want                      int
	}{
		{name: "global default", globalTrigger: 32000, cap: 80000, want: 32000},
		{name: "model override", globalTrigger: 32000, cap: 80000, model: 64000, want: 64000},
		{name: "cap model override", globalTrigger: 32000, cap: 48000, model: 64000, want: 48000},
		{name: "cap global default", globalTrigger: 96000, cap: 80000, want: 80000},
		{name: "disabled global without model", globalTrigger: 0, cap: 80000, want: 0},
		{name: "disabled globally", globalTrigger: 0, cap: 80000, model: 64000, want: 0},
		{name: "no cap", globalTrigger: 32000, cap: 0, model: 96000, want: 96000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveCompactionTokenTrigger(tc.globalTrigger, tc.cap, tc.model); got != tc.want {
				t.Fatalf("effective trigger = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveCompactionModelIDPriority(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-model-priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Models", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	createModel := func(label string) string {
		t.Helper()
		model, createErr := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: channel.ID, Kind: "chat", RequestID: label, Label: label, Enabled: true,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return model.ID
	}
	defaultID := createModel("default")
	conversationID := createModel("conversation")
	taskID := createModel("task")
	dedicatedID := createModel("dedicated")
	if err := store.SetSetting(db, "default_model_id", defaultID); err != nil {
		t.Fatal(err)
	}

	assertResolved := func(want string) {
		t.Helper()
		got, resolveErr := resolveCompactionModelID(context.Background(), db, conversationID)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if got != want {
			t.Fatalf("resolved compaction model = %q, want %q", got, want)
		}
	}
	assertResolved(conversationID)
	if err := store.SetSetting(db, "task_model_id", taskID); err != nil {
		t.Fatal(err)
	}
	assertResolved(conversationID)
	if err := store.SetSetting(db, "context_compaction_model_id", dedicatedID); err != nil {
		t.Fatal(err)
	}
	assertResolved(dedicatedID)
}

func TestResolveCompactionModelIDSkipsUnavailableCandidates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-model-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Models", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	createModel := func(label, kind string, enabled bool) string {
		t.Helper()
		model, createErr := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: channel.ID, Kind: kind, RequestID: label, Label: label, Enabled: enabled,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return model.ID
	}
	disabledDedicated := createModel("disabled-dedicated", "chat", false)
	nonChatTask := createModel("embedding-task", "embedding", true)
	conversationID := createModel("conversation", "chat", true)
	defaultID := createModel("default", "chat", true)
	if err := store.SetSetting(db, "context_compaction_model_id", disabledDedicated); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "task_model_id", nonChatTask); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "default_model_id", defaultID); err != nil {
		t.Fatal(err)
	}

	got, err := resolveCompactionModelID(context.Background(), db, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if got != conversationID {
		t.Fatalf("resolved model = %q, want enabled conversation model %q", got, conversationID)
	}

	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id=?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCompactionModelID(context.Background(), db, conversationID); err == nil {
		t.Fatal("disabled channel unexpectedly remained usable")
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=1 WHERE id=?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET enabled=0 WHERE id=?`, conversationID); err != nil {
		t.Fatal(err)
	}
	got, err = resolveCompactionModelID(context.Background(), db, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultID {
		t.Fatalf("resolved model = %q, want enabled global default %q", got, defaultID)
	}
}

func TestCompactionKeepCountRetainsPercentageWithRoundFloor(t *testing.T) {
	if got := compactionKeepCount(20, 6, 40); got != 12 {
		t.Fatalf("20 messages: keep = %d, want round floor 12", got)
	}
	if got := compactionKeepCount(100, 6, 40); got != 40 {
		t.Fatalf("100 messages: keep = %d, want 40%% = 40", got)
	}
	if got := compactionKeepCount(100, 30, 10); got != 60 {
		t.Fatalf("large round floor: keep = %d, want 60", got)
	}
	if got := compactionKeepCount(3, 6, 40); got != 3 {
		t.Fatalf("short conversation: keep = %d, want all 3", got)
	}
}

func TestCompactConversationNowBypassesAutomaticThresholdAndKeepsLatestTurn(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	history := buildHistory(6)
	persistCompactionFixture(t, db, conv, history)
	keep, blocks, err := CompactConversationNow(context.Background(), db, nil, conv, history, "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 2 || keep[0].ID != "m4" || keep[1].ID != "m5" {
		t.Fatalf("manual compact kept %+v, want latest complete round m4..m5", keep)
	}
	if len(blocks) != 1 || blocks[0].FromMessageID != "m0" || blocks[0].AnchorMessageID != "m3" {
		t.Fatalf("manual compact blocks = %+v, want m0..m3", blocks)
	}
}

func TestCompactConversationNowReportsNothingForSingleTurn(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	keep, blocks, err := CompactConversationNow(context.Background(), db, nil, conv, buildHistory(2), "", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 2 || len(blocks) != 0 {
		t.Fatalf("single-turn manual compact keep=%d blocks=%d, want 2/0", len(keep), len(blocks))
	}
}

func TestRebasedCompactionRequestTokensUsesFreshRenderedHistory(t *testing.T) {
	nonHistory := 900
	plannedRenderedTokens := 240
	plannedTokens := nonHistory + plannedRenderedTokens
	// This value deliberately represents the fully transformed fresh Unified
	// history, not raw store messages. It may be smaller after NoTools/Fast/Raw
	// filtering or larger because new messages arrived while the job was queued.
	freshRenderedTokens := 510
	got := RebasedCompactionRequestTokens(plannedTokens, plannedRenderedTokens, freshRenderedTokens)
	want := nonHistory + freshRenderedTokens
	if got != want {
		t.Fatalf("rebased request tokens = %d, want %d", got, want)
	}
}

func TestMaybeCompactForRequestPreservesCurrentNonHistoryOverhead(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 100); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 1200); err != nil {
		t.Fatal(err)
	}

	history := buildHistory(16)
	for i := range history {
		fat, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: strings.Repeat("history ", 120)}})
		history[i].Blocks = fat
	}
	renderedHistoryTokens := 600
	requestTokens := renderedHistoryTokens + 900 // system/RAG/MCP overhead
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	persistCompactionFixture(t, db, conv, history)
	keep, blocks, err := MaybeCompactForRequest(
		context.Background(), db, nil,
		conv,
		history, requestTokens, renderedHistoryTokens, 0, "", "u1", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) == 0 || len(keep) >= len(history) {
		t.Fatalf("complete request overhead did not deepen compaction: keep=%d blocks=%d", len(keep), len(blocks))
	}
}

func TestCompactionHistoryForRequestMatchesToolAndFastFiltering(t *testing.T) {
	raw := json.RawMessage(`{"provider":"native-envelope"}`)
	blocks, _ := json.Marshal([]UnifiedBlock{
		{Kind: "text", Text: "visible"},
		{Kind: "tool_call", ToolName: "python_execute", Input: json.RawMessage(`{"code":"print(1)"}`)},
		{Kind: "tool_output", ToolName: "python_execute", Text: "1"},
	})
	history := []store.Message{{ID: "a1", Role: "assistant", Provider: "openai", ModelID: "m1", Raw: raw, Blocks: blocks}}

	got := compactionHistoryForRequest(history, "openai", "m1", true, map[string]bool{}, true, true)
	if len(got) != 1 {
		t.Fatalf("transformed history length = %d, want 1", len(got))
	}
	if len(got[0].Raw) != 0 {
		t.Fatalf("fast/no-tools transformed history retained native raw: %s", got[0].Raw)
	}
	if len(got[0].Blocks) != 1 || got[0].Blocks[0].Kind != "text" || got[0].Blocks[0].Text != "visible" {
		t.Fatalf("transformed blocks = %+v, want visible text only", got[0].Blocks)
	}
}

func TestPlanCompactionForRequestIgnoresFilteredRawHistorySize(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_enabled", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 100); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_token_trigger", 1200); err != nil {
		t.Fatal(err)
	}

	history := buildHistory(4)
	for i := range history {
		// This provider-native envelope represents data that NoTools, fast mode,
		// or a model switch removed from the request. It must not compete with the
		// caller's complete estimate of the transformed upstream body.
		history[i].Raw = json.RawMessage(`{"provider_payload":"` + strings.Repeat("discarded ", 2400) + `"}`)
	}
	request := UnifiedChatRequest{History: []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "visible question"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "visible answer"}}},
	}}
	requestTokens := estimateRequestTokens(request)
	if requestTokens >= 1200 {
		t.Fatalf("test request estimate = %d, want below trigger", requestTokens)
	}

	keep, blocks, action := PlanCompactionForRequest(
		db,
		&store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")},
		history,
		requestTokens,
		0,
	)
	if action != compactNone || len(keep) != len(history) || len(blocks) != 0 {
		t.Fatalf("filtered raw history triggered compaction: action=%d keep=%d blocks=%d", action, len(keep), len(blocks))
	}
}

func TestEstimateRequestTokensAddsUnifiedMessageStructure(t *testing.T) {
	req := UnifiedChatRequest{History: []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "alpha"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "beta"}}},
	}}
	want := estimateTokens("alpha") + estimateTokens("beta") + 2*msgStructuralOverhead
	if got := estimateRequestTokens(req); got != want {
		t.Fatalf("request tokens = %d, want %d", got, want)
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
	persistCompactionFixture(t, db, conv, hist)

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

func TestMaybeCompactProtectsStreamingAssistantForAPIMaxGenerationWindow(t *testing.T) {
	previousGrace := inflightGrace
	inflightGrace = defaultInflightGrace
	t.Cleanup(func() { inflightGrace = previousGrace })
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hist := buildHistory(16)
	hist[3].Status = "streaming"
	hist[3].Blocks = json.RawMessage("[]")
	// The API allows a detached generation to run for 90 minutes. A 60-minute
	// placeholder is therefore still live and must remain behind the frontier.
	hist[3].CreatedAt = time.Now().Add(-time.Hour).Unix()
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	persistCompactionFixture(t, db, conv, hist)

	keep, blocks, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AnchorMessageID != "m1" {
		t.Fatalf("live long generation was summarized: blocks=%+v, want anchor m1", blocks)
	}
	if len(keep) == 0 || keep[0].ID != "m2" {
		t.Fatalf("keep starts at %s, want m2 for the protected long generation", keep[0].ID)
	}
}

func TestInflightGraceAlwaysCoversConfiguredGenerationWindow(t *testing.T) {
	previousGrace := inflightGrace
	t.Cleanup(func() { inflightGrace = previousGrace })
	t.Setenv("AIVORY_API_MAX_GEN_DURATION", "3h")

	inflightGrace = time.Minute
	if got, want := effectiveInflightGrace(), 3*time.Hour+generationcfg.FinalizationMargin; got != want {
		t.Fatalf("short in-flight grace = %v, want protected generation window %v", got, want)
	}

	inflightGrace = 4 * time.Hour
	if got := effectiveInflightGrace(); got != 4*time.Hour {
		t.Fatalf("explicit longer in-flight grace = %v, want 4h", got)
	}
}

// TestMaybeCompactZombieStreamingNotProtected: a row stuck in status="streaming"
// far beyond the generation time cap is a crash leftover that will never gain
// content — it must NOT clamp the cut, otherwise one zombie row freezes
// compaction (and prompt growth) forever.
func TestMaybeCompactZombieStreamingNotProtected(t *testing.T) {
	previousGrace := inflightGrace
	inflightGrace = defaultInflightGrace
	t.Cleanup(func() { inflightGrace = previousGrace })
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hist := buildHistory(16)
	hist[3].Status = "streaming"
	hist[3].Blocks = json.RawMessage("[]")
	hist[3].CreatedAt = time.Now().Add(-3 * time.Hour).Unix() // beyond the two-hour protection window
	conv := &store.Conversation{ID: "c1", UserID: "u1", SummaryBlocks: json.RawMessage("[]")}
	persistCompactionFixture(t, db, conv, hist)

	_, blocks, err := MaybeCompact(context.Background(), db, nil, conv, hist, 0, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].AnchorMessageID != "m3" {
		t.Fatalf("zombie row wrongly protected: blocks=%+v, want one block anchored at m3", blocks)
	}
}
