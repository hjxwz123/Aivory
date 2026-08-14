package llm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

type toolRouteCaptureProvider struct {
	routeResponse string
	routeErr      error
	routeCalls    int
	taskRequests  []UnifiedChatRequest
	mainRequests  []UnifiedChatRequest
	invokeTool    string
	toolRunErr    error
}

func (p *toolRouteCaptureProvider) ID() string { return "openai" }

func (p *toolRouteCaptureProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	tools ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	if req.Model.RequestID == "task-route-test" {
		p.taskRequests = append(p.taskRequests, req)
		var output string
		switch {
		case strings.Contains(req.SystemPrompt, "Reply only 0 or 1"):
			p.routeCalls++
			if p.routeErr != nil {
				return nil, p.routeErr
			}
			output = p.routeResponse
			if output == "" {
				output = "1"
			}
		case strings.Contains(req.SystemPrompt, "planning an investigation"):
			output = `{"title":"Test","research_type":"concept","scope":"current","sub_questions":[{"id":"q1","dimension":"facts","question":"What is known?","search_queries":["test query"]}]}`
		case strings.Contains(req.SystemPrompt, "auditing research coverage"):
			output = `{"sufficient":true,"uncovered":[],"weak_claims":[],"new_queries":[]}`
		case strings.Contains(req.SystemPrompt, "cross-validating research evidence"):
			output = `{"confirmed":[],"disputed":[],"unverified":[]}`
		default:
			output = `{}`
		}
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "text", Text: output}},
			StopReason: "stop",
			Usage:      Usage{InputTokens: 2, OutputTokens: 1},
		}, nil
	}
	p.mainRequests = append(p.mainRequests, req)
	if p.invokeTool != "" {
		_, _, p.toolRunErr = tools.Run(context.Background(), p.invokeTool, nil)
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "answer"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 3, OutputTokens: 1},
	}, nil
}

type toolRouteTestTools struct{}

