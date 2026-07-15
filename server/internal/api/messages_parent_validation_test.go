package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestPostMessageRejectsInvalidExplicitBranchParentBeforeSSE(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "branch-parent-api.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','branch-api@example.com','h','user')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	for _, conversationID := range []string{"c1", "c2"} {
		if _, err := store.CreateConversation(context.Background(), db, store.Conversation{
			ID: conversationID, UserID: "u1", Title: conversationID,
		}); err != nil {
			t.Fatalf("create conversation %s: %v", conversationID, err)
		}
	}
	foreign, err := store.CreateMessage(context.Background(), db, store.Message{
		ID: "foreign-parent", ConversationID: "c2", Role: "assistant",
	})
	if err != nil {
		t.Fatalf("create foreign parent: %v", err)
	}

	for _, tt := range []struct {
		name     string
		parentID string
	}{
		{name: "missing", parentID: "m_stale_optimistic_parent"},
		{name: "other conversation", parentID: foreign.ID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"text":"edited question","parent_id":"` + tt.parentID + `","branch":true,"tool_mode":"enabled"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/messages", body)
			ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"})
			ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": "c1"})
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()
			var logs bytes.Buffer
			handler := errorResponseLoggingMiddleware(log.New(&logs, "", 0), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				postMessageHandler(Deps{DB: db}, w, r)
			}))

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
			}
			if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("content-type = %q, want JSON (SSE must not have started)", contentType)
			}
			if !strings.Contains(rec.Body.String(), llm.ErrInvalidMessageParent.Error()) {
				t.Fatalf("response body %q does not contain domain error %q", rec.Body.String(), llm.ErrInvalidMessageParent)
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "foreign key") || strings.Contains(strings.ToLower(rec.Body.String()), "save user message") {
				t.Fatalf("response leaked persistence details: %s", rec.Body.String())
			}
			logText := strings.ToLower(logs.String())
			if !strings.Contains(logText, "status=409") || !strings.Contains(logText, llm.ErrInvalidMessageParent.Error()) {
				t.Fatalf("409 domain reason missing from common HTTP log: %s", logs.String())
			}
			if strings.Contains(logText, "foreign key") || strings.Contains(logText, "save user message") || strings.Contains(logText, "23503") {
				t.Fatalf("common HTTP log leaked persistence details: %s", logs.String())
			}
			var messageCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&messageCount); err != nil {
				t.Fatalf("count primary messages: %v", err)
			}
			if messageCount != 0 {
				t.Fatalf("rejected branch persisted %d messages", messageCount)
			}
		})
	}
}
