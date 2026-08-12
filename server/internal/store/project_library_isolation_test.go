package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectLibrariesAreExcludedFromStandaloneKBsAndCannotBeDeleted(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id)
		VALUES('legacy-project-kb','creator','Legacy project library','emb-a',3,'')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('legacy-project','creator','Legacy project','legacy-project-kb','')`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id)
		VALUES('tagged-project-kb','creator','Tagged project library','emb-a',3,'detached-project','')`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id)
		VALUES('workspace-project-kb','creator','Workspace project library','emb-a',3,'ws1')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('workspace-project','creator','Workspace project','workspace-project-kb','ws1')`)

	personal, err := ListKBs(ctx, db, "creator")
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	assertKnowledgeBaseIDs(t, personal, map[string]bool{
		"legacy-project-kb": false,
		"tagged-project-kb": false,
		"personal-kb":       true,
	})

	workspace, err := ListWorkspaceKBsForUser(ctx, db, "ws1", "creator")
	if err != nil {
		t.Fatalf("ListWorkspaceKBsForUser: %v", err)
	}
	assertKnowledgeBaseIDs(t, workspace, map[string]bool{
		"workspace-project-kb": false,
		"workspace-kb":         true,
	})

	for _, tc := range []struct {
		name  string
		kbID  string
		actor string
	}{
		{name: "legacy reverse reference", kbID: "legacy-project-kb", actor: "creator"},
		{name: "durable project marker", kbID: "tagged-project-kb", actor: "creator"},
		{name: "workspace owner", kbID: "workspace-project-kb", actor: "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := DeleteKB(ctx, db, tc.kbID, tc.actor); !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteKB(%s) error=%v, want ErrNotFound", tc.kbID, err)
			}
			assertStoreRowPresence(t, db, "knowledge_bases", tc.kbID, true)
		})
	}

	var linkedKBID string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(kb_id,'') FROM projects WHERE id='legacy-project'`).Scan(&linkedKBID); err != nil {
		t.Fatalf("read legacy project: %v", err)
	}
	if linkedKBID != "legacy-project-kb" {
		t.Fatalf("legacy project kb_id=%q, want library retained", linkedKBID)
	}
}

