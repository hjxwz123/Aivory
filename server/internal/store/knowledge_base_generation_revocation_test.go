package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestScrubKnowledgeBaseRevokedGenerationIsScopedAndRepeatable(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "knowledge-base-generation-revocation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES
		('kb-generation-user','kb-generation-user@example.test','h','user','active'),
		('kb-generation-other','kb-generation-other@example.test','h','user','active')`)
	for _, conv := range []Conversation{
		{ID: "kb-generation-conversation", UserID: "kb-generation-user", Title: "Revoked"},
		{ID: "kb-generation-other-conversation", UserID: "kb-generation-other", Title: "Other"},
	} {
		if _, err := CreateConversation(ctx, db, conv); err != nil {
			t.Fatalf("create conversation %s: %v", conv.ID, err)
		}
	}
	secretPatch := MessageFinishPatch{
		Blocks:     json.RawMessage(`[{"kind":"text","text":"provider-derived secret"}]`),
		Raw:        json.RawMessage(`{"provider_secret":true}`),
		Citations:  json.RawMessage(`[{"url":"https://secret.example"}]`),
		StopReason: "stop", InputTokens: 11, ContextTokens: 12, OutputTokens: 13,
		CacheReadTokens: 14, CacheWriteTokens: 15, Cost: 1.25, Credits: 2.5,
		Status: "complete", Error: "provider detail", GenMs: 99,
	}
	createAssistant := func(id, convID, author string) {
		t.Helper()
		message, err := CreateMessage(ctx, db, Message{
			ID: id, ConversationID: convID, Role: "assistant", AuthorID: author,
			Blocks: json.RawMessage(`[]`), Status: "streaming",
		})
		if err != nil {
			t.Fatalf("create assistant %s: %v", id, err)
		}
		if err := FinishMessage(ctx, db, message.ID, secretPatch); err != nil {
			t.Fatalf("finish assistant %s: %v", id, err)
		}
		exec(t, db, `UPDATE messages SET verify='{"verdict":"secret"}' WHERE id=?`, id)
	}
	createAssistant("kb-revoked-assistant", "kb-generation-conversation", "kb-generation-user")
	createAssistant("kb-unrelated-assistant", "kb-generation-other-conversation", "kb-generation-other")

	for _, tc := range []struct {
		name           string
		messageID      string
		conversationID string
		userID         string
		wantChanged    bool
	}{
		{name: "wrong conversation", messageID: "kb-revoked-assistant", conversationID: "kb-generation-other-conversation", userID: "kb-generation-user"},
		{name: "wrong author", messageID: "kb-revoked-assistant", conversationID: "kb-generation-conversation", userID: "kb-generation-other"},
		{name: "exact generation", messageID: "kb-revoked-assistant", conversationID: "kb-generation-conversation", userID: "kb-generation-user", wantChanged: true},
		{name: "repeat after late finalization", messageID: "kb-revoked-assistant", conversationID: "kb-generation-conversation", userID: "kb-generation-user", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "repeat after late finalization" {
				if err := FinishMessage(ctx, db, tc.messageID, secretPatch); err != nil {
					t.Fatalf("simulate late provider finish: %v", err)
				}
				exec(t, db, `UPDATE messages SET verify='{"verdict":"late-secret"}' WHERE id=?`, tc.messageID)
			}
			changed, err := ScrubKnowledgeBaseRevokedGeneration(ctx, db, tc.messageID, tc.conversationID, tc.userID)
			if err != nil {
				t.Fatalf("scrub: %v", err)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed=%t, want %t", changed, tc.wantChanged)
			}
		})
	}

	revoked, err := GetMessage(ctx, db, "kb-revoked-assistant")
	if err != nil {
		t.Fatalf("load revoked assistant: %v", err)
	}
	if string(revoked.Blocks) != "[]" || len(revoked.Raw) != 0 || string(revoked.Citations) != "[]" {
		t.Fatalf("revoked content retained: blocks=%s raw=%s citations=%s", revoked.Blocks, revoked.Raw, revoked.Citations)
	}
	if revoked.Status != "stopped" || revoked.StopReason != "stopped" || revoked.Error != "" || len(revoked.Verify) != 0 {
		t.Fatalf("revoked terminal state=%+v", revoked)
	}
	if revoked.InputTokens != 0 || revoked.ContextTokens != 0 || revoked.OutputTokens != 0 ||
		revoked.CacheReadTokens != 0 || revoked.CacheWriteTokens != 0 || revoked.Cost != 0 ||
		revoked.Credits != 0 || revoked.GenMs != 0 {
		t.Fatalf("revoked accounting retained: %+v", revoked)
	}

	unrelated, err := GetMessage(ctx, db, "kb-unrelated-assistant")
	if err != nil {
		t.Fatalf("load unrelated assistant: %v", err)
	}
	if unrelated.Status != "complete" || string(unrelated.Blocks) == "[]" || unrelated.OutputTokens != 13 {
		t.Fatalf("unrelated assistant changed: %+v", unrelated)
	}
}
