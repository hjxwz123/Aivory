package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/vector"
)

type conversationDeleteVectorRecorder struct {
	deletedConversations []string
}

func (*conversationDeleteVectorRecorder) Enabled() bool { return true }
func (*conversationDeleteVectorRecorder) Upsert(context.Context, int, []vector.Point) error {
	return nil
}
func (*conversationDeleteVectorRecorder) Search(context.Context, int, []float32, vector.Scope, int) ([]vector.Hit, error) {
	return nil, nil
}
func (*conversationDeleteVectorRecorder) SearchKeyword(context.Context, int, string, vector.Scope, int) ([]vector.Hit, error) {
	return nil, nil
}
func (*conversationDeleteVectorRecorder) ExistingChunkIDs(context.Context, int, vector.Scope) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (*conversationDeleteVectorRecorder) VectorChunkStatuses(context.Context, int, vector.Scope) (map[string]vector.ChunkVectorStatus, error) {
	return map[string]vector.ChunkVectorStatus{}, nil
}
func (*conversationDeleteVectorRecorder) AllVectorChunkStatuses(context.Context, int) (map[string]vector.ChunkVectorStatus, error) {
	return map[string]vector.ChunkVectorStatus{}, nil
}
func (*conversationDeleteVectorRecorder) DeleteByDocument(context.Context, string) error { return nil }
func (*conversationDeleteVectorRecorder) DeleteByKB(context.Context, string) error       { return nil }
func (r *conversationDeleteVectorRecorder) DeleteByConversation(_ context.Context, id string) error {
	r.deletedConversations = append(r.deletedConversations, id)
	return nil
}

func TestDeleteConversationHandlerPreservesForeignInlineStorageAndVectors(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "delete-handler.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`)
	mustExec(`INSERT INTO users(id,email,password_hash,role) VALUES('member','member@example.test','h','user')`)
	workspace, err := store.CreateWorkspace(ctx, db, "owner", "Shared")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	for _, conversation := range []store.Conversation{
		{ID: "shared-root", UserID: "owner", Title: "root", WorkspaceID: workspace.ID},
		{ID: "member-inline", UserID: "member", Title: "member", InlineSourceConv: "shared-root"},
		{ID: "owner-grandchild", UserID: "owner", Title: "grand", InlineSourceConv: "member-inline"},
	} {
		if _, err := store.CreateConversation(ctx, db, conversation); err != nil {
			t.Fatalf("create %s: %v", conversation.ID, err)
		}
	}

	uploadDir := t.TempDir()
	rootPath := filepath.Join(uploadDir, "root.txt")
	memberPath := filepath.Join(uploadDir, "member.txt")
	grandPath := filepath.Join(uploadDir, "grand.txt")
	for path, content := range map[string]string{rootPath: "root", memberPath: "member", grandPath: "grand"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	mustExec(`INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_root','owner','shared-root','root.txt',?)`, rootPath)
	mustExec(`INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_member','member','member-inline','member.txt',?)`, memberPath)
	mustExec(`INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_grand','owner','owner-grandchild','grand.txt',?)`, grandPath)

	vectorRecorder := &conversationDeleteVectorRecorder{}
	ragService := rag.New(db, nil, log.New(io.Discard, "", 0), uploadDir)
	ragService.SetVectorStore(vectorRecorder)
	deps := Deps{
		DB: db,
		Config: config.Config{
			UploadDir:   uploadDir,
			ArtifactDir: uploadDir,
		},
		RAG: ragService,
	}
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/conversations/:id", wrap(deps, deleteConversationHandler))
	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/shared-root", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{rootPath, grandPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted owner's path %q still exists or stat failed: %v", path, err)
		}
	}
	if _, err := os.Stat(memberPath); err != nil {
		t.Fatalf("foreign inline attachment was removed: %v", err)
	}
	var memberConversationID string
	if err := db.QueryRowContext(ctx, `SELECT conversation_id FROM files WHERE id='f_member'`).Scan(&memberConversationID); err != nil {
		t.Fatalf("foreign inline file row was removed: %v", err)
	}
	if memberConversationID != "member-inline" {
		t.Fatalf("foreign inline file conversation_id=%q, want member-inline", memberConversationID)
	}
	wantDeleted := map[string]bool{"shared-root": true, "owner-grandchild": true}
	if len(vectorRecorder.deletedConversations) != len(wantDeleted) {
		t.Fatalf("vector cleanup ids=%v, want root and owner grandchild only", vectorRecorder.deletedConversations)
	}
	for _, id := range vectorRecorder.deletedConversations {
		if !wantDeleted[id] {
			t.Fatalf("vector cleanup crossed ownership boundary: ids=%v", vectorRecorder.deletedConversations)
		}
	}
}