func TestCreateProjectWithLibraryPersistsProjectOwnershipMarker(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	project, err := CreateProjectWithLibraryAndLimit(ctx, db, Project{
		ID: "new-project", UserID: "creator", Name: "New project",
	}, KnowledgeBase{
		ID: "new-project-kb", UserID: "creator", Name: "New project library",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3,
	}, 0)
	if err != nil {
		t.Fatalf("CreateProjectWithLibraryAndLimit: %v", err)
	}
	if project.KBID != "new-project-kb" {
		t.Fatalf("project kb_id=%q, want new-project-kb", project.KBID)
	}
	if project.KBEmbeddingModelID != "emb-a" || project.KBEmbeddingDim != 3 {
		t.Fatalf("project KB embedding identity=%q/%d, want emb-a/3", project.KBEmbeddingModelID, project.KBEmbeddingDim)
	}

	var projectID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(project_id,'') FROM knowledge_bases WHERE id='new-project-kb'`,
	).Scan(&projectID); err != nil {
		t.Fatalf("read project library marker: %v", err)
	}
	if projectID != project.ID {
		t.Fatalf("project library marker=%q, want %q", projectID, project.ID)
	}

	rows, err := ListKBs(ctx, db, "creator")
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	assertKnowledgeBaseIDs(t, rows, map[string]bool{"new-project-kb": false})
	if err := DeleteKB(ctx, db, "new-project-kb", "creator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteKB(project library) error=%v, want ErrNotFound", err)
	}
	assertStoreRowPresence(t, db, "knowledge_bases", "new-project-kb", true)
}

func TestDeleteProjectRequiresResourceManager(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workspaceID   string
		actor         string
		revokeCreator bool
		wantAllowed   bool
	}{
		{name: "personal creator", actor: "creator", wantAllowed: true},
		{name: "personal non-creator", actor: "member"},
		{name: "workspace owner", workspaceID: "ws1", actor: "owner", wantAllowed: true},
		{name: "workspace current creator", workspaceID: "ws1", actor: "creator", wantAllowed: true},
		{name: "workspace ordinary member", workspaceID: "ws1", actor: "member"},
		{name: "workspace former creator", workspaceID: "ws1", actor: "creator", revokeCreator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id)
				VALUES('delete-project-kb','creator','Delete project library','emb-a',3,'delete-project',?)`, tc.workspaceID)
			exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
				VALUES('delete-project','creator','Delete project','delete-project-kb',?)`, tc.workspaceID)
			if tc.revokeCreator {
				exec(t, db, `DELETE FROM workspace_members WHERE workspace_id='ws1' AND user_id='creator'`)
			}

			err := DeleteProject(context.Background(), db, "delete-project", tc.actor)
			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("DeleteProject error=%v, want success", err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteProject error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "projects", "delete-project", !tc.wantAllowed)
			assertStoreRowPresence(t, db, "knowledge_bases", "delete-project-kb", !tc.wantAllowed)
		})
	}
}

func TestDeleteProjectCleansDedicatedLibrariesAndReferencedStorage(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	storageRoot := t.TempDir()
	orphanPath := filepath.Join(storageRoot, "project-only.txt")
	sharedPath := filepath.Join(storageRoot, "shared.txt")
	if err := os.WriteFile(orphanPath, []byte("project only"), 0o600); err != nil {
		t.Fatalf("write project-only file: %v", err)
	}
	if err := os.WriteFile(sharedPath, []byte("shared"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}

	// tagged-project-kb uses the durable marker. legacy-project-kb deliberately
	// has no marker and can only be discovered through projects.kb_id.
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id) VALUES
		('tagged-delete-kb','creator','Tagged delete library','emb-a',3,'delete-project',''),
		('legacy-delete-kb','creator','Legacy delete library','emb-a',3,NULL,'')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('delete-project','creator','Delete project','legacy-delete-kb','')`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES
		('tagged-delete-document','tagged-delete-kb','tagged.txt','text/plain',1,'ready',?),
		('legacy-delete-document','legacy-delete-kb','legacy.txt','text/plain',1,'ready',?),
		('shared-keeper-document','personal-kb','keeper.txt','text/plain',1,'ready',?)`,
		orphanPath, sharedPath, sharedPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,kb_id,seq,content,embedding_model) VALUES
		('tagged-delete-chunk','tagged-delete-document','tagged-delete-kb',0,'tagged','emb-a'),
		('legacy-delete-chunk','legacy-delete-document','legacy-delete-kb',0,'legacy','emb-a')`)
	exec(t, db, `UPDATE conversations
		SET project_id='delete-project', kb_ids='["tagged-delete-kb","personal-kb","legacy-delete-kb","tagged-delete-kb"]'
		WHERE id='personal-conversation'`)

	deletion, err := DeleteProjectWithState(ctx, db, "delete-project", "creator", storageRoot)
	if err != nil {
		t.Fatalf("DeleteProjectWithState: %v", err)
	}
	if len(deletion.KnowledgeBaseIDs) != 2 || deletion.KnowledgeBaseIDs[0] != "legacy-delete-kb" || deletion.KnowledgeBaseIDs[1] != "tagged-delete-kb" {
		t.Fatalf("deleted knowledge base ids=%v, want both legacy and tagged libraries", deletion.KnowledgeBaseIDs)
	}
	pathSet := make(map[string]bool, len(deletion.StoragePaths))
	for _, path := range deletion.StoragePaths {
		pathSet[path] = true
	}
	if len(pathSet) != 2 || !pathSet[orphanPath] || !pathSet[sharedPath] {
		t.Fatalf("deleted storage paths=%v, want project-only and shared paths", deletion.StoragePaths)
	}
	for _, row := range []struct {
		table string
		id    string
	}{
		{table: "projects", id: "delete-project"},
		{table: "knowledge_bases", id: "tagged-delete-kb"},
		{table: "knowledge_bases", id: "legacy-delete-kb"},
		{table: "documents", id: "tagged-delete-document"},
		{table: "documents", id: "legacy-delete-document"},
		{table: "chunks", id: "tagged-delete-chunk"},
		{table: "chunks", id: "legacy-delete-chunk"},
	} {
		assertStoreRowPresence(t, db, row.table, row.id, false)
	}
	assertStoreRowPresence(t, db, "documents", "shared-keeper-document", true)

	var projectID sql.NullString
	var rawKBIDs string
	if err := db.QueryRowContext(ctx,
		`SELECT project_id, kb_ids FROM conversations WHERE id='personal-conversation'`,
	).Scan(&projectID, &rawKBIDs); err != nil {
		t.Fatalf("read cleaned conversation: %v", err)
	}
	if projectID.Valid {
		t.Fatalf("conversation project_id=%q, want NULL", projectID.String)
	}
	var kbIDs []string
	if err := json.Unmarshal([]byte(rawKBIDs), &kbIDs); err != nil {
		t.Fatalf("decode conversation kb_ids %q: %v", rawKBIDs, err)
	}
	if len(kbIDs) != 1 || kbIDs[0] != "personal-kb" {
		t.Fatalf("conversation kb_ids=%v, want [personal-kb]", kbIDs)
	}

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("project-only file still exists or stat failed: %v", err)
	}
	if contents, err := os.ReadFile(sharedPath); err != nil || string(contents) != "shared" {
		t.Fatalf("shared referenced file contents=%q err=%v", contents, err)
	}
}

func TestDeleteProjectRollsBackWhenConversationKBReferencesCannotBeCleaned(t *testing.T) {
	db := openKBPermissionTestDB(t)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id)
		VALUES('rollback-project-kb','creator','Rollback project library','emb-a',3,'rollback-project','')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('rollback-project','creator','Rollback project','rollback-project-kb','')`)
	exec(t, db, `UPDATE conversations SET kb_ids='["rollback-project-kb"' WHERE id='personal-conversation'`)

	if err := DeleteProject(context.Background(), db, "rollback-project", "creator"); err == nil {
		t.Fatal("DeleteProject succeeded with malformed conversation kb_ids, want rollback error")
	}
	assertStoreRowPresence(t, db, "projects", "rollback-project", true)
	assertStoreRowPresence(t, db, "knowledge_bases", "rollback-project-kb", true)
}

func assertKnowledgeBaseIDs(t *testing.T, rows []KnowledgeBase, wants map[string]bool) {
	t.Helper()
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		seen[row.ID] = true
	}
	for id, want := range wants {
		if seen[id] != want {
			t.Fatalf("knowledge base %s listed=%v, want %v (all=%v)", id, seen[id], want, seen)
		}
	}
}
