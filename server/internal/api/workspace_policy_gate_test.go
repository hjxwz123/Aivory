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

	// The merged tool-calling switch subtracts every tool from all-tools mode.
	policy := store.DefaultWorkspacePolicy("ws")
	policy.AllowToolCalling = false
	narrowed := applyWorkspaceToolPolicy(base, policy)
	for _, denied := range []string{"builtin:python_execute", "builtin:code_interpreter", "builtin:image_generate", "hosted:image_generation", "mcp:wiki", "usermcp:mine"} {
		if narrowed.Allows(denied) {
			t.Fatalf("switch did not deny %s: %+v", denied, narrowed)
		}
	}
	if narrowed.Mode != store.ResourceAccessNone {
		t.Fatalf("tool-calling shutdown did not force none mode: %+v", narrowed)
	}
	// Direct drawing is independent from tool calling. It is folded into the
	// image-model gate by the turn handler, not used to deny image tools here.
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowDrawing = false
	drawingOff := applyWorkspaceToolPolicy(base, policy)
	if drawingOff.AllowDrawing {
		t.Fatalf("drawing switch was not folded: %+v", drawingOff)
	}
	if !drawingOff.Allows("builtin:image_generate") {
		t.Fatalf("drawing switch incorrectly denied a tool invocation: %+v", drawingOff)
	}

	// Official workspace allowlists stay out of the generic group Mode/IDs. The
	// orchestrator and registry apply them through WorkspacePolicy so user-owned
	// MCP ids can remain in their independent namespace.
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowedToolIDs = []string{"builtin:web_search", "hosted:image_generation"}
	policy.AllowedMCPServerIDs = []string{"mcp:wiki"}
	selected := applyWorkspaceToolPolicy(base, policy)
	if selected.Mode != store.ResourceAccessAll || len(selected.IDs) != 0 {
		t.Fatalf("official allowlist leaked into group selection policy: %+v", selected)
	}
	if !selected.Allows("usermcp:teammate") {
		t.Fatalf("group-all policy removed teammate user MCP: %+v", selected)
	}
	if policy.ToolDeniedByPolicy("builtin:web_search") || !policy.ToolDeniedByPolicy("builtin:not-listed") {
		t.Fatalf("dedicated workspace allowlist filter is inconsistent: %+v", policy)
	}

	// A selected group ceiling remains the group ceiling; the dedicated workspace
	// filter subsequently narrows official declarations without widening it.
	selectedBase := &llm.ToolAccessPolicy{
		Mode: store.ResourceAccessSelected,
		IDs:  []string{"builtin:web_search", "builtin:python_execute"},
	}
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowedToolIDs = []string{"builtin:web_search", "builtin:save_memory"}
	intersected := applyWorkspaceToolPolicy(selectedBase, policy)
	if len(intersected.IDs) != 2 || intersected.IDs[0] != "builtin:web_search" || intersected.IDs[1] != "builtin:python_execute" {
		t.Fatalf("workspace allowlist rewrote group ceiling: %+v", intersected)
	}
	if intersected.Allows("builtin:save_memory") {
		t.Fatalf("group ceiling widened by workspace policy: %+v", intersected)
	}

	// Official-server allowlists must not remove a user-owned MCP id already
	// present in a selected group ceiling either.
	selectedBase = &llm.ToolAccessPolicy{
		Mode: store.ResourceAccessSelected,
		IDs:  []string{"usermcp:mine", "builtin:web_search", "builtin:python_execute"},
	}
	policy = store.DefaultWorkspacePolicy("ws")
	policy.AllowedToolIDs = []string{"builtin:web_search"}
	policy.AllowedMCPServerIDs = []string{"mcp:official", "usermcp:should-be-ignored"}
	intersected = applyWorkspaceToolPolicy(selectedBase, policy)
	if len(intersected.IDs) != 3 || intersected.IDs[0] != "usermcp:mine" ||
		intersected.IDs[1] != "builtin:web_search" || intersected.IDs[2] != "builtin:python_execute" {
		t.Fatalf("user MCP was narrowed by official workspace allowlist: %+v", intersected)
	}
	if store.DefaultWorkspacePolicy("ws").ToolDeniedByPolicy("usermcp:mine") {
		t.Fatal("permissive workspace denied user MCP")
	}
	policy.AllowedToolIDs = []string{"builtin:web_search"}
	policy.AllowedMCPServerIDs = []string{"mcp:official"}
	if policy.ToolDeniedByPolicy("usermcp:mine") {
		t.Fatal("official workspace allowlist denied user MCP")
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
	toolCallingOff := false
	if _, err := store.UpdateWorkspacePolicy(ctx, db, workspace.ID, "owner", store.WorkspacePolicyPatch{AllowToolCalling: &toolCallingOff}); err != nil {
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
