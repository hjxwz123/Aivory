package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeUserGroupPermissionsDefaultsNewCapabilitiesForLegacyJSON(t *testing.T) {
	legacy, err := NormalizeUserGroupPermissions(json.RawMessage(`{"allow_sharing":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.AllowSharing {
		t.Fatal("explicit allow_sharing=false was not preserved")
	}
	if !legacy.AllowKnowledgeBases || !legacy.AllowKnowledgeBaseSharing || !legacy.AllowConversationDeletion || !legacy.AllowDrawing {
		t.Fatalf("missing legacy fields did not retain permissive defaults: %+v", legacy)
	}

	restricted, err := NormalizeUserGroupPermissions(json.RawMessage(`{"allow_knowledge_bases":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if restricted.AllowKnowledgeBases {
		t.Fatal("explicit allow_knowledge_bases=false was not preserved")
	}
	if restricted.AllowKnowledgeBaseSharing {
		t.Fatal("knowledge-base sharing remained enabled without knowledge-base access")
	}

	contradictory, err := NormalizeUserGroupPermissions(json.RawMessage(`{
		"allow_knowledge_bases":false,
		"allow_knowledge_base_sharing":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if contradictory.AllowKnowledgeBaseSharing {
		t.Fatal("contradictory sharing policy was not normalized closed")
	}
}

func TestFilterResourceIDsMakesRestrictedPoliciesExplicit(t *testing.T) {
	selected := ResourceAccessPolicy{Mode: ResourceAccessSelected, IDs: []string{"builtin:web_fetch", "mcp:docs"}}
	ids, configured := FilterResourceIDs(selected, nil, false)
	if !configured || !reflect.DeepEqual(ids, selected.IDs) {
		t.Fatalf("omitted selected policy = %#v configured=%v", ids, configured)
	}

	ids, configured = FilterResourceIDs(selected, []string{"mcp:other", "mcp:docs"}, true)
	if !configured || !reflect.DeepEqual(ids, []string{"mcp:docs"}) {
		t.Fatalf("selected intersection = %#v configured=%v", ids, configured)
	}

	ids, configured = FilterResourceIDs(ResourceAccessPolicy{Mode: ResourceAccessNone}, []string{"builtin:web_fetch"}, false)
	if !configured || len(ids) != 0 {
		t.Fatalf("none policy = %#v configured=%v", ids, configured)
	}
}

func TestUserLibraryPoliciesRequireAnAllowedCatalogSourceWhenRestricted(t *testing.T) {
	policy := ResourceAccessPolicy{Mode: ResourceAccessSelected, IDs: []string{"skill_allowed", "prompt_allowed"}}
	if UserSkillPolicyAllows(policy, UserSkill{ID: "personal"}) {
		t.Fatal("personal skill bypassed selected catalog policy")
	}
	if !UserSkillPolicyAllows(policy, UserSkill{SourceSkillID: "skill_allowed"}) {
		t.Fatal("allowed catalog skill copy was rejected")
	}
	if UserPromptPolicyAllows(policy, UserPrompt{SourcePromptID: "prompt_blocked"}) {
		t.Fatal("blocked catalog prompt copy was allowed")
	}
}

func TestUserGroupPermissionStateExpiresTemporaryMembershipImmediately(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "expired-permissions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	free := DefaultUserGroupPermissions()
	free.AllowDrawing = false
	freeRaw, err := permissionsJSON(free)
	if err != nil {
		t.Fatal(err)
	}
	pro := DefaultUserGroupPermissions()
	pro.AllowDrawing = true
	proRaw, err := permissionsJSON(pro)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id,name,is_default,permissions) VALUES
		 ('ug_free','Free',1,?), ('ug_pro','Pro',0,?)`, freeRaw, proRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,role,status,group_id,group_expires_at,previous_group_id)
		 VALUES('expired-user','expired@example.test','hash','user','active','ug_pro',?,'ug_free')`,
		time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	state, err := UserGroupPermissionStateForUser(ctx, db, "expired-user")
	if err != nil {
		t.Fatal(err)
	}
	if state.GroupID != "ug_free" || state.GroupExpiresAt != 0 || state.Permissions.AllowDrawing {
		t.Fatalf("expired permission state = %+v, want free group with drawing disabled", state)
	}

	var groupID, previousGroupID string
	var expiresAt int64
	if err := db.QueryRowContext(ctx,
		`SELECT group_id,group_expires_at,previous_group_id FROM users WHERE id='expired-user'`,
	).Scan(&groupID, &expiresAt, &previousGroupID); err != nil {
		t.Fatal(err)
	}
	if groupID != "ug_free" || expiresAt != 0 || previousGroupID != "" {
		t.Fatalf("expired membership persisted as group=%q expiry=%d previous=%q", groupID, expiresAt, previousGroupID)
	}
}
