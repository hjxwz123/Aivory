package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type selectedToolsRegistry struct {
	calls        []string
	mcpListCalls []selectedToolsMCPListCall
}

type selectedToolsMCPListCall struct {
	modelID     string
	userID      string
	workspaceID string
}

func (r *selectedToolsRegistry) List(string) []ToolDef {
	largeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", 2400) + `"}`)
	return []ToolDef{
		{Name: "aivory_web_search", Description: "Search the web", InputSchema: largeSchema},
		{Name: "python_execute", Description: "Run code", InputSchema: largeSchema},
	}
}

func (r *selectedToolsRegistry) ListMCP(modelID string, userID string, workspaceID string) []MCPToolDef {
	r.mcpListCalls = append(r.mcpListCalls, selectedToolsMCPListCall{
		modelID: modelID, userID: userID, workspaceID: workspaceID,
	})
	largeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("y", 2400) + `"}`)
	return []MCPToolDef{
		{
			ToolDef: ToolDef{
				Name: "mcp_train_lookup_abc123", Description: "Look up train schedules", InputSchema: largeSchema,
			},
			ServerID: "rail", DisplayName: "Train schedules", DisplayDescription: "Look up railway trips", Icon: "Train",
		},
		{
			ToolDef: ToolDef{
				Name: "mcp_paper_search_def456", Description: "Search papers", InputSchema: largeSchema,
			},
			ServerID: "papers", DisplayName: "Paper search", DisplayDescription: "Search literature", Icon: "BookOpen",
		},
		{
			ToolDef: ToolDef{
				Name: "mcp_private_notes_123abc", Description: "Search personal notes", InputSchema: largeSchema,
			},
			ServerID: "personal", DisplayName: "Personal notes", DisplayDescription: "Search private notes", Icon: "Notebook",
			UserOwned: true, OwnerExempt: true,
		},
		{
			ToolDef: ToolDef{
				Name: "mcp_team_docs_456def", Description: "Search team docs", InputSchema: largeSchema,
			},
			ServerID: "team", DisplayName: "Team docs", DisplayDescription: "Search shared docs", Icon: "Users",
			UserOwned: true, OwnerExempt: false,
		},
	}
}

func (r *selectedToolsRegistry) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	r.calls = append(r.calls, name)
	return "ok", nil, nil
}

func TestSelectedToolSubsetFiltersBuiltinHostedAndMCPDeclarations(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	registry := &selectedToolsRegistry{}
	orchestrator.tools = registry
	if _, err := db.Exec(`UPDATE models SET official_tools=? WHERE id=?`,
		`[
			{"name":"web_search","icon":"Search","request":{"tools":[{"type":"web_search"}]}},
			{"name":"code_interpreter","icon":"Terminal","request":{"tools":[{"type":"code_interpreter"}]}}
		]`, model.ID); err != nil {
		t.Fatal(err)
	}

	provider.invokeTool = "python_execute"
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode:                ToolModeEnabled,
		SelectedToolsConfigured: true,
		SelectedToolIDs: []string{
			"builtin:aivory_web_search", "hosted:web_search", "mcp:rail",
		},
	})

	request := provider.mainRequests[0]
	for _, allowed := range []string{"aivory_web_search", "mcp_train_lookup_abc123"} {
		if !requestHasTool(request, allowed) {
			t.Fatalf("selected tool %q was not declared: %+v", allowed, request.Tools)
		}
	}
	for _, denied := range []string{"python_execute", "mcp_paper_search_def456"} {
		if requestHasTool(request, denied) {
			t.Fatalf("unselected tool %q was declared: %+v", denied, request.Tools)
		}
	}
	if len(request.OfficialToolNames) != 1 || request.OfficialToolNames[0] != "web_search" {
		t.Fatalf("hosted selection=%v, want [web_search]", request.OfficialToolNames)
	}
	if provider.toolRunErr == nil || !strings.Contains(provider.toolRunErr.Error(), "not enabled") {
		t.Fatalf("forged unselected call reached execution: err=%v calls=%v", provider.toolRunErr, registry.calls)
	}
	if len(registry.calls) != 0 {
		t.Fatalf("forged call reached registry: %v", registry.calls)
	}
}

