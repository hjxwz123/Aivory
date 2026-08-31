package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorkspaceMemberPermissionsDefaultAndOwnerManagement(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	if err := JoinWorkspace(ctx, db, "ws1", "outsider"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	workspace, err := GetWorkspaceForMember(ctx, db, "ws1", "outsider")
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if !workspace.CanCreateProjects || !workspace.CanPrivateConversations || !workspace.CanCreateSkillsPrompts || !workspace.CanCreateKB ||
		!workspace.CanAddKBFiles || !workspace.CanDeleteKBContent || !workspace.CanDeleteConversations {
		t.Fatalf("new member permissions=%+v, want all enabled", workspace)
	}

	denied := WorkspaceMemberPermissions{}
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "member", "outsider", denied); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner update error=%v, want ErrNotFound", err)
	}
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "owner", denied); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner self-update error=%v, want ErrNotFound", err)
	}

	updated, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "outsider", denied)
	if err != nil {
		t.Fatalf("owner update permissions: %v", err)
	}
	if updated.CanCreateProjects || updated.CanPrivateConversations || updated.CanCreateSkillsPrompts || updated.CanCreateKB ||
		updated.CanAddKBFiles || updated.CanDeleteKBContent || updated.CanDeleteConversations {
		t.Fatalf("updated permissions=%+v, want all disabled", updated)
	}
	workspace, err = GetWorkspaceForMember(ctx, db, "ws1", "outsider")
	if err != nil || workspace.CanCreateProjects || workspace.CanPrivateConversations || workspace.CanCreateSkillsPrompts || workspace.CanCreateKB ||
		workspace.CanAddKBFiles || workspace.CanDeleteKBContent || workspace.CanDeleteConversations {
		t.Fatalf("effective workspace permissions=%+v err=%v", workspace, err)
	}
	if _, err := CreateProject(ctx, db, Project{UserID: "outsider", WorkspaceID: "ws1", Name: "Denied project"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateProject error=%v, want ErrNotFound", err)
	}
	if _, err := CreateKB(ctx, db, KnowledgeBase{
		UserID: "outsider", WorkspaceID: "ws1", Name: "Denied KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateKB error=%v, want ErrNotFound", err)
	}
}

func TestWorkspaceMemberPermissionsLegacyJSONKeepsConversationDeletionEnabled(t *testing.T) {
	var legacy WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{"can_create_projects":false}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.CanDeleteConversations {
		t.Fatalf("legacy payload disabled conversation deletion: %+v", legacy)
	}

	var explicit WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{"can_delete_conversations":false}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.CanDeleteConversations {
		t.Fatalf("explicit false was not preserved: %+v", explicit)
	}
}

func TestWorkspaceMemberPermissionsGranularCreationIsIndependentAndConservative(t *testing.T) {
	var permissions WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{
		"can_create_prompts":true,
		"can_create_skills":true,
		"can_create_mcp":false,
		"can_use_prompts":true,
		"can_use_skills":false,
		"can_use_mcp":true
	}`), &permissions); err != nil {
		t.Fatal(err)
	}
	if !permissions.CanCreatePrompts || !permissions.CanCreateSkills || permissions.CanCreateMCP {
		t.Fatalf("granular create fields=%+v", permissions)
	}
	if permissions.CanCreateSkillsPrompts {
		t.Fatalf("legacy aggregate broadened disabled MCP: %+v", permissions)
	}
	if !permissions.CanUsePrompts || permissions.CanUseSkills || !permissions.CanUseMCP {
		t.Fatalf("granular use fields=%+v", permissions)
	}

	// An old payload with only the combined bit still maps to all three new
	// creation capabilities, preserving rolling-upgrade behavior.
	var legacy WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{"can_create_skills_prompts":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.CanCreateSkillsPrompts || !legacy.CanCreatePrompts || !legacy.CanCreateSkills || !legacy.CanCreateMCP {
		t.Fatalf("legacy aggregate did not expand conservatively: %+v", legacy)
	}
}

func TestWorkspaceMemberPermissionsPartialJSONPreservesOmittedCapabilities(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	initial := fullWorkspaceMemberPermissions()
	initial.CanCreateSkills = false
	initial.CanUsePrompts = false
	initial.CanUseMCP = false
	initial.CanDeleteConversations = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", initial); err != nil {
		t.Fatalf("seed member permissions: %v", err)
	}

	// This is the shape an older client sends: it round-trips the retired
	// aggregate while changing an unrelated permission and knows none of the new
	// granular use fields. Missing fields must preserve their stored values.
	var legacyPatch WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{
		"can_create_projects":false,
		"can_create_skills_prompts":false
	}`), &legacyPatch); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", legacyPatch)
	if err != nil {
		t.Fatalf("apply legacy partial patch: %v", err)
	}
	if updated.CanCreateProjects {
		t.Fatalf("explicit project creation update was ignored: %+v", updated)
	}
	if !updated.CanCreatePrompts || updated.CanCreateSkills || !updated.CanCreateMCP {
		t.Fatalf("legacy aggregate round-trip rewrote granular creation: %+v", updated)
	}
	if updated.CanUsePrompts || !updated.CanUseSkills || updated.CanUseMCP {
		t.Fatalf("omitted use permissions were rewritten: %+v", updated)
	}
	if updated.CanDeleteConversations {
		t.Fatalf("omitted conversation deletion was broadened: %+v", updated)
	}
}

