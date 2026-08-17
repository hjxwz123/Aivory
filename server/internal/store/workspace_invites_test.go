package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// §workspace RBAC phase 3 — invite records and ownership transfer.

func TestWorkspaceInviteLifecycle(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// Ordinary members cannot mint invites.
	if _, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "member", "", WorkspaceRoleMember, 0, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member create invite error=%v, want ErrForbidden", err)
	}
	// Ordinary admins may invite members and guests…
	if _, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "admin", "", WorkspaceRoleMember, 0, 5); err != nil {
		t.Fatalf("admin member invite: %v", err)
	}
	// …but never admins.
	if _, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "admin", "", WorkspaceRoleAdmin, 0, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin admin-invite error=%v, want ErrForbidden", err)
	}
	// The owner may mint admin invites.
	adminInvite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleAdmin, 0, 1)
	if err != nil {
		t.Fatalf("owner admin-invite: %v", err)
	}

	// Email binding: full match required.
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('invitee','invitee@example.test','h','user','active')`)
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('other','other@example.test','h','user','active')`)
	bound, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "Invitee@Example.test", WorkspaceRoleMember, 0, 0)
	if err != nil {
		t.Fatalf("owner bound invite: %v", err)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, bound.Token, "other", "other@example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("email mismatch join error=%v, want ErrNotFound", err)
	}
	ws, role, err := JoinWorkspaceByInviteRecord(ctx, fx.db, bound.Token, "invitee", "invitee@example.test")
	if err != nil || role != WorkspaceRoleMember || ws.ID != fx.workspaceID {
		t.Fatalf("email match join ws=%+v role=%q err=%v", ws, role, err)
	}
	if r, _ := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, "invitee"); r != WorkspaceRoleMember {
		t.Fatalf("invitee role=%q, want member", r)
	}

	// Preview resolves live invites and hides dead ones.
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, adminInvite.Token); err != nil {
		t.Fatalf("preview live invite: %v", err)
	}
	expired, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleGuest, time.Now().Add(-time.Hour).Unix(), 0)
	if err != nil {
		t.Fatalf("create expired invite: %v", err)
	}
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, expired.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired preview error=%v, want ErrNotFound", err)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, expired.Token, "other", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired join error=%v, want ErrNotFound", err)
	}

	// One-time invites die after a single use.
	oneTime, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleGuest, 0, 1)
	if err != nil {
		t.Fatalf("create one-time invite: %v", err)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, oneTime.Token, "other", ""); err != nil {
		t.Fatalf("one-time join: %v", err)
	}
	if r, _ := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, "other"); r != WorkspaceRoleGuest {
		t.Fatalf("one-time join role=%q, want guest", r)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, oneTime.Token, "invitee", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted join error=%v, want ErrNotFound", err)
	}

	// Revocation kills even unlimited invites.
	unlimited, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleMember, 0, 0)
	if err != nil {
		t.Fatalf("create unlimited invite: %v", err)
	}
	if err := RevokeWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", unlimited.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, err := JoinWorkspaceByInviteRecord(ctx, fx.db, unlimited.Token, "invitee", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked join error=%v, want ErrNotFound", err)
	}

	// Listing is admin-only surface.
	if _, err := ListWorkspaceInvites(ctx, fx.db, fx.workspaceID, "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member list invites error=%v, want ErrForbidden", err)
	}
	invites, err := ListWorkspaceInvites(ctx, fx.db, fx.workspaceID, "admin")
	if err != nil || len(invites) < 4 {
		t.Fatalf("admin list invites=%d err=%v, want >=4", len(invites), err)
	}
}

func TestWorkspaceInviteOneTimeConcurrency(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	for i := 0; i < 4; i++ {
		exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?,'h','user','active')`,
			"racer"+string(rune('a'+i)), "racer"+string(rune('a'+i))+"@example.test")
	}
	invite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	const racers = 4
	results := make(chan error, racers)
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			_, _, err := JoinWorkspaceByInviteRecord(context.Background(), fx.db, invite.Token, "racer"+string(rune('a'+i)), "")
			results <- err
		}(i)
	}
	start.Done()
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected join error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("one-time invite succeeded %d times, want exactly 1", succeeded)
	}
	var members int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=?`, fx.workspaceID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	// owner/admin/member/guest + exactly one racer.
	if members != 5 {
		t.Fatalf("member rows=%d, want 5", members)
	}
}

