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

func TestOpenAIChatToolLoopReplaysReasoningContent(t *testing.T) {
	requests := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "text/event-stream")
		switch len(requests) {
		case 1:
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"choices":[{"delta":{"reasoning_content":"think before tool","tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		case 2:
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"choices":[{"delta":{"reasoning_content":"think after tool","content":"done"},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		default:
			t.Errorf("unexpected request %d", len(requests))
		}
	}))
	defer server.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{RequestID: "reasoning-model", BaseURL: server.URL, APIKey: "key", APIFormat: "chat"},
		History: []UnifiedMessage{{
			Role:   "user",
			Blocks: []UnifiedBlock{{Kind: "text", Text: "use the lookup tool"}},
		}},
		Tools: []ToolDef{{
			Name:        "lookup",
			Description: "Look up a value",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}, staticToolRunner("tool output"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}

	secondMessages, _ := requests[1]["messages"].([]any)
	if len(secondMessages) < 3 {
		t.Fatalf("second messages = %#v", secondMessages)
	}
	assistant, _ := secondMessages[1].(map[string]any)
	if assistant["reasoning_content"] != "think before tool" {
		t.Fatalf("tool-loop assistant reasoning_content = %#v", assistant["reasoning_content"])
	}

	var rawTurns []map[string]any
	if err := json.Unmarshal(result.Raw, &rawTurns); err != nil {
		t.Fatalf("decode result Raw: %v", err)
	}
	var reasoning []string
	for _, turn := range rawTurns {
		if turn["role"] == "assistant" {
			value, _ := turn["reasoning_content"].(string)
			reasoning = append(reasoning, value)
		}
	}
	if strings.Join(reasoning, "|") != "think before tool|think after tool" {
		t.Fatalf("persisted assistant reasoning = %#v", reasoning)
	}
}

func TestOpenAIChatRepairsReasoningMissingFromLegacyRaw(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
	}))
	defer server.Close()

	legacyRaw := json.RawMessage(`[
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"tool output"},
		{"role":"assistant","content":"legacy answer"}
	]`)
	_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{RequestID: "reasoning-model", BaseURL: server.URL, APIKey: "key", APIFormat: "chat"},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "old question"}}},
			{
				Role: "assistant",
				Blocks: []UnifiedBlock{
					{Kind: "thinking", Text: "legacy thought one"},
					{Kind: "tool_call", ToolName: "lookup", ToolID: "call_1"},
					{Kind: "tool_output", ToolName: "lookup", ToolID: "call_1", Text: "tool output"},
					{Kind: "thinking", Text: "legacy thought two"},
					{Kind: "text", Text: "legacy answer"},
				},
				Raw: legacyRaw,
			},
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "follow up"}}},
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	messages, _ := captured["messages"].([]any)
	assistantReasoning := []string{}
	for _, value := range messages {
		message, _ := value.(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		reasoning, _ := message["reasoning_content"].(string)
		assistantReasoning = append(assistantReasoning, reasoning)
	}
	if strings.Join(assistantReasoning, "|") != "legacy thought one|legacy thought two" {
		t.Fatalf("repaired assistant reasoning = %#v", assistantReasoning)
	}
}