func TestSelectedToolIDsOmittedUsesModelDefaultsAndExplicitEmptyMeansNone(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		orchestrator, provider, model, conversation, _, _ := setupToolRouteTest(t)
		orchestrator.tools = &selectedToolsRegistry{}
		runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
		request := provider.mainRequests[0]
		for _, name := range []string{"aivory_web_search", "python_execute"} {
			if !requestHasTool(request, name) {
				t.Fatalf("omitted selection lost %q: %+v", name, request.Tools)
			}
		}
		for _, name := range []string{"mcp_train_lookup_abc123", "mcp_paper_search_def456"} {
			if requestHasTool(request, name) {
				t.Fatalf("default-off MCP tool %q was declared: %+v", name, request.Tools)
			}
		}
		for _, name := range []string{"mcp_private_notes_123abc", "mcp_team_docs_456def"} {
			if requestHasTool(request, name) {
				t.Fatalf("user MCP tool %q became a model default: %+v", name, request.Tools)
			}
		}
	})

	t.Run("explicit empty auto", func(t *testing.T) {
		orchestrator, provider, model, conversation, _, _ := setupToolRouteTest(t)
		orchestrator.tools = &selectedToolsRegistry{}
		provider.routeResponse = "1"
		runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
			ToolMode: ToolModeAuto, SelectedToolsConfigured: true, SelectedToolIDs: []string{},
		})
		if provider.routeCalls != 0 {
			t.Fatalf("empty selection called automatic router %d time(s)", provider.routeCalls)
		}
		if len(provider.mainRequests) != 1 || len(provider.mainRequests[0].Tools) != 0 || provider.mainRequests[0].ToolsEnabled {
			t.Fatalf("empty selection exposed tools: %+v", provider.mainRequests)
		}
	})
}

func TestUserMCPSelectionUsesScopedNamespaceAndOwnerExemption(t *testing.T) {
	orchestrator, provider, model, conversation, _, _ := setupToolRouteTest(t)
	registry := &selectedToolsRegistry{}
	orchestrator.tools = registry
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode: ToolModeEnabled, SelectedToolsConfigured: true,
		SelectedToolIDs:  []string{"usermcp:personal", "usermcp:team"},
		ToolAccessPolicy: &ToolAccessPolicy{Mode: store.ResourceAccessNone},
	})

	request := provider.mainRequests[0]
	if !requestHasTool(request, "mcp_private_notes_123abc") {
		t.Fatalf("owner-exempt user MCP was not declared: %+v", request.Tools)
	}
	if requestHasTool(request, "mcp_team_docs_456def") {
		t.Fatalf("teammate user MCP bypassed group policy: %+v", request.Tools)
	}
	if len(registry.mcpListCalls) == 0 {
		t.Fatal("scoped MCP registry was not consulted")
	}
	call := registry.mcpListCalls[len(registry.mcpListCalls)-1]
	if call.modelID != model.ID || call.userID != "u1" || call.workspaceID != "" {
		t.Fatalf("primary MCP scope = %+v", call)
	}
}

func TestUserMCPOwnerExemptionCannotBypassHardDenies(t *testing.T) {
	registry := &selectedToolsRegistry{}
	defs := registry.ListMCP("model", "u1", "")
	ownerPolicy := &ToolAccessPolicy{
		Mode:                  store.ResourceAccessNone,
		AllowToolCalling:      true,
		ToolCallingConfigured: true,
		AllowMCP:              true,
		MCPConfigured:         true,
		DenyIDs:               []string{"usermcp:personal"},
	}
	filtered := filterMCPToolsByAccess(defs, ownerPolicy)
	for _, definition := range filtered {
		if definition.ServerID == "personal" {
			t.Fatalf("owner exemption bypassed explicit deny: %+v", filtered)
		}
	}

	workspacePolicy := *ownerPolicy
	workspacePolicy.DenyIDs = nil
	workspacePolicy.AllowMCP = false
	filtered = filterMCPToolsByAccess(defs, &workspacePolicy)
	for _, definition := range filtered {
		if definition.UserOwned {
			t.Fatalf("owner exemption bypassed workspace MCP switch: %+v", filtered)
		}
	}

	workspacePolicy.AllowMCP = true
	workspacePolicy.AllowToolCalling = false
	filtered = filterMCPToolsByAccess(defs, &workspacePolicy)
	for _, definition := range filtered {
		if definition.UserOwned {
			t.Fatalf("owner exemption bypassed workspace tool-calling switch: %+v", filtered)
		}
	}
}