func (toolRouteTestTools) List(string) []ToolDef {
	largeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", 2400) + `"}`)
	return []ToolDef{
		{Name: "python_execute", Description: "Run Python for calculations and spreadsheet analysis.", InputSchema: largeSchema},
		{Name: "use_skill", Description: "Load one of the model's configured skills.", InputSchema: largeSchema},
		{Name: "aivory_web_search", Description: "Search the public web for current information.", InputSchema: largeSchema},
	}
}

type smallToolRouteTestTools struct{}

func (smallToolRouteTestTools) List(string) []ToolDef {
	return []ToolDef{{
		Name:        "aivory_web_search",
		Description: "Search current information.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}
}

func (smallToolRouteTestTools) Run(_ context.Context, _ string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	return "ok", nil, nil
}

func (toolRouteTestTools) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	switch name {
	case "aivory_web_search":
		return "A current test result.", []Citation{{ID: "w1", Index: 1, Title: "Result", URL: "https://example.com", Snippet: "test", Source: "web"}}, nil
	case "web_fetch":
		return "Detailed source text.", nil, nil
	default:
		return "ok", nil, nil
	}
}

func setupToolRouteTest(t *testing.T) (*Orchestrator, *toolRouteCaptureProvider, *store.Model, *store.Conversation, *bytes.Buffer, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "tool-route.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','route@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Route", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	taskModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "task-route-test", Label: "Task", Enabled: true, Stream: true, ToolMode: "none",
	})
	if err != nil {
		t.Fatalf("create task model: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "chat-route-test", Label: "Chat", Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatalf("create chat model: %v", err)
	}
	if err := store.SetSetting(db, "task_model_id", taskModel.ID); err != nil {
		t.Fatalf("set task model: %v", err)
	}
	if err := store.SetSetting(db, "tool_route_model_id", taskModel.ID); err != nil {
		t.Fatalf("set tool route model: %v", err)
	}
	// The settings cache is process-global in tests; reset this key so a prior
	// disabled-tools test using another temporary DB cannot affect this fixture.
	if err := store.SetSetting(db, "disabled_tools", []string{}); err != nil {
		t.Fatalf("reset disabled tools: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Existing title", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	var logs bytes.Buffer
	logger := log.New(io.MultiWriter(&logs), "", 0)
	provider := &toolRouteCaptureProvider{}
	registry := NewRegistry(logger)
	registry.Register(provider)
	task := NewTaskLLM(db, registry, logger)
	orchestrator := NewOrchestrator(db, registry, toolRouteTestTools{}, nil, nil, nil, task, nil, logger)
	return orchestrator, provider, model, conversation, &logs, db
}

func runToolRouteTurn(t *testing.T, orchestrator *Orchestrator, model, conversation string, req RunRequest) {
	t.Helper()
	req.UserID = "u1"
	req.ConversationID = conversation
	if req.ModelID == "" {
		req.ModelID = model
	}
	if req.UserText == "" {
		req.UserText = "What should I do?"
	}
	if _, err := orchestrator.Run(context.Background(), req, func(SseEvent) {}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestAutoToolRouteYesNoAndFailOpen(t *testing.T) {
	cases := []struct {
		name        string
		response    string
		routeErr    error
		wantTools   bool
		wantFailLog bool
	}{
		{name: "yes", response: "1", wantTools: true},
		{name: "no", response: "0", wantTools: false},
		{name: "legacy json fails open", response: `{"use_tools":false}`, wantTools: true, wantFailLog: true},
		{name: "invalid text fails open", response: `not-a-verdict`, wantTools: true, wantFailLog: true},
		{name: "provider failure fails open", routeErr: errors.New("task backend unavailable"), wantTools: true, wantFailLog: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orchestrator, provider, model, conv, logs, db := setupToolRouteTest(t)
			if _, err := db.Exec(`UPDATE models SET official_tools='[{"name":"hosted_search","icon":"search","request":{"tools":[{"type":"hosted-search"}]}}]' WHERE id=?`, model.ID); err != nil {
				t.Fatalf("configure hosted tool: %v", err)
			}
			provider.routeResponse = tc.response
			provider.routeErr = tc.routeErr
			runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "Give me the answer"})
			if provider.routeCalls != 1 {
				t.Fatalf("tool route calls = %d, want 1", provider.routeCalls)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatalf("main requests = %d, want 1", len(provider.mainRequests))
			}
			gotTools := len(provider.mainRequests[0].Tools) > 0
			if gotTools != tc.wantTools {
				t.Fatalf("main tools present = %v, want %v", gotTools, tc.wantTools)
			}
			gotHostedTools := len(provider.mainRequests[0].OfficialToolRequests) > 0
			if gotHostedTools != tc.wantTools {
				t.Fatalf("main hosted tools present = %v, want %v", gotHostedTools, tc.wantTools)
			}
			if len(provider.taskRequests) != 1 || !strings.Contains(renderBlocksAsText(provider.taskRequests[0].History[0].Blocks), "CAP=web,code,file,skill") {
				t.Fatalf("automatic classifier did not receive compact capability flags: %+v", provider.taskRequests)
			}
			if tc.wantFailLog && !strings.Contains(logs.String(), "enabling tools") {
				t.Fatalf("missing fail-open log: %s", logs.String())
			}
		})
	}
}

func TestAutoSmallToolDeclarationSkipsClassifier(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	orchestrator.tools = smallToolRouteTestTools{}
	if _, err := db.Exec(`UPDATE models SET builtin_tools='["aivory_web_search"]', official_tools='[]' WHERE id=?`, model.ID); err != nil {
		t.Fatalf("configure small tool surface: %v", err)
	}
	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode: ToolModeAuto,
		UserText: "Explain dependency injection",
	})
	if provider.routeCalls != 0 || len(provider.taskRequests) != 0 {
		t.Fatalf("small declaration spent a classifier call: route=%d task=%d", provider.routeCalls, len(provider.taskRequests))
	}
	if len(provider.mainRequests) != 1 || !requestHasTool(provider.mainRequests[0], "aivory_web_search") {
		t.Fatalf("small declaration was not sent to the main model: %+v", provider.mainRequests)
	}
}

func TestAutoWithoutDedicatedRouteModelFailsOpenWithoutUsingTaskOrDefaultModel(t *testing.T) {
	orchestrator, provider, model, conv, logs, db := setupToolRouteTest(t)
	if err := store.SetSetting(db, "tool_route_model_id", ""); err != nil {
		t.Fatalf("clear tool route model: %v", err)
	}
	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode: ToolModeAuto,
		UserText: "Explain dependency injection",
	})
	if provider.routeCalls != 0 || len(provider.taskRequests) != 0 {
		t.Fatalf("unset dedicated model fell back to another model: route=%d task=%d", provider.routeCalls, len(provider.taskRequests))
	}
	if len(provider.mainRequests) != 1 || !provider.mainRequests[0].ToolsEnabled {
		t.Fatalf("unset dedicated model did not fail open: %+v", provider.mainRequests)
	}
	if !strings.Contains(logs.String(), "settings.tool_route_model_id is unset") {
		t.Fatalf("missing dedicated-model diagnostic: %s", logs.String())
	}
}

func TestToolRouteCapabilityNamesKeepHostedAndLocalNamespacesDistinct(t *testing.T) {
	local := []ToolDef{
		{Name: "aivory_web_search"},
		{Name: "python_execute"},
		{Name: "image_generate"},
	}
	hostedNames := []string{"web_search", "renamed_code", "image_generation", "maps_lookup"}
	hostedRequests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"web_search"}]}`),
		json.RawMessage(`{"tools":[{"type":"code_interpreter"}]}`),
		json.RawMessage(`{"tools":[{"type":"image_generation"}]}`),
		json.RawMessage(`{"tools":[{"type":"vendor_maps"}]}`),
	}

	capabilities, set := toolRouteCapabilities(local, hostedNames, hostedRequests)
	if got := strings.Join(capabilities, ","); got != "web,code,file,image,custom:maps_lookup" {
		t.Fatalf("capabilities = %q", got)
	}
	for _, capability := range []string{"web", "code", "file", "image", "custom:maps_lookup"} {
		if !set[capability] {
			t.Fatalf("capability set lost %q: %+v", capability, set)
		}
	}
	for _, leakedName := range []string{"web_search", "aivory_web_search", "python_execute", "code_interpreter", "image_generate", "image_generation"} {
		if set[leakedName] {
			t.Fatalf("raw tool name %q leaked into capability set: %+v", leakedName, set)
		}
	}
}

