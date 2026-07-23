package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponsesWebSearchCitations verifies the hosted web_search citation path:
// inline url_citation annotations AND the web_search_call.action.sources list
// (returned via include) both become citations, deduped by URL, and are emitted
// live + returned for persistence.
func TestResponsesWebSearchCitations(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call"}}`,
		`data: {"type":"response.output_text.delta","delta":"Here is the news."}`,
		`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://a.com","title":"A"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://a.com","title":"A dup"},{"url":"https://b.com","title":"B"}]}}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var emitted int
	onEvent := func(ev SseEvent) {
		if ev.Type == "citation" {
			emitted++
		}
	}
	text, _, _, hosted, citations, _, _, err := readOpenAIResponsesStream(strings.NewReader(stream), onEvent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(text, "Here is the news.") {
		t.Errorf("missing answer text: %q", text)
	}
	if len(hosted) != 1 || hosted[0].Name != "web_search" {
		t.Errorf("expected one web_search hosted round, got %+v", hosted)
	}
	if len(citations) != 2 {
		t.Fatalf("expected 2 deduped citations (a.com, b.com), got %d: %+v", len(citations), citations)
	}
	if citations[0].URL != "https://a.com" || citations[1].URL != "https://b.com" {
		t.Errorf("unexpected citation URLs: %+v", citations)
	}
	if citations[0].Index != 1 || citations[1].Index != 2 {
		t.Errorf("citations should be 1-indexed in order: %+v", citations)
	}
	if emitted != 2 {
		t.Errorf("expected 2 live citation events, got %d", emitted)
	}
}

func TestResponsesStreamReturnsCompletedOutputForToolContinuation(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"}}`,
		`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"web_fetch","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"url\":\"https://example.com\"}"}`,
		`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"web_fetch","arguments":"{\"url\":\"https://example.com\"}"}}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7},"output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"web_fetch","arguments":"{\"url\":\"https://example.com\"}"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	_, _, calls, _, _, usage, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v, want 11/7", usage)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "web_fetch" {
		t.Fatalf("calls = %+v, want web_fetch call_1", calls)
	}
	if string(calls[0].Input) != `{"url":"https://example.com"}` {
		t.Fatalf("call input = %s", calls[0].Input)
	}
	if len(outputItems) != 2 {
		t.Fatalf("output items = %d, want reasoning + function_call: %+v", len(outputItems), outputItems)
	}
	if outputItems[0]["type"] != "reasoning" || outputItems[0]["encrypted_content"] != "enc" {
		t.Fatalf("reasoning output item not preserved: %+v", outputItems[0])
	}
	if outputItems[1]["type"] != "function_call" || outputItems[1]["call_id"] != "call_1" {
		t.Fatalf("function_call output item not preserved: %+v", outputItems[1])
	}
}

// A response.failed AFTER response.completed is a relay-side protocol
// violation (completed is terminal): some gateways append a bogus failed
// event while closing the connection. It must be ignored — the answer and
// usage are already in hand; flipping the turn to error showed the user
// "provider returned an error" on a fully delivered, billed reply.
func TestResponsesStreamIgnoresTrailingFailedAfterCompleted(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"The answer."}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":2584,"output_tokens":412},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"The answer."}]}]}}`,
		`data: {"type":"response.failed","response":{"error":{"message":"Upstream request failed"}}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	text, _, _, _, _, usage, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("trailing failed after completed must be ignored, got error: %v", err)
	}
	if text != "The answer." {
		t.Fatalf("text = %q, want the delivered answer", text)
	}
	if usage.InputTokens != 2584 || usage.OutputTokens != 412 {
		t.Fatalf("usage = %+v, want 2584/412", usage)
	}
	if len(outputItems) != 1 {
		t.Fatalf("completed output must be preserved, got %+v", outputItems)
	}
}

// A genuine response.failed (no completed before it) still errors, and the
// streamed partial text is returned alongside so callers can preserve it.
func TestResponsesStreamRealFailedStillErrors(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		`data: {"type":"response.failed","response":{"error":{"message":"Upstream request failed"}}}`,
		``,
	}, "\n\n")

	text, _, _, _, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err == nil {
		t.Fatal("real failed (no completed) must return an error")
	}
	if !strings.Contains(err.Error(), "Upstream request failed") {
		t.Fatalf("error should carry the upstream message, got: %v", err)
	}
	if text != "partial" {
		t.Fatalf("partial text should be returned for preservation, got %q", text)
	}
}

func TestAppendResponsesIncludeKeepsRequiredValues(t *testing.T) {
	body := map[string]any{"include": []any{"existing"}}
	appendResponsesInclude(body, "web_search_call.action.sources", "reasoning.encrypted_content", "existing")
	include, ok := body["include"].([]string)
	if !ok {
		t.Fatalf("include = %#v, want []string", body["include"])
	}
	want := []string{"existing", "web_search_call.action.sources", "reasoning.encrypted_content"}
	if len(include) != len(want) {
		t.Fatalf("include = %#v, want %#v", include, want)
	}
	for i := range want {
		if include[i] != want[i] {
			t.Fatalf("include = %#v, want %#v", include, want)
		}
	}
}

func TestOpenAIResponsesOfficialToolsSurviveExtraParamsMerge(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v\n%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"ok"}`,
			`data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")))
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	_, err := p.Stream(context.Background(), UnifiedChatRequest{
		Model:                ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History:              []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search"}}}},
		OfficialToolNames:    []string{"custom_search"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"web_search","search_context_size":"medium"}]}`)},
		ToolModeOfficial:     true,
		ExtraParams:          json.RawMessage(`{"tools":[{"type":"function","name":"extra_tool"}],"include":["custom.include"]}`),
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	tools, ok := captured["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("official tools lost or replaced by extra_params: %#v", captured["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "web_search" {
		t.Fatalf("official web search tool = %#v", tool)
	}
	include, _ := captured["include"].([]any)
	seen := map[string]bool{}
	for _, value := range include {
		if item, ok := value.(string); ok {
			seen[item] = true
		}
	}
	for _, want := range []string{"custom.include", "web_search_call.action.sources", "reasoning.encrypted_content"} {
		if !seen[want] {
			t.Fatalf("include missing %q: %#v", want, captured["include"])
		}
	}
}

