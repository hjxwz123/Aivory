package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicProviderReturnsVisiblePartialResultOnStreamError(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
		`data: {"type":"content_block_start","content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"partial thought"}}`,
		`data: {"type":"content_block_stop"}`,
		`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_1","name":"lookup"}}`,
		`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"q\":\"docs\"}"}}`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"stream interrupted"}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	var events []SseEvent
	result, err := (&AnthropicProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "test-key"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(event SseEvent) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("error = %v, want provider stream interruption", err)
	}
	if result == nil || result.StopReason != "error" {
		t.Fatalf("partial result = %+v, want stop reason error", result)
	}
	if result.Usage.InputTokens != 5 {
		t.Fatalf("partial usage = %+v, want current-round input tokens", result.Usage)
	}
	assertPartialBlock(t, result.Blocks, "thinking", "", "partial thought")
	tool := assertPartialBlock(t, result.Blocks, "tool_call", "lookup", "")
	if tool.ToolID != "tool_1" || string(tool.Input) != `{"q":"docs"}` {
		t.Fatalf("partial tool block = %+v", tool)
	}
	if !hasSSEEvent(events, "thinking_delta") || !hasSSEEvent(events, "tool_start") || !hasSSEEvent(events, "tool_input") {
		t.Fatalf("streamed events = %+v, want visible thinking and tool trace", events)
	}
}

func TestGoogleProviderReturnsVisiblePartialResultOnStreamError(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"partial thought","thought":true},{"text":"partial answer"},{"functionCall":{"name":"lookup","args":{"q":"docs"}}}]}}]}`,
		`data: {"error":{"code":503,"message":"stream interrupted","status":"UNAVAILABLE"}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	var events []SseEvent
	result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "test-key"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(event SseEvent) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("error = %v, want provider stream interruption", err)
	}
	if result == nil || result.StopReason != "error" {
		t.Fatalf("partial result = %+v, want stop reason error", result)
	}
	assertPartialBlock(t, result.Blocks, "thinking", "", "partial thought")
	assertPartialBlock(t, result.Blocks, "text", "", "partial answer")
	tool := assertPartialBlock(t, result.Blocks, "tool_call", "lookup", "")
	if string(tool.Input) != `{"q":"docs"}` {
		t.Fatalf("partial tool input = %s", tool.Input)
	}
	if !hasSSEEvent(events, "thinking_delta") || !hasSSEEvent(events, "text_delta") || !hasSSEEvent(events, "tool_start") {
		t.Fatalf("streamed events = %+v, want visible thinking, text, and tool trace", events)
	}
}

func TestGoogleProviderReturnsVisiblePartialResultOnPrematureEOF(t *testing.T) {
	stream := `data: {"candidates":[{"content":{"parts":[{"text":"partial answer"},{"functionCall":{"name":"lookup","args":{"q":"docs"}}}]}}]}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "test-key"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "response ended before a terminal event") {
		t.Fatalf("error = %v, want premature EOF protocol error", err)
	}
	if result == nil || result.StopReason != "error" {
		t.Fatalf("partial result = %+v, want stop reason error", result)
	}
	assertPartialBlock(t, result.Blocks, "text", "", "partial answer")
	assertPartialBlock(t, result.Blocks, "tool_call", "lookup", "")
}

func TestOpenAIResponsesReturnsVisiblePartialResultOnPrematureEOF(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial answer"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"q\":\"docs\"}"}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gpt-test", BaseURL: server.URL, APIKey: "test-key", APIFormat: "responses",
		},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "response ended before a terminal event") {
		t.Fatalf("error = %v, want premature EOF protocol error", err)
	}
	if result == nil || result.StopReason != "error" {
		t.Fatalf("partial result = %+v, want stop reason error", result)
	}
	assertPartialBlock(t, result.Blocks, "text", "", "partial answer")
	tool := assertPartialBlock(t, result.Blocks, "tool_call", "lookup", "")
	if tool.ToolID != "call_1" || string(tool.Input) != `{"q":"docs"}` {
		t.Fatalf("partial tool block = %+v", tool)
	}
	if strings.Contains(string(result.Raw), "function_call") {
		t.Fatalf("partial raw contains dangling function call: %s", result.Raw)
	}
}

func TestOpenAIChatReturnsVisibleToolOnExplicitStreamError(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":\"docs\"}"}}]}}]}`,
		`data: {"error":{"message":"stream interrupted"}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "test-key"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("error = %v, want provider stream interruption", err)
	}
	if result == nil || result.StopReason != "error" {
		t.Fatalf("partial result = %+v, want stop reason error", result)
	}
	tool := assertPartialBlock(t, result.Blocks, "tool_call", "lookup", "")
	if tool.ToolID != "call_1" || string(tool.Input) != `{"q":"docs"}` {
		t.Fatalf("partial tool block = %+v", tool)
	}
}

type partialPromptToolRunner struct{}

func (partialPromptToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	return "tool output", nil, nil
}

func TestPromptToolLoopPreservesSafeVisiblePrefixAndUsageOnStreamError(t *testing.T) {
	round := 0
	var events []SseEvent
	text, blocks, usage, _, err := RunPromptToolLoop(
		context.Background(), "system", nil,
		[]ToolDef{{Name: "lookup", InputSchema: []byte(`{"type":"object"}`)}},
		func(context.Context, []UnifiedMessage, string) (string, Usage, error) {
			round++
			if round == 1 {
				return `before tool<tool_call>{"name":"lookup","arguments":{"q":"docs"}}</tool_call>`, Usage{InputTokens: 2, OutputTokens: 3}, nil
			}
			return "partial final answer", Usage{InputTokens: 4, OutputTokens: 5}, errors.New("stream interrupted")
		},
		partialPromptToolRunner{},
		func(event SseEvent) { events = append(events, event) },
	)
	if err == nil || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("error = %v, want stream interruption", err)
	}
	if text != "before toolpartial final answer" {
		t.Fatalf("partial text = %q", text)
	}
	if usage.InputTokens != 6 || usage.OutputTokens != 8 {
		t.Fatalf("partial usage = %+v", usage)
	}
	assertPartialBlock(t, blocks, "tool_call", "lookup", "")
	assertPartialBlock(t, blocks, "text", "", text)
	if !hasSSEEvent(events, "tool_start") || !hasSSEEvent(events, "tool_result") || !hasSSEEvent(events, "text_delta") {
		t.Fatalf("events = %+v, want tool trace and visible text", events)
	}
}

func assertPartialBlock(t *testing.T, blocks []UnifiedBlock, kind, toolName, text string) UnifiedBlock {
	t.Helper()
	for _, block := range blocks {
		if block.Kind == kind && block.ToolName == toolName && block.Text == text {
			return block
		}
	}
	t.Fatalf("blocks = %+v, missing kind=%q tool=%q text=%q", blocks, kind, toolName, text)
	return UnifiedBlock{}
}

func hasSSEEvent(events []SseEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
