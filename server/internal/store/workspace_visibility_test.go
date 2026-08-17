package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// §workspace RBAC phase 2 — unified private/workspace visibility on projects
// and knowledge bases (is_public), scope changes and RAG attach boundaries.

func TestWorkspaceProjectKnowledgeBaseVisibilityMatrix(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t) // shared conv/kb/project, roles owner/admin/member/guest

	privateProject, err := CreateProject(ctx, fx.db, Project{
		UserID: "member", WorkspaceID: fx.workspaceID, Name: "Member private project", IsPublic: false,
	})
	if err != nil {
		t.Fatalf("create private project: %v", err)
	}
	privateKB, err := CreateKB(ctx, fx.db, KnowledgeBase{
		UserID: "member", WorkspaceID: fx.workspaceID, Name: "Member private KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3, IsPublic: false,
	})
	if err != nil {
		t.Fatalf("create private kb: %v", err)
	}

	// Shared rows: every member (guests included) reads them.
	for _, actor := range []string{"owner", "admin", "member", "guest"} {
		if _, err := GetProject(ctx, fx.db, fx.projectID, actor); err != nil {
			t.Fatalf("%s read shared project: %v", actor, err)
		}
		if _, err := GetKB(ctx, fx.db, fx.kbID, actor); err != nil {
			t.Fatalf("%s read shared kb: %v", actor, err)
		}
	}
	// Private rows: creator + admins only.
	for _, actor := range []string{"member", "owner", "admin"} {
		if _, err := GetProject(ctx, fx.db, privateProject.ID, actor); err != nil {
			t.Fatalf("%s read private project: %v", actor, err)
		}
	}
	for _, actor := range []string{"guest"} {
		if _, err := GetProject(ctx, fx.db, privateProject.ID, actor); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s private project read err=%v, want ErrNotFound", actor, err)
		}
		if _, err := GetKB(ctx, fx.db, privateKB.ID, actor); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s private kb read err=%v, want ErrNotFound", actor, err)
		}
	}

	// A second ordinary member cannot see another member's private rows.
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, "member2"); err != nil {
		t.Fatalf("join member2: %v", err)
	}
	if _, err := GetProject(ctx, fx.db, privateProject.ID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 private project err=%v, want ErrNotFound", err)
	}
	if _, err := GetKB(ctx, fx.db, privateKB.ID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 private kb err=%v, want ErrNotFound", err)
	}

	// Listings respect visibility for every role.
	projects, err := ListWorkspaceProjectsForUser(ctx, fx.db, fx.workspaceID, "member2")
	if err != nil || len(projects) != 1 || !projects[0].IsPublic {
		t.Fatalf("member2 project list=%+v err=%v, want only the shared row", projects, err)
	}
	kbs, err := ListWorkspaceKBsForUser(ctx, fx.db, fx.workspaceID, "guest")
	if err != nil || len(kbs) != 1 {
		t.Fatalf("guest kb list=%+v err=%v, want only the shared row", kbs, err)
	}
	adminProjects, err := ListWorkspaceProjectsForUser(ctx, fx.db, fx.workspaceID, "admin")
	if err != nil || len(adminProjects) != 2 {
		t.Fatalf("admin project list=%+v err=%v, want shared+private", adminProjects, err)
	}

	// RAG attach boundary (OwnedKBIDs): another member cannot attach the
	// creator's private KB inside the workspace, but the shared one passes.
	if got := OwnedKBIDs(ctx, fx.db, "member2", fx.workspaceID, []string{privateKB.ID, fx.kbID}); len(got) != 1 || got[0] != fx.kbID {
		t.Fatalf("member2 owned kb ids=%v, want only the shared kb", got)
	}
	// The creator and admins keep their private KB attachable.
	if got := OwnedKBIDs(ctx, fx.db, "member", fx.workspaceID, []string{privateKB.ID}); len(got) != 1 {
		t.Fatalf("creator owned kb ids=%v, want the private kb", got)
	}
	if got := OwnedKBIDs(ctx, fx.db, "admin", fx.workspaceID, []string{privateKB.ID}); len(got) != 1 {
		t.Fatalf("admin owned kb ids=%v, want the private kb", got)
	}
}

