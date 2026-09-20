package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
)

type searchOnlyTestRunner struct {
	calls atomic.Int32
	err   error
	empty bool
}

func (r *searchOnlyTestRunner) Run(_ context.Context, _ string, input []byte) (string, []Citation, error) {
	r.calls.Add(1)
	var args struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal(input, &args); err != nil || len(args.Queries) != 2 {
		return "", nil, errors.New("batch search arguments were lost")
	}
	if r.empty {
		return "", nil, nil
	}
	if r.err != nil {
		return "", nil, r.err
	}
	return "[1] Search evidence for the answer", []Citation{{Index: 1, URL: "https://example.com/source", Source: "web"}}, nil
}

func TestSearchOnlyProvidersFinishAfterThreeCalls(t *testing.T) {
	const arguments = `{"queries":["first fact","second fact"]}`
	cases := []struct {
		name     string
		provider Provider
		format   string
		toolSSE  string
		textSSE  func(string) string
	}{
		{
			name: "openai-chat", provider: &OpenAIProvider{}, format: "chat",
			toolSSE: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"aivory_web_search","arguments":` + mustJSON(arguments) + `}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n",
			textSSE: func(s string) string {
				return `data: {"choices":[{"delta":{"content":` + mustJSON(s) + `},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
			},
		},
		{
			name: "openai-responses", provider: &OpenAIProvider{}, format: "responses",
			toolSSE: `data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"aivory_web_search","arguments":` + mustJSON(arguments) + `}}` + "\n\n" +
				`data: {"type":"response.completed","response":{"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"aivory_web_search","arguments":` + mustJSON(arguments) + `}]}}` + "\n\n",
			textSSE: func(s string) string {
				return `data: {"type":"response.output_text.delta","delta":` + mustJSON(s) + `}` + "\n\n" +
					`data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + mustJSON(s) + `}]}]}}` + "\n\n"
			},
		},
		{
			name: "anthropic", provider: &AnthropicProvider{},
			toolSSE: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"aivory_web_search","input":` + arguments + `}}` + "\n\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			textSSE: anthropicTextStream,
		},
		{
			name: "google", provider: &GoogleProvider{},
			toolSSE: `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"aivory_web_search","args":` + arguments + `}}]},"finishReason":"STOP"}]}` + "\n\n",
			textSSE: geminiTextStream,
		},
	}
	for _, tc := range cases {
		for _, mode := range []string{"native", "prompt", "full-tools"} {
			for _, outcome := range []string{"success", "failure", "empty", "early-answer"} {
				failed := outcome == "failure"
				empty := outcome == "empty"
				early := outcome == "early-answer"
				name := tc.name + "/" + mode + "/" + outcome
				if failed {
					name += "/failed-search"
				}
				t.Run(name, func(t *testing.T) {
					searchOnly := mode != "full-tools"
					toolRounds := 3
					if !searchOnly {
						toolRounds = 4
					}
					if early {
						toolRounds = 1
					}
					var requests []map[string]any
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests = append(requests, decodeBudgetTestRequest(t, r))
						roundArguments := strings.ReplaceAll(arguments, "fact", fmt.Sprintf("fact round %d", len(requests)))
						roundSSE := strings.ReplaceAll(tc.toolSSE, "fact", fmt.Sprintf("fact round %d", len(requests)))
						if tc.name == "google" && mode == "prompt" {
							text := "answer from the available evidence"
							if len(requests) <= toolRounds {
								text = `<tool_call>{"name":"aivory_web_search","arguments":` + roundArguments + `}</tool_call>`
							}
							w.Header().Set("content-type", "application/json")
							_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":`+mustJSON(text)+`}]},"finishReason":"STOP"}]}`)
							return
						}
						w.Header().Set("content-type", "text/event-stream")
						if len(requests) <= toolRounds {
							if mode == "prompt" {
								_, _ = io.WriteString(w, tc.textSSE(`<tool_call>{"name":"aivory_web_search","arguments":`+roundArguments+`}</tool_call>`))
							} else {
								_, _ = io.WriteString(w, roundSSE)
							}
							return
						}
						_, _ = io.WriteString(w, tc.textSSE("answer from the available evidence"))
					}))
					defer server.Close()
					runner := &searchOnlyTestRunner{empty: empty}
					if failed {
						runner.err = errors.New("search unavailable")
					}
					req := budgetTestRequest(ModelInfo{RequestID: "test-model", BaseURL: server.URL, APIKey: "k", APIFormat: tc.format})
					req.SearchOnly = searchOnly
					req.ToolModePrompt = mode == "prompt"
					req.OfficialToolRequests = nil
					req.Tools[0].Name = "aivory_web_search"
					var events []SseEvent
					result, err := tc.provider.Stream(context.Background(), req, runner, func(e SseEvent) { events = append(events, e) })
					if err != nil {
						t.Fatal(err)
					}
					if len(requests) != toolRounds+1 || runner.calls.Load() != int32(toolRounds) {
						t.Fatalf("model/tool calls = %d/%d, want %d/%d", len(requests), runner.calls.Load(), toolRounds+1, toolRounds)
					}
					if searchOnly && !early {
						assertToolFieldsRemoved(t, requests[toolRounds])
						if !strings.Contains(mustJSON(requests[toolRounds]), "The search allowance is complete") {
							t.Fatal("answer request did not receive completion instruction")
						}
					} else if mode != "prompt" && requests[1]["tools"] == nil {
						t.Fatal("full-tools mode lost its second tool round")
					}
					if unifiedResultText(result) != "answer from the available evidence" {
						t.Fatalf("answer = %q", unifiedResultText(result))
					}
					if !failed && !empty && len(result.Citations) == 0 {
						t.Fatal("search citations were lost")
					}
					for _, event := range events {
						if event.Type == "tool_result" && !failed && event.Status != "complete" {
							t.Fatalf("successful search appeared as an error: %+v", event)
						}
					}
				})
			}
		}
	}
}