func TestRotateWorkspaceInviteUsesBoundedAuditedInviteRecord(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	workspace, err := GetWorkspace(ctx, fx.db, fx.workspaceID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	// The persisted compatibility value must no longer resolve or be consumable.
	if _, err := GetWorkspaceByInviteToken(ctx, fx.db, workspace.InviteToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy preview error=%v, want ErrNotFound", err)
	}
	if _, err := JoinWorkspaceByInviteToken(ctx, fx.db, workspace.InviteToken, "guest"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy join error=%v, want ErrNotFound", err)
	}
	// A user who is no longer an administrator cannot rotate after a stale
	// handler approval; the store re-check is the final authority.
	if _, err := UpdateWorkspaceMemberRole(ctx, fx.db, fx.workspaceID, "owner", "admin", WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote admin: %v", err)
	}
	if _, err := RotateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("demoted admin rotate error=%v, want ErrForbidden", err)
	}
	if _, err := RotateWorkspaceInvite(ctx, fx.db, "missing-workspace", "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing workspace rotate error=%v, want ErrNotFound", err)
	}

	first, err := RotateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner")
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	var role, purpose string
	var expiresAt, maxUses, usedCount, revokedAt int64
	if err := fx.db.QueryRowContext(ctx, `SELECT role, purpose, expires_at, max_uses, used_count, revoked_at
		FROM workspace_invites WHERE token=?`, first).Scan(&role, &purpose, &expiresAt, &maxUses, &usedCount, &revokedAt); err != nil {
		t.Fatalf("load quick invite: %v", err)
	}
	if role != WorkspaceRoleMember || purpose != workspaceInvitePurposeQuickLink || maxUses != 1 || usedCount != 0 || revokedAt != 0 {
		t.Fatalf("quick invite constraints role=%q purpose=%q max=%d used=%d revoked=%d", role, purpose, maxUses, usedCount, revokedAt)
	}
	if expiresAt <= time.Now().Unix() || expiresAt > time.Now().Add(quickWorkspaceInviteTTL+time.Minute).Unix() {
		t.Fatalf("quick invite expiry=%d is not bounded to the expected window", expiresAt)
	}
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, first); err != nil {
		t.Fatalf("quick invite preview: %v", err)
	}
	second, err := RotateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner")
	if err != nil || second == first {
		t.Fatalf("second rotate token=%q err=%v", second, err)
	}
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("superseded quick invite preview error=%v, want ErrNotFound", err)
	}
	var auditCount int
	if err := fx.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_audit_logs WHERE workspace_id=? AND action=?`,
		fx.workspaceID, AuditInviteRotated).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("rotation audit count=%d err=%v, want 2", auditCount, err)
	}
}

func TestWorkspaceInviteExistingMemberReturnsStoredRole(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	guestInvite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleGuest, 0, 1)
	if err != nil {
		t.Fatalf("create guest invite: %v", err)
	}
	_, role, err := JoinWorkspaceByInviteRecord(ctx, fx.db, guestInvite.Token, "admin", "admin@example.test")
	if err != nil || role != WorkspaceRoleAdmin {
		t.Fatalf("existing admin join role=%q err=%v, want admin", role, err)
	}
	var used int
	if err := fx.db.QueryRowContext(ctx, `SELECT used_count FROM workspace_invites WHERE id=?`, guestInvite.ID).Scan(&used); err != nil || used != 0 {
		t.Fatalf("existing admin consumed invite used=%d err=%v", used, err)
	}

	memberInvite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create member invite: %v", err)
	}
	_, role, err = JoinWorkspaceByInviteRecord(ctx, fx.db, memberInvite.Token, "guest", "guest@example.test")
	if err != nil || role != WorkspaceRoleGuest {
		t.Fatalf("existing guest join role=%q err=%v, want guest", role, err)
	}
	if err := fx.db.QueryRowContext(ctx, `SELECT used_count FROM workspace_invites WHERE id=?`, memberInvite.ID).Scan(&used); err != nil || used != 0 {
		t.Fatalf("existing guest consumed invite used=%d err=%v", used, err)
	}
}

func TestWorkspaceInviteCreatorForeignKeyUsesSetNull(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	invite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "admin", "", WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create admin invite: %v", err)
	}
	// Exercise the database constraint directly. Normal account deletion removes
	// creator-owned capabilities first, while this assertion protects the schema
	// migration for unrelated administrative deletion paths as well.
	if _, err := fx.db.ExecContext(ctx, `DELETE FROM users WHERE id='admin'`); err != nil {
		t.Fatalf("delete invite creator directly: %v", err)
	}
	var createdBy string
	if err := fx.db.QueryRowContext(ctx, `SELECT COALESCE(created_by,'') FROM workspace_invites WHERE id=?`, invite.ID).Scan(&createdBy); err != nil {
		t.Fatalf("load invite after creator delete: %v", err)
	}
	if createdBy != "" {
		t.Fatalf("invite creator after delete=%q, want null", createdBy)
	}
}

func TestMigrateWorkspaceInviteCreatorForeignKeyFromRestrictiveSchema(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-invite-migration.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE workspace_invites`); err != nil {
		t.Fatalf("drop current invite table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE workspace_invites (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		token TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'guest',
		expires_at INTEGER NOT NULL DEFAULT 0,
		max_uses INTEGER NOT NULL DEFAULT 1,
		used_count INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL REFERENCES users(id),
		revoked_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create restrictive invite table: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('owner','owner@example.test','h','user','active')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('creator','creator@example.test','h','user','active')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('migration-ws','Migration','owner','legacy')`)
	exec(t, db, `INSERT INTO workspace_invites(id,workspace_id,token,created_by) VALUES('migration-invite','migration-ws','migration-token','creator')`)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate restrictive invite table: %v", err)
	}
	var purpose string
	if err := db.QueryRowContext(ctx, `SELECT purpose FROM workspace_invites WHERE id='migration-invite'`).Scan(&purpose); err != nil || purpose != workspaceInvitePurposeManual {
		t.Fatalf("migrated purpose=%q err=%v, want manual", purpose, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id='creator'`); err != nil {
		t.Fatalf("delete creator after FK migration: %v", err)
	}
	var createdBy string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(created_by,'') FROM workspace_invites WHERE id='migration-invite'`).Scan(&createdBy); err != nil || createdBy != "" {
		t.Fatalf("migrated FK creator=%q err=%v, want null", createdBy, err)
	}
}

func TestWorkspaceOwnershipTransfer(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	oldOwnerAdminInvite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleAdmin, 0, 1)
	if err != nil {
		t.Fatalf("create old-owner admin invite: %v", err)
	}
	memberInvite, err := CreateWorkspaceInvite(ctx, fx.db, fx.workspaceID, "owner", "", WorkspaceRoleMember, 0, 1)
	if err != nil {
		t.Fatalf("create old-owner member invite: %v", err)
	}

	// Only the current owner may initiate.
	if _, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "admin", "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin transfer error=%v, want ErrForbidden", err)
	}
	// The receiver must be a current member.
	if _, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "owner", "stranger"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger transfer error=%v, want ErrNotFound", err)
	}
	// Self-transfer is rejected.
	if _, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "owner", "owner"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self transfer error=%v, want ErrForbidden", err)
	}

	ws, err := TransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "owner", "member")
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if ws.OwnerID != "member" {
		t.Fatalf("new owner=%q, want member", ws.OwnerID)
	}
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, oldOwnerAdminInvite.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old owner admin invite after transfer=%v, want ErrNotFound", err)
	}
	if _, err := GetWorkspaceInvitePreview(ctx, fx.db, memberInvite.Token); err != nil {
		t.Fatalf("old owner member invite should remain live: %v", err)
	}
	// Receiver becomes admin; previous owner keeps admin but loses owner-
	// exclusive authority.
	if role, _ := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, "member"); role != WorkspaceRoleAdmin {
		t.Fatalf("new owner role=%q, want admin", role)
	}
	if role, _ := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, "owner"); role != WorkspaceRoleAdmin {
		t.Fatalf("old owner role=%q, want retained admin", role)
	}
	oldOwnerDecision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "owner", Action: ActionWorkspaceDelete,
	})
	if oldOwnerDecision.Allowed {
		t.Fatal("old owner kept owner-exclusive delete authority")
	}
	newOwnerDecision, _ := AuthorizeWorkspace(ctx, fx.db, WorkspaceAuthorizationRequest{
		WorkspaceID: fx.workspaceID, UserID: "member", Action: ActionWorkspaceDelete,
	})
	if !newOwnerDecision.Allowed {
		t.Fatal("new owner lacks owner-exclusive delete authority")
	}
	// The owner row is protected from the (new) owner's own kick/re-role.
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "member", "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("new owner self-kick error=%v, want ErrForbidden", err)
	}
}

func TestDeleteUserBlockedWhileOwningMemberWorkspace(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	// The fixture workspace has other members, so its owner cannot delete the
	// account without transferring or deleting the workspace first.
	if err := DeleteUser(ctx, fx.db, "owner"); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatalf("owner delete error=%v, want ErrWorkspaceOwnership", err)
	}
}