func TestWorkspaceMemberPermissionsLegacyAggregateStillSupportsIntentionalToggle(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	initial := fullWorkspaceMemberPermissions()
	initial.CanCreateSkills = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", initial); err != nil {
		t.Fatalf("seed member permissions: %v", err)
	}

	var enable WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{"can_create_skills_prompts":true}`), &enable); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", enable)
	if err != nil {
		t.Fatalf("enable legacy aggregate: %v", err)
	}
	if !updated.CanCreatePrompts || !updated.CanCreateSkills || !updated.CanCreateMCP || !updated.CanCreateSkillsPrompts {
		t.Fatalf("legacy aggregate enable did not reach all creation fields: %+v", updated)
	}

	var disable WorkspaceMemberPermissions
	if err := json.Unmarshal([]byte(`{"can_create_skills_prompts":false}`), &disable); err != nil {
		t.Fatal(err)
	}
	updated, err = UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", disable)
	if err != nil {
		t.Fatalf("disable legacy aggregate: %v", err)
	}
	if updated.CanCreatePrompts || updated.CanCreateSkills || updated.CanCreateMCP || updated.CanCreateSkillsPrompts {
		t.Fatalf("legacy aggregate disable did not reach all creation fields: %+v", updated)
	}
}

func TestWorkspaceMemberPermissionsAggregateIncludesMCP(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	permissions := fullWorkspaceMemberPermissions()
	permissions.CanCreateMCP = false
	updated, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", permissions)
	if err != nil {
		t.Fatalf("update member permissions: %v", err)
	}
	if updated.CanCreateSkillsPrompts {
		t.Fatalf("aggregate broadened disabled MCP: %+v", updated)
	}
	var aggregate, prompts, skills, mcp bool
	if err := db.QueryRow(`SELECT can_create_skills_prompts, can_create_prompts, can_create_skills, can_create_mcp
		FROM workspace_members WHERE workspace_id='ws1' AND user_id='member'`).Scan(&aggregate, &prompts, &skills, &mcp); err != nil {
		t.Fatal(err)
	}
	if aggregate || !prompts || !skills || mcp {
		t.Fatalf("stored granular permissions aggregate=%v prompts=%v skills=%v mcp=%v", aggregate, prompts, skills, mcp)
	}
}

func TestWorkspaceMemberPermissionsGranularFieldsRemainAuthoritativeWhenAggregateIsStale(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	exec(t, db, `UPDATE workspace_members SET can_create_skills_prompts=0 WHERE workspace_id='ws1' AND user_id='member'`)
	workspace, err := GetWorkspaceForMember(ctx, db, "ws1", "member")
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.CanCreateSkillsPrompts || !workspace.CanCreatePrompts || !workspace.CanCreateSkills || !workspace.CanCreateMCP {
		t.Fatalf("stale aggregate narrowed granular permissions on read: %+v", workspace)
	}
	members, err := ListWorkspaceMembers(ctx, db, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.UserID != "member" {
			continue
		}
		if !member.CanCreateSkillsPrompts || !member.CanCreatePrompts || !member.CanCreateSkills || !member.CanCreateMCP {
			t.Fatalf("stale aggregate narrowed granular permissions in member listing: %+v", member)
		}
		return
	}
	t.Fatal("legacy member missing from member listing")
}

func TestWorkspaceMemberPermissionsGranularFieldsDriveLibraryCreationIndependently(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	// Leave the retired aggregate at its default value while disabling only skill
	// creation. The granular field must be authoritative for the corresponding
	// resource, and must not accidentally disable prompts or MCP.
	exec(t, db, `UPDATE workspace_members
		SET can_create_skills=0, can_create_skills_prompts=1
		WHERE workspace_id='ws1' AND user_id='member'`)
	if _, err := CreateUserSkill(ctx, db, UserSkill{
		UserID: "member", WorkspaceID: "ws1", Name: "blocked-skill", Description: "blocked", Instructions: "blocked",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("skill creation with granular deny err=%v, want ErrNotFound", err)
	}
	if _, err := CreateUserPrompt(ctx, db, UserPrompt{
		UserID: "member", WorkspaceID: "ws1", Name: "allowed-prompt", Description: "allowed", Content: "allowed",
	}); err != nil {
		t.Fatalf("prompt creation was narrowed by skill-only deny: %v", err)
	}
}

func TestWorkspaceMemberRolePromotionReturnsFullAdminCapabilities(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", WorkspaceMemberPermissions{}); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateWorkspaceMemberRole(ctx, db, "ws1", "owner", "member", WorkspaceRoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != WorkspaceRoleAdmin || updated.IsOwner {
		t.Fatalf("promoted member identity=%+v", updated)
	}
	if !updated.CanCreateProjects || !updated.CanPrivateConversations ||
		!updated.CanCreateSkillsPrompts || !updated.CanCreatePrompts ||
		!updated.CanCreateSkills || !updated.CanCreateMCP ||
		!updated.CanUsePrompts || !updated.CanUseSkills || !updated.CanUseMCP ||
		!updated.CanCreateKB || !updated.CanAddKBFiles ||
		!updated.CanDeleteKBContent || !updated.CanDeleteConversations {
		t.Fatalf("promoted admin capabilities=%+v, want all enabled", updated)
	}
}

func TestWorkspaceMemberGuestRoundTripPreservesStoredPermissions(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	want := fullWorkspaceMemberPermissions()
	want.CanCreateProjects = false
	want.CanCreateSkills = false
	want.CanCreateMCP = false
	want.CanUsePrompts = false
	want.CanUseMCP = false
	want.CanDeleteConversations = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", want); err != nil {
		t.Fatalf("seed member permissions: %v", err)
	}

	assertPreserved := func(stage string, member *WorkspaceMember) {
		t.Helper()
		if member.CanCreateProjects != want.CanCreateProjects ||
			member.CanCreatePrompts != want.CanCreatePrompts ||
			member.CanCreateSkills != want.CanCreateSkills ||
			member.CanCreateMCP != want.CanCreateMCP ||
			member.CanUsePrompts != want.CanUsePrompts ||
			member.CanUseSkills != want.CanUseSkills ||
			member.CanUseMCP != want.CanUseMCP ||
			member.CanDeleteConversations != want.CanDeleteConversations {
			t.Fatalf("%s permissions=%+v, want stored values preserved", stage, member)
		}
	}
	guest, err := UpdateWorkspaceMemberRole(ctx, db, "ws1", "owner", "member", WorkspaceRoleGuest)
	if err != nil {
		t.Fatalf("demote member to guest: %v", err)
	}
	if guest.Role != WorkspaceRoleGuest {
		t.Fatalf("demoted role=%q, want guest", guest.Role)
	}
	assertPreserved("guest", guest)

	member, err := UpdateWorkspaceMemberRole(ctx, db, "ws1", "owner", "member", WorkspaceRoleMember)
	if err != nil {
		t.Fatalf("restore guest to member: %v", err)
	}
	if member.Role != WorkspaceRoleMember {
		t.Fatalf("restored role=%q, want member", member.Role)
	}
	assertPreserved("restored member", member)
}

func TestWorkspaceConversationDeletionPermissionMigrationDefaultsExistingMembers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "workspace-delete-capability-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('migration-owner','migration-owner@example.test','h','user')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('migration-ws','Migration','migration-owner','migration-token')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('migration-ws','migration-owner','admin')`)

	// Simulate a database upgraded from the version immediately before this
	// capability existed. Migrate must add it with the historical permissive
	// default rather than unexpectedly revoking an existing member.
	exec(t, db, `ALTER TABLE workspace_members DROP COLUMN can_delete_conversations`)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy workspace members: %v", err)
	}
	var canDelete bool
	if err := db.QueryRow(`SELECT can_delete_conversations FROM workspace_members WHERE workspace_id='migration-ws' AND user_id='migration-owner'`).Scan(&canDelete); err != nil {
		t.Fatal(err)
	}
	if !canDelete {
		t.Fatal("existing workspace member lost conversation deletion during migration")
	}
}

