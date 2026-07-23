package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type recordingToolRunner struct {
	calls []string
}

func (r *recordingToolRunner) Run(_ context.Context, name string, _ []byte) (string, []Citation, error) {
	r.calls = append(r.calls, name)
	return "ran " + name, nil, nil
}

type recordingToolRegistry struct {
	calls []string
}

func (r *recordingToolRegistry) List(string) []ToolDef {
	return []ToolDef{{Name: "web_search"}, {Name: "web_fetch"}, {Name: "python_execute"}}
}

func (r *recordingToolRegistry) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	r.calls = append(r.calls, name)
	return "ok", nil, nil
}

type snippetSearchRegistry struct {
	calls []string
}

func (r *snippetSearchRegistry) List(string) []ToolDef {
	return []ToolDef{{Name: "web_search"}, {Name: "web_fetch"}}
}

func (r *snippetSearchRegistry) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	r.calls = append(r.calls, name)
	if name == "web_search" {
		return "search result", []Citation{{
			Title: "Official result", URL: "https://www.nist.gov/example", Snippet: "A useful search snippet.", Source: "web",
		}}, nil
	}
	return "fetched body", nil, nil
}

type toolContextRecordingRegistry struct {
	modelIDs []string
}

func (r *toolContextRecordingRegistry) List(string) []ToolDef {
	return []ToolDef{{Name: "use_skill"}}
}

func (r *toolContextRecordingRegistry) Run(_ context.Context, _ string, _ []byte, tc *ToolContext) (string, []Citation, error) {
	r.modelIDs = append(r.modelIDs, tc.ModelID)
	return "ok", nil, nil
}

func TestBuiltinToolAllowlistFiltersNativeDeclarationsAndFinalRunner(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET builtin_tools='["web_search"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	provider.invokeTool = "python_execute"
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	request := provider.mainRequests[0]
	if len(request.Tools) != 1 || request.Tools[0].Name != "web_search" {
		t.Fatalf("native declarations = %+v, want only web_search", request.Tools)
	}
	if provider.toolRunErr == nil || !strings.Contains(provider.toolRunErr.Error(), "not enabled") {
		t.Fatalf("forged native call reached execution: %v", provider.toolRunErr)
	}
}

func TestGlobalDisabledToolsRemainDeniedAtFinalRunner(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET tool_mode='prompt', builtin_tools='["web_search","python_execute"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "disabled_tools", []string{"python_execute"}); err != nil {
		t.Fatal(err)
	}
	provider.invokeTool = "python_execute"
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	if !provider.mainRequests[0].ToolModePrompt {
		t.Fatal("test request did not exercise prompt tool mode")
	}
	if requestHasTool(provider.mainRequests[0], "python_execute") {
		t.Fatalf("globally disabled tool was declared: %+v", provider.mainRequests[0].Tools)
	}
	if provider.toolRunErr == nil || !strings.Contains(provider.toolRunErr.Error(), "not enabled") {
		t.Fatalf("globally disabled forged call reached execution: %v", provider.toolRunErr)
	}
}

