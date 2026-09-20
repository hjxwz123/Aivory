package store

import (
	"context"
	"errors"
	"testing"
)

func TestWorkspaceProfileSharedWithDomainAndRestrictedToAdmins(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	profile := WorkspaceProfile{IconURL: "https://example.com/team.png", Description: "Team introduction"}
	for _, actor := range []string{"member", "guest", "outsider"} {
		err := UpdateWorkspaceProfile(ctx, fx.db, fx.workspaceID, actor, profile)
		if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s update: %v", actor, err)
		}
	}
	for _, actor := range []string{"owner", "admin"} {
		if err := UpdateWorkspaceProfile(ctx, fx.db, fx.workspaceID, actor, profile); err != nil {
			t.Fatal(err)
		}
	}
	rule := RegistrationDomain{Domain: "example.com", WorkspaceID: fx.workspaceID, Enabled: true}
	if err := SaveRegistrationDomain(ctx, fx.db, rule, true); err != nil {
		t.Fatal(err)
	}
	domains, err := ListRegistrationDomains(ctx, fx.db)
	if err != nil || len(domains) != 1 || domains[0].IconURL == nil || *domains[0].IconURL != profile.IconURL || *domains[0].Description != profile.Description {
		t.Fatalf("domains=%+v err=%v", domains, err)
	}
	icon, description := "/api/icons/new.png", "Domain administrator update"
	rule.IconURL, rule.Description = &icon, &description
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"owner", "admin", "member", "guest"} {
		workspaces, err := ListWorkspacesForUser(ctx, fx.db, actor)
		if err != nil || len(workspaces) != 1 || workspaces[0].IconURL != icon || workspaces[0].Description != description {
			t.Fatalf("%s list=%+v err=%v", actor, workspaces, err)
		}
	}
	// Updating a domain rule through an older client must not erase branding.
	rule.IconURL, rule.Description = nil, nil
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); err != nil {
		t.Fatal(err)
	}
	got, err := GetWorkspace(ctx, fx.db, fx.workspaceID)
	if err != nil || got.IconURL != icon || got.Description != description {
		t.Fatalf("profile=%+v err=%v", got, err)
	}
	// Profile validation rolls back the entire domain mutation.
	invalid := "javascript:alert(1)"
	rule.IconURL, rule.LockPersonal = &invalid, true
	if err := SaveRegistrationDomain(ctx, fx.db, rule, false); !errors.Is(err, ErrInvalidWorkspaceProfile) {
		t.Fatalf("invalid profile error=%v", err)
	}
	domains, err = ListRegistrationDomains(ctx, fx.db)
	if err != nil || domains[0].LockPersonal || *domains[0].IconURL != icon {
		t.Fatalf("domain mutation not atomic: %+v %v", domains, err)
	}
	// Explicit empty values clear the profile.
	if err := UpdateWorkspaceProfile(ctx, fx.db, fx.workspaceID, "admin", WorkspaceProfile{}); err != nil {
		t.Fatal(err)
	}
	got, err = GetWorkspaceForMember(ctx, fx.db, fx.workspaceID, "member")
	if err != nil || got.IconURL != "" || got.Description != "" {
		t.Fatalf("clear=%+v err=%v", got, err)
	}
}

func TestWorkspaceProfileRejectsUnsafeOrOversizedInput(t *testing.T) {
	for _, icon := range []string{"javascript:alert(1)", "data:image/svg+xml,test", "//example.com/a.png", "https://user:pass@example.com/a.png"} {
		if _, err := NormalizeWorkspaceProfile(WorkspaceProfile{IconURL: icon}); !errors.Is(err, ErrInvalidWorkspaceProfile) {
			t.Fatalf("accepted %q: %v", icon, err)
		}
	}
}

func TestWorkspaceProfileMigrationPreservesExistingWorkspaces(t *testing.T) {
	fx := newRBACFixture(t)
	for _, ddl := range []string{`ALTER TABLE workspaces DROP COLUMN icon_url`, `ALTER TABLE workspaces DROP COLUMN description`} {
		if _, err := fx.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(fx.db); err != nil {
		t.Fatal(err)
	}
	workspace, err := GetWorkspace(context.Background(), fx.db, fx.workspaceID)
	if err != nil || workspace.IconURL != "" || workspace.Description != "" {
		t.Fatalf("migrated=%+v err=%v", workspace, err)
	}
	profile := WorkspaceProfile{IconURL: "/api/icons/migrated.png", Description: "Existing workspace"}
	if err := UpdateWorkspaceProfile(context.Background(), fx.db, fx.workspaceID, "owner", profile); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(fx.db); err != nil {
		t.Fatal(err)
	}
	workspace, err = GetWorkspace(context.Background(), fx.db, fx.workspaceID)
	if err != nil || workspace.IconURL != profile.IconURL || workspace.Description != profile.Description {
		t.Fatalf("second migration=%+v err=%v", workspace, err)
	}
}