func TestWorkspaceVisibilityChanges(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member2','member2@example.test','h','user','active')`)
	if err := JoinWorkspace(ctx, fx.db, fx.workspaceID, "member2"); err != nil {
		t.Fatalf("join member2: %v", err)
	}

	// The creator privatizes their project; ordinary members lose it.
	makePrivate := false
	updated, err := UpdateProject(ctx, fx.db, fx.projectID, "member", ProjectPatch{IsPublic: &makePrivate})
	if err != nil || updated.IsPublic {
		t.Fatalf("creator make private: %+v err=%v", updated, err)
	}
	if _, err := GetProject(ctx, fx.db, fx.projectID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 after privatize err=%v, want ErrNotFound", err)
	}
	if _, err := GetProject(ctx, fx.db, fx.projectID, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest after privatize err=%v, want ErrNotFound", err)
	}
	// Admins still see it and may re-share.
	makeShared := true
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, "admin", ProjectPatch{IsPublic: &makeShared}); err != nil {
		t.Fatalf("admin re-share: %v", err)
	}
	if _, err := GetProject(ctx, fx.db, fx.projectID, "member2"); err != nil {
		t.Fatalf("member2 after re-share: %v", err)
	}

	// Another member cannot flip the creator's project scope.
	if _, err := UpdateProject(ctx, fx.db, fx.projectID, "member2", ProjectPatch{IsPublic: &makePrivate}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 scope flip err=%v, want ErrNotFound", err)
	}

	// KB visibility flips through the dedicated store path.
	kb, err := UpdateKBVisibility(ctx, fx.db, fx.kbID, "member", false)
	if err != nil || kb.IsPublic {
		t.Fatalf("creator kb private: %+v err=%v", kb, err)
	}
	if _, err := GetKB(ctx, fx.db, fx.kbID, "member2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 private kb err=%v, want ErrNotFound", err)
	}
	if _, err := UpdateKBVisibility(ctx, fx.db, fx.kbID, "member2", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member2 kb scope flip err=%v, want ErrNotFound", err)
	}
	if _, err := UpdateKBVisibility(ctx, fx.db, fx.kbID, "admin", true); err != nil {
		t.Fatalf("admin kb re-share: %v", err)
	}

	// Personal KBs have no visibility scope.
	personal, err := CreateKB(ctx, fx.db, KnowledgeBase{UserID: "member", Name: "Personal", EmbeddingModelID: "emb-a", EmbeddingDim: 3})
	if err != nil {
		t.Fatalf("create personal kb: %v", err)
	}
	if _, err := UpdateKBVisibility(ctx, fx.db, personal.ID, "member", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal kb visibility err=%v, want ErrNotFound", err)
	}

	// The authorizer mirrors the same boundaries.
	decision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "member2", Action: ActionProjectRead,
		Resource: "project", ResourceID: fx.projectID,
	})
	if !decision.Allowed {
		t.Fatalf("authorizer shared project read denied: %+v", decision)
	}
}

func TestProjectVisibilitySynchronizesItsDedicatedKnowledgeBase(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	project, err := CreateProject(ctx, fx.db, Project{
		UserID: "member", WorkspaceID: fx.workspaceID, Name: "Project with library", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	kb, err := CreateKB(ctx, fx.db, KnowledgeBase{
		UserID: "member", WorkspaceID: fx.workspaceID, ProjectID: project.ID,
		Name: "Project library", EmbeddingModelID: "emb-a", EmbeddingDim: 3, IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create project library: %v", err)
	}
	exec(t, fx.db, `UPDATE projects SET kb_id=? WHERE id=?`, kb.ID, project.ID)

	makePrivate := false
	if _, err := UpdateProject(ctx, fx.db, project.ID, "member", ProjectPatch{IsPublic: &makePrivate}); err != nil {
		t.Fatalf("private project: %v", err)
	}
	if _, err := GetKB(ctx, fx.db, kb.ID, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest read private project library=%v, want ErrNotFound", err)
	}
	var public int
	if err := fx.db.QueryRowContext(ctx, `SELECT is_public FROM knowledge_bases WHERE id=?`, kb.ID).Scan(&public); err != nil || public != 0 {
		t.Fatalf("project library visibility=%d err=%v, want 0", public, err)
	}
}

func TestWorkspaceVisibilityMigrationKeepsExistingRowsShared(t *testing.T) {
	ctx := context.Background()
	dbh, err := Open(filepath.Join(t.TempDir(), "visibility-migration.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer dbh.Close()
	if _, err := dbh.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	// Simulate a pre-phase-2 database: the workspace columns exist (an earlier
	// migration added them) but the visibility columns do not.
	exec(t, dbh, `ALTER TABLE projects DROP COLUMN is_public`)
	exec(t, dbh, `ALTER TABLE knowledge_bases DROP COLUMN is_public`)
	exec(t, dbh, `ALTER TABLE projects ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`)
	exec(t, dbh, `ALTER TABLE knowledge_bases ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`)
	exec(t, dbh, `INSERT INTO users(id,email,password_hash,role,status) VALUES('owner','owner@example.test','h','user','active')`)
	exec(t, dbh, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member','member@example.test','h','user','active')`)
	exec(t, dbh, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('vis-ws','Visibility','owner','vis-token')`)
	exec(t, dbh, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('vis-ws','owner','admin')`)
	exec(t, dbh, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('vis-ws','member','member')`)
	exec(t, dbh, `INSERT INTO channels(id,name,type) VALUES('vis-ch','Embedding','openai')`)
	exec(t, dbh, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('vis-emb','vis-ch','embedding','vis-emb','Embedding',3)`)
	exec(t, dbh, `INSERT INTO projects(id,user_id,name,workspace_id) VALUES('legacy-project','owner','Legacy','vis-ws')`)
	exec(t, dbh, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('legacy-kb','owner','Legacy KB','vis-emb',3,'vis-ws')`)

	if err := Migrate(dbh); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// §17.3: previously shared rows must stay shared — the member keeps access.
	if _, err := GetProject(ctx, dbh, "legacy-project", "member"); err != nil {
		t.Fatalf("legacy project after migration: %v", err)
	}
	if _, err := GetKB(ctx, dbh, "legacy-kb", "member"); err != nil {
		t.Fatalf("legacy kb after migration: %v", err)
	}
}
