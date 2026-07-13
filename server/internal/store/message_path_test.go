package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// CreateMessagePath (§ fork batching): one transaction inserts the whole
// copied chain with auto-chained parent links and points the conversation's
// active leaf at the last message — replacing the per-message CreateMessage
// loop whose per-row commit+fsync made forking long conversations take
// seconds (users double-clicked and forked twice).
func TestCreateMessagePathChainsAndSetsActiveLeaf(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "path.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	conv, err := CreateConversation(ctx, db, Conversation{UserID: "u1", Title: "fork target"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	block := func(text string) json.RawMessage {
		b, _ := json.Marshal([]map[string]string{{"kind": "text", "text": text}})
		return b
	}
	msgs := []Message{
		{ConversationID: conv.ID, Role: "user", Blocks: block("q1"), ModelLabel: "GPT X"},
		{ConversationID: conv.ID, Role: "assistant", Blocks: block("a1"), ModelLabel: "GPT X"},
		{ConversationID: conv.ID, Role: "user", Blocks: block("q2")},
	}
	leaf, err := CreateMessagePath(ctx, db, msgs)
	if err != nil {
		t.Fatalf("CreateMessagePath: %v", err)
	}

	got, err := ListMessages(ctx, db, conv.ID, leaf)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3", len(got))
	}
	// Parent chain: first has no parent, each next parents on the previous.
	if got[0].ParentID != "" {
		t.Fatalf("first message parent = %q, want root", got[0].ParentID)
	}
	if got[1].ParentID != got[0].ID || got[2].ParentID != got[1].ID {
		t.Fatalf("parent chain broken: %q→%q, %q→%q", got[1].ParentID, got[0].ID, got[2].ParentID, got[1].ID)
	}
	if got[2].ID != leaf {
		t.Fatalf("returned leaf = %q, want last message %q", leaf, got[2].ID)
	}
	// model_label passes through verbatim (no per-row lookup in the batch).
	if got[0].ModelLabel != "GPT X" {
		t.Fatalf("model_label lost: %q", got[0].ModelLabel)
	}
	// Active leaf advanced to the last copied message.
	cv, err := GetConversation(ctx, db, conv.ID, "u1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if cv.ActiveLeafID != leaf {
		t.Fatalf("active_leaf = %q, want %q", cv.ActiveLeafID, leaf)
	}
}