func TestSearchOnlyFallbackKeepsScope(t *testing.T) {
	o, _, model, _, _, db := setupToolRouteTest(t)
	fallback, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: model.ChannelID, Kind: "chat", RequestID: "fallback", Label: "Fallback", Enabled: true, Stream: true, ToolMode: "native",
		OfficialTools: json.RawMessage(`[{"name":"hosted","request":{"tools":[{"type":"web_search"}]}}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := UnifiedChatRequest{UserID: "u1", ToolsEnabled: true, SearchOnly: true,
		SystemPromptOptions: &systemPromptOpts{SearchOnly: true, SkillsAllowed: true},
	}
	got, _, _, err := o.buildFallbackRequest(context.Background(), base, fallback.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SearchOnly || len(got.Tools) != 1 || got.Tools[0].Name != "aivory_web_search" || len(got.OfficialToolRequests) != 0 || got.SystemPromptOptions.SkillsAllowed {
		t.Fatal("fallback broadened search-only permissions")
	}
}

func TestSearchOnlyHonorsPermissionsAndDoesNotGenerateQueries(t *testing.T) {
	for _, mode := range []string{ToolModeDisabled, ToolModeAuto} {
		for _, deny := range []string{"", "model", "global", "group", "selection", "none"} {
			t.Run(mode+"/"+deny, func(t *testing.T) {
				o, provider, model, conv, _, db := setupToolRouteTest(t)
				provider.routeResponse = "0"
				req := RunRequest{ToolMode: mode, ForceWebSearch: true}
				switch deny {
				case "model":
					if _, err := db.Exec(`UPDATE models SET builtin_tools='["python_execute"]' WHERE id=?`, model.ID); err != nil {
						t.Fatal(err)
					}
				case "global":
					if err := store.SetSetting(db, "disabled_tools", []string{"aivory_web_search"}); err != nil {
						t.Fatal(err)
					}
				case "group":
					req.ToolAccessPolicy = &ToolAccessPolicy{Mode: "selected", IDs: []string{"builtin:python_execute"}}
				case "selection":
					req.SelectedToolsConfigured = true
					req.SelectedToolIDs = []string{"builtin:python_execute"}
				case "none":
					if _, err := db.Exec(`UPDATE models SET tool_mode='none' WHERE id=?`, model.ID); err != nil {
						t.Fatal(err)
					}
				}
				runToolRouteTurn(t, o, model.ID, conv.ID, req)
				got := provider.mainRequests[0]
				if deny == "" {
					if !got.SearchOnly || len(got.Tools) != 1 || got.Tools[0].Name != "aivory_web_search" {
						t.Fatal("search-only mode did not retain search")
					}
				} else if len(got.Tools) != 0 || len(got.OfficialToolRequests) != 0 {
					t.Fatal("search-only mode bypassed a tool restriction")
				}
				if len(provider.taskRequests) != provider.routeCalls {
					t.Fatal("search-only mode added a query-generation model call")
				}
				if mode == ToolModeDisabled && provider.routeCalls != 0 {
					t.Fatal("explicit disabled mode added a routing call")
				}
			})
		}
	}
}

func TestSearchOnlyConcurrentCallsShareLimitAcrossBatches(t *testing.T) {
	ctx := contextWithSearchOnly(context.Background(), true)
	runner := &searchOnlyTestRunner{}
	call := toolCallSpec{Name: "aivory_web_search", Input: json.RawMessage(`{"queries":["a","b"]}`)}
	first := runToolsConcurrent(ctx, runner, []toolCallSpec{call, call}, func(SseEvent) {})
	if signal := toolBatchFinalization(ctx, first); signal != nil {
		t.Fatalf("stopped before third call: %v", signal)
	}
	// Re-entering a provider with the same turn context must not reset its budget.
	ctx = contextWithSearchOnly(ctx, true)
	second := runToolsConcurrent(ctx, runner, []toolCallSpec{call, call, call}, func(SseEvent) {})
	if runner.calls.Load() != 3 || second[0].Err != nil || !errors.Is(second[1].Err, errSearchRoundComplete) || !errors.Is(second[2].Err, errSearchRoundComplete) {
		t.Fatalf("calls=%d results=%+v", runner.calls.Load(), second)
	}
	if !errors.Is(toolBatchFinalization(ctx, second), errSearchRoundComplete) {
		t.Fatal("exhausted budget did not finalize")
	}
	fresh := contextWithSearchOnly(context.Background(), true)
	runToolsConcurrent(fresh, runner, []toolCallSpec{call}, func(SseEvent) {})
	if runner.calls.Load() != 4 || toolBatchFinalization(fresh, first) != nil {
		t.Fatal("new turn inherited old budget")
	}
}

func TestSearchOnlyAllowsRefiningEmptyResultsButStopsDuplicates(t *testing.T) {
	ctx := contextWithSearchOnly(context.Background(), true)
	tc := &ToolContext{}
	for i := 0; i < 3; i++ {
		if !reserveSearchOnlyCall(ctx) {
			t.Fatal("search rejected before limit")
		}
		output, citations, err := tc.executeTrackedTool(ctx, "aivory_web_search", []byte(fmt.Sprintf(`{"query":"refined query %d"}`, i)), func() (string, []Citation, error) { return "", nil, nil })
		var noProgress *ErrToolNoProgress
		if !errors.As(err, &noProgress) || noProgress.Kind != "no_new_evidence" {
			t.Fatalf("expected empty evidence: %v", err)
		}
		signal := toolBatchFinalization(ctx, []toolCallResult{{Output: output, Citations: citations, Err: err}})
		if i < 2 && signal != nil {
			t.Fatalf("empty result stopped refinement: %v", signal)
		}
		if i == 2 && !errors.Is(signal, errSearchRoundComplete) {
			t.Fatal("third empty result did not stop")
		}
	}
	ctx = contextWithSearchOnly(context.Background(), true)
	duplicate := &ErrToolNoProgress{Kind: "duplicate_request"}
	if toolBatchFinalization(ctx, []toolCallResult{{Err: duplicate}}) != duplicate {
		t.Fatal("duplicate protection lost")
	}
	for i := 0; i < 3; i++ {
		reserveSearchOnlyCall(ctx)
	}
	hardLimit := &ErrToolBudgetExceeded{Kind: "time"}
	if toolBatchFinalization(ctx, []toolCallResult{{Err: hardLimit}}) != hardLimit {
		t.Fatal("hard budget was masked")
	}
}