func TestToolRouteInputIsTokenBoundedAndKeepsBothEdges(t *testing.T) {
	input := strings.Repeat("始", 220) + "MIDDLE_MUST_BE_DROPPED" + strings.Repeat("终", 220)
	got := truncateToolRouteInput(input)
	if estimateTokens(got) > 256 {
		t.Fatalf("route input estimate = %d, want at most 256: %q", estimateTokens(got), got)
	}
	if !strings.HasPrefix(got, strings.Repeat("始", 20)) || !strings.HasSuffix(got, strings.Repeat("终", 20)) {
		t.Fatalf("route input did not preserve both edges: %q", got)
	}
	if strings.Contains(got, "MIDDLE_MUST_BE_DROPPED") {
		t.Fatalf("route input retained the discarded middle: %q", got)
	}
}

func TestExplicitToolModesSkipTaskClassifier(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantTools bool
	}{
		{mode: ToolModeDisabled, wantTools: false},
		{mode: ToolModeEnabled, wantTools: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			orchestrator, provider, model, conv, _, _ := setupToolRouteTest(t)
			runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: tc.mode})
			if provider.routeCalls != 0 {
				t.Fatalf("tool route calls = %d, want 0", provider.routeCalls)
			}
			gotTools := len(provider.mainRequests[0].Tools) > 0
			if gotTools != tc.wantTools {
				t.Fatalf("main tools present = %v, want %v", gotTools, tc.wantTools)
			}
		})
	}
}

func TestUnifiedToolModeUsesAllConfiguredToolsAndIgnoresLegacySelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		selected []string
	}{
		{
			name:     "enabled ignores legacy subset",
			mode:     ToolModeEnabled,
			selected: []string{"second"},
		},
		{name: "legacy official maps to enabled", mode: ToolModeOfficial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
			configured := `[
				{"name":"first","icon":"search","request":{"tools":[{"type":"hosted-first"}],"vendor":{"value":"first"}}},
				{"name":"second","icon":"terminal","request":{"tools":[{"type":"hosted-second"}],"vendor":{"value":"second"}}}
			]`
			if _, err := db.Exec(`UPDATE models SET official_tools=? WHERE id=?`, configured, model.ID); err != nil {
				t.Fatalf("configure official tools: %v", err)
			}

			runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
				ToolMode:          tc.mode,
				OfficialToolNames: tc.selected,
			})
			if provider.routeCalls != 0 {
				t.Fatalf("explicit mode called tool router %d times", provider.routeCalls)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatalf("main requests = %d, want 1", len(provider.mainRequests))
			}
			request := provider.mainRequests[0]
			if len(request.Tools) == 0 {
				t.Fatal("unified enabled mode did not expose local Function tools")
			}
			want := []string{"first", "second"}
			if strings.Join(request.OfficialToolNames, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("hosted names = %v, want complete admin order %v", request.OfficialToolNames, want)
			}
			if len(request.OfficialToolRequests) != len(want) {
				t.Fatalf("hosted requests = %d, want %d", len(request.OfficialToolRequests), len(want))
			}
			for index, name := range want {
				if !strings.Contains(string(request.OfficialToolRequests[index]), "hosted-"+name) {
					t.Fatalf("request %d does not match %q: %s", index, name, request.OfficialToolRequests[index])
				}
			}
			if !request.ToolsEnabled {
				t.Fatal("unified enabled policy was not retained on the provider request")
			}
		})
	}
}