func TestFallbackRebuildPreservesUserMCPSelectionAndWorkspaceScope(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	registry := &selectedToolsRegistry{}
	orchestrator.tools = registry
	workspace, err := store.CreateWorkspace(context.Background(), db, "u1", "Fallback MCP scope")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "user-mcp-fallback", Label: "User MCP fallback",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{
		UserID: "u1", WorkspaceID: workspace.ID, ToolsEnabled: true,
		SelectedToolsConfigured: true, SelectedToolIDs: []string{"usermcp:personal"},
		ToolAccessPolicy: &ToolAccessPolicy{Mode: store.ResourceAccessNone},
	}, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "mcp_private_notes_123abc" {
		t.Fatalf("fallback user MCP tools=%+v", request.Tools)
	}
	if len(registry.mcpListCalls) == 0 {
		t.Fatal("fallback did not consult scoped MCP registry")
	}
	call := registry.mcpListCalls[len(registry.mcpListCalls)-1]
	if call.modelID != fallback.ID || call.userID != "u1" || call.workspaceID != workspace.ID {
		t.Fatalf("fallback MCP scope = %+v", call)
	}
}

func TestFallbackRebuildRechecksWorkspaceMemberMCPPermission(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	registry := &selectedToolsRegistry{}
	orchestrator.tools = registry
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u2','fallback-member@example.test','h','user')`); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.CreateWorkspace(ctx, db, "u1", "Fallback member policy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.JoinWorkspace(ctx, db, workspace.ID, "u2"); err != nil {
		t.Fatal(err)
	}
	permissions := store.WorkspaceMemberPermissions{
		CanCreateProjects: true, CanPrivateConversations: true,
		CanCreatePrompts: true, CanCreateSkills: true, CanCreateMCP: true,
		CanUsePrompts: true, CanUseSkills: false, CanUseMCP: false,
		CanCreateKB: true, CanAddKBFiles: true, CanDeleteKBContent: true,
		CanDeleteConversations: true,
	}
	if _, err := store.UpdateWorkspaceMemberPermissions(ctx, db, workspace.ID, "u1", "u2", permissions); err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "member-policy-fallback", Label: "Member policy fallback",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, _, err := orchestrator.buildFallbackRequest(ctx, UnifiedChatRequest{
		UserID: "u2", WorkspaceID: workspace.ID, ToolsEnabled: true,
		SelectedToolsConfigured: true, SelectedToolIDs: []string{"usermcp:personal"},
		ToolAccessPolicy: &ToolAccessPolicy{
			Mode: store.ResourceAccessAll, AllowToolCalling: true, ToolCallingConfigured: true,
			AllowMCP: true, MCPConfigured: true, AllowSkills: true,
			SkillMode: store.ResourceAccessAll,
		},
	}, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.ToolAccessPolicy == nil || request.ToolAccessPolicy.AllowMCP || request.ToolAccessPolicy.AllowSkills {
		t.Fatalf("fallback member policy was not re-applied: %+v", request.ToolAccessPolicy)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("fallback declared MCP tools after member revocation: %+v", request.Tools)
	}
}