func TestPromptModePreambleAndForgedCallUseFilteredDefinitions(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET tool_mode='prompt', builtin_tools='["web_search"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	request := provider.mainRequests[0]
	if !request.ToolModePrompt || len(request.Tools) != 1 || request.Tools[0].Name != "web_search" {
		t.Fatalf("prompt request tools=%+v prompt=%v", request.Tools, request.ToolModePrompt)
	}
	preamble := PromptToolPreamble(request.Tools)
	if !strings.Contains(preamble, "web_search") || strings.Contains(preamble, "python_execute") {
		t.Fatalf("prompt preamble leaked unsupported tools: %s", preamble)
	}

	underlying := &recordingToolRunner{}
	exactRunner := toolDefAllowlistRunner{next: underlying, allowed: toolDefNameSet(request.Tools)}
	round := 0
	var systems []string
	_, _, _, _, err := RunPromptToolLoop(
		context.Background(), "system", nil, request.Tools,
		func(_ context.Context, _ []UnifiedMessage, system string) (string, Usage, error) {
			systems = append(systems, system)
			round++
			if round == 1 {
				return `<tool_call>{"name":"python_execute","arguments":{"code":"print(1)"}}</tool_call>`, Usage{}, nil
			}
			return "done", Usage{}, nil
		},
		exactRunner,
		func(SseEvent) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(underlying.calls) != 0 {
		t.Fatalf("forged prompt call executed: %v", underlying.calls)
	}
	if len(systems) == 0 || !strings.Contains(systems[0], "web_search") || strings.Contains(systems[0], "python_execute") {
		t.Fatalf("RunPromptToolLoop received unfiltered definitions: %v", systems)
	}
}

func TestPromptModeWithEmptyBuiltinAllowlistUsesPlainSingleTurnRequest(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET tool_mode='prompt', builtin_tools='[]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	request := provider.mainRequests[0]
	if len(request.Tools) != 0 || request.ToolModePrompt {
		t.Fatalf("empty allowlist started prompt tool loop: tools=%+v prompt=%v", request.Tools, request.ToolModePrompt)
	}
}

func TestAutoRouterCandidatesExcludeUnavailableToolsAndSkills(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET builtin_tools='["web_search"]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	skill, err := store.CreateSkill(context.Background(), db, store.Skill{ID: "sk-allow", Name: "private-skill", Description: "Must not become a route candidate", Instructions: "instructions", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSkillsForModel(context.Background(), db, model.ID, []string{skill.ID}); err != nil {
		t.Fatal(err)
	}
	provider.routeResponse = `{"use_tools":false}`
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "answer this"})
	if len(provider.taskRequests) != 1 {
		t.Fatalf("task requests=%d", len(provider.taskRequests))
	}
	prompt := provider.taskRequests[0].History[0].Blocks[0].Text
	if !strings.Contains(prompt, "web_search") {
		t.Fatalf("router lost allowed tool: %s", prompt)
	}
	for _, forbidden := range []string{"python_execute", "use_skill", "skill:private-skill", "Must not become a route candidate"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("router included unavailable %q: %s", forbidden, prompt)
		}
	}
}