func TestWorkspaceMemberGranularCreationMigrationBackfillsLegacyAggregate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "workspace-granular-create-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('granular-owner','granular-owner@example.test','h','user'),
		('granular-denied','granular-denied@example.test','h','user'),
		('granular-allowed','granular-allowed@example.test','h','user')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES
		('granular-ws','Granular migration','granular-owner','granular-token')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role,can_create_skills_prompts) VALUES
		('granular-ws','granular-owner','admin',1),
		('granular-ws','granular-denied','member',0),
		('granular-ws','granular-allowed','member',1)`)

	// Rebuild this table into the shape used before the granular columns were
	// introduced, then run the real migration. The old aggregate value must be
	// copied into all three new columns exactly once.
	for _, column := range []string{"can_create_prompts", "can_create_skills", "can_create_mcp"} {
		exec(t, db, "ALTER TABLE workspace_members DROP COLUMN "+column)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy granular permissions: %v", err)
	}
	rows, err := db.Query(`SELECT user_id, can_create_skills_prompts, can_create_prompts, can_create_skills, can_create_mcp
		FROM workspace_members WHERE workspace_id='granular-ws' ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		var aggregate, prompts, skills, mcp bool
		if err := rows.Scan(&userID, &aggregate, &prompts, &skills, &mcp); err != nil {
			t.Fatal(err)
		}
		want := userID != "granular-denied"
		if aggregate != want || prompts != want || skills != want || mcp != want {
			t.Fatalf("migrated %s aggregate=%v prompts=%v skills=%v mcp=%v, want all=%v", userID, aggregate, prompts, skills, mcp, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePrivateConversationPermission(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	private, err := CreateConversation(ctx, db, Conversation{
		ID: "member-private", UserID: "member", WorkspaceID: "ws1", Title: "Private by default",
	})
	if err != nil || private.IsPublic {
		t.Fatalf("private conversation=%+v err=%v", private, err)
	}

	permissions := fullWorkspaceMemberPermissions()
	permissions.CanPrivateConversations = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", permissions); err != nil {
		t.Fatalf("revoke private permission: %v", err)
	}
	forcedPublic, err := CreateConversation(ctx, db, Conversation{
		ID: "member-forced-public", UserID: "member", WorkspaceID: "ws1", Title: "Forced public",
	})
	if err != nil || !forcedPublic.IsPublic {
		t.Fatalf("forced-public conversation=%+v err=%v", forcedPublic, err)
	}
	makePrivate := false
	if _, err := UpdateConversation(ctx, db, forcedPublic.ID, "member", ConversationPatch{IsPublic: &makePrivate}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("make-private update error=%v, want ErrNotFound", err)
	}
	retained, err := GetConversation(ctx, db, forcedPublic.ID, "member")
	if err != nil || !retained.IsPublic {
		t.Fatalf("conversation visibility changed despite revocation: %+v err=%v", retained, err)
	}
}

func TestWorkspaceKnowledgeBasePermissionsUseBothLayers(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	items, err := ListWorkspaceKnowledgeBaseMemberPermissions(ctx, db, "workspace-kb", "creator")
	if err != nil {
		t.Fatalf("creator list permissions: %v", err)
	}
	byUser := map[string]WorkspaceKnowledgeBaseMemberPermission{}
	for _, item := range items {
		byUser[item.UserID] = item
	}
	if !byUser["owner"].Locked || !byUser["creator"].Locked || byUser["member"].Locked {
		t.Fatalf("locked principals=%+v", byUser)
	}
	if _, err := ListWorkspaceKnowledgeBaseMemberPermissions(ctx, db, "workspace-kb", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ordinary member list error=%v, want ErrNotFound", err)
	}

	if _, err := UpdateWorkspaceKnowledgeBaseMemberPermission(
		ctx, db, "workspace-kb", "creator", "member", false, false,
	); err != nil {
		t.Fatalf("disable library permissions: %v", err)
	}
	kb, err := GetKB(ctx, db, "workspace-kb", "member")
	if err != nil || kb.CanUpload || kb.CanDeleteContent {
		t.Fatalf("library-level revocation not effective: %+v err=%v", kb, err)
	}
	if _, err := CreateDocumentForUser(ctx, db, Document{
		ID: "denied-library-upload", KBID: "workspace-kb", Filename: "denied.txt", MimeType: "text/plain",
	}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upload error=%v, want ErrNotFound", err)
	}
	if err := DeleteDocumentForUser(ctx, db, "workspace-document", "kb", "workspace-kb", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete error=%v, want ErrNotFound", err)
	}

	if _, err := UpdateWorkspaceKnowledgeBaseMemberPermission(
		ctx, db, "workspace-kb", "owner", "member", true, true,
	); err != nil {
		t.Fatalf("enable library permissions: %v", err)
	}
	total := fullWorkspaceMemberPermissions()
	total.CanAddKBFiles = false
	total.CanDeleteKBContent = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "member", total); err != nil {
		t.Fatalf("disable total permissions: %v", err)
	}
	kb, err = GetKB(ctx, db, "workspace-kb", "member")
	if err != nil || kb.CanUpload || kb.CanDeleteContent {
		t.Fatalf("total permission did not cap library permission: %+v err=%v", kb, err)
	}
}