func TestUnifiedToolModeCannotOverrideModelNonePolicy(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	configured := `[{"name":"hosted","icon":"search","request":{"tools":[{"type":"hosted-search"}]}}]`
	if _, err := db.Exec(`UPDATE models SET tool_mode='none', official_tools=? WHERE id=?`, configured, model.ID); err != nil {
		t.Fatalf("configure deny-all model: %v", err)
	}

	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode:          ToolModeEnabled,
		OfficialToolNames: []string{"hosted"},
	})
	if len(provider.mainRequests) != 1 {
		t.Fatalf("main requests = %d, want 1", len(provider.mainRequests))
	}
	request := provider.mainRequests[0]
	if request.ToolsEnabled || len(request.OfficialToolNames) != 0 || len(request.OfficialToolRequests) != 0 || len(request.Tools) != 0 {
		t.Fatalf("tool_mode=none exposed tools: enabled=%v names=%v requests=%s local=%v",
			request.ToolsEnabled, request.OfficialToolNames, request.OfficialToolRequests, request.Tools)
	}
	if request.SystemPromptOptions == nil || request.SystemPromptOptions.ToolMode != "none" {
		t.Fatalf("tool_mode=none prompt options = %+v", request.SystemPromptOptions)
	}
}

func TestAutoFalseUsesUnifiedNoToolsPipelineForBothToolCategories(t *testing.T) {
	ctx := context.Background()
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET official_tools=? WHERE id=?`, `[
		{"name":"configured","icon":"search","request":{"tools":[{"type":"hosted-search"}]}}
	]`, model.ID); err != nil {
		t.Fatalf("configure official tools: %v", err)
	}
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ConversationID: conv.ID,
		Filename:       "official-empty-context.txt",
		MimeType:       "text/plain",
		SizeBytes:      32,
		Status:         "ready",
	})
	if err != nil {
		t.Fatalf("create RAG document: %v", err)
	}
	const ragText = "official-empty-rag-marker"
	if err := store.CreateChunk(ctx, db, doc.ID, "", conv.ID, 0, ragText, ""); err != nil {
		t.Fatalf("create RAG chunk: %v", err)
	}
	orchestrator.rag = rag.New(db, nil, log.New(io.Discard, "", 0))

	skill, err := store.CreateSkill(ctx, db, store.Skill{
		ID:           "sk-official-empty",
		Name:         "official-empty-secret-skill",
		Description:  "must not be advertised on a no-tools turn",
		Instructions: "official-empty-secret-instructions",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if err := store.SetSkillsForModel(ctx, db, model.ID, []string{skill.ID}); err != nil {
		t.Fatalf("bind skill: %v", err)
	}

	previousUserBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "old question"}})
	previousUser, err := store.CreateMessage(ctx, db, store.Message{
		ConversationID: conv.ID,
		Role:           "user",
		Provider:       "openai",
		ModelID:        model.ID,
		Blocks:         previousUserBlocks,
	})
	if err != nil {
		t.Fatalf("create previous user message: %v", err)
	}
	previousAssistantBlocks, _ := json.Marshal([]UnifiedBlock{
		{Kind: "tool_call", ToolName: "legacy_disallowed_tool", ToolID: "legacy-call"},
		{Kind: "tool_output", ToolID: "legacy-call", Text: "legacy-tool-output"},
		{Kind: "text", Text: "ordinary-prior-answer"},
	})
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ConversationID: conv.ID,
		ParentID:       previousUser.ID,
		Role:           "assistant",
		Provider:       "openai",
		ModelID:        model.ID,
		Blocks:         previousAssistantBlocks,
		Raw:            json.RawMessage(`[{"type":"function_call","name":"legacy_disallowed_tool"}]`),
	}); err != nil {
		t.Fatalf("create previous assistant message: %v", err)
	}

	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode:          ToolModeAuto,
		OfficialToolNames: []string{"stale"},
		UserText:          "Use the attached context",
	})
	if len(provider.mainRequests) != 1 {
		t.Fatalf("main requests = %d, want 1", len(provider.mainRequests))
	}
	request := provider.mainRequests[0]
	if request.ToolsEnabled || request.ToolModePrompt || len(request.Tools) != 0 ||
		len(request.OfficialToolNames) != 0 || len(request.OfficialToolRequests) != 0 {
		t.Fatalf("auto=false exposed tools: enabled=%v prompt=%v local=%+v names=%v requests=%s",
			request.ToolsEnabled, request.ToolModePrompt, request.Tools, request.OfficialToolNames, request.OfficialToolRequests)
	}
	if request.SystemPromptOptions == nil || request.SystemPromptOptions.ToolMode != "none" || request.SystemPromptOptions.SkillsAllowed {
		t.Fatalf("auto=false did not use no-tools prompt options: %+v", request.SystemPromptOptions)
	}
	for _, forbidden := range []string{"official-empty-secret-skill", "official-empty-secret-instructions"} {
		if strings.Contains(request.SystemPrompt, forbidden) {
			t.Fatalf("no-tools prompt leaked skill %q:\n%s", forbidden, request.SystemPrompt)
		}
	}
	if len(request.RAGSnippets) != 1 || !strings.Contains(request.RAGSnippets[0].Snippet, ragText) {
		t.Fatalf("automatic RAG was disabled with the tool surface: %+v", request.RAGSnippets)
	}
	historyJSON, _ := json.Marshal(request.History)
	for _, forbidden := range []string{"legacy_disallowed_tool", "legacy-tool-output"} {
		if strings.Contains(string(historyJSON), forbidden) {
			t.Fatalf("no-tools history retained %q: %s", forbidden, historyJSON)
		}
	}
	if !strings.Contains(string(historyJSON), "ordinary-prior-answer") || !strings.Contains(string(historyJSON), ragText) {
		t.Fatalf("no-tools history lost ordinary answer or inline RAG context: %s", historyJSON)
	}
}

func TestUnifiedToolFallbackRebuildsFallbackModelConfiguration(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID,
		Kind:      "chat",
		RequestID: "official-fallback",
		Label:     "Official fallback",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
		OfficialTools: json.RawMessage(`[
			{"name":"second","icon":"terminal","request":{"tools":[{"type":"fallback-second"}]}},
			{"name":"third","icon":"image","request":{"tools":[{"type":"fallback-third"}]}}
		]`),
	})
	if err != nil {
		t.Fatalf("create fallback model: %v", err)
	}
	base := UnifiedChatRequest{
		UserID:               "u1",
		ToolsEnabled:         true,
		Tools:                []ToolDef{{Name: "must_not_survive"}},
		OfficialToolNames:    []string{"first", "second"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"primary-first"}]}`), json.RawMessage(`{"tools":[{"type":"primary-second"}]}`)},
	}

	got, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatalf("build fallback request: %v", err)
	}
	if !got.ToolsEnabled || len(got.Tools) == 0 || toolDefsContain(got.Tools, "must_not_survive") {
		t.Fatalf("fallback did not rebuild its local tools: enabled=%v tools=%+v", got.ToolsEnabled, got.Tools)
	}
	if strings.Join(got.OfficialToolNames, "\x00") != "second\x00third" {
		t.Fatalf("fallback hosted names = %v, want [second third]", got.OfficialToolNames)
	}
	if len(got.OfficialToolRequests) != 2 || !strings.Contains(string(got.OfficialToolRequests[0]), "fallback-second") ||
		!strings.Contains(string(got.OfficialToolRequests[1]), "fallback-third") {
		t.Fatalf("fallback requests did not use the complete fallback model definition: %s", got.OfficialToolRequests)
	}
}

