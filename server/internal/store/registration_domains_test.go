package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRegistrationDomainMigrationPreservesExistingVerificationPolicy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-domains.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE registration_domains (
		domain TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		lock_personal INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO registration_domains(domain,workspace_id,lock_personal,enabled)
		VALUES('legacy.example','legacy-workspace',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var verificationRequired int
	var initialGroup any
	if err := db.QueryRow(`SELECT email_verification_required,initial_group_id
		FROM registration_domains WHERE domain='legacy.example'`).Scan(&verificationRequired, &initialGroup); err != nil {
		t.Fatal(err)
	}
	if verificationRequired != 1 || initialGroup != nil {
		t.Fatalf("migrated policy verification=%d initial_group=%v", verificationRequired, initialGroup)
	}
}

func TestRegistrationDomainEnrollmentAndPermissions(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	rule := RegistrationDomain{Domain: " Company.Example ", WorkspaceID: fx.workspaceID, Enabled: true, LockPersonal: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := CreateRegisteredUser(ctx, fx.db, "New@Company.Example", "New", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	if user.Status != "active" {
		t.Fatal("explicit registration status must remain authoritative when domain verification is disabled")
	}
	if role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user.ID); err != nil || role != "member" {
		t.Fatalf("membership=%s err=%v", role, err)
	}
	access, err := GetDomainAccess(ctx, fx.db, user.ID)
	if err != nil || access == nil || !access.Locked || access.WorkspaceID != fx.workspaceID {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	// Old accounts are not retroactively claimed by a rule, nor are subdomains.
	for _, email := range []string{"another@sub.company.example", "another@notcompany.example"} {
		other, err := CreateUser(ctx, fx.db, email, "Other", "hash")
		if err != nil {
			t.Fatal(err)
		}
		a, err := GetDomainAccess(ctx, fx.db, other.ID)
		if err != nil || a != nil {
			t.Fatalf("unmatched email enrolled: %s", email)
		}
	}
	exec(t, fx.db, `UPDATE users SET email='changed@external.example',status='active' WHERE id=?`, user.ID)
	access, err = GetDomainAccess(ctx, fx.db, user.ID)
	if err != nil || access == nil || !access.Locked {
		t.Fatal("email edit bypassed restriction")
	}
	if err := LeaveWorkspace(ctx, fx.db, fx.workspaceID, user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("locked leave: %v", err)
	}
	if err := RemoveWorkspaceMember(ctx, fx.db, fx.workspaceID, "owner", user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("locked kick: %v", err)
	}
	otherWorkspace, err := CreateWorkspace(ctx, fx.db, "owner", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := JoinWorkspace(ctx, fx.db, otherWorkspace.ID, user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("locked join: %v", err)
	}
	if err := MarkWorkspaceDeleting(ctx, fx.db, fx.workspaceID, "owner"); !errors.Is(err, ErrWorkspaceDomainBound) {
		t.Fatalf("bound delete: %v", err)
	}
	// A pause stops new enrollments without granting existing users more access.
	rule.Domain = "company.example"
	rule.Enabled = false
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	paused, err := CreateRegisteredUser(ctx, fx.db, "paused@company.example", "Paused", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != "active" {
		t.Fatalf("paused registration=%s", paused.Status)
	}
	access, _ = GetDomainAccess(ctx, fx.db, user.ID)
	if !access.Locked {
		t.Fatal("pause unlocked existing member")
	}
	unlock := false
	if err := UpdateDomainUserAccess(ctx, fx.db, rule.Domain, user.ID, &unlock); err != nil {
		t.Fatal(err)
	}
	access, _ = GetDomainAccess(ctx, fx.db, user.ID)
	if access.Locked {
		t.Fatal("individual exception ignored")
	}
	if err := LeaveWorkspace(ctx, fx.db, fx.workspaceID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDomainUserAccess(ctx, fx.db, rule.Domain, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	access, _ = GetDomainAccess(ctx, fx.db, user.ID)
	if !access.Locked {
		t.Fatal("inherit ignored")
	}
	if _, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user.ID); err != nil {
		t.Fatal("relock must restore membership", err)
	}
	rule.LockPersonal = false
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	access, _ = GetDomainAccess(ctx, fx.db, user.ID)
	if access.Locked {
		t.Fatal("rule update ignored")
	}
	if err := DeleteRegistrationDomain(ctx, fx.db, rule.Domain); err != nil {
		t.Fatal(err)
	}
	access, err = GetDomainAccess(ctx, fx.db, user.ID)
	if err != nil || access != nil {
		t.Fatalf("rule deletion did not unlock: %+v %v", access, err)
	}
	if _, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user.ID); err != nil {
		t.Fatal("rule deletion removed membership")
	}
}

func TestRegistrationDomainValidationAndAtomicity(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	for _, domain := range []string{"", "@example.com", "https://example.com", "*.example.com", "a..com", "-a.com", "a-.com", "example.com/path", "example.com.", "localhost"} {
		if _, err := NormalizeRegistrationDomain(domain); !errors.Is(err, ErrInvalidDomain) {
			t.Errorf("accepted domain %q", domain)
		}
	}
	rule := RegistrationDomain{Domain: "company.example", WorkspaceID: fx.workspaceID, Enabled: true, InitialGroupID: "missing-group"}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing initial group accepted: %v", err)
	}
	rule.InitialGroupID = ""
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err == nil {
		t.Fatal("duplicate rule accepted")
	}
	// Force enrollment to fail: the account insertion must roll back as well.
	exec(t, fx.db, `UPDATE workspaces SET deleting=1 WHERE id=?`, fx.workspaceID)
	if _, err := CreateRegisteredUser(ctx, fx.db, "rollback@company.example", "Rollback", "hash", "active"); err == nil {
		t.Fatal("expected enrollment failure")
	}
	if _, err := FindUserByEmail(ctx, fx.db, "rollback@company.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan registration survived: %v", err)
	}
}

func TestRegistrationDomainInitialGroupClearsWhenGroupIsDeleted(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	group, err := CreateUserGroup(ctx, fx.db, UserGroup{Name: "Company Members"})
	if err != nil {
		t.Fatal(err)
	}
	rule := RegistrationDomain{
		Domain: "company.example", WorkspaceID: fx.workspaceID, Enabled: true, InitialGroupID: group.ID,
	}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := CreateRegisteredUser(ctx, fx.db, "member@company.example", "Member", "hash", "active")
	if err != nil || user.GroupID != group.ID {
		t.Fatalf("initial group user=%+v err=%v", user, err)
	}
	if err := DeleteUserGroup(ctx, fx.db, group.ID); err != nil {
		t.Fatal(err)
	}
	rules, err := ListRegistrationDomains(ctx, fx.db)
	if err != nil || len(rules) != 1 || rules[0].InitialGroupID != "" || rules[0].InitialGroupName != "" {
		t.Fatalf("rule after group deletion=%+v err=%v", rules, err)
	}
}

func TestAdminWorkspaceOwnershipReplacement(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	exec(t, fx.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('platform','platform@external.example','h','admin','active')`)
	if _, err := AdminTransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "member", "guest"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin transfer: %v", err)
	}
	if _, err := AdminTransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "platform", "platform"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member transfer: %v", err)
	}
	ws, err := AdminTransferWorkspaceOwnership(ctx, fx.db, fx.workspaceID, "platform", "member")
	if err != nil {
		t.Fatal(err)
	}
	if ws.OwnerID != "member" {
		t.Fatal("wrong owner")
	}
	for user, want := range map[string]string{"owner": "member", "member": "admin", "admin": "admin"} {
		role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user)
		if err != nil || role != want {
			t.Errorf("%s role=%s want=%s err=%v", user, role, want, err)
		}
	}
}
