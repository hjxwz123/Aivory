package store

import (
	"context"
	"errors"
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
	if !workspace.CanCreateProjects || !workspace.CanPrivateConversations || !workspace.CanCreateKB ||
		!workspace.CanAddKBFiles || !workspace.CanDeleteKBContent {
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
	if updated.CanCreateProjects || updated.CanPrivateConversations || updated.CanCreateKB ||
		updated.CanAddKBFiles || updated.CanDeleteKBContent {
		t.Fatalf("updated permissions=%+v, want all disabled", updated)
	}
	workspace, err = GetWorkspaceForMember(ctx, db, "ws1", "outsider")
	if err != nil || workspace.CanCreateProjects || workspace.CanPrivateConversations || workspace.CanCreateKB ||
		workspace.CanAddKBFiles || workspace.CanDeleteKBContent {
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
	if err != nil || !kb.CanUpload || !kb.CanDeleteContent {
		t.Fatalf("rejoined member did not receive defaults: %+v err=%v", kb, err)
	}
}