func TestNativeHistoryCompatibilityAcrossTTFTFallback(t *testing.T) {
	primary := ModelInfo{Provider: "openai", APIFormat: "chat"}
	tests := []struct {
		name             string
		primary          ModelInfo
		fallbackProvider string
		fallbackFormat   string
		want             bool
	}{
		{name: "same openai chat wire", primary: primary, fallbackProvider: "openai", fallbackFormat: "chat", want: true},
		{name: "legacy empty openai format is chat", primary: ModelInfo{Provider: "openai"}, fallbackProvider: "openai", fallbackFormat: "chat", want: true},
		{name: "openai chat to responses", primary: primary, fallbackProvider: "openai", fallbackFormat: "responses", want: false},
		{name: "cross provider", primary: primary, fallbackProvider: "anthropic", want: false},
		{name: "provider aliases same family", primary: ModelInfo{Provider: "claude"}, fallbackProvider: "anthropic", want: true},
		{name: "unknown primary is fail closed", primary: ModelInfo{}, fallbackProvider: "openai", fallbackFormat: "chat", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeHistoryCompatible(tc.primary, tc.fallbackProvider, tc.fallbackFormat); got != tc.want {
				t.Fatalf("nativeHistoryCompatible(%+v, %q, %q) = %v, want %v", tc.primary, tc.fallbackProvider, tc.fallbackFormat, got, tc.want)
			}
		})
	}
}

func TestNativeHistoryModelCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name              string
		primary, fallback string
		want              bool
	}{
		{name: "same model", primary: "model-a", fallback: "model-a", want: true},
		{name: "different models", primary: "model-a", fallback: "model-b", want: false},
		{name: "missing primary", fallback: "model-b", want: false},
		{name: "missing fallback", primary: "model-a", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeHistoryModelCompatible(tc.primary, tc.fallback); got != tc.want {
				t.Fatalf("nativeHistoryModelCompatible(%q, %q) = %v, want %v", tc.primary, tc.fallback, got, tc.want)
			}
		})
	}
}

func TestNativeRawForPersistedModelDropsTTFTFallbackExchange(t *testing.T) {
	raw := json.RawMessage(`[{"type":"reasoning","encrypted_content":"secret"}]`)
	if got := nativeRawForPersistedModel(raw, ""); string(got) != string(raw) {
		t.Fatalf("primary-model raw = %s, want %s", got, raw)
	}
	if got := nativeRawForPersistedModel(raw, "Fallback model"); len(got) != 0 {
		t.Fatalf("TTFT fallback raw was retained: %s", got)
	}
}

func TestNativeRawForPersistedModelKeepsPromptToolEnvelopeOnTTFTFallback(t *testing.T) {
	raw := marshalPromptToolRawEnvelope([]promptToolRawOutput{{
		Name: "paper_lookup", ID: "pt_0", Output: "complete result", Status: "complete",
	}})
	if len(raw) == 0 {
		t.Fatal("marshalPromptToolRawEnvelope returned empty Raw")
	}
	got := nativeRawForPersistedModel(raw, "Fallback model")
	if string(got) != string(raw) {
		t.Fatalf("prompt tool Raw changed on TTFT fallback: got=%s want=%s", got, raw)
	}
}

func TestBuildFallbackRequestDropsIncompatibleNativeRaw(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	responsesChannel, err := store.CreateChannel(context.Background(), db, "Responses fallback", "openai", "responses", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: responsesChannel.ID, Kind: "chat", RequestID: "responses-fallback", Label: "Responses fallback",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native"}]}]`)
	base := UnifiedChatRequest{
		Model:   ModelInfo{Provider: "openai", APIFormat: "chat"},
		History: []UnifiedMessage{{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "canonical"}}, Raw: raw}},
	}
	// The fixture's primary model is OpenAI Chat; make the metadata explicit so
	// the compatibility gate is exercising the same values used in production.
	base.Model.ID = model.ID
	got, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 1 || len(got.History[0].Raw) != 0 {
		t.Fatalf("incompatible OpenAI Responses fallback replayed primary Raw: %+v", got.History)
	}
	if got.History[0].Blocks[0].Text != "canonical" {
		t.Fatalf("fallback lost canonical history while dropping Raw: %+v", got.History[0].Blocks)
	}
}

func TestBuildFallbackRequestDropsNativeRawWhenModelChangesOnSameResponsesChannel(t *testing.T) {
	orchestrator, _, primary, _, _, db := setupToolRouteTest(t)
	responsesChannel, err := store.CreateChannel(context.Background(), db, "Responses same-channel fallback", "openai", "responses", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: responsesChannel.ID, Kind: "chat", RequestID: "responses-fallback-model", Label: "Responses fallback model",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`[{"type":"reasoning","encrypted_content":"model-a-secret"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"native"}]}]`)
	base := UnifiedChatRequest{
		Model:   ModelInfo{ID: primary.ID, Provider: "openai", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "canonical"}}, Raw: raw}},
	}

	got, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 1 || len(got.History[0].Raw) != 0 {
		t.Fatalf("same-channel model switch replayed encrypted native raw: %+v", got.History)
	}
	if got.History[0].Blocks[0].Text != "canonical" {
		t.Fatalf("canonical history was lost: %+v", got.History[0].Blocks)
	}
}

