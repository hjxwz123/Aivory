package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestWorkspacePolicyFiltersDirectToolDeclarations(t *testing.T) {
	policy := store.DefaultWorkspacePolicy("ws-runtime")
	policy.AllowedToolIDs = []string{"builtin:aivory_web_search", "hosted:web_search"}
	policy.AllowedMCPServerIDs = []string{"mcp:official"}

	builtin := filterBuiltinToolsByWorkspacePolicy([]ToolDef{
		{Name: "aivory_web_search"},
		{Name: "python_execute"},
	}, &policy)
	if len(builtin) != 1 || builtin[0].Name != "aivory_web_search" {
		t.Fatalf("builtin declarations = %+v, want only the allowlisted tool", builtin)
	}

	hostedNames, hostedRequests := filterHostedToolsByWorkspacePolicy(
		[]string{"web_search", "code_interpreter"},
		[]json.RawMessage{json.RawMessage(`{"tools":[{"type":"web_search"}]}`), json.RawMessage(`{"tools":[{"type":"code_interpreter"}]}`)},
		&policy,
	)
	if len(hostedNames) != 1 || hostedNames[0] != "web_search" || len(hostedRequests) != 1 {
		t.Fatalf("hosted declarations = names:%v requests:%v", hostedNames, hostedRequests)
	}

	mcp := []MCPToolDef{
		{ToolDef: ToolDef{Name: "official_tool"}, ServerID: "official"},
		{ToolDef: ToolDef{Name: "other_tool"}, ServerID: "other"},
		{ToolDef: ToolDef{Name: "private_tool"}, ServerID: "private", UserOwned: true},
	}
	filtered := filterMCPToolsByWorkspacePolicy(mcp, &policy)
	if len(filtered) != 2 || filtered[0].ServerID != "official" || filtered[1].ServerID != "private" {
		t.Fatalf("MCP declarations = %+v, want official + user-owned", filtered)
	}

	policy.AllowMCP = false
	filtered = filterMCPToolsByWorkspacePolicy(mcp, &policy)
	if len(filtered) != 0 {
		t.Fatalf("MCP shutdown left declarations: %+v", filtered)
	}
	policy.AllowMCP = true
	policy.AllowToolCalling = false
	if got := filterBuiltinToolsByWorkspacePolicy([]ToolDef{{Name: "aivory_web_search"}}, &policy); len(got) != 0 {
		t.Fatalf("tool-calling shutdown left builtin declarations: %+v", got)
	}
}

func TestBuildFallbackRequestRejectsUnavailableWorkspaceModel(t *testing.T) {
	orchestrator, _, primary, _, _, db := setupToolRouteTest(t)
	workspace, err := store.CreateWorkspace(context.Background(), db, "u1", "Fallback policy")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: primary.ChannelID, Kind: "chat", RequestID: "workspace-denied-fallback",
		Label: "Workspace denied fallback", Enabled: true, Stream: true,
	})
	if err != nil {
		t.Fatalf("create fallback model: %v", err)
	}
	allowed := []string{primary.ID}
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, workspace.ID, "u1", store.WorkspacePolicyPatch{
		AllowedModelIDs: &allowed,
	}); err != nil {
		t.Fatalf("set workspace model allowlist: %v", err)
	}
	_, _, _, err = orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{
		UserID: "u1", WorkspaceID: workspace.ID, ToolsEnabled: true,
	}, fallback.ID)
	if err == nil || !strings.Contains(err.Error(), "not allowed in this workspace") {
		t.Fatalf("fallback error = %v, want workspace model denial", err)
	}
}

func TestBuildFallbackRequestRejectsDisabledModelAndChannel(t *testing.T) {
	orchestrator, _, primary, _, _, db := setupToolRouteTest(t)
	ctx := context.Background()
	disabledModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: primary.ChannelID, Kind: "chat", RequestID: "disabled-fallback",
		Label: "Disabled fallback", Enabled: false, Stream: true,
	})
	if err != nil {
		t.Fatalf("create disabled fallback: %v", err)
	}
	if _, _, _, err := orchestrator.buildFallbackRequest(ctx, UnifiedChatRequest{}, disabledModel.ID); err == nil || !strings.Contains(err.Error(), "fallback model is disabled") {
		t.Fatalf("disabled model error = %v", err)
	}

	disabledChannel, err := store.CreateChannel(ctx, db, "Disabled fallback channel", "openai", "chat", "https://fallback.invalid", "key")
	if err != nil {
		t.Fatalf("create disabled channel: %v", err)
	}
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id=?`, disabledChannel.ID); err != nil {
		t.Fatalf("disable fallback channel: %v", err)
	}
	channelModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: disabledChannel.ID, Kind: "chat", RequestID: "disabled-channel-fallback",
		Label: "Disabled channel fallback", Enabled: true, Stream: true,
	})
	if err != nil {
		t.Fatalf("create channel fallback: %v", err)
	}
	if _, _, _, err := orchestrator.buildFallbackRequest(ctx, UnifiedChatRequest{}, channelModel.ID); err == nil || !strings.Contains(err.Error(), "fallback channel is disabled") {
		t.Fatalf("disabled channel error = %v", err)
	}
}

func TestRunRejectsWorkspaceModelBeforeProviderCall(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	ctx := context.Background()
	workspace, err := store.CreateWorkspace(ctx, db, "u1", "Run policy")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := db.Exec(`UPDATE conversations SET workspace_id=? WHERE id=?`, workspace.ID, conversation.ID); err != nil {
		t.Fatalf("scope conversation: %v", err)
	}
	allowed := []string{"some-other-model"}
	if _, err := store.UpdateWorkspacePolicy(ctx, db, workspace.ID, "u1", store.WorkspacePolicyPatch{
		AllowedModelIDs: &allowed,
	}); err != nil {
		t.Fatalf("set model allowlist: %v", err)
	}
	_, err = orchestrator.Run(ctx, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "This must be rejected",
	}, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "not allowed in this workspace") {
		t.Fatalf("Run error = %v, want workspace model denial", err)
	}
	if len(provider.mainRequests) != 0 || len(provider.taskRequests) != 0 {
		t.Fatalf("provider was called despite model denial: main=%d task=%d", len(provider.mainRequests), len(provider.taskRequests))
	}
}
