package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// §workspace RBAC phase 4 — workspace capability policy store semantics.

func TestWorkspacePolicyStoreSemantics(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// No row = fully permissive default.
	policy, err := GetWorkspacePolicy(ctx, fx.db, fx.workspaceID)
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	if !policy.AllowSandbox || !policy.AllowImageGeneration || !policy.AllowKnowledgeBases ||
		!policy.AllowFileUpload || len(policy.AllowedModelIDs) != 0 || policy.MemberMonthlyCreditLimit != 0 {
		t.Fatalf("default policy not permissive: %+v", policy)
	}

	// Ordinary members cannot change the policy.
	if _, err := UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "member", WorkspacePolicyPatch{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member policy update error=%v, want ErrForbidden", err)
	}
	// Admins can; guests can never use the capabilities regardless.
	models := []string{"m1"}
	noUpload := false
	updated, err := UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "admin", WorkspacePolicyPatch{
		AllowedModelIDs: &models,
		AllowFileUpload: &noUpload,
	})
	if err != nil || updated.AllowFileUpload {
		t.Fatalf("admin policy update: %+v err=%v", updated, err)
	}
	if !updated.ModelAllowedByPolicy("m1") || updated.ModelAllowedByPolicy("m2") {
		t.Fatal("model allowlist not applied")
	}
	if updated.UpdatedBy != "admin" || updated.UpdatedAt == 0 {
		t.Fatalf("policy audit fields: %+v", updated)
	}

	// Legacy fields remain writable for rolling clients, but they must not
	// change the new capability switches. The retired sandbox switch alone no
	// longer denies a tool at runtime.
	sandbox := false
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowSandbox: &sandbox})
	if err != nil {
		t.Fatalf("owner policy update: %v", err)
	}
	if updated.AllowSandbox || !updated.AllowToolCalling || updated.AllowFileUpload || len(updated.AllowedModelIDs) != 1 {
		t.Fatalf("patch clobbered other fields: %+v", updated)
	}

	if updated.ToolDeniedByPolicy("builtin:python_execute") {
		t.Fatal("retired sandbox switch unexpectedly denied python_execute")
	}
	image := false
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowImageGeneration: &image})
	if err != nil {
		t.Fatalf("legacy image policy update: %v", err)
	}
	if !updated.AllowToolCalling || !updated.AllowDrawing {
		t.Fatalf("legacy image switch changed new capabilities: %+v", updated)
	}
	if updated.ToolDeniedByPolicy("builtin:image_generate") {
		t.Fatal("retired image switch unexpectedly denied image tool")
	}

	// The merged tool switch is the sole workspace-wide tool gate.
	toolCalling := false
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowToolCalling: &toolCalling})
	if err != nil {
		t.Fatalf("tool-calling policy update: %v", err)
	}
	for _, denied := range []string{"builtin:python_execute", "builtin:image_generate", "hosted:image_generation", "mcp:server-x", "usermcp:server-y"} {
		if !updated.ToolDeniedByPolicy(denied) {
			t.Fatalf("tool-calling switch did not deny %s", denied)
		}
	}
	if updated.ToolDeniedByPolicy("not-a-catalog-entry") {
		t.Fatal("tool-calling switch denied an unrelated id")
	}
	toolCalling = true
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowToolCalling: &toolCalling})
	if err != nil {
		t.Fatalf("re-enable tool-calling policy: %v", err)
	}
	tools := []string{"builtin:web_search"}
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowedToolIDs: &tools})
	if err != nil {
		t.Fatalf("tool allowlist update: %v", err)
	}
	if !updated.ToolDeniedByPolicy("builtin:python_execute") || !updated.ToolDeniedByPolicy("builtin:save_memory") {
		t.Fatal("tool allowlist did not narrow to web_search")
	}
	if !updated.ToolDeniedByPolicy("mcp:server-x") {
		t.Fatal("mcp servers outside the allowlist must be denied")
	}
	mcp := []string{"mcp:server-x"}
	updated, err = UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{AllowedMCPServerIDs: &mcp})
	if err != nil {
		t.Fatalf("mcp allowlist update: %v", err)
	}
	if updated.ToolDeniedByPolicy("mcp:server-x") {
		t.Fatal("allowlisted mcp server wrongly denied")
	}

	// Personal workspaces have no policy scope (empty id).
	personal, err := GetWorkspacePolicy(ctx, fx.db, "")
	if err != nil || len(personal.AllowedModelIDs) != 0 {
		t.Fatalf("personal policy=%+v err=%v, want permissive default", personal, err)
	}
}

func TestWorkspacePolicyMalformedAllowlistFailsClosed(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)

	// [] remains the intentionally permissive, administrator-configured value.
	// Corrupt/non-array JSON must never be coerced to [] because that would turn
	// a damaged restrictive policy into "allow everything".
	exec(t, fx.db, `INSERT INTO workspace_policies(
		workspace_id, allowed_model_ids, allowed_tool_ids, allowed_mcp_server_ids,
		allow_sandbox, allow_image_generation, allow_knowledge_bases, allow_file_upload,
		member_monthly_credit_limit, updated_by, updated_at
	) VALUES(?, '{broken', '[]', '[]', 1, 1, 1, 1, 0, 'owner', 1)`, fx.workspaceID)
	if _, err := GetWorkspacePolicy(ctx, fx.db, fx.workspaceID); err == nil {
		t.Fatal("malformed policy allowlist must return an error instead of allowing every model")
	}
	if _, err := UpdateWorkspacePolicy(ctx, fx.db, fx.workspaceID, "owner", WorkspacePolicyPatch{}); err == nil {
		t.Fatal("malformed policy allowlist must block partial policy updates until repaired")
	}
}

func TestWorkspaceUsageRollups(t *testing.T) {
	ctx := context.Background()
	fx := newRBACFixture(t)
	now := time.Now().Unix()
	exec(t, fx.db, `INSERT INTO usage_logs(user_id,workspace_id,model_id,purpose,input_tokens,output_tokens,credits,created_at) VALUES
		('member',?,'m1','chat',10,20,1.5,?),
		('member',?,'m1','chat',1,2,0.5,?),
		('guest',?,'m1','chat',5,5,2,?),
		('member','','m1','chat',99,99,99,?)`,
		fx.workspaceID, now, fx.workspaceID, now, fx.workspaceID, now, now)

	used, err := WorkspaceMemberMonthlyUsage(ctx, fx.db, fx.workspaceID, "member")
	if err != nil || used != 2 {
		t.Fatalf("member monthly usage=%v err=%v, want 2", used, err)
	}
	rows, err := SumWorkspaceUsageByMember(ctx, fx.db, fx.workspaceID, 30)
	if err != nil {
		t.Fatalf("workspace usage rollup: %v", err)
	}
	byUser := map[string]WorkspaceUsageRow{}
	for _, row := range rows {
		byUser[row.UserID] = row
	}
	if byUser["member"].Credits != 2 || byUser["member"].Messages != 2 {
		t.Fatalf("member rollup=%+v", byUser["member"])
	}
	if byUser["guest"].Credits != 2 || byUser["guest"].Messages != 1 {
		t.Fatalf("guest rollup=%+v", byUser["guest"])
	}
	if _, ok := byUser["owner"]; ok {
		t.Fatal("rollup included a member without usage rows mapping wrong")
	}
}
