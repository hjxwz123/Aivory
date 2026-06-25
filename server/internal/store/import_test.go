package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportConversation exercises the conversation-import path with a tree that
// mirrors the complex real-world export (chat-export-…942370.json): a root, a
// fork into two sibling user questions (an edit-branch), one of which has an
// EMPTY assistant reply (an aborted source turn) while the other continues into
// a deep active branch. It verifies tree remapping, the active-leaf override
// (the active leaf is NOT the last-inserted node), sibling/branch metadata, and
// the empty-reply status fix.
func TestImportConversation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "imp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)

	// Client ids are the source platform's; ImportConversation must remap them.
	// Insertion order puts the ACTIVE branch FIRST and the dead branch LAST, so
	// CreateMessage leaves active_leaf on the dead branch's leaf (a3) — the
	// override to a5 is what must win. (parents precede children, as required.)
	msgs := []ImportMessageInput{
		{ClientID: "cu0", ParentClientID: "", Role: "user", Content: "root q"},
		{ClientID: "ca1", ParentClientID: "cu0", Role: "assistant", Content: "a1 answer"},
		{ClientID: "cu2b", ParentClientID: "ca1", Role: "user", Content: "u2b q"},
		{ClientID: "ca4", ParentClientID: "cu2b", Role: "assistant", Content: "a4 answer"},
		{ClientID: "cu3", ParentClientID: "ca4", Role: "user", Content: "u3 q"},
		{ClientID: "ca5", ParentClientID: "cu3", Role: "assistant", Content: "a5 answer"},
		{ClientID: "cu2a", ParentClientID: "ca1", Role: "user", Content: "u2a q"},
		{ClientID: "ca3", ParentClientID: "cu2a", Role: "assistant", Content: "   "}, // empty/aborted reply
	}
	convID, err := ImportConversation(ctx, db, Conversation{UserID: "u1", Title: "New Chat", ModelID: "m1"}, msgs, "ca5")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	all, err := ListAllMessages(ctx, db, convID)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 8 {
		t.Fatalf("imported %d messages, want 8", len(all))
	}
	text := func(m Message) string {
		var blocks []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(m.Blocks, &blocks)
		if len(blocks) > 0 {
			return blocks[0].Text
		}
		return ""
	}
	byText := map[string]Message{}
	for _, m := range all {
		byText[text(m)] = m
	}

	// 1. Active-leaf override: the active path must follow ca5 (root→a1→u2b→a4→
	//    u3→a5), NOT the dead branch (a3) that was inserted last.
	path, err := ListMessages(ctx, db, convID, "")
	if err != nil {
		t.Fatalf("list path: %v", err)
	}
	gotPath := make([]string, len(path))
	for i, m := range path {
		gotPath[i] = text(m)
	}
	wantPath := []string{"root q", "a1 answer", "u2b q", "a4 answer", "u3 q", "a5 answer"}
	if len(gotPath) != len(wantPath) {
		t.Fatalf("active path = %v, want %v", gotPath, wantPath)
	}
	for i := range wantPath {
		if gotPath[i] != wantPath[i] {
			t.Fatalf("active path[%d] = %q, want %q (full %v)", i, gotPath[i], wantPath[i], gotPath)
		}
	}

	// 2. Parent links were rewired through the id map (not the raw client ids).
	if p := byText["a1 answer"].ParentID; p != byText["root q"].ID {
		t.Errorf("a1.parent = %q, want root's server id %q", p, byText["root q"].ID)
	}
	if byText["a1 answer"].ParentID == "ca1" || byText["root q"].ID == "cu0" {
		t.Error("client ids leaked into the stored tree (not remapped)")
	}

	// 3. The two follow-up questions under a1 are siblings → branch_count 2.
	sib, err := BatchSiblingsOf(ctx, db, all)
	if err != nil {
		t.Fatalf("siblings: %v", err)
	}
	for _, q := range []string{"u2a q", "u2b q"} {
		if n := len(sib[byText[q].ID]); n != 2 {
			t.Errorf("%q sibling count = %d, want 2 (the edit-branch)", q, n)
		}
	}
	// The deep-branch assistant a5 is an only child → no branch picker.
	if n := len(sib[byText["a5 answer"].ID]); n != 1 {
		t.Errorf("a5 sibling count = %d, want 1", n)
	}

	// 4. The empty/aborted reply (whitespace-only in the source) is stored
	//    'stopped', not 'complete' — so the UI shows an empty turn, not a false
	//    "no response — retry" error banner.
	var empty Message
	for _, m := range all {
		if m.Role == "assistant" && strings.TrimSpace(text(m)) == "" {
			empty = m
			break
		}
	}
	if empty.ID == "" {
		t.Fatal("expected an empty assistant reply in the import")
	}
	if empty.Status != "stopped" {
		t.Errorf("empty reply status = %q, want stopped", empty.Status)
	}

	// 5. Sibling ORDER follows insertion order (u2b inserted before u2a).
	if byText["u2b q"].CreatedAt >= byText["u2a q"].CreatedAt {
		t.Errorf("sibling order not preserved: u2b created_at %d should precede u2a %d",
			byText["u2b q"].CreatedAt, byText["u2a q"].CreatedAt)
	}
}