func TestWorkspaceKnowledgeBaseCreatorIsNotCappedByMemberTotals(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	permissions := fullWorkspaceMemberPermissions()
	permissions.CanAddKBFiles = false
	permissions.CanDeleteKBContent = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "creator", permissions); err != nil {
		t.Fatalf("disable creator totals: %v", err)
	}
	items, err := ListWorkspaceKnowledgeBaseMemberPermissions(ctx, db, "workspace-kb", "creator")
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	for _, item := range items {
		if item.UserID != "creator" {
			continue
		}
		if !item.Locked || !item.CanAddFiles || !item.CanDeleteContent ||
			!item.TotalCanAddKBFiles || !item.TotalCanDeleteKBContent {
			t.Fatalf("creator permission row=%+v, want uncapped locked principal", item)
		}
		return
	}
	t.Fatal("creator missing from workspace knowledge-base permissions")
}

func TestWorkspaceKnowledgeBasePermissionOverridesAreClearedOnLeave(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	if _, err := UpdateWorkspaceKnowledgeBaseMemberPermission(
		ctx, db, "workspace-kb", "owner", "member", false, false,
	); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if err := LeaveWorkspace(ctx, db, "ws1", "member"); err != nil {
		t.Fatalf("leave workspace: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_kb_member_permissions WHERE kb_id='workspace-kb' AND user_id='member'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("permission override survived leave: count=%d", count)
	}
	if err := JoinWorkspace(ctx, db, "ws1", "member"); err != nil {
		t.Fatalf("rejoin workspace: %v", err)
	}
	kb, err := GetKB(ctx, db, "workspace-kb", "member")
	if err != nil || !kb.CanUpload || kb.CanDeleteContent {
		// §workspace RBAC: CanDeleteContent now means "manage any member's
		// content" (admins and the library creator only). Ordinary members
		// keep deleting their OWN uploads via the document-level rule.
		t.Fatalf("rejoined member defaults: %+v err=%v; want upload=yes manage-any=no", kb, err)
	}
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id) VALUES
		('rejoined-own-upload','workspace-kb','rejoined.txt','text/plain',1,'ready','','member')`)
	if err := DeleteDocumentForUser(ctx, db, "rejoined-own-upload", "kb", "workspace-kb", "member"); err != nil {
		t.Fatalf("rejoined member deleting own upload: %v", err)
	}
	if err := DeleteDocumentForUser(ctx, db, "workspace-document", "kb", "workspace-kb", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejoined member deleting creator document error=%v, want ErrNotFound", err)
	}
}
