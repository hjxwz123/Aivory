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

func TestMergeOfficialToolRequestsDeepMergeAndAppendArrays(t *testing.T) {
	body := map[string]any{
		"tools":   []map[string]any{{"type": "native"}},
		"include": []string{"native.include"},
		"vendor": map[string]any{
			"keep":    "native",
			"replace": "native",
			"nested":  map[string]any{"items": []string{"native"}},
		},
	}
	requests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"hosted-a"}],"include":["a.include"],"vendor":{"replace":"a","nested":{"items":["a"],"a":true}}}`),
		json.RawMessage(`{"tools":[{"type":"hosted-b"}],"include":["b.include"],"vendor":{"replace":"b","nested":{"items":["b"],"b":true}}}`),
		json.RawMessage(`not-json`),
	}

	got := MergeOfficialToolRequests(body, requests)
	assertArrayField(t, got, "tools", []string{"native", "hosted-a", "hosted-b"}, func(item any) string {
		object, _ := item.(map[string]any)
		value, _ := object["type"].(string)
		return value
	})
	assertArrayField(t, got, "include", []string{"native.include", "a.include", "b.include"}, func(item any) string {
		value, _ := item.(string)
		return value
	})
	vendor, _ := got["vendor"].(map[string]any)
	if vendor["keep"] != "native" || vendor["replace"] != "b" {
		t.Fatalf("object/scalar merge = %#v", vendor)
	}
	nested, _ := vendor["nested"].(map[string]any)
	if nested["a"] != true || nested["b"] != true {
		t.Fatalf("nested object merge = %#v", nested)
	}
	assertArrayField(t, nested, "items", []string{"native", "a", "b"}, func(item any) string {
		value, _ := item.(string)
		return value
	})
}

func TestEstimateRequestTokensCountsMergedOfficialToolRequests(t *testing.T) {
	requests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"hosted-a","description":"first schema"}],"vendor":{"items":["a"]}}`),
		json.RawMessage(`{"tools":[{"type":"hosted-b","description":"second schema"}],"vendor":{"items":["b"]}}`),
	}
	base := UnifiedChatRequest{SystemPrompt: "system"}
	req := base
	req.ToolModeOfficial = true
	req.OfficialToolNames = []string{"a", "b"}
	req.OfficialToolRequests = requests

	merged, err := json.Marshal(MergeOfficialToolRequests(nil, requests))
	if err != nil {
		t.Fatalf("marshal merged official requests: %v", err)
	}
	want := estimateRequestTokens(base) + estimateTokens(string(merged))
	if got := estimateRequestTokens(req); got != want {
		t.Fatalf("official request token estimate = %d, want %d (merged=%s)", got, want, merged)
	}
}

func TestOfficialToolRequestsReachEveryProviderBody(t *testing.T) {
	requests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"hosted-a"}],"vendor":{"items":["a"],"value":"a"}}`),
		json.RawMessage(`{"tools":[{"type":"hosted-b"}],"vendor":{"items":["b"],"value":"b"}}`),
	}
	base := UnifiedChatRequest{
		History:              []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		Tools:                []ToolDef{{Name: "system-tool", Description: "must not be exposed", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		OfficialToolNames:    []string{"a", "b"},
		OfficialToolRequests: requests,
		ToolModeOfficial:     true,
	}

	t.Run("openai chat", func(t *testing.T) {
		captured, server := captureProviderBody(t, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"}
		if _, err := (&OpenAIProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedOfficialProviderBody(t, *captured)
	})

	t.Run("anthropic", func(t *testing.T) {
		captured, server := captureProviderBody(t, anthropicTextStream("ok"))
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"}
		if _, err := (&AnthropicProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedOfficialProviderBody(t, *captured)
	})

	t.Run("google", func(t *testing.T) {
		captured, server := captureProviderBody(t, `data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`+"\n\n")
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"}
		if _, err := (&GoogleProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedOfficialProviderBody(t, *captured)
	})
}

func captureProviderBody(t *testing.T, response string) (*map[string]any, *httptest.Server) {
	t.Helper()
	captured := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode provider body: %v\n%s", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response))
	}))
	return &captured, server
}

func assertMergedOfficialProviderBody(t *testing.T, body map[string]any) {
	t.Helper()
	assertArrayField(t, body, "tools", []string{"hosted-a", "hosted-b"}, func(item any) string {
		object, _ := item.(map[string]any)
		value, _ := object["type"].(string)
		return value
	})
	vendor, _ := body["vendor"].(map[string]any)
	if vendor["value"] != "b" {
		t.Fatalf("later scalar did not win: %#v", vendor)
	}
	assertArrayField(t, vendor, "items", []string{"a", "b"}, func(item any) string {
		value, _ := item.(string)
		return value
	})
}

func assertArrayField(t *testing.T, object map[string]any, key string, want []string, render func(any) string) {
	t.Helper()
	items, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want array", key, object[key])
	}
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = render(item)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
