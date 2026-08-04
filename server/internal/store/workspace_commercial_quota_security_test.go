package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWorkspaceResourcesCountAgainstCreatorCommercialCapsWhileAccessible(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "workspace-commercial-quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?,'x','user','active')`, id, id+"@example.test")
	}
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('quota-channel','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('embed','quota-channel','embedding','embed','Embedding',3)`)
	ws, err := CreateWorkspace(ctx, db, "owner", "Quota")
	if err != nil {
		t.Fatal(err)
	}
	if err := JoinWorkspace(ctx, db, ws.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(ctx, db, Project{ID: "personal-project", UserID: "member", Name: "Personal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(ctx, db, Project{ID: "workspace-project", UserID: "member", WorkspaceID: ws.ID, Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateKB(ctx, db, KnowledgeBase{ID: "personal-kb", UserID: "member", Name: "Personal", EmbeddingModelID: "embed", EmbeddingDim: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateKB(ctx, db, KnowledgeBase{ID: "workspace-kb", UserID: "member", WorkspaceID: ws.ID, Name: "Shared", EmbeddingModelID: "embed", EmbeddingDim: 3}); err != nil {
		t.Fatal(err)
	}

	if n, err := CountProjectsByUser(ctx, db, "member"); err != nil || n != 2 {
		t.Fatalf("accessible project cap count = %d, %v; want 2", n, err)
	}
	if n, err := CountStandaloneKBsByUser(ctx, db, "member"); err != nil || n != 2 {
		t.Fatalf("accessible KB cap count = %d, %v; want 2", n, err)
	}

	if err := RemoveWorkspaceMember(ctx, db, ws.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if n, err := CountProjectsByUser(ctx, db, "member"); err != nil || n != 1 {
		t.Fatalf("revoked project cap count = %d, %v; want personal-only 1", n, err)
	}
	if n, err := CountStandaloneKBsByUser(ctx, db, "member"); err != nil || n != 1 {
		t.Fatalf("revoked KB cap count = %d, %v; want personal-only 1", n, err)
	}
}
