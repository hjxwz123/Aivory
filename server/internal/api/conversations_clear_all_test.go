package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestClearAllConversationsHandlerDeletesEveryPersonalConversation(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "clear-all-conversations.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('owner','owner@example.test','h','user'),
		('other','other@example.test','h','user')`)

	for i := 0; i < 25; i++ {
		archived := 0
		if i%2 == 0 {
			archived = 1
		}
		mustExec(t, db, `INSERT INTO conversations(id,user_id,title,archived) VALUES(?,?,?,?)`,
			"personal-"+string(rune('a'+i)), "owner", "Personal", archived)
	}
	// Inline threads are removed with their root, but never appear as a second
	// root in the batch selection.
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,inline_source_conv) VALUES
		('personal-inline','owner','Inline','personal-a')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES
		('other-personal','other','Other')`)

	workspace, err := store.CreateWorkspace(ctx, db, "owner", "Shared")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id,is_public) VALUES
		('workspace-conversation','owner','Shared',?,1)`, workspace.ID)

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	clearAllConversationsHandler(Deps{DB: db, Cache: cache.NewMemory()}, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		DeletedConversations int `json:"deleted_conversations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.DeletedConversations != 25 {
		t.Fatalf("deleted roots=%d, want 25", response.DeletedConversations)
	}

	for _, check := range []struct {
		name  string
		query string
		want  int
	}{
		{"owner personal roots", `SELECT COUNT(*) FROM conversations WHERE user_id='owner' AND COALESCE(workspace_id,'')='' AND COALESCE(inline_source_conv,'')=''`, 0},
		{"owner inline conversations", `SELECT COUNT(*) FROM conversations WHERE id='personal-inline'`, 0},
		{"other user", `SELECT COUNT(*) FROM conversations WHERE id='other-personal'`, 1},
		{"workspace conversation", `SELECT COUNT(*) FROM conversations WHERE id='workspace-conversation'`, 1},
	} {
		var got int
		if err := db.QueryRowContext(ctx, check.query).Scan(&got); err != nil || got != check.want {
			t.Fatalf("%s count=%d err=%v, want %d", check.name, got, err, check.want)
		}
	}
}

func TestClearAllConversationsHandlerRequiresDeletePermission(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "clear-all-conversations-permission.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES
		('no-delete','No deletion','{"allow_conversation_deletion":false}')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id) VALUES
		('owner','owner@example.test','h','user','no-delete')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES
		('personal','owner','Personal')`)

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	clearAllConversationsHandler(Deps{DB: db, Cache: cache.NewMemory()}, rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("clear status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id='personal'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restricted conversation count=%d err=%v, want 1", count, err)
	}
}