func TestAutoWithEmptyEffectiveToolSetSkipsClassifierAndUsesNoToolsPipeline(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET tool_mode='prompt', builtin_tools='[]' WHERE id=?`, model.ID); err != nil {
		t.Fatal(err)
	}
	provider.routeResponse = `{"use_tools":true}`
	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{
		ToolMode: ToolModeAuto,
		UserText: "Use any available tool",
	})
	if provider.routeCalls != 0 {
		t.Fatalf("empty effective tool set invoked classifier %d time(s)", provider.routeCalls)
	}
	if len(provider.taskRequests) != 0 {
		t.Fatalf("empty effective tool set issued task-model requests: %+v", provider.taskRequests)
	}
	if len(provider.mainRequests) != 1 {
		t.Fatalf("main requests=%d, want 1", len(provider.mainRequests))
	}
	request := provider.mainRequests[0]
	if len(request.Tools) != 0 || request.ToolModePrompt {
		t.Fatalf("deny-all Auto request exposed tools: tools=%+v prompt=%v", request.Tools, request.ToolModePrompt)
	}
	if request.SystemPromptOptions == nil || request.SystemPromptOptions.ToolMode != "none" {
		t.Fatalf("deny-all Auto did not enter no-tools prompt pipeline: %+v", request.SystemPromptOptions)
	}
	if strings.Contains(request.SystemPrompt, "Tool guidance") || strings.Contains(request.SystemPrompt, "python_execute") {
		t.Fatalf("deny-all Auto prompt retained tool guidance:\n%s", request.SystemPrompt)
	}
}

func TestDeepResearchDoesNotBypassBuiltinToolAllowlist(t *testing.T) {
	registry := &recordingToolRegistry{}
	var events []SseEvent
	rs := &researcher{
		o:        &Orchestrator{tools: registry},
		tc:       &ToolContext{BuiltinTools: map[string]bool{}},
		question: "test question",
		emit:     func(event SseEvent) { events = append(events, event) },
		logger:   func(string, ...any) {},
	}
	plan := rs.plan(context.Background())
	rs.researchLoop(context.Background(), plan)
	if len(registry.calls) != 0 {
		t.Fatalf("Deep Research executed disallowed tools: %v", registry.calls)
	}
	if len(rs.state.Tasks) != 1 || rs.state.Tasks[0].Status != "done" {
		t.Fatalf("denied search left research tasks pending: %+v", rs.state.Tasks)
	}
	foundDone := false
	for _, event := range events {
		if event.Type == "research_task" && event.Status == "done" {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("denied search did not emit a completed task event: %+v", events)
	}
}

func TestDeepResearchKeepsSearchEvidenceWithoutWebFetch(t *testing.T) {
	registry := &snippetSearchRegistry{}
	rs := &researcher{
		o:        &Orchestrator{tools: registry},
		tc:       &ToolContext{BuiltinTools: map[string]bool{"web_search": true}},
		question: "test question",
		emit:     func(SseEvent) {},
		seen:     map[string]int{},
		sourceID: map[string]string{},
		logger:   func(string, ...any) {},
	}
	plan := rs.plan(context.Background())
	rs.researchLoop(context.Background(), plan)
	if len(registry.calls) != 1 || registry.calls[0] != "web_search" {
		t.Fatalf("Deep Research calls=%v, want web_search only", registry.calls)
	}
	if len(rs.evidence) != 1 || rs.evidence[0].Snippet != "A useful search snippet." || rs.evidence[0].Body != "" {
		t.Fatalf("search-only evidence was not preserved correctly: %+v", rs.evidence)
	}
	if len(rs.cites) != 1 || len(rs.state.Sources) != 1 || rs.state.Sources[0].Status != "kept" {
		t.Fatalf("search-only source state is incomplete: cites=%+v state=%+v", rs.cites, rs.state.Sources)
	}
}

func TestForcedSearchAndFallbackRespectBuiltinToolAllowlist(t *testing.T) {
	t.Run("forced search", func(t *testing.T) {
		registry := &recordingToolRegistry{}
		o := &Orchestrator{tools: registry}
		text, cites := o.forcedWebSearch(
			context.Background(),
			RunRequest{UserID: "u1", ConversationID: "c1", UserText: "latest"},
			&store.Conversation{ID: "c1"}, nil, 0, map[string]bool{}, func(SseEvent) {},
		)
		if text != "" || cites != nil || len(registry.calls) != 0 {
			t.Fatalf("forced search bypassed deny-all: text=%q cites=%v calls=%v", text, cites, registry.calls)
		}
	})

	t.Run("fallback declarations and runner", func(t *testing.T) {
		orchestrator, _, model, _, _, db := setupToolRouteTest(t)
		fallback, err := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: model.ChannelID, Kind: "chat", RequestID: "builtin-fallback", Label: "Builtin fallback",
			Enabled: true, Stream: true, ToolMode: "native", BuiltinTools: json.RawMessage(`["web_search"]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		base := UnifiedChatRequest{Tools: []ToolDef{{Name: "python_execute"}, {Name: "web_search"}, {Name: "use_skill"}}}
		request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != "web_search" {
			t.Fatalf("fallback tools=%+v, want only web_search", request.Tools)
		}
		underlying := &recordingToolRunner{}
		runner := toolDefAllowlistRunner{next: underlying, allowed: toolDefNameSet(request.Tools)}
		if _, _, err := runner.Run(context.Background(), "python_execute", nil); err == nil {
			t.Fatal("fallback runner accepted disallowed tool")
		}
		if len(underlying.calls) != 0 {
			t.Fatalf("fallback executed disallowed tool: %v", underlying.calls)
		}
	})

	t.Run("fallback recomposes prompt skills and combined history filters", func(t *testing.T) {
		orchestrator, _, model, _, _, db := setupToolRouteTest(t)
		fallback, err := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: model.ChannelID, Kind: "chat", RequestID: "prompt-fallback", Label: "Prompt fallback",
			Enabled: true, Stream: true, ToolMode: "prompt", BuiltinTools: json.RawMessage(`["web_search"]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		options := systemPromptOpts{
			ModelLabel: "Primary", ToolMode: "prompt",
			ToolNames:          []string{"python_execute", "web_search", "use_skill"},
			Skills:             []SkillIndex{{Name: "PRIMARY-SKILL-MARKER", When: "primary marker"}},
			SkillsFull:         []SkillFull{{Name: "PRIMARY-SKILL-MARKER", Instructions: "PRIMARY-INSTRUCTIONS-MARKER"}},
			SandboxFiles:       []ProjectFileSummary{{Name: "primary.csv", Kind: "text/csv"}},
			SkillToolAvailable: true,
			SkillsAllowed:      true,
		}
		base := UnifiedChatRequest{
			SystemPrompt:        composeSystemPrompt(options),
			SystemPromptOptions: &options,
			Tools:               []ToolDef{{Name: "python_execute"}, {Name: "web_search"}, {Name: "use_skill"}},
			History: []UnifiedMessage{{
				Role: "assistant",
				Blocks: []UnifiedBlock{
					{Kind: "image", MimeType: "image/png", Data: "aW1n"},
					{Kind: "tool_call", ToolName: "python_execute", ToolID: "denied"},
					{Kind: "tool_output", ToolID: "denied", Text: "output"},
					{Kind: "tool_call", ToolName: "web_search", ToolID: "allowed"},
					{Kind: "text", Text: "answer"},
				},
				Raw: json.RawMessage(`[{"type":"input_image"},{"type":"function_call","name":"python_execute"}]`),
			}},
		}
		request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != "web_search" || !request.ToolModePrompt {
			t.Fatalf("fallback request tools=%+v prompt=%v", request.Tools, request.ToolModePrompt)
		}
		for _, forbidden := range []string{"python_execute", "PRIMARY-SKILL-MARKER", "PRIMARY-INSTRUCTIONS-MARKER", "primary.csv"} {
			if strings.Contains(request.SystemPrompt, forbidden) {
				t.Fatalf("fallback system prompt retained %q:\n%s", forbidden, request.SystemPrompt)
			}
		}
		if !strings.Contains(request.SystemPrompt, "web_search") {
			t.Fatalf("fallback prompt lost allowed tool:\n%s", request.SystemPrompt)
		}
		encoded, _ := json.Marshal(request.History)
		if strings.Contains(string(encoded), "python_execute") || strings.Contains(string(encoded), "input_image") || strings.Contains(string(encoded), `"kind":"image"`) {
			t.Fatalf("fallback history retained denied tool or image: %s", encoded)
		}
		if !strings.Contains(string(encoded), "web_search") || !strings.Contains(string(encoded), "answer") {
			t.Fatalf("fallback history lost allowed content: %s", encoded)
		}
		if options.ToolMode != "prompt" || len(options.ToolNames) != 3 || len(options.SandboxFiles) != 1 {
			t.Fatalf("fallback mutated primary prompt options: %+v", options)
		}
	})

	t.Run("fallback advertises only its own bound skills", func(t *testing.T) {
		orchestrator, _, model, _, _, db := setupToolRouteTest(t)
		fallback, err := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: model.ChannelID, Kind: "chat", RequestID: "skill-fallback", Label: "Skill fallback",
			Enabled: true, Stream: true, ToolMode: "native", BuiltinTools: json.RawMessage(`["use_skill"]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		fallbackSkill, err := store.CreateSkill(context.Background(), db, store.Skill{
			ID: "fallback-skill", Name: "FALLBACK-SKILL-MARKER", Description: "FALLBACK-DESCRIPTION-MARKER",
			Instructions: "FALLBACK-INSTRUCTIONS-MARKER", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetSkillsForModel(context.Background(), db, fallback.ID, []string{fallbackSkill.ID}); err != nil {
			t.Fatal(err)
		}
		options := systemPromptOpts{
			ModelLabel: "Primary", ToolMode: "native", ToolNames: []string{"use_skill"},
			Skills:             []SkillIndex{{Name: "PRIMARY-SKILL-MARKER", When: "PRIMARY-DESCRIPTION-MARKER"}},
			SkillsFull:         []SkillFull{{Name: "PRIMARY-SKILL-MARKER", Instructions: "PRIMARY-INSTRUCTIONS-MARKER"}},
			SkillToolAvailable: true, SkillsAllowed: true,
		}
		request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{
			SystemPrompt: composeSystemPrompt(options), SystemPromptOptions: &options, Tools: []ToolDef{{Name: "use_skill"}},
		}, fallback.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.SystemPrompt, "FALLBACK-SKILL-MARKER") || !strings.Contains(request.SystemPrompt, "FALLBACK-DESCRIPTION-MARKER") {
			t.Fatalf("fallback prompt did not advertise its bound skill:\n%s", request.SystemPrompt)
		}
		for _, forbidden := range []string{"PRIMARY-SKILL-MARKER", "PRIMARY-DESCRIPTION-MARKER", "PRIMARY-INSTRUCTIONS-MARKER"} {
			if strings.Contains(request.SystemPrompt, forbidden) {
				t.Fatalf("fallback prompt retained primary skill %q:\n%s", forbidden, request.SystemPrompt)
			}
		}
	})

	t.Run("fallback empty prompt policy", func(t *testing.T) {
		orchestrator, _, model, _, _, db := setupToolRouteTest(t)
		fallback, err := store.CreateModel(context.Background(), db, store.Model{
			ChannelID: model.ChannelID, Kind: "chat", RequestID: "empty-prompt-fallback", Label: "Empty prompt fallback",
			Enabled: true, Stream: true, ToolMode: "prompt", BuiltinTools: json.RawMessage(`[]`),
		})
		if err != nil {
			t.Fatal(err)
		}
		request, _, _, err := orchestrator.buildFallbackRequest(context.Background(), UnifiedChatRequest{Tools: []ToolDef{{Name: "web_search"}}}, fallback.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 0 || request.ToolModePrompt {
			t.Fatalf("empty fallback policy started prompt tool loop: tools=%+v prompt=%v", request.Tools, request.ToolModePrompt)
		}
	})
}

func TestBuiltinToolHistoryDropsDisallowedCallsAndRaw(t *testing.T) {
	history := []UnifiedMessage{{
		Role: "assistant",
		Blocks: []UnifiedBlock{
			{Kind: "tool_call", ToolName: "python_execute", ToolID: "denied", Input: json.RawMessage(`{"code":"print(1)"}`)},
			{Kind: "tool_output", ToolID: "denied", Text: "1"},
			{Kind: "tool_call", ToolName: "web_search", ToolID: "allowed"},
			{Kind: "text", Text: "final"},
		},
		Raw: json.RawMessage(`[{"type":"function_call","name":"python_execute"}]`),
	}, {
		Role:   "assistant",
		Blocks: []UnifiedBlock{{Kind: "tool_call", ToolName: "web_search", ToolID: "still-allowed"}},
		Raw:    json.RawMessage(`[{"type":"function_call","name":"web_search"}]`),
	}}
	filtered := stripDisallowedBuiltinToolBlocks(history, map[string]bool{"web_search": true})
	encoded, _ := json.Marshal(filtered)
	if strings.Contains(string(encoded), "python_execute") || strings.Contains(string(encoded), `print(1)`) || len(filtered[0].Raw) != 0 {
		t.Fatalf("disallowed historical call survived: %s", encoded)
	}
	if !strings.Contains(string(encoded), "web_search") || !strings.Contains(string(encoded), "final") {
		t.Fatalf("allowed history was removed: %s", encoded)
	}
	if len(filtered[1].Raw) == 0 {
		t.Fatal("unaffected allowed history lost provider Raw")
	}
	if len(history[0].Raw) == 0 || history[0].Blocks[0].ToolName != "python_execute" {
		t.Fatal("history filter mutated its input")
	}
}

func TestDefaultAllPolicyStillDropsHistoryForUnregisteredTool(t *testing.T) {
	orchestrator, provider, model, conversation, _, db := setupToolRouteTest(t)
	// builtin_tools remains NULL (default all). "retired_tool" is deliberately
	// absent from the live registry, so exact per-request filtering must remove
	// its old native exchange even without an explicit model allowlist.
	userBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "old question"}})
	previousUser, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conversation.ID, Role: "user", Provider: "openai", ModelID: model.ID, Blocks: userBlocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	assistantBlocks, _ := json.Marshal([]UnifiedBlock{
		{Kind: "tool_call", ToolName: "retired_tool", ToolID: "retired-call"},
		{Kind: "tool_output", ToolID: "retired-call", Text: "legacy output"},
		{Kind: "text", Text: "old final answer"},
	})
	if _, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conversation.ID, ParentID: previousUser.ID, Role: "assistant", Provider: "openai", ModelID: model.ID,
		Blocks: assistantBlocks, Raw: json.RawMessage(`[{"type":"function_call","name":"retired_tool"}]`),
	}); err != nil {
		t.Fatal(err)
	}

	runToolRouteTurn(t, orchestrator, model.ID, conversation.ID, RunRequest{ToolMode: ToolModeEnabled})
	if len(provider.mainRequests) != 1 {
		t.Fatalf("main requests=%d", len(provider.mainRequests))
	}
	encoded, _ := json.Marshal(provider.mainRequests[0].History)
	if strings.Contains(string(encoded), "retired_tool") || strings.Contains(string(encoded), "legacy output") {
		t.Fatalf("default-all request replayed an unregistered tool: %s", encoded)
	}
	if !strings.Contains(string(encoded), "old final answer") {
		t.Fatalf("history filtering removed ordinary answer content: %s", encoded)
	}
	for _, message := range provider.mainRequests[0].History {
		if strings.Contains(string(message.Raw), "retired_tool") {
			t.Fatalf("affected history retained provider Raw: %s", message.Raw)
		}
	}
}

