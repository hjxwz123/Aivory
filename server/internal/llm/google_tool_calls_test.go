package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var pythonToolDefinition = ToolDef{
	Name:        "python_execute",
	Description: "Run Python",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","minLength":1}},"required":["code"]}`),
}

type recordingGeminiToolRunner struct {
	mu     sync.Mutex
	inputs []string
}

func (r *recordingGeminiToolRunner) Run(_ context.Context, name string, input []byte) (string, []Citation, error) {
	r.mu.Lock()
	r.inputs = append(r.inputs, name+":"+string(input))
	r.mu.Unlock()
	return "ok", nil, nil
}

func (r *recordingGeminiToolRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.inputs...)
}

func writeGeminiTestStream(w http.ResponseWriter, payload string) {
	w.Header().Set("content-type", "text/event-stream")
	_, _ = io.WriteString(w, "data: "+payload+"\n\n")
}

func TestReadGeminiStreamMergesStandardFunctionCallFragments(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"python_execute","args":{}}}]},"finishReason":null,"index":0}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{"code":"print(1)"}}}]},"finishReason":null,"index":0}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}]}`,
		``,
	}, "\n\n")

	var events []SseEvent
	_, _, calls, modelParts, _, _, err := readGeminiStream(strings.NewReader(stream), func(event SseEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("readGeminiStream: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "python_execute" || string(calls[0].Args) != `{"code":"print(1)"}` {
		t.Fatalf("merged calls = %+v, want one complete python_execute call", calls)
	}
	if err := validateGeminiCalls(calls, []ToolDef{pythonToolDefinition}); err != nil {
		t.Fatalf("validate merged call: %v", err)
	}
	if len(modelParts) != 1 {
		t.Fatalf("model parts = %#v, want one merged functionCall part", modelParts)
	}
	functionCall, _ := modelParts[0]["functionCall"].(map[string]any)
	args, _ := functionCall["args"].(map[string]any)
	if functionCall["name"] != "python_execute" || args["code"] != "print(1)" {
		t.Fatalf("merged model part = %#v", modelParts[0])
	}
	for _, event := range events {
		if event.Type == "tool_start" || event.Type == "tool_input" {
			t.Fatalf("parser started tool before the complete call was validated: %+v", event)
		}
	}
}

func TestGoogleProviderUsesUniqueIDsForRepeatedFunctionCalls(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[`+
				`{"functionCall":{"name":"python_execute","args":{"code":"print(1)"}}},`+
				`{"functionCall":{"name":"python_execute","args":{"code":"print(2)"}}}`+
				`]},"finishReason":"STOP"}]}`)
			return
		}
		writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(upstream.Close)

	runner := &recordingGeminiToolRunner{}
	var events []SseEvent
	result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gemini-test", BaseURL: upstream.URL, APIKey: "test-key"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run both"}}}},
		Tools:   []ToolDef{pythonToolDefinition},
	}, runner, func(event SseEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits.Load() != 2 || len(runner.snapshot()) != 2 {
		t.Fatalf("provider/tool calls = %d/%d, want 2/2", hits.Load(), len(runner.snapshot()))
	}

	var startIDs []string
	for _, event := range events {
		if event.Type == "tool_start" {
			startIDs = append(startIDs, event.ID)
			if event.Name != "python_execute" || !geminiArgsAreObject(event.Input) {
				t.Fatalf("tool_start = %+v, want named call with object input", event)
			}
		}
	}
	if len(startIDs) != 2 || startIDs[0] == startIDs[1] || startIDs[0] == "python_execute" || startIDs[1] == "python_execute" {
		t.Fatalf("tool_start ids = %#v, want two unique generated ids", startIDs)
	}
	if result == nil || len(result.Blocks) != 5 || result.Blocks[0].ToolID == result.Blocks[2].ToolID || result.Blocks[4].Text != "done" {
		t.Fatalf("result blocks = %+v, want two distinct tool rounds and final text", result)
	}
}

func TestGoogleProviderFallsBackAfterCompletedToolRound(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if primaryHits.Add(1) == 1 {
			writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"python_execute","args":{"code":"print(1)"}}}]},"finishReason":"STOP"}]}`)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"当前分组上游负载已饱和，请稍后再试","type":"new_api_error","code":"model_price_error"}}`)
	}))
	t.Cleanup(primary.Close)

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[{"text":"fallback final answer"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(fallback.Close)

	flag := new(atomic.Bool)
	visible := new(atomic.Bool)
	runner := &recordingGeminiToolRunner{}
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	result, err := (&GoogleProvider{}).Stream(ctx, UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gemini-test",
			BaseURL:   primary.URL,
			APIKey:    "primary-key",
			Fallback:  &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback-key"},
		},
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run python"}}}},
		Tools:        []ToolDef{pythonToolDefinition},
		FallbackUsed: flag,
	}, runner, observeProviderVisibleOutput(func(SseEvent) {}, visible))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if primaryHits.Load() != 2 || fallbackHits.Load() != 1 {
		t.Fatalf("primary/fallback requests = %d/%d, want 2/1", primaryHits.Load(), fallbackHits.Load())
	}
	if len(runner.snapshot()) != 1 {
		t.Fatalf("tool executions = %d, want exactly 1", len(runner.snapshot()))
	}
	if !flag.Load() || !visible.Load() {
		t.Fatalf("fallback/visible flags = %v/%v, want true/true", flag.Load(), visible.Load())
	}
	if result == nil || len(result.Blocks) != 3 || result.Blocks[2].Text != "fallback final answer" {
		t.Fatalf("result blocks = %+v, want completed tool round followed by fallback answer", result)
	}
}

func TestGoogleProviderRejectsMalformedFunctionCallsBeforeExecution(t *testing.T) {
	tests := []struct {
		name         string
		functionCall string
	}{
		{name: "missing name", functionCall: `{"args":{"code":"print(1)"}}`},
		{name: "missing required code", functionCall: `{"name":"python_execute","args":{}}`},
		{name: "blank required code", functionCall: `{"name":"python_execute","args":{"code":"  "}}`},
		{name: "args is not an object", functionCall: `{"name":"python_execute","args":"{\"code\":\"print(1)\"}"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[{"functionCall":`+tt.functionCall+`}]},"finishReason":"STOP"}]}`)
			}))
			t.Cleanup(primary.Close)

			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeGeminiTestStream(w, `{"candidates":[{"content":{"parts":[{"text":"recovered"}]},"finishReason":"STOP"}]}`)
			}))
			t.Cleanup(fallback.Close)

			flag := new(atomic.Bool)
			runner := &recordingGeminiToolRunner{}
			var events []SseEvent
			result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
				Model: ModelInfo{
					RequestID: "gemini-test",
					BaseURL:   primary.URL,
					APIKey:    "primary-key",
					Fallback:  &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback-key"},
				},
				History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run python"}}}},
				Tools:        []ToolDef{pythonToolDefinition},
				FallbackUsed: flag,
			}, runner, func(event SseEvent) { events = append(events, event) })
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if !flag.Load() || len(runner.snapshot()) != 0 {
				t.Fatalf("fallback/tool executions = %v/%d, want true/0", flag.Load(), len(runner.snapshot()))
			}
			for _, event := range events {
				if event.Type == "tool_start" || event.Type == "tool_input" || strings.TrimSpace(event.Name) == "undefined" {
					t.Fatalf("malformed primary call leaked as tool event: %+v", event)
				}
			}
			if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Text != "recovered" {
				t.Fatalf("result = %+v, want fallback text only", result)
			}
		})
	}
}
