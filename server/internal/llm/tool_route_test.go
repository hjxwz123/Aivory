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
		case strings.Contains(req.SystemPrompt, "AVAILABLE tools"):
			p.routeCalls++
			if p.routeErr != nil {
				return nil, p.routeErr
			}
			output = p.routeResponse
			if output == "" {
				output = `{"use_tools":true}`
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
	return []ToolDef{
		{Name: "python_execute", Description: "Run Python for calculations and spreadsheet analysis."},
		{Name: "use_skill", Description: "Load one of the model's configured skills."},
		{Name: "web_search", Description: "Search the public web for current information."},
	}
}

func (toolRouteTestTools) Run(_ context.Context, name string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	switch name {
	case "web_search":
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
		{name: "yes", response: `{"use_tools":true}`, wantTools: true},
		{name: "no", response: `{"use_tools":false}`, wantTools: false},
		{name: "missing field fails open", response: `{}`, wantTools: true, wantFailLog: true},
		{name: "invalid json fails open", response: `not-json`, wantTools: true, wantFailLog: true},
		{name: "provider failure fails open", routeErr: errors.New("task backend unavailable"), wantTools: true, wantFailLog: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orchestrator, provider, model, conv, logs, _ := setupToolRouteTest(t)
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
			if tc.wantFailLog && !strings.Contains(logs.String(), "tool route: decision failed, enabling tools") {
				t.Fatalf("missing fail-open log: %s", logs.String())
			}
		})
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

func TestOfficialToolModeFiltersSelectionAndDisablesSystemTools(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
		want     []string
	}{
		{
			name:     "configured subset in model order",
			selected: []string{"second", "missing", "first", "second"},
			want:     []string{"first", "second"},
		},
		{name: "empty selection means no tools"},
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
				ToolMode:          ToolModeOfficial,
				OfficialToolNames: tc.selected,
			})
			if provider.routeCalls != 0 {
				t.Fatalf("official mode called tool router %d times", provider.routeCalls)
			}
			if len(provider.mainRequests) != 1 {
				t.Fatalf("main requests = %d, want 1", len(provider.mainRequests))
			}
			request := provider.mainRequests[0]
			if !request.ToolModeOfficial {
				t.Fatal("provider request lost official-mode state")
			}
			if len(request.Tools) != 0 {
				t.Fatalf("official mode exposed system tools: %+v", request.Tools)
			}
			if strings.Join(request.OfficialToolNames, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("official names = %v, want %v", request.OfficialToolNames, tc.want)
			}
			if len(request.OfficialToolRequests) != len(tc.want) {
				t.Fatalf("official requests = %d, want %d", len(request.OfficialToolRequests), len(tc.want))
			}
			for index, name := range tc.want {
				if !strings.Contains(string(request.OfficialToolRequests[index]), "hosted-"+name) {
					t.Fatalf("request %d does not match %q: %s", index, name, request.OfficialToolRequests[index])
				}
			}
		})
	}
}

func TestOfficialToolFallbackReappliesFallbackModelAllowlist(t *testing.T) {
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
		Tools:                []ToolDef{{Name: "must_not_survive"}},
		OfficialToolNames:    []string{"first", "second"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"primary-first"}]}`), json.RawMessage(`{"tools":[{"type":"primary-second"}]}`)},
		ToolModeOfficial:     true,
	}

	got, _, _, err := orchestrator.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatalf("build fallback request: %v", err)
	}
	if !got.ToolModeOfficial || len(got.Tools) != 0 {
		t.Fatalf("fallback changed official mode or retained system tools: mode=%v tools=%+v", got.ToolModeOfficial, got.Tools)
	}
	if len(got.OfficialToolNames) != 1 || got.OfficialToolNames[0] != "second" {
		t.Fatalf("fallback official names = %v, want [second]", got.OfficialToolNames)
	}
	if len(got.OfficialToolRequests) != 1 || !strings.Contains(string(got.OfficialToolRequests[0]), "fallback-second") {
		t.Fatalf("fallback request did not use fallback model definition: %s", got.OfficialToolRequests)
	}
}

func TestOfficialToolModeCannotExecuteSystemToolRunner(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET official_tools='["web_search"]' WHERE id=?`, model.ID); err != nil {
		t.Fatalf("configure official tool: %v", err)
	}
	provider.invokeTool = "web_search"
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{
		ToolMode:          ToolModeOfficial,
		OfficialToolNames: []string{"web_search"},
	})
	if provider.toolRunErr == nil || !strings.Contains(provider.toolRunErr.Error(), "unavailable in official mode") {
		t.Fatalf("official provider reached system tool runner: %v", provider.toolRunErr)
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
	provider.routeResponse = `{"use_tools":false}`
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "Analyze the uploaded data"})
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
		if !requestHasTool(provider.mainRequests[0], "web_search") {
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

func TestToolRoutePromptIncludesActualToolsAndSkillMetadata(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
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
	provider.routeResponse = `{"use_tools":false}`
	runToolRouteTurn(t, orchestrator, model.ID, conv.ID, RunRequest{ToolMode: ToolModeAuto, UserText: "Use release-notes for v2"})
	if len(provider.taskRequests) != 1 || len(provider.taskRequests[0].History) != 1 {
		t.Fatalf("unexpected task requests: %+v", provider.taskRequests)
	}
	prompt := provider.taskRequests[0].History[0].Blocks[0].Text
	for _, want := range []string{"Use release-notes for v2", "web_search", "skill:release-notes", "Use for producing versioned release notes"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("tool-route prompt missing %q: %s", want, prompt)
		}
	}
	for _, absent := range []string{"python_execute", "PRIVATE_FULL_SKILL_INSTRUCTIONS"} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("tool-route prompt leaked unavailable/private value %q: %s", absent, prompt)
		}
	}
}

func TestToolRouteUsagePurposeIsPinnedAndCountedInTurnCost(t *testing.T) {
	orchestrator, provider, model, conv, _, db := setupToolRouteTest(t)
	if _, err := db.Exec(`UPDATE models SET price_input=1000000, price_output=1000000 WHERE request_id='task-route-test'`); err != nil {
		t.Fatalf("set task pricing: %v", err)
	}
	provider.routeResponse = `{"use_tools":true}`
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
