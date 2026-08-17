package api

import (
	"context"
	"path/filepath"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

// §workspace RBAC phase 4 — folding the workspace capability policy into the
// group-derived tool ceiling. The workspace may only narrow, never widen.

func TestApplyWorkspaceToolPolicy(t *testing.T) {
	base := &llm.ToolAccessPolicy{Mode: store.ResourceAccessAll, AllowDrawing: true}

	// Permissive workspace keeps the all-tools mode untouched.
	kept := applyWorkspaceToolPolicy(base, store.DefaultWorkspacePolicy("ws"))
	if kept.Mode != store.ResourceAccessAll || len(kept.DenyIDs) != 0 {
		t.Fatalf("permissive workspace narrowed the policy: %+v", kept)
	}

	// Sandbox / image switches subtract even from all-tools mode.
	policy := store.DefaultWorkspacePolicy("ws")
	policy.AllowSandbox = false
	policy.AllowImageGeneration = false
	narrowed := applyWorkspaceToolPolicy(base, policy)
	for _, denied := range []string{"builtin:python_execute", "builtin:code_interpreter", "builtin:image_generate", "hosted:image_generation"} {
		if narrowed.Allows(denied) {
			t.Fatalf("switch did not deny %s: %+v", denied, narrowed)
		}
	}
	if !narrowed.Allows("builtin:web_search") {
		t.Fatalf("switch wrongly denied web_search: %+v", narrowed)
	}

	// A workspace tool allowlist converts all-mode into the selected subset.
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowedToolIDs = []string{"builtin:web_search", "hosted:image_generation"}
	policy.AllowedMCPServerIDs = []string{"mcp:wiki"}
	selected := applyWorkspaceToolPolicy(base, policy)
	if selected.Mode != store.ResourceAccessSelected || len(selected.IDs) != 3 {
		t.Fatalf("allowlist not applied: %+v", selected)
	}
	if !selected.Allows("mcp:wiki") || selected.Allows("builtin:python_execute") {
		t.Fatalf("mcp allowlist misapplied: %+v", selected)
	}

	// A selected group ceiling intersects with the workspace allowlist.
	selectedBase := &llm.ToolAccessPolicy{
		Mode: store.ResourceAccessSelected,
		IDs:  []string{"builtin:web_search", "builtin:python_execute"},
	}
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowedToolIDs = []string{"builtin:web_search", "builtin:save_memory"}
	intersected := applyWorkspaceToolPolicy(selectedBase, policy)
	if len(intersected.IDs) != 1 || intersected.IDs[0] != "builtin:web_search" {
		t.Fatalf("intersection wrong: %+v", intersected)
	}
	if intersected.Allows("builtin:save_memory") {
		t.Fatalf("group ceiling widened by workspace policy: %+v", intersected)
	}

	// A none group ceiling stays none.
	if out := applyWorkspaceToolPolicy(&llm.ToolAccessPolicy{Mode: store.ResourceAccessNone}, store.DefaultWorkspacePolicy("ws")); out.Mode != store.ResourceAccessNone {
		t.Fatalf("none ceiling widened: %+v", out)
	}
}

func TestGenerationWorkspaceAccessSnapshotRevokesRolePolicyAndVisibilityChanges(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-generation-access.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES
		('owner','owner@example.test','h','user','active'),
		('member','member@example.test','h','user','active')`)
	workspace, err := store.CreateWorkspace(ctx, db, "owner", "Access snapshot")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join member: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		UserID: "owner", WorkspaceID: workspace.ID, Title: "Shared", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	snapshot, err := captureGenerationWorkspaceAccess(ctx, db, conversation, "member")
	if err != nil || !snapshot.stillCurrent(ctx, db) {
		t.Fatalf("initial snapshot valid=%v err=%v", snapshot != nil && snapshot.stillCurrent(ctx, db), err)
	}
	if _, err := store.UpdateWorkspaceMemberRole(ctx, db, workspace.ID, "owner", "member", store.WorkspaceRoleGuest); err != nil {
		t.Fatalf("demote member: %v", err)
	}
	if snapshot.stillCurrent(ctx, db) {
		t.Fatal("guest role retained generation authority")
	}

	if _, err := store.UpdateWorkspaceMemberRole(ctx, db, workspace.ID, "owner", "member", store.WorkspaceRoleMember); err != nil {
		t.Fatalf("restore member: %v", err)
	}
	snapshot, err = captureGenerationWorkspaceAccess(ctx, db, conversation, "member")
	if err != nil {
		t.Fatalf("capture after restore: %v", err)
	}
	sandboxOff := false
	if _, err := store.UpdateWorkspacePolicy(ctx, db, workspace.ID, "owner", store.WorkspacePolicyPatch{AllowSandbox: &sandboxOff}); err != nil {
		t.Fatalf("tighten policy: %v", err)
	}
	if snapshot.stillCurrent(ctx, db) {
		t.Fatal("changed workspace policy retained generation authority")
	}

	snapshot, err = captureGenerationWorkspaceAccess(ctx, db, conversation, "member")
	if err != nil {
		t.Fatalf("capture after policy: %v", err)
	}
	private := false
	if _, err := store.UpdateConversation(ctx, db, conversation.ID, "owner", store.ConversationPatch{IsPublic: &private}); err != nil {
		t.Fatalf("make conversation private: %v", err)
	}
	if snapshot.stillCurrent(ctx, db) {
		t.Fatal("private conversation retained other member generation authority")
	}
}
