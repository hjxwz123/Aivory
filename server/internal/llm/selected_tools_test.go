package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type selectedToolsRegistry struct {
	calls []string
}

func (r *selectedToolsRegistry) List(string) []ToolDef {
	largeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", 2400) + `"}`)
	return []ToolDef{
		{Name: "aivory_web_search", Description: "Search the web", InputSchema: largeSchema},
		{Name: "python_execute", Description: "Run code", InputSchema: largeSchema},
	}
}

func (r *selectedToolsRegistry) ListMCP(string) []MCPToolDef {
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

func TestSelectedToolIDsOmittedMeansAllAndExplicitEmptyMeansNone(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		orchestrator, provider, model, conversation, _, _ := setupToolRouteTest(t)
		orchestrator.tools = &selectedToolsRegistry{}
		runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
		request := provider.mainRequests[0]
		for _, name := range []string{
			"aivory_web_search", "python_execute", "mcp_train_lookup_abc123", "mcp_paper_search_def456",
		} {
			if !requestHasTool(request, name) {
				t.Fatalf("omitted selection lost %q: %+v", name, request.Tools)
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
