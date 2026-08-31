package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMessageStoppingStateRemainsFinalizable(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "message-stopping.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`)
	conversation, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "Stopping"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	message, err := CreateMessageForUser(ctx, db, Message{
		ID: "a1", ConversationID: conversation.ID, Role: "assistant", AuthorID: "u1",
		Blocks: json.RawMessage(`[]`), Status: "streaming",
	}, "u1")
	if err != nil {
		t.Fatalf("create placeholder: %v", err)
	}

	changed, err := MarkMessageStoppingForUser(ctx, db, message.ID, conversation.ID, "u1")
	if err != nil || !changed {
		t.Fatalf("mark stopping: changed=%v err=%v", changed, err)
	}
	stopping, err := GetMessage(ctx, db, message.ID)
	if err != nil {
		t.Fatalf("load stopping row: %v", err)
	}
	if stopping.Status != "stopping" || stopping.StopReason != "stopped" {
		t.Fatalf("transitional state=%q stop=%q, want stopping/stopped", stopping.Status, stopping.StopReason)
	}

	partial := json.RawMessage(`[{"kind":"text","text":"partial answer"}]`)
	if err := FinishMessageForUser(ctx, db, message.ID, conversation.ID, "u1", MessageFinishPatch{
		Blocks: partial, Citations: json.RawMessage(`[]`), StopReason: "stopped",
		OutputTokens: 2, Status: "stopped", GenMs: 25,
	}); err != nil {
		t.Fatalf("finalize stopping row: %v", err)
	}
	final, err := GetMessage(ctx, db, message.ID)
	if err != nil {
		t.Fatalf("load final row: %v", err)
	}
	if final.Status != "stopped" || string(final.Blocks) != string(partial) || final.OutputTokens != 2 {
		t.Fatalf("final row status=%q blocks=%s output_tokens=%d", final.Status, final.Blocks, final.OutputTokens)
	}
	changed, err = MarkMessageStoppingForUser(ctx, db, message.ID, conversation.ID, "u1")
	if err != nil || changed {
		t.Fatalf("remark terminal row: changed=%v err=%v, want false/nil", changed, err)
	}
}
