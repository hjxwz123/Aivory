package api

import (
	"context"
	"database/sql"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestTeardownWorkspaceKeepsParentWhenProjectDeleteFailsAndCanRetry(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-teardown.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status)
		VALUES('owner','owner@example.test','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('embedding-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim)
		VALUES('emb','embedding-channel','embedding','emb','Embedding',3)`)
	workspace, err := store.CreateWorkspace(ctx, db, "owner", "Retryable teardown")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustExec(t, db, `INSERT INTO knowledge_bases(
		id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id
	) VALUES('project-kb','owner','Project library','emb',3,'project-1',?)`, workspace.ID)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('project-1','owner','Project','project-kb',?)`, workspace.ID)

	_, projectIDs, standaloneKBIDs, err := store.WorkspaceContentIDs(ctx, db, workspace.ID)
	if err != nil {
		t.Fatalf("workspace content ids: %v", err)
	}
	if len(projectIDs) != 1 || projectIDs[0] != "project-1" || len(standaloneKBIDs) != 0 {
		t.Fatalf("teardown worklist projects=%v standalone_kbs=%v", projectIDs, standaloneKBIDs)
	}

	mustExec(t, db, `CREATE TRIGGER block_project_delete
		BEFORE DELETE ON projects WHEN OLD.id='project-1'
		BEGIN SELECT RAISE(ABORT, 'blocked project delete'); END`)
	memoryCache := cache.NewMemory()
	d := Deps{
		DB:     db,
		Cache:  memoryCache,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	req := httptest.NewRequest("DELETE", "/api/workspaces/"+workspace.ID, nil)
	if err := teardownWorkspace(d, req, workspace); err == nil {
		t.Fatal("teardown succeeded despite blocked project delete")
	}
	assertWorkspaceTeardownRowCount(t, db, "workspaces", workspace.ID, 1)
	assertWorkspaceTeardownRowCount(t, db, "projects", "project-1", 1)
	assertWorkspaceTeardownRowCount(t, db, "knowledge_bases", "project-kb", 1)
	if _, revoked := memoryCache.Get(workspaceGenerationRevocationKey(workspace.ID)); revoked {
		t.Fatal("failed teardown left the workspace-wide generation revocation active")
	}

	mustExec(t, db, `DROP TRIGGER block_project_delete`)
	if err := teardownWorkspace(d, req, workspace); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	assertWorkspaceTeardownRowCount(t, db, "workspaces", workspace.ID, 0)
	assertWorkspaceTeardownRowCount(t, db, "projects", "project-1", 0)
	assertWorkspaceTeardownRowCount(t, db, "knowledge_bases", "project-kb", 0)
}

func assertWorkspaceTeardownRowCount(t *testing.T, db *sql.DB, table, id string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatalf("count %s %s: %v", table, id, err)
	}
	if got != want {
		t.Fatalf("%s %s rows=%d, want %d", table, id, got, want)
	}
}
