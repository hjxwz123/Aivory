package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type budgetExceededToolRunner struct {
	calls atomic.Int32
}

func (r *budgetExceededToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	r.calls.Add(1)
	return "", nil, &ErrToolBudgetExceeded{Kind: "total_calls", Limit: 1}
}

type mixedBudgetToolRunner struct {
	calls atomic.Int32
}

type noProgressToolRunner struct {
	calls atomic.Int32
}

func (r *noProgressToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	r.calls.Add(1)
	return "", nil, &ErrToolNoProgress{Kind: "duplicate_request", Tool: "lookup"}
}

func (r *mixedBudgetToolRunner) Run(_ context.Context, name string, _ []byte) (string, []Citation, error) {
	r.calls.Add(1)
	if name == "lookup_ok" {
		return "useful result", []Citation{{URL: "https://result.test", Title: "Result"}}, nil
	}
	return "", nil, &ErrToolBudgetExceeded{Kind: "total_calls", Limit: 1}
}

func budgetTestRequest(model ModelInfo) UnifiedChatRequest {
	return UnifiedChatRequest{
		Model:        model,
		SystemPrompt: "Answer accurately.",
		History: []UnifiedMessage{{
			Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "look it up"}},
		}},
		Tools: []ToolDef{{
			Name: "lookup", Description: "Look up a value", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		ExtraParams: json.RawMessage(`{"tool_choice":"required","parallel_tool_calls":true}`),
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search"}],"tool_choice":"required"}`),
		},
	}
}

func decodeBudgetTestRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	return body
}

func assertToolFieldsRemoved(t *testing.T, body map[string]any) {
	t.Helper()
	for _, key := range []string{
		"tools", "tool_choice", "toolChoice", "functions", "function_call", "functionCall",
		"parallel_tool_calls", "tool_config", "toolConfig", "web_search_options", "webSearchOptions",
	} {
		if value, exists := body[key]; exists {
			t.Fatalf("tool-free finalization retained %s=%#v", key, value)
		}
	}
}

func unifiedResultText(result *UnifiedResult) string {
	if result == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range result.Blocks {
		if block.Kind == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func TestOpenAIChatBudgetRunsOneToolFreeFinalization(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"final from existing results"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"})
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
	messages, _ := requests[1]["messages"].([]any)
	var paired, instructed bool
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" && message["tool_call_id"] == "call_1" && message["content"] == toolBudgetExceededOutput {
			paired = true
		}
		if message["role"] == "system" && strings.Contains(stringValue(message["content"]), "Do not call or request any tools") {
			instructed = true
		}
	}
	if !paired || !instructed {
		t.Fatalf("finalization messages missing paired result/instruction: %#v", messages)
	}
	if got := unifiedResultText(result); got != "final from existing results" {
		t.Fatalf("final text = %q", got)
	}
}

func TestOpenAIChatNoProgressRunsOneToolFreeFinalization(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"final without more tools"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &noProgressToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"})
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
	messages, _ := requests[1]["messages"].([]any)
	var paired, instructed bool
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" && message["tool_call_id"] == "call_1" && message["content"] == toolNoProgressOutput {
			paired = true
		}
		if message["role"] == "system" && strings.Contains(stringValue(message["content"]), "would not add new evidence") {
			instructed = true
		}
	}
	if !paired || !instructed {
		t.Fatalf("no-progress finalization messages missing result/instruction: %#v", messages)
	}
	if got := unifiedResultText(result); got != "final without more tools" {
		t.Fatalf("final text = %q", got)
	}
}

func TestOpenAIResponsesBudgetRunsOneToolFreeFinalization(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"final response"}`,
			`data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"final response"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses"})
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
	if !strings.Contains(stringValue(requests[1]["instructions"]), "Do not call or request any tools") {
		t.Fatalf("finalization instructions = %#v", requests[1]["instructions"])
	}
	input, _ := requests[1]["input"].([]any)
	paired := false
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call_output" && item["call_id"] == "call_1" && item["output"] == toolBudgetExceededOutput {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("Responses finalization input missing paired output: %#v", input)
	}
	if got := unifiedResultText(result); got != "final response" {
		t.Fatalf("final text = %q", got)
	}
}

