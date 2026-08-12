package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func compactionHandlerFixture(t *testing.T, messageCount int) (Deps, *store.User, *store.Conversation) {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db := openMigrated(t, filepath.Join(t.TempDir(), "manual-compaction.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','compact@example.test','h','user')`)
	user, err := store.FindUserByID(context.Background(), db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{
		ID: "c1", UserID: user.ID, Title: "Compact", ModelID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	parentID := ""
	for i := 0; i < messageCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		blocks, _ := json.Marshal([]llm.UnifiedBlock{{Kind: "text", Text: "message content"}})
		message, createErr := store.CreateMessage(context.Background(), db, store.Message{
			ConversationID: conv.ID, ParentID: parentID, Role: role, AuthorID: user.ID,
			Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		parentID = message.ID
	}
	if parentID != "" {
		if _, err := db.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, parentID, conv.ID); err != nil {
			t.Fatal(err)
		}
		conv.ActiveLeafID = parentID
	}
	registry := llm.NewRegistry(nil)
	task := llm.NewTaskLLM(db, registry, nil)
	orchestrator := llm.NewOrchestrator(db, registry, nil, nil, cache.NewMemory(), nil, task, nil, nil)
	return Deps{DB: db, Cache: cache.NewMemory(), Orchestrator: orchestrator}, user, conv
}

func TestCompactConversationHandlerReportsDisabled(t *testing.T) {
	d, user, conv := compactionHandlerFixture(t, 4)
	if err := store.SetSetting(d.DB, "compaction_enabled", false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusConflict || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result llm.ManualCompactionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "disabled" || result.Compacted {
		t.Fatalf("result=%+v", result)
	}
}

func TestCompactConversationHandlerRejectsStreamingConversation(t *testing.T) {
	d, user, conv := compactionHandlerFixture(t, 2)
	blocks := json.RawMessage(`[{"kind":"text","text":"partial"}]`)
	if _, err := store.CreateMessage(context.Background(), d.DB, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "assistant", AuthorID: user.ID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "streaming",
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result llm.ManualCompactionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "generation_in_progress" {
		t.Fatalf("result=%+v", result)
	}
}
