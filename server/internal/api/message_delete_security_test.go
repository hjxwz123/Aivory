package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestDeleteMessageHandlerRejectsWorkspaceBranchWithForeignDescendants(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-delete-security.db"))
	t.Cleanup(func() { _ = db.Close() })
	for _, user := range []struct{ id, email string }{
		{"owner", "owner-delete@example.test"},
		{"member-a", "member-a-delete@example.test"},
		{"member-b", "member-b-delete@example.test"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?,?,'user')`, user.id, user.email, "h")
	}
	workspace, err := store.CreateWorkspace(t.Context(), db, "owner", "Delete security")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, userID := range []string{"member-a", "member-b"} {
		if err := store.JoinWorkspace(t.Context(), db, workspace.ID, userID); err != nil {
			t.Fatalf("join %s: %v", userID, err)
		}
	}
	conversation, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "delete-security-conversation", UserID: "owner", WorkspaceID: workspace.ID, Title: "Shared",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	insert := func(id, parent, role, author string, createdAt int64) {
		t.Helper()
		var parentValue any
		if parent != "" {
			parentValue = parent
		}
		mustExec(t, db,
			`INSERT INTO messages(id,conversation_id,parent_id,role,author_id,created_at) VALUES(?,?,?,?,?,?)`,
			id, conversation.ID, parentValue, role, author, createdAt,
		)
	}
	insert("question-a", "", "user", "member-a", 1000)
	insert("answer-foreign", "question-a", "assistant", "", 1001)
	insert("answer-legacy", "question-a", "assistant", "", 1002)
	insert("answer-own", "question-a", "assistant", "", 1003)
	insert("question-b", "answer-foreign", "user", "member-b", 1004)
	insert("answer-b", "question-b", "assistant", "", 1005)
	insert("question-legacy", "answer-legacy", "user", "", 1006)
	insert("answer-legacy-child", "question-legacy", "assistant", "", 1007)
	insert("question-a-child", "answer-own", "user", "member-a", 1008)
	insert("answer-a-child", "question-a-child", "assistant", "", 1009)
	mustExec(t, db, `UPDATE conversations SET active_leaf_id='answer-b' WHERE id=?`, conversation.ID)

	d := Deps{DB: db, Cache: cache.NewMemory()}
	request := func(userID, messageID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete,
			"/api/conversations/"+conversation.ID+"/messages/"+messageID, nil)
		ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
		ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": conversation.ID, "msgId": messageID})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		deleteMessageHandler(d, rec, req)
		return rec
	}
	assertExists := func(messageID string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(t.Context(), `SELECT id FROM messages WHERE id=?`, messageID).Scan(&got); err != nil {
			t.Fatalf("message %s should remain: %v", messageID, err)
		}
	}
	assertDeleted := func(messageID string) {
		t.Helper()
		var got string
		err := db.QueryRowContext(t.Context(), `SELECT id FROM messages WHERE id=?`, messageID).Scan(&got)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("message %s should be deleted: %v", messageID, err)
		}
	}

	// This selector passes the handler's round-root author precheck: answer-foreign
	// belongs to member-a's question. The store must still reject it because the
	// actual branch deletion set contains member-b's continuation.
	if rec := request("member-a", "answer-foreign"); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign-descendant branch status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"question-a", "answer-foreign", "question-b", "answer-b"} {
		assertExists(id)
	}

	// Direct selectors for another member's question or answer remain bound to
	// that round's author and cannot bypass the branch-root check.
	for _, id := range []string{"question-b", "answer-b"} {
		if rec := request("member-a", id); rec.Code != http.StatusNotFound {
			t.Fatalf("foreign round via %s status=%d body=%s", id, rec.Code, rec.Body.String())
		}
	}
	assertExists("question-b")
	assertExists("answer-b")

	if rec := request("member-a", "answer-legacy"); rec.Code != http.StatusNotFound {
		t.Fatalf("legacy-descendant branch status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertExists("answer-legacy")
	assertExists("question-legacy")

	if rec := request("member-a", "answer-own"); rec.Code != http.StatusOK {
		t.Fatalf("own branch status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"answer-own", "question-a-child", "answer-a-child"} {
		assertDeleted(id)
	}
	for _, id := range []string{"answer-foreign", "question-b", "answer-b", "answer-legacy", "question-legacy"} {
		assertExists(id)
	}
}
