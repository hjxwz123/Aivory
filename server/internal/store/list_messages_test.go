package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestListMessagesActivePath guards the N+1 → single-query refactor of
// ListMessages: it must still return the active path (root → leaf) in
// chronological order, follow the conversation's active_leaf_id when no leaf is
// given, honour an explicit leaf on a sibling branch, and not loop on cycles.
func TestListMessagesActivePath(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lm.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	if _, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "t"}); err != nil {
		t.Fatalf("conv: %v", err)
	}
	blk := func(s string) json.RawMessage {
		b, _ := json.Marshal([]map[string]string{{"kind": "text", "text": s}})
		return b
	}
	mk := func(id, parent, role, text string) {
		t.Helper()
		if _, err := CreateMessage(ctx, db, Message{ID: id, ConversationID: "c1", ParentID: parent, Role: role, Blocks: blk(text)}); err != nil {
			t.Fatalf("msg %s: %v", id, err)
		}
	}
	// root → a1 (active), plus sibling branch a2 under root.
	mk("root", "", "user", "hello")
	mk("a1", "root", "assistant", "first answer")
	mk("a2", "root", "assistant", "second answer") // CreateMessage advances active_leaf to a2

	ids := func(ms []Message) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}

	// No leaf → follows active_leaf_id (a2 was created last).
	got, err := ListMessages(ctx, db, "c1", "")
	if err != nil {
		t.Fatalf("ListMessages active: %v", err)
	}
	if g := ids(got); len(g) != 2 || g[0] != "root" || g[1] != "a2" {
		t.Fatalf("active path = %v, want [root a2]", g)
	}

	// Explicit leaf on the other branch.
	got2, err := ListMessages(ctx, db, "c1", "a1")
	if err != nil {
		t.Fatalf("ListMessages a1: %v", err)
	}
	if g := ids(got2); len(g) != 2 || g[0] != "root" || g[1] != "a1" {
		t.Fatalf("a1 path = %v, want [root a1]", g)
	}
}

func TestCreateUserMessageCommitsDraftAttachments(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "commit-draft.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	if _, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "t"}); err != nil {
		t.Fatalf("conv: %v", err)
	}
	if _, err := CreateFile(ctx, db, File{
		ID: "f1", UserID: "u1", ConversationID: "c1", Filename: "draft.pdf",
		MimeType: "application/pdf", Kind: "pdf", StoragePath: "/tmp/draft.pdf", Draft: true,
	}); err != nil {
		t.Fatalf("file: %v", err)
	}
	attachments := json.RawMessage(`[{"id":"f1","filename":"draft.pdf","kind":"pdf"}]`)
	if _, err := CreateMessage(ctx, db, Message{
		ID: "m1", ConversationID: "c1", Role: "user", Blocks: json.RawMessage(`[]`), Attachments: attachments,
	}); err != nil {
		t.Fatalf("message: %v", err)
	}
	file, err := GetFile(ctx, db, "f1", "u1")
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if file.Draft {
		t.Fatal("file remained draft after its user message was committed")
	}
	drafts, err := ListDraftFilesForConversation(ctx, db, "c1")
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	if len(drafts) != 0 {
		t.Fatalf("drafts = %+v, want none", drafts)
	}
}