func TestOpenAIChatBudgetPairsEveryConcurrentToolResult(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_ok","function":{"name":"lookup_ok","arguments":"{}"}},{"index":1,"id":"call_limit","function":{"name":"lookup_limit","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"combined final"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &mixedBudgetToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"})
	req.Tools = []ToolDef{
		{Name: "lookup_ok", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "lookup_limit", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 2 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/2", len(requests), runner.calls.Load())
	}
	messages, _ := requests[1]["messages"].([]any)
	outputs := map[string]string{}
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" {
			outputs[stringValue(message["tool_call_id"])] = stringValue(message["content"])
		}
	}
	if outputs["call_ok"] != "useful result" || outputs["call_limit"] != toolBudgetExceededOutput {
		t.Fatalf("paired concurrent outputs = %#v", outputs)
	}
	if len(result.Citations) != 1 || result.Citations[0].URL != "https://result.test" {
		t.Fatalf("successful concurrent citations were lost: %+v", result.Citations)
	}
}

func TestAnthropicBudgetRunsOneToolFreeFinalization(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, anthropicTextStream("final anthropic"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"})
	result, err := (&AnthropicProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
	if !strings.Contains(mustJSON(requests[1]["system"]), "Do not call or request any tools") {
		t.Fatalf("finalization system = %#v", requests[1]["system"])
	}
	if !strings.Contains(mustJSON(requests[1]["messages"]), `"tool_use_id":"tool_1"`) ||
		!strings.Contains(mustJSON(requests[1]["messages"]), toolBudgetExceededOutput) {
		t.Fatalf("Anthropic finalization messages missing paired tool_result: %#v", requests[1]["messages"])
	}
	if got := unifiedResultText(result); got != "final anthropic" {
		t.Fatalf("final text = %q", got)
	}
}

func TestGeminiBudgetRunsOneToolFreeFinalization(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, geminiTextStream("final gemini"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"})
	result, err := (&GoogleProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
	if !strings.Contains(mustJSON(requests[1]["systemInstruction"]), "Do not call or request any tools") {
		t.Fatalf("finalization system = %#v", requests[1]["systemInstruction"])
	}
	if !strings.Contains(mustJSON(requests[1]["contents"]), `"functionResponse"`) ||
		!strings.Contains(mustJSON(requests[1]["contents"]), toolBudgetExceededOutput) {
		t.Fatalf("Gemini finalization contents missing paired functionResponse: %#v", requests[1]["contents"])
	}
	if got := unifiedResultText(result); got != "final gemini" {
		t.Fatalf("final text = %q", got)
	}
}

func TestPromptModeBudgetRunsOneToolFreeFinalization(t *testing.T) {
	var modelCalls int
	runner := func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		modelCalls++
		if modelCalls == 1 {
			if isToolBudgetFinalization(ctx) {
				t.Fatal("first prompt round was marked as finalization")
			}
			return PromptToolRound{Text: `<tool_call>{"name":"lookup","arguments":{}}</tool_call>`}, nil
		}
		if !isToolBudgetFinalization(ctx) {
			t.Fatal("closing prompt round was not marked as finalization")
		}
		if strings.Contains(system, "## Available tools") || !strings.Contains(system, "Do not call or request any tools") {
			t.Fatalf("closing prompt system still advertises tools: %q", system)
		}
		if !strings.Contains(mustJSON(history), toolBudgetExceededOutput) {
			t.Fatalf("closing prompt history missing budget result: %#v", history)
		}
		return PromptToolRound{Text: "final prompt answer"}, nil
	}
	toolRunner := &budgetExceededToolRunner{}
	text, _, _, _, _, _, err := RunPromptToolLoopWithRaw(
		context.Background(), "base system",
		[]UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "question"}}}},
		[]ToolDef{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		runner, toolRunner, func(SseEvent) {},
	)
	if err != nil {
		t.Fatalf("RunPromptToolLoopWithRaw: %v", err)
	}
	if modelCalls != 2 || toolRunner.calls.Load() != 1 || text != "final prompt answer" {
		t.Fatalf("model/tool calls and text = %d/%d/%q, want 2/1/final", modelCalls, toolRunner.calls.Load(), text)
	}
}