func TestUnifiedToolFallbackCannotOverrideFallbackNonePolicy(t *testing.T) {
	orchestrator, _, model, _, _, db := setupToolRouteTest(t)
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID,
		Kind:      "chat",
		RequestID: "official-fallback-none",
		Label:     "Official fallback none",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "none",
		OfficialTools: json.RawMessage(`[
			{"name":"hosted","icon":"search","request":{"tools":[{"type":"fallback-hosted"}]}}
		]`),
	})
	if err != nil {
		t.Fatalf("create fallback model: %v", err)
	}
	base := UnifiedChatRequest{
		UserID:               "u1",
		ToolsEnabled:         true,
		OfficialToolNames:    []string{"hosted"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"primary-hosted"}]}`)},
	}

	got, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatalf("build fallback request: %v", err)
	}
	if got.ToolsEnabled || len(got.OfficialToolNames) != 0 || len(got.OfficialToolRequests) != 0 || len(got.Tools) != 0 {
		t.Fatalf("fallback tool_mode=none exposed tools: enabled=%v names=%v requests=%s local=%v",
			got.ToolsEnabled, got.OfficialToolNames, got.OfficialToolRequests, got.Tools)
	}
	if got.SystemPromptOptions != nil && got.SystemPromptOptions.ToolMode != "none" {
		t.Fatalf("fallback tool_mode=none prompt options = %+v", got.SystemPromptOptions)
	}
}

func TestUnifiedToolModeExecutesLocalFunctionAlongsideHostedTools(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET official_tools='["web_search","code_interpreter"]' WHERE id=?`, model.ID); err != nil {
		t.Fatalf("configure official tool: %v", err)
	}
	provider.invokeTool = "aivory_web_search"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode:          ToolModeEnabled,
		OfficialToolNames: []string{"stale-user-selection"},
	})
	if provider.toolRunErr != nil {
		t.Fatalf("local Function call failed while hosted tools were present: %v", provider.toolRunErr)
	}
	request := provider.mainRequests[0]
	if !requestHasTool(request, "aivory_web_search") || !requestHasTool(request, "web_search") || !requestHasTool(request, "code_interpreter") {
		t.Fatalf("unified request did not contain both categories: local=%+v hosted=%v", request.Tools, request.OfficialToolNames)
	}
}

func TestAutoSpreadsheetUsesServerFilenameAndSkipsClassifier(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	path := filepath.Join(t.TempDir(), "legacy.DATA.CSV")
	if err := os.WriteFile(path, []byte("name,value\na,1\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, err := store.CreateFile(context.Background(), db, store.File{
		ID: "f1", UserID: "u1", ConversationID: conv.ID, Filename: "legacy.DATA.CSV",
		MimeType: "text/csv", Kind: "text", StoragePath: path,
	}); err != nil {
		t.Fatalf("create legacy file: %v", err)
	}
	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode: ToolModeAuto,
		UserText: "Analyze the uploaded data",
		Attachments: []Attachment{{
			ID: "f1", Filename: "legacy.DATA.CSV", MimeType: "text/csv", Kind: "text",
		}},
	})
	if provider.routeCalls != 0 {
		t.Fatalf("spreadsheet should bypass classifier, calls=%d", provider.routeCalls)
	}
	if !requestHasTool(provider.mainRequests[0], "python_execute") {
		t.Fatalf("spreadsheet auto turn did not enable python_execute: %+v", provider.mainRequests[0].Tools)
	}
}

func TestFastAndDeepResearchSkipToolClassifier(t *testing.T) {
	t.Run("fast", func(t *testing.T) {
		orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
		if err := store.SetFastModel(context.Background(), db, model.ID); err != nil {
			t.Fatalf("set fast model: %v", err)
		}
		runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, Fast: true})
		if provider.routeCalls != 0 {
			t.Fatalf("fast route calls = %d, want 0", provider.routeCalls)
		}
		if requestHasTool(provider.mainRequests[0], "python_execute") {
			t.Fatal("fast request exposed python_execute")
		}
		if !requestHasTool(provider.mainRequests[0], "aivory_web_search") {
			t.Fatalf("fast request lost non-Python tools: tools=%+v official=%v", provider.mainRequests[0].Tools, provider.mainRequests[0].OfficialToolNames)
		}
	})

	t.Run("deep research", func(t *testing.T) {
		orchestrator, provider, model, conv, _, _ := setupToolRouteTest(t)
		runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, Mode: ModeDeepResearch, UserText: "Research this topic"})
		if provider.routeCalls != 0 {
			t.Fatalf("deep research route calls = %d, want 0", provider.routeCalls)
		}
	})
}

