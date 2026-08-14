package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

func TestCompactConversationHandlerIgnoresStaleStreamingConversation(t *testing.T) {
	d, user, conv := compactionHandlerFixture(t, 2)
	blocks := json.RawMessage(`[{"kind":"text","text":"abandoned partial"}]`)
	stale, err := store.CreateMessage(context.Background(), d.DB, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "assistant", AuthorID: user.ID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "streaming",
		CreatedAt: time.Now().Add(-3 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, stale.ID, conv.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale streaming row blocked compaction: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCompactConversationHandlerProtectsLongRunningGeneration(t *testing.T) {
	d, user, conv := compactionHandlerFixture(t, 2)
	blocks := json.RawMessage(`[{"kind":"text","text":"long-running partial"}]`)
	streaming, err := store.CreateMessage(context.Background(), d.DB, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "assistant", AuthorID: user.ID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "streaming",
		CreatedAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, streaming.ID, conv.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("long-running generation was not protected: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result llm.ManualCompactionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "generation_in_progress" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCompactConversationHandlerReportsNothingForShortConversation(t *testing.T) {
	d, user, conv := compactionHandlerFixture(t, 2)
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result llm.ManualCompactionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "nothing_to_compact" || result.Compacted || result.DroppedMessages != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestCompactConversationHandlerRejectsWorkspaceCollaborator(t *testing.T) {
	d, _, conv := compactionHandlerFixture(t, 4)
	mustExec(t, d.DB, `INSERT INTO users(id,email,password_hash,role) VALUES('u2','collaborator@example.test','h','user')`)
	mustExec(t, d.DB, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('w1','Shared','u1','invite-w1')`)
	mustExec(t, d.DB, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('w1','u2','member')`)
	mustExec(t, d.DB, `UPDATE conversations SET workspace_id='w1', is_public=1 WHERE id=?`, conv.ID)
	collaborator, err := store.FindUserByID(context.Background(), d.DB, "u2")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, collaborator))
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID}))
	rec := httptest.NewRecorder()
	compactConversationHandler(d, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCompactConversationErrorResponses(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{name: "disabled", err: llm.ErrCompactionDisabled, wantStatus: http.StatusConflict, wantReason: "disabled"},
		{name: "generation in progress", err: llm.ErrCompactionInFlight, wantStatus: http.StatusConflict, wantReason: "generation_in_progress"},
		{name: "conversation changed", err: llm.ErrCompactionChanged, wantStatus: http.StatusConflict, wantReason: "conversation_changed"},
		{name: "summary failed", err: llm.ErrCompactionFailed, wantStatus: http.StatusInternalServerError, wantReason: "compaction_failed"},
		{name: "billing persistence failed", err: llm.ErrTaskBillingRecord, wantStatus: http.StatusInternalServerError, wantReason: "compaction_failed"},
		{name: "summary persistence failed", err: llm.ErrCompactionPersist, wantStatus: http.StatusInternalServerError, wantReason: "persistence_failed"},
		{name: "timed out", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantReason: "timed_out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeCompactConversationError(context.Background(), rec, llm.ManualCompactionResult{Reason: "nothing_to_compact"}, tt.err)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
			var result llm.ManualCompactionResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("response is not a compaction result: %v (body=%s)", err, rec.Body.String())
			}
			if result.Reason != tt.wantReason || result.Compacted {
				t.Fatalf("result=%+v, want reason %q and compacted=false", result, tt.wantReason)
			}
		})
	}
}

func TestCompactConversationCanceledRequestWritesNoErrorResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	writeCompactConversationError(ctx, rec, llm.ManualCompactionResult{}, context.Canceled)
	if rec.Body.Len() != 0 {
		t.Fatalf("canceled request wrote a response: %q", rec.Body.String())
	}
}

func TestCompactConversationProviderCancellationIsStructuredFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCompactConversationError(context.Background(), rec, llm.ManualCompactionResult{}, context.Canceled)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
	var result llm.ManualCompactionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Reason != "compaction_failed" || result.Compacted {
		t.Fatalf("result=%+v", result)
	}
}