func TestPromptModeNoProgressRunsOneToolFreeFinalization(t *testing.T) {
	var modelCalls int
	runner := func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		modelCalls++
		if modelCalls == 1 {
			return PromptToolRound{Text: `<tool_call>{"name":"lookup","arguments":{}}</tool_call>`}, nil
		}
		signal := toolFinalizationFromContext(ctx)
		if !IsToolNoProgress(signal) {
			t.Fatalf("closing prompt signal = %T %v", signal, signal)
		}
		if strings.Contains(system, "## Available tools") || !strings.Contains(system, "would not add new evidence") {
			t.Fatalf("closing prompt system still advertises tools: %q", system)
		}
		if !strings.Contains(mustJSON(history), toolNoProgressOutput) {
			t.Fatalf("closing prompt history missing no-progress result: %#v", history)
		}
		return PromptToolRound{Text: "final prompt answer after no progress"}, nil
	}
	toolRunner := &noProgressToolRunner{}
	text, _, _, _, _, _, err := RunPromptToolLoopWithRaw(
		context.Background(), "base system",
		[]UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "question"}}}},
		[]ToolDef{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		runner, toolRunner, func(SseEvent) {},
	)
	if err != nil {
		t.Fatalf("RunPromptToolLoopWithRaw: %v", err)
	}
	if modelCalls != 2 || toolRunner.calls.Load() != 1 || text != "final prompt answer after no progress" {
		t.Fatalf("model/tool calls and text = %d/%d/%q", modelCalls, toolRunner.calls.Load(), text)
	}
}

func TestBudgetFinalizationDoesNotExecuteASecondToolCall(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"})
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if !IsToolBudgetExceeded(err) {
		t.Fatalf("error = %v, want ErrToolBudgetExceeded", err)
	}
	if result == nil || result.StopReason != "tool_budget_exceeded" {
		t.Fatalf("result = %#v, want preserved budget-exceeded partial", result)
	}
	if len(requests) != 2 || runner.calls.Load() != 1 {
		t.Fatalf("provider/tool calls = %d/%d, want exactly 2/1", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[1])
}

func TestProviderIterationLimitAlsoRunsToolFreeFinalization(t *testing.T) {
	t.Setenv("AIVORY_LLM_MAX_ITER_2", "1")
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"iteration final"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"})
	result, err := (&OpenAIProvider{}).Stream(context.Background(), req, staticToolRunner("tool result"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || unifiedResultText(result) != "iteration final" {
		t.Fatalf("requests/final = %d/%q, want 2/iteration final", len(requests), unifiedResultText(result))
	}
	assertToolFieldsRemoved(t, requests[1])
}

func TestResponsesExternalFinalizationNeverEntersItsInnerToolLoop(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, decodeBudgetTestRequest(t, r))
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{}"}`,
			`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}}`,
			`data: {"type":"response.completed","response":{"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses"})
	result, err := (&OpenAIProvider{}).Stream(contextWithToolBudgetFinalization(context.Background()), req, runner, func(SseEvent) {})
	if !IsToolBudgetExceeded(err) {
		t.Fatalf("error = %v, want budget finalization failure", err)
	}
	if result == nil || result.StopReason != "tool_budget_exceeded" {
		t.Fatalf("result = %#v", result)
	}
	if len(requests) != 1 || runner.calls.Load() != 0 {
		t.Fatalf("provider/tool calls = %d/%d, want exactly 1/0", len(requests), runner.calls.Load())
	}
	assertToolFieldsRemoved(t, requests[0])
}

func TestToolFreeFinalizationDoesNotRetryTheFallbackChannel(t *testing.T) {
	var fallbackCalls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"fallback should not run"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer fallback.Close()

	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := primaryCalls.Add(1)
		if call == 1 {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		http.Error(w, "closing request failed", http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	runner := &budgetExceededToolRunner{}
	req := budgetTestRequest(ModelInfo{
		RequestID: "gpt-test", BaseURL: primary.URL, APIKey: "primary", APIFormat: "chat",
		Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback"},
	})
	_, err := (&OpenAIProvider{}).Stream(context.Background(), req, runner, func(SseEvent) {})
	if !IsToolBudgetExceeded(err) {
		t.Fatalf("error = %v, want finalization budget error", err)
	}
	if primaryCalls.Load() != 2 || fallbackCalls.Load() != 0 {
		t.Fatalf("primary/fallback calls = %d/%d, want 2/0", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestToolBudgetErrorIsPublicAndWrapSafe(t *testing.T) {
	budgetErr := &ErrToolBudgetExceeded{Kind: "tool_calls", Tool: "lookup", Limit: 3}
	wrapped := errors.Join(errors.New("diagnostic"), errors.New("wrapped: "+budgetErr.Error()), budgetErr)
	if !IsToolBudgetExceeded(wrapped) {
		t.Fatal("wrapped budget error was not recognized")
	}
	if got := publicToolErrorOutput(wrapped); got != toolBudgetExceededOutput || got == publicToolFailureMessage {
		t.Fatalf("public budget output = %q", got)
	}
	if ToolBudgetExceededMessage() != "已达到工具执行时间上限" {
		t.Fatalf("public final failure = %q", ToolBudgetExceededMessage())
	}
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
