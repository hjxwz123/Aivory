package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestWorkspaceUploadPreflightsCanonicalOwnerQuotaBeforeWritingBytes(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-upload-quota.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,created_at,updated_at) VALUES('owner-capped','Owner capped',1,1,1)`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('owner','owner@example.test','h','user','active','owner-capped')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member','member@example.test','h','user','active')`)
	workspace, err := store.CreateWorkspace(ctx, db, "owner", "Storage")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	if _, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "workspace-conversation", UserID: "member", WorkspaceID: workspace.ID, Title: "Shared",
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at)
		VALUES('owner-full','owner','full.bin','application/octet-stream',1048576,'/quota/owner-full','other',0,1)`)

	uploadDir := filepath.Join(t.TempDir(), "uploads")
	deps := Deps{
		DB: db, Cache: cache.NewMemory(),
		Config: config.Config{UploadDir: uploadDir, MaxUploadBytes: 2 << 20},
	}
	member := &store.User{ID: "member", Role: "user", Status: "active"}
	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "committed", target: "/api/files?conversation_id=workspace-conversation"},
		{name: "composer draft", target: "/api/files?conversation_id=workspace-conversation&draft=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := uploadRequestWithFile(t, tc.target, member, "notes.txt", []byte("one more byte"))
			uploadFileHandler(deps, rec, req)
			if rec.Code != http.StatusInsufficientStorage {
				t.Fatalf("status=%d body=%s, want 507 owner quota", rec.Code, rec.Body.String())
			}
		})
	}
	var rows int
	mustQuery(t, db, `SELECT COUNT(*) FROM files`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("files rows=%d, want only owner's existing file", rows)
	}
	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Fatalf("upload directory exists after quota preflight: %v", err)
	}
}