func TestFallbackRebuildRechecksCurrentUserGroupMCPPermission(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	registry := &selectedToolsRegistry{}
	orchestrator.tools = registry
	ctx := context.Background()
	workspace, err := store.CreateWorkspace(ctx, db, "u1", "Fallback group policy")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "group-policy-fallback", Label: "Group policy fallback",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	permissions := store.DefaultUserGroupPermissions()
	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('fallback-none','Fallback none',?);
		UPDATE users SET role='user', group_id='fallback-none' WHERE id='u1'`, string(raw)); err != nil {
		t.Fatal(err)
	}

	request, _, _, err := orchestrator.buildFallbackRequest(ctx, UnifiedChatRequest{
		UserID: "u1", WorkspaceID: workspace.ID, ToolsEnabled: true,
		SelectedToolsConfigured: true,
		SelectedToolIDs:         []string{"mcp:rail", "usermcp:team"},
		// Model the permissive request-start snapshot captured before the group
		// policy was revoked. The fallback must intersect it with the current row.
		ToolAccessPolicy: groupToolAccessPolicy(store.DefaultUserGroupPermissions()),
	}, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("fallback exposed official or teammate user MCP after group revocation: %+v", request.Tools)
	}
}

func TestFallbackRebuildFailsClosedWhenCurrentUserGroupPermissionLookupFails(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "missing-user-fallback", Label: "Missing user fallback",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{
		UserID: "missing-user", ToolsEnabled: true,
		SelectedToolsConfigured: true, SelectedToolIDs: []string{"mcp:rail", "usermcp:team"},
	}, fallback.ID)
	if err == nil || !strings.Contains(err.Error(), "resolve fallback user-group permissions") {
		t.Fatalf("fallback permission lookup error=%v", err)
	}
}

func TestModelMCPDefaultsFilterOmittedSelectionButNotExplicitUserSelection(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	orchestrator.tools = &selectedToolsRegistry{}
	if _, err := db.Exec(`UPDATE models SET mcp_server_ids='["rail"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}

	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	request := provider.mainRequests[0]
	if !requestHasTool(request, "mcp_train_lookup_abc123") || requestHasTool(request, "mcp_paper_search_def456") {
		t.Fatalf("model MCP defaults were not applied: %+v", request.Tools)
	}

	orchestrator, provider, model, conversation, _, db = setupToolRouteTest(t)
	orchestrator.tools = &selectedToolsRegistry{}
	if _, err := db.Exec(`UPDATE models SET mcp_server_ids='["rail"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode: ToolModeEnabled, SelectedToolsConfigured: true, SelectedToolIDs: []string{"mcp:papers"},
	})
	request = provider.mainRequests[0]
	if requestHasTool(request, "mcp_train_lookup_abc123") || !requestHasTool(request, "mcp_paper_search_def456") {
		t.Fatalf("explicit user selection did not override model defaults: %+v", request.Tools)
	}
}

func TestAutomaticRouterReceivesOnlySelectedCandidates(t *testing.T) {
	orchestrator, provider, model, conversation, _, _ := setupToolRouteTest(t)
	orchestrator.tools = &selectedToolsRegistry{}
	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode: ToolModeAuto, SelectedToolsConfigured: true,
		SelectedToolIDs: []string{"mcp:rail"},
		UserText:        "Help me decide what to do next",
	})
	if len(provider.taskRequests) != 1 {
		t.Fatalf("task requests=%d, want 1", len(provider.taskRequests))
	}
	prompt := provider.taskRequests[0].History[0].Blocks[0].Text
	if !strings.Contains(prompt, "custom:mcp_train_lookup") {
		t.Fatalf("router did not receive selected MCP capability: %s", prompt)
	}
	for _, forbidden := range []string{"CAP=web", "CAP=code", "mcp_paper_search"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("router received unselected capability %q: %s", forbidden, prompt)
		}
	}
}

func TestFallbackRebuildPreservesSelectedToolSubset(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	orchestrator.tools = &selectedToolsRegistry{}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "selected-fallback", Label: "Selected fallback",
		Enabled: true, Stream: true, ToolMode: "native",
		OfficialTools: json.RawMessage(`[
			{"name":"web_search","icon":"Search","request":{"tools":[{"type":"web_search"}]}},
			{"name":"code_interpreter","icon":"Terminal","request":{"tools":[{"type":"code_interpreter"}]}}
		]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := UnifiedChatRequest{
		UserID: "u1", ToolsEnabled: true,
		SelectedToolsConfigured: true,
		SelectedToolIDs:         []string{"mcp:papers", "hosted:web_search"},
	}
	request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "mcp_paper_search_def456" {
		t.Fatalf("fallback local tools=%+v", request.Tools)
	}
	if len(request.OfficialToolNames) != 1 || request.OfficialToolNames[0] != "web_search" {
		t.Fatalf("fallback hosted tools=%v", request.OfficialToolNames)
	}
}

func TestFallbackRebuildUsesFallbackModelMCPDefaults(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	orchestrator.tools = &selectedToolsRegistry{}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "mcp-default-fallback", Label: "MCP default fallback",
		Enabled: true, Stream: true, ToolMode: "native", MCPServerIDs: json.RawMessage(`["papers"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{
		UserID: "u1", ToolsEnabled: true,
	}, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) == 0 || requestHasTool(request, "mcp_train_lookup_abc123") || !requestHasTool(request, "mcp_paper_search_def456") {
		t.Fatalf("fallback MCP defaults were not applied: %+v", request.Tools)
	}
}