func TestFallbackToolRunnerUsesFallbackModelContext(t *testing.T) {
	registry := &toolContextRecordingRegistry{}
	orchestrator := &Orchestrator{tools: registry}
	primary := &orchToolRunner{
		orch: orchestrator,
		ctx:  &ToolContext{ModelID: "primary-model", BuiltinTools: map[string]bool{"use_skill": true}},
	}
	runner := toolDefAllowlistRunner{next: primary, allowed: map[string]bool{"use_skill": true}}
	fallbackRunner := toolRunnerForModelRequest(runner, "fallback-model", []ToolDef{{Name: "use_skill"}})
	if _, _, err := fallbackRunner.Run(context.Background(), "use_skill", nil); err != nil {
		t.Fatal(err)
	}
	if len(registry.modelIDs) != 1 || registry.modelIDs[0] != "fallback-model" {
		t.Fatalf("fallback execution used model contexts %v", registry.modelIDs)
	}
}

func TestToolDefAllowlistRunnerFailsClosed(t *testing.T) {
	underlying := &recordingToolRunner{}
	runner := toolDefAllowlistRunner{next: underlying, allowed: map[string]bool{"web_search": true}}
	if _, _, err := runner.Run(context.Background(), "python_execute", nil); err == nil {
		t.Fatal("runner accepted an undeclared tool")
	}
	if len(underlying.calls) != 0 {
		t.Fatalf("runner executed undeclared tool: %v", underlying.calls)
	}
	if _, _, err := runner.Run(context.Background(), "web_search", nil); err != nil {
		t.Fatalf("runner rejected declared tool: %v", err)
	}
	if len(underlying.calls) != 1 || underlying.calls[0] != "web_search" {
		t.Fatalf("declared tool calls=%v", underlying.calls)
	}
}
