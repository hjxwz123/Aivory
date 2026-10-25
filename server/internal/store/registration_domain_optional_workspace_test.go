package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestRegistrationDomainWithoutWorkspace(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	initial, err := CreateUserGroup(ctx, fx.db, UserGroup{Name: "Domain signup group"})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := CreateUser(ctx, fx.db, "existing@company.example", "Existing", "hash")
	if err != nil {
		t.Fatal(err)
	}
	rule := RegistrationDomain{Domain: "company.example", InitialGroupID: initial.ID, EmailVerificationRequired: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	if access, err := GetDomainAccess(ctx, fx.db, existing.ID); err != nil || access != nil {
		t.Fatalf("historical account auto-enrolled: %+v, %v", access, err)
	}
	user, err := CreateRegisteredUser(ctx, fx.db, "new@company.example", "New", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	if user.GroupID != initial.ID {
		t.Fatalf("new user group = %q, want %q", user.GroupID, initial.ID)
	}
	if required, err := registrationDomainEmailVerificationRequired(ctx, fx.db, user.Email); err != nil || !required {
		t.Fatalf("verification policy = %v, %v", required, err)
	}
	if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{existing.ID}); err != nil {
		t.Fatal(err)
	}
	if refreshed, err := FindUserByID(ctx, fx.db, existing.ID); err != nil || refreshed.GroupID != existing.GroupID {
		t.Fatalf("manual enrollment changed existing group: %+v, %v", refreshed, err)
	}
	for _, userID := range []string{user.ID, existing.ID} {
		access, err := GetDomainAccess(ctx, fx.db, userID)
		if err != nil || access == nil || access.WorkspaceID != "" || access.Locked {
			t.Fatalf("unassigned account access = %+v, %v", access, err)
		}
		var memberships int
		if err := fx.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id=?`, userID).Scan(&memberships); err != nil || memberships != 0 {
			t.Fatalf("unassigned account joined %d workspaces: %v", memberships, err)
		}
		if _, err := CreateConversation(ctx, fx.db, Conversation{ID: "personal-" + userID, UserID: userID, Title: "Personal"}); err != nil {
			t.Fatal(err)
		}
		if status, err := GetDomainPersonalDataStatus(ctx, fx.db, userID); err != nil || status.NeedsAction || status.CanMigrate {
			t.Fatalf("unassigned account prompted to migrate personal data: %+v, %v", status, err)
		}
	}
	locked := true
	if err := UpdateDomainUserAccess(ctx, fx.db, rule.Domain, user.ID, &locked); !errors.Is(err, ErrDomainWorkspaceRequired) {
		t.Fatalf("individual lock without workspace = %v", err)
	}
	rule.LockPersonal = true
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); !errors.Is(err, ErrDomainWorkspaceRequired) {
		t.Fatalf("rule lock without workspace = %v", err)
	}
	rule.LockPersonal = false
	rule.Enabled = false
	rule.Domains = []string{"new.example"}
	rule.InitialGroupID = ""
	rule.SubscriptionPurchaseDisabled = true
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	if access, err := GetDomainAccess(ctx, fx.db, user.ID); err != nil || access == nil || access.Domain != rule.Domain || !access.SubscriptionPurchaseDisabled || access.Locked {
		t.Fatalf("editing/stopping rule changed existing binding: %+v, %v", access, err)
	}
	if refreshed, err := FindUserByID(ctx, fx.db, user.ID); err != nil || refreshed.GroupID != initial.ID {
		t.Fatalf("editing initial group changed an existing member: %+v, %v", refreshed, err)
	}
	rules, err := ListRegistrationDomains(ctx, fx.db)
	if err != nil || len(rules) != 1 || rules[0].WorkspaceID != "" || rules[0].WorkspaceName != "" || rules[0].MemberCount != 2 {
		t.Fatalf("list unassigned rule = %+v, %v", rules, err)
	}
	rule.WorkspaceID = fx.workspaceID
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); !errors.Is(err, ErrDomainWorkspaceHasMembers) {
		t.Fatalf("existing target workspace must remain immutable: %v", err)
	}
	if removed, err := RemoveDomainUser(ctx, fx.db, rule.Domain, user.ID); err != nil || removed.WorkspaceMembershipRemoved || removed.WorkspaceID != "" {
		t.Fatalf("remove unassigned member = %+v, %v", removed, err)
	}
	if conversations, err := ListConversations(ctx, fx.db, user.ID, "", "", 10, 0); err != nil || len(conversations) != 1 {
		t.Fatalf("removing domain member changed personal data: %+v, %v", conversations, err)
	}
}

func TestRegistrationDomainWorkspaceChangesWithoutMembers(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	other, err := CreateWorkspace(ctx, fx.db, "owner", "Other")
	if err != nil {
		t.Fatal(err)
	}
	rule := RegistrationDomain{Domain: "company.example", Enabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	for _, workspaceID := range []string{fx.workspaceID, other.ID, "", fx.workspaceID} {
		rule.WorkspaceID = workspaceID
		rule.LockPersonal = workspaceID != ""
		if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
			t.Fatalf("change empty rule to workspace %q: %v", workspaceID, err)
		}
		rules, err := ListRegistrationDomains(ctx, fx.db)
		if err != nil || len(rules) != 1 || rules[0].WorkspaceID != workspaceID || rules[0].LockPersonal != rule.LockPersonal || rules[0].MemberCount != 0 {
			t.Fatalf("updated workspace %q: rules=%+v err=%v", workspaceID, rules, err)
		}
	}
	user, err := CreateRegisteredUser(ctx, fx.db, "new@company.example", "New", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	if access, err := GetDomainAccess(ctx, fx.db, user.ID); err != nil || access == nil || access.WorkspaceID != fx.workspaceID || !access.Locked {
		t.Fatalf("signup did not use the updated target: %+v, %v", access, err)
	}
	if role, err := IsWorkspaceMember(ctx, fx.db, fx.workspaceID, user.ID); err != nil || role != WorkspaceRoleMember {
		t.Fatalf("updated target membership = %q, %v", role, err)
	}
	if role, err := IsWorkspaceMember(ctx, fx.db, other.ID, user.ID); err != nil || role != "" {
		t.Fatalf("signup joined a previous target: %q, %v", role, err)
	}
}

func TestRegistrationDomainWorkspaceCannotChangeWithMembers(t *testing.T) {
	for _, assigned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unassigned", true: "assigned"}[assigned], func(t *testing.T) {
			fx := newRBACFixture(t)
			ctx := t.Context()
			rule := RegistrationDomain{Domain: "company.example", Enabled: true}
			if assigned {
				rule.WorkspaceID = fx.workspaceID
			}
			if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
				t.Fatal(err)
			}
			if _, err := EnrollExistingDomainUsers(ctx, fx.db, rule.Domain, []string{"member"}); err != nil {
				t.Fatal(err)
			}
			changed := rule
			if assigned {
				changed.WorkspaceID = ""
			} else {
				changed.WorkspaceID = fx.workspaceID
			}
			changed.Domains = []string{"changed.example"}
			changed.SubscriptionPurchaseDisabled = true
			changed.Enabled = false
			if err := SaveRegistrationDomain(ctx, fx.db, changed, false); !errors.Is(err, ErrDomainWorkspaceHasMembers) {
				t.Fatalf("changing a populated rule = %v", err)
			}
			rules, err := ListRegistrationDomains(ctx, fx.db)
			if err != nil || len(rules) != 1 || rules[0].WorkspaceID != rule.WorkspaceID || rules[0].Domains[0] != rule.Domain || rules[0].SubscriptionPurchaseDisabled || !rules[0].Enabled {
				t.Fatalf("rejected change partially saved: %+v, %v", rules, err)
			}
			rule.Enabled = false
			if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
				t.Fatalf("policy edit with unchanged workspace: %v", err)
			}
			if _, err := RemoveDomainUser(ctx, fx.db, rule.Domain, "member"); err != nil {
				t.Fatal(err)
			}
			if err := SaveRegistrationDomain(ctx, fx.db, changed, false); err != nil {
				t.Fatalf("changing a rule after its last member is removed: %v", err)
			}
		})
	}
}

func TestRegistrationDomainInitialGroupChangesOnlyAffectFutureSignups(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	rule := RegistrationDomain{Domain: "company.example", Enabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	before, err := CreateRegisteredUser(ctx, fx.db, "before@company.example", "Before", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	group, err := CreateUserGroup(ctx, fx.db, UserGroup{Name: "Later signup group"})
	if err != nil {
		t.Fatal(err)
	}
	rule.InitialGroupID = group.ID
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	refreshed, err := FindUserByID(ctx, fx.db, before.ID)
	if err != nil || refreshed.GroupID != before.GroupID {
		t.Fatalf("adding an initial group changed an existing user: %+v, %v", refreshed, err)
	}
	after, err := CreateRegisteredUser(ctx, fx.db, "after@company.example", "After", "hash", "active")
	if err != nil || after.GroupID != group.ID {
		t.Fatalf("future signup did not use the new initial group: %+v, %v", after, err)
	}
}

func TestRegistrationDomainOptionalWorkspaceMigrationPreservesBindings(t *testing.T) {
	fx := newRBACFixture(t)
	ctx := t.Context()
	rule := RegistrationDomain{Domain: "company.example", Domains: []string{"company.example", "affiliate.example"}, WorkspaceID: fx.workspaceID, LockPersonal: true, EmailVerificationRequired: true, SubscriptionPurchaseDisabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := CreateRegisteredUser(ctx, fx.db, "member@affiliate.example", "Member", "hash", "active")
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-upgrade parent while keeping its dependent rows intact.
	exec(t, fx.db, `PRAGMA foreign_keys=OFF`)
	exec(t, fx.db, `CREATE TABLE registration_domains_legacy (
		domain TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
		lock_personal INTEGER NOT NULL DEFAULT 0, email_verification_required INTEGER NOT NULL DEFAULT 1,
		initial_group_id TEXT REFERENCES user_groups(id) ON DELETE SET NULL,
		enabled INTEGER NOT NULL DEFAULT 1, subscription_purchase_disabled INTEGER NOT NULL DEFAULT 0)`)
	exec(t, fx.db, `INSERT INTO registration_domains_legacy SELECT domain,workspace_id,lock_personal,email_verification_required,initial_group_id,enabled,subscription_purchase_disabled FROM registration_domains`)
	exec(t, fx.db, `DROP TABLE registration_domains`)
	exec(t, fx.db, `ALTER TABLE registration_domains_legacy RENAME TO registration_domains`)
	exec(t, fx.db, `PRAGMA foreign_keys=ON`)
	for i := 0; i < 2; i++ {
		if err := Migrate(fx.db); err != nil {
			t.Fatal(err)
		}
	}
	rules, err := ListRegistrationDomains(ctx, fx.db)
	if err != nil || len(rules) != 1 || len(rules[0].Domains) != 2 || rules[0].MemberCount != 1 || !rules[0].SubscriptionPurchaseDisabled || !rules[0].EmailVerificationRequired {
		t.Fatalf("upgrade lost rule policies/matches/users: %+v, %v", rules, err)
	}
	access, err := GetDomainAccess(ctx, fx.db, user.ID)
	if err != nil || access == nil || !access.Locked || access.WorkspaceID != fx.workspaceID {
		t.Fatalf("upgrade changed member access: %+v, %v", access, err)
	}
	if err := SaveRegistrationDomain(ctx, fx.db, RegistrationDomain{Domain: "unassigned.example"}, true); err != nil {
		t.Fatal(err)
	}
	var workspace sql.NullString
	if err := fx.db.QueryRow(`SELECT workspace_id FROM registration_domains WHERE domain='unassigned.example'`).Scan(&workspace); err != nil || workspace.Valid {
		t.Fatalf("missing workspace must be SQL NULL: %+v, %v", workspace, err)
	}
	var enabled int
	if err := fx.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys were not restored: %d, %v", enabled, err)
	}
	if _, err := fx.db.Exec(`INSERT INTO registration_domains(domain,workspace_id) VALUES('bad.example','missing')`); err == nil {
		t.Fatal("workspace foreign key was lost during upgrade")
	}
}