func TestOpenAIResponsesToolLoopReplaysOutputItems(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var captured map[string]any
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v\n%s", err, string(body))
		}
		requests = append(requests, captured)
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(requests) {
		case 1:
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
				`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"}}`,
				`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"q\":\"x\"}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		case 2:
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"done"}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		default:
			t.Fatalf("unexpected request %d", len(requests))
		}
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	req := UnifiedChatRequest{
		Model: ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{
			Role:   "user",
			Blocks: []UnifiedBlock{{Kind: "text", Text: "use a tool"}},
		}},
		Tools: []ToolDef{{
			Name:        "lookup",
			Description: "Lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}
	result, err := p.Stream(context.Background(), req, staticToolRunner("tool output"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if result == nil || len(result.Blocks) == 0 || result.Blocks[len(result.Blocks)-1].Text != "done" {
		t.Fatalf("result blocks = %+v, want final text done", result)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	firstInclude, _ := requests[0]["include"].([]any)
	hasReasoningInclude := false
	for _, v := range firstInclude {
		if v == "reasoning.encrypted_content" {
			hasReasoningInclude = true
		}
	}
	if !hasReasoningInclude {
		t.Fatalf("first request include = %#v, want reasoning.encrypted_content", requests[0]["include"])
	}
	secondInput, _ := requests[1]["input"].([]any)
	var hasReasoning, hasFunctionCall, hasFunctionOutput bool
	for _, raw := range secondInput {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "reasoning":
			hasReasoning = item["encrypted_content"] == "enc"
		case "function_call":
			hasFunctionCall = item["call_id"] == "call_1"
		case "function_call_output":
			hasFunctionOutput = item["call_id"] == "call_1" && item["output"] == "tool output"
		}
	}
	if !hasReasoning || !hasFunctionCall || !hasFunctionOutput {
		t.Fatalf("second request input missing continuation items: %#v", secondInput)
	}
}

func TestOpenAIChatToolCallsAreOrderedAndEmitStartWhenNameArrivesLate(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"beta","arguments":"{\"b\":1}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a"}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"alpha","arguments":"{\"a\":1}"}}]}}]}`,
		`data: {"choices":[{"finish_reason":"tool_calls","delta":{}}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var starts []string
	_, _, calls, finish, _, err := readOpenAIChatStream(strings.NewReader(stream), func(ev SseEvent) {
		if ev.Type == "tool_start" {
			starts = append(starts, ev.ID+":"+ev.Name)
		}
	})
	if err != nil {
		t.Fatalf("readOpenAIChatStream: %v", err)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish = %q, want tool_calls", finish)
	}
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[0].Name != "alpha" || calls[1].ID != "call_b" || calls[1].Name != "beta" {
		t.Fatalf("calls not ordered by index: %+v", calls)
	}
	if strings.Join(starts, ",") != "call_b:beta,call_a:alpha" {
		t.Fatalf("tool_start events = %#v", starts)
	}
}

type staticToolRunner string

func (s staticToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	return string(s), nil, nil
}