func TestToolRoutePromptUsesOnlyCompactCurrentTurnSignals(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	fallbackChannel, err := store.CreateChannel(context.Background(), db, "Route fallback", "openai", "chat", "https://fallback.invalid", "fallback-key")
	if err != nil {
		t.Fatalf("create route fallback channel: %v", err)
	}
	if _, err := db.Exec(`UPDATE models SET fallback_channel_id=?, extra_params=? WHERE request_id='task-route-test'`,
		fallbackChannel.ID, `{"temperature":0.9,"reasoning":{"effort":"high"}}`); err != nil {
		t.Fatalf("configure task model fallback/extra params: %v", err)
	}
	if err := store.SetSetting(db, "disabled_tools", []string{"python_execute"}); err != nil {
		t.Fatalf("disable python: %v", err)
	}
	skill, err := store.CreateSkill(context.Background(), db, store.Skill{
		ID: "sk1", Name: "release-notes", Description: "Use for producing versioned release notes.",
		Instructions: "PRIVATE_FULL_SKILL_INSTRUCTIONS", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if err := store.SetSkillsForModel(context.Background(), db, model.ID, []string{skill.ID}); err != nil {
		t.Fatalf("bind skill: %v", err)
	}
	if _, err := store.CreateFile(context.Background(), db, store.File{
		ID: "route-secret-file", UserID: "u1", ConversationID: conv.ID,
		Filename: "ROUTE_SECRET_FILE.py", MimeType: "text/x-python", Kind: "code", StoragePath: filepath.Join(t.TempDir(), "route-secret.py"),
	}); err != nil {
		t.Fatalf("create staged route file: %v", err)
	}
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode: ToolModeDisabled,
		UserText: "HISTORY_SECRET_MUST_NOT_REACH_ROUTER",
	})
	provider.mainRequests = nil
	provider.routeResponse = "0"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "Prepare version two"})
	if len(provider.taskRequests) != 1 || len(provider.taskRequests[0].History) != 1 {
		t.Fatalf("unexpected task requests: %+v", provider.taskRequests)
	}
	request := provider.taskRequests[0]
	prompt := request.History[0].Blocks[0].Text
	for _, want := range []string{"CAP=web,skill", "ATT=none", "FILES=1", "INPUT=Prepare version two"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("tool-route prompt missing %q: %s", want, prompt)
		}
	}
	for _, absent := range []string{
		"python_execute", "aivory_web_search", "use_skill", "release-notes",
		"Use for producing versioned release notes", "PRIVATE_FULL_SKILL_INSTRUCTIONS",
		"HISTORY_SECRET_MUST_NOT_REACH_ROUTER", "ROUTE_SECRET_FILE.py", "route-secret.py",
	} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("tool-route prompt leaked unavailable/private value %q: %s", absent, prompt)
		}
	}
	if request.MaxOutputTokens != toolRouteMaxOutputTokens || string(request.ExtraParams) != `{"temperature":0}` || request.Model.Fallback != nil {
		t.Fatalf("tool route request was not latency constrained: max=%d extra=%s fallback=%+v", request.MaxOutputTokens, request.ExtraParams, request.Model.Fallback)
	}

	// An exact enabled skill name is a deterministic positive signal and must not
	// spend a second classifier round trip.
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "Use release-notes for v2"})
	if provider.routeCalls != 1 || len(provider.taskRequests) != 1 {
		t.Fatalf("exact skill mention invoked classifier again: route=%d task=%d", provider.routeCalls, len(provider.taskRequests))
	}
	if !provider.mainRequests[len(provider.mainRequests)-1].ToolsEnabled {
		t.Fatal("exact skill mention did not enable the configured tools")
	}
}

func TestToolRouteUsagePurposeIsPinnedAndCountedInTurnCost(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET price_input=1000000, price_output=1000000 WHERE request_id='task-route-test'`); err != nil {
		t.Fatalf("set task pricing: %v", err)
	}
	provider.routeResponse = "1"
	result, err := orchestrator.Run(context.Background(), RunRequest{
		UserID: "u1", ConversationID: conv.ID, ModelID: model.ID,
		UserText: "Search for this", ToolMode: ToolModeAuto,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var purpose, messageID string
	var taskCost float64
	if err := db.QueryRow(`SELECT purpose, message_id, cost FROM usage_logs WHERE purpose='task.tool_route' LIMIT 1`).Scan(&purpose, &messageID, &taskCost); err != nil {
		t.Fatalf("load tool-route usage: %v", err)
	}
	if purpose != string(TaskToolRoute) || messageID != result.AssistantMessage.ID {
		t.Fatalf("usage purpose/message = %q/%q, want %q/%q", purpose, messageID, TaskToolRoute, result.AssistantMessage.ID)
	}
	stored, err := store.GetMessage(context.Background(), db, result.AssistantMessage.ID)
	if err != nil {
		t.Fatalf("load assistant: %v", err)
	}
	if taskCost <= 0 || stored.Cost < taskCost {
		t.Fatalf("tool-route cost not counted: usage=%f message=%f", taskCost, stored.Cost)
	}
}

func requestHasTool(req UnifiedChatRequest, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	for _, tool := range req.OfficialToolNames {
		if tool == name {
			return true
		}
	}
	return false
}
