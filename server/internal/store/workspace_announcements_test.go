package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkspaceAnnouncementScopesAndAuthorizes(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	config := `{"enabled":true,"title":"Only this space","body":"hello","remember_dismiss":true,"updated_at":1}`

	if _, err := UpdateWorkspaceAnnouncement(ctx, fx.db, fx.workspaceID, "member", config); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member update error=%v, want ErrForbidden", err)
	}
	if _, err := UpdateWorkspaceAnnouncement(ctx, fx.db, fx.workspaceID, "owner", config); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	for _, userID := range []string{"owner", "admin", "member", "guest"} {
		got, err := GetWorkspaceAnnouncement(ctx, fx.db, fx.workspaceID, userID)
		if err != nil || !strings.Contains(got, "Only this space") {
			t.Fatalf("%s read config=%q err=%v", userID, got, err)
		}
	}
	if _, err := GetWorkspaceAnnouncement(ctx, fx.db, fx.workspaceID, "outsider"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider read error=%v, want ErrNotFound", err)
	}
	var auditCount int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM workspace_audit_logs WHERE workspace_id=? AND action=?`, fx.workspaceID, AuditAnnouncementUpdated).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("announcement audit rows=%d, want 1", auditCount)
	}
}

func TestWorkspaceAnnouncementCascadesWithWorkspace(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	workspace, err := CreateWorkspace(ctx, fx.db, "owner", "Cascade")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateWorkspaceAnnouncement(ctx, fx.db, workspace.ID, "owner", `{"enabled":true}`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := fx.db.Exec(`DELETE FROM workspaces WHERE id=?`, workspace.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM workspace_announcements WHERE workspace_id=?`, workspace.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("announcement rows after cascade=%d, want 0", count)
	}
}
