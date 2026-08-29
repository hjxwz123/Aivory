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

func TestEstimateRequestTokensCountsHostedAndLocalToolRequests(t *testing.T) {
	requests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"hosted-a","description":"first schema"}],"vendor":{"items":["a"]}}`),
		json.RawMessage(`{"tools":[{"type":"hosted-b","description":"second schema"}],"vendor":{"items":["b"]}}`),
	}
	base := UnifiedChatRequest{SystemPrompt: "system"}
	req := base
	req.Tools = []ToolDef{{Name: "local", Description: "local schema", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	req.OfficialToolNames = []string{"a", "b"}
	req.OfficialToolRequests = requests

	merged, err := json.Marshal(MergeOfficialToolRequests(nil, requests))
	if err != nil {
		t.Fatalf("marshal merged official requests: %v", err)
	}
	local, _ := json.Marshal(req.Tools)
	want := estimateRequestTokens(base) + estimateTokens(string(merged)) + estimateTokens(string(local))
	if got := estimateRequestTokens(req); got != want {
		t.Fatalf("official request token estimate = %d, want %d (merged=%s)", got, want, merged)
	}
}

func TestEstimateRequestTokensCountsMergedExtraParameters(t *testing.T) {
	base := UnifiedChatRequest{SystemPrompt: "system"}
	req := base
	req.ExtraParams = json.RawMessage(`{"metadata":{"large":"` + strings.Repeat("context ", 400) + `"},"temperature":0.2}`)
	req.ParamControls = json.RawMessage(`[{"key":"detail","options":[{"value":"high","params":{"vendor":{"instructions":"` + strings.Repeat("preserve ", 300) + `"}}}]}]`)
	req.ParamOverrides = map[string]any{"detail": "high"}

	merged, err := json.Marshal(MergeRequestParams(nil, req.ExtraParams, req.ParamControls, req.ParamOverrides))
	if err != nil {
		t.Fatalf("marshal merged parameters: %v", err)
	}
	want := estimateRequestTokens(base) + estimateTokens(string(merged))
	if got := estimateRequestTokens(req); got != want {
		t.Fatalf("extra parameter token estimate = %d, want %d", got, want)
	}
}

func TestConfiguredHostedToolsPreserveEveryProviderToolAndHistoryAlias(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"renamed_code_tool","icon":"terminal","request":{"tools":[{"type":"code_interpreter"}]}},
		{"name":"renamed_image_tool","icon":"image","request":{"tools":[{"type":"image_generation"}]}},
		{"name":"renamed_search_tool","icon":"search","request":{"tools":[{"type":"web_search_preview"}]}},
		{"name":"renamed_claude_search","icon":"search","request":{"tools":[{"type":"web_search_20250305","name":"web_search"}]}}
	]`)

	names, requests := configuredOfficialToolRequests(raw)
	if strings.Join(names, "\x00") != "renamed_code_tool\x00renamed_image_tool\x00renamed_search_tool\x00renamed_claude_search" {
		t.Fatalf("hosted names = %v, want every configured provider tool", names)
	}
	allowed := unifiedToolNameSet(nil, names, requests)
	for _, name := range []string{
		"renamed_code_tool",
		"renamed_image_tool",
		"renamed_search_tool",
		"renamed_claude_search",
		"code_interpreter",
		"image_generation",
		"web_search_preview",
		"web_search_20250305",
		"web_search",
	} {
		if !allowed[name] {
			t.Errorf("hosted history alias %q was not preserved: %#v", name, allowed)
		}
	}
	for _, name := range []string{"python_execute", "image_generate"} {
		if allowed[name] {
			t.Errorf("local-only tool name %q appeared in hosted aliases: %#v", name, allowed)
		}
	}
}

func TestHostedAndLocalToolRequestsReachEveryProviderBody(t *testing.T) {
	requests := []json.RawMessage{
		json.RawMessage(`{"tools":[{"type":"hosted-a"}],"vendor":{"items":["a"],"value":"a"}}`),
		json.RawMessage(`{"tools":[{"type":"hosted-b"}],"vendor":{"items":["b"],"value":"b"}}`),
	}
	base := UnifiedChatRequest{
		History:              []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		Tools:                []ToolDef{{Name: "system-tool", Description: "local Function", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		OfficialToolNames:    []string{"a", "b"},
		OfficialToolRequests: requests,
	}

	t.Run("openai chat", func(t *testing.T) {
		captured, server := captureProviderBody(t, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "chat"}
		if _, err := (&OpenAIProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedHostedProviderBody(t, *captured, "openai")
	})

	t.Run("anthropic", func(t *testing.T) {
		captured, server := captureProviderBody(t, anthropicTextStream("ok"))
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"}
		if _, err := (&AnthropicProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedHostedProviderBody(t, *captured, "anthropic")
	})

	t.Run("google", func(t *testing.T) {
		captured, server := captureProviderBody(t, `data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`+"\n\n")
		defer server.Close()
		req := base
		req.Model = ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"}
		if _, err := (&GoogleProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertMergedHostedProviderBody(t, *captured, "google")
	})
}

func TestOpenAIResponsesUsesDistinctOfficialAndAivoryWireNames(t *testing.T) {
	captured, server := captureProviderBody(t, strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n"))
	defer server.Close()

	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use tools"}}}},
		Tools: []ToolDef{
			{Name: "aivory_web_search", Description: "local search", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "python_execute", Description: "local code", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "image_generate", Description: "local image", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search"}]}`),
			json.RawMessage(`{"tools":[{"type":"code_interpreter"}]}`),
			json.RawMessage(`{"tools":[{"type":"image_generation"}]}`),
		},
	}
	if _, err := (&OpenAIProvider{}).Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	items, ok := (*captured)["tools"].([]any)
	if !ok || len(items) != 6 {
		t.Fatalf("wire tools = %#v, want 3 local + 3 official", (*captured)["tools"])
	}
	wantLocal := []string{"aivory_web_search", "python_execute", "image_generate"}
	for i, want := range wantLocal {
		item, _ := items[i].(map[string]any)
		if item["type"] != "function" {
			t.Fatalf("local item %d = %#v, want Function", i, item)
		}
		if item["name"] != want {
			t.Fatalf("local item %d name = %#v, want %q", i, item["name"], want)
		}
	}
	wantOfficial := []string{"web_search", "code_interpreter", "image_generation"}
	for i, want := range wantOfficial {
		item, _ := items[len(wantLocal)+i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("official item %d = %#v, want type %q", i, item, want)
		}
	}
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

func assertMergedHostedProviderBody(t *testing.T, body map[string]any, provider string) {
	t.Helper()
	items, ok := body["tools"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("%s mixed tools = %#v, want one local and two hosted declarations", provider, body["tools"])
	}
	local, _ := items[0].(map[string]any)
	switch provider {
	case "openai":
		if local["type"] != "function" {
			t.Fatalf("OpenAI local Function declaration = %#v", local)
		}
	case "anthropic":
		if local["name"] != "system-tool" {
			t.Fatalf("Anthropic local Function declaration = %#v", local)
		}
	case "google":
		if _, ok := local["functionDeclarations"]; !ok {
			t.Fatalf("Google local Function declaration = %#v", local)
		}
	}
	gotHosted := make([]string, 0, 2)
	for _, item := range items[1:] {
		object, _ := item.(map[string]any)
		value, _ := object["type"].(string)
		gotHosted = append(gotHosted, value)
	}
	if strings.Join(gotHosted, "\x00") != "hosted-a\x00hosted-b" {
		t.Fatalf("%s hosted tool order = %v", provider, gotHosted)
	}
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

func TestAnthropicHostedAndLocalToolStreamsStaySeparate(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"latest news\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","title":"A","url":"https://a.test"}]}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"local_1","name":"aivory_web_search","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"local query\"}"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")

	var events []SseEvent
	stopReason, local, hosted, text, _, _, nativeContent, _, err := readAnthropicStream(strings.NewReader(stream), func(ev SseEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("readAnthropicStream: %v", err)
	}
	if text != "" || stopReason != "tool_use" {
		t.Fatalf("text/stop reason = %q/%q, want empty/tool_use", text, stopReason)
	}
	if len(local) != 1 || local[0].Name != "aivory_web_search" || string(local[0].Input) != `{"query":"local query"}` {
		t.Fatalf("local calls = %+v, want only aivory_web_search", local)
	}
	if len(hosted) != 1 || hosted[0].Name != "web_search" || hosted[0].ID != "srv_1" ||
		string(hosted[0].Input) != `{"query":"latest news"}` || hosted[0].Status != "complete" {
		t.Fatalf("hosted calls = %+v, want completed official web_search", hosted)
	}

	for _, ev := range events {
		if ev.Type == "tool_input" && (ev.ID == "" || ev.Name == "") {
			t.Fatalf("hosted/local tool input lost identity: %+v", ev)
		}
	}
	var sawHostedStart, sawLocalStart, sawHostedResult bool
	for _, ev := range events {
		switch {
		case ev.Type == "tool_start" && ev.ID == "srv_1" && ev.Name == "web_search":
			sawHostedStart = true
		case ev.Type == "tool_start" && ev.ID == "local_1" && ev.Name == "aivory_web_search":
			sawLocalStart = true
		case ev.Type == "tool_result" && ev.ID == "srv_1" && ev.Name == "web_search" && ev.Status == "complete":
			sawHostedResult = true
		}
	}
	if !sawHostedStart || !sawLocalStart || !sawHostedResult {
		t.Fatalf("mixed hosted/local events = %+v", events)
	}
	if len(nativeContent) != 3 || nativeContent[0]["type"] != "server_tool_use" || nativeContent[1]["type"] != "web_search_tool_result" || nativeContent[2]["type"] != "tool_use" {
		t.Fatalf("native mixed content was not preserved in provider order: %#v", nativeContent)
	}

	turn := buildAssistantTurn("", nil, hosted, local)
	content, ok := turn["content"].([]map[string]any)
	if !ok || len(content) < 3 {
		t.Fatalf("mixed assistant turn = %#v", turn)
	}
	if content[0]["type"] != "server_tool_use" || content[0]["name"] != "web_search" {
		t.Fatalf("official block was not preserved: %#v", content[0])
	}
	if content[1]["type"] != "web_search_tool_result" || content[2]["type"] != "tool_use" || content[2]["name"] != "aivory_web_search" {
		t.Fatalf("mixed block order/names = %#v", content)
	}
}

func TestGeminiHostedPartsNeverBecomeAivoryFunctionCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[` +
			`{"executableCode":{"language":"PYTHON","code":"print(1)"}},` +
			`{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}},` +
			`{"functionCall":{"name":"aivory_web_search","args":{"query":"local"}}}` +
			`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`,
		``,
	}, "\n\n")

	_, _, calls, modelParts, _, _, err := readGeminiStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("readGeminiStream: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "aivory_web_search" || string(calls[0].Args) != `{"query":"local"}` {
		t.Fatalf("client Function calls = %+v, want only aivory_web_search", calls)
	}
	if len(modelParts) != 3 {
		t.Fatalf("model parts = %#v, want hosted code/result plus local functionCall", modelParts)
	}
	if _, ok := modelParts[0]["executableCode"]; !ok {
		t.Fatalf("hosted executableCode was not preserved: %#v", modelParts)
	}
	if _, ok := modelParts[1]["codeExecutionResult"]; !ok {
		t.Fatalf("hosted codeExecutionResult was not preserved: %#v", modelParts)
	}
	if fc, ok := modelParts[2]["functionCall"].(map[string]any); !ok || fc["name"] != "aivory_web_search" {
		t.Fatalf("local functionCall was not preserved separately: %#v", modelParts[2])
	}
}

func TestAnthropicPauseTurnReplaysNativeHostedContentAndContinues(t *testing.T) {
	requests := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests = append(requests, body)
		w.Header().Set("content-type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Searching. "}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srv_pause","name":"web_search","input":{}}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"current release\"}"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":2}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_pause","content":[{"type":"web_search_result","title":"Release","url":"https://release.test"}]}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Finished."}}`,
			`data: {"type":"content_block_stop","index":1}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	result, err := (&AnthropicProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "find it"}}}},
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want pause continuation", len(requests))
	}

	messages, _ := requests[1]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("continuation messages = %#v, want user plus paused assistant", messages)
	}
	paused, _ := messages[1].(map[string]any)
	content, _ := paused["content"].([]any)
	if paused["role"] != "assistant" || len(content) != 2 {
		t.Fatalf("paused assistant message = %#v", paused)
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	input, _ := second["input"].(map[string]any)
	if first["type"] != "text" || first["text"] != "Searching. " || second["type"] != "server_tool_use" || input["query"] != "current release" {
		t.Fatalf("paused content was not replayed unchanged and in order: %#v", content)
	}
	if _, changed := first["cache_control"]; changed {
		t.Fatalf("pause_turn text block was modified with cache metadata: %#v", first)
	}
	if _, changed := second["cache_control"]; changed {
		t.Fatalf("pause_turn server tool block was modified with cache metadata: %#v", second)
	}

	if result.StopReason != "end_turn" || result.Usage.InputTokens != 8 || result.Usage.OutputTokens != 6 {
		t.Fatalf("result stop/usage = %q/%+v", result.StopReason, result.Usage)
	}
	hostedBlocks := 0
	for _, block := range result.Blocks {
		if block.Kind == "tool_call" && block.ToolID == "srv_pause" {
			hostedBlocks++
			if block.ToolName != "web_search" || block.Summary != "web_search completed" {
				t.Fatalf("merged hosted block = %+v", block)
			}
		}
	}
	if hostedBlocks != 1 {
		t.Fatalf("hosted canonical blocks = %d, want one merged pause/result card: %+v", hostedBlocks, result.Blocks)
	}
	var rawTurns []map[string]any
	if err := json.Unmarshal(result.Raw, &rawTurns); err != nil || len(rawTurns) != 2 {
		t.Fatalf("native continuation raw = %s / %v", result.Raw, err)
	}

	// The same hosted pause must also survive an administrator-selected prompt
	// protocol for local Functions. Reset the fake server's round counter and run
	// through the outer text-tool loop this time.
	requests = nil
	promptResult, err := (&AnthropicProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "find it"}}}},
		Tools:          []ToolDef{{Name: "aivory_web_search", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("prompt Stream: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("prompt provider requests = %d, want hosted pause continuation", len(requests))
	}
	if promptResult == nil {
		t.Fatal("prompt result is nil")
	}
	promptEnvelope, ok := parsePromptToolRawEnvelope(promptResult.Raw)
	if !ok || len(promptEnvelope.Outputs) != 1 || promptEnvelope.Outputs[0].Name != "web_search" ||
		!strings.Contains(promptEnvelope.Outputs[0].Output, "https://release.test") {
		t.Fatalf("prompt hosted result was not preserved in the neutral envelope: raw=%s envelope=%+v", promptResult.Raw, promptEnvelope)
	}
	promptHosted := 0
	for _, block := range promptResult.Blocks {
		if block.Kind == "tool_call" && block.ToolID == "srv_pause" {
			promptHosted++
		}
	}
	if promptHosted != 1 {
		t.Fatalf("prompt hosted blocks = %+v, want one merged provider call", promptResult.Blocks)
	}
	if stopSequences, _ := requests[0]["stop_sequences"].([]any); len(stopSequences) != 1 || stopSequences[0] != PromptToolStopSequence() {
		t.Fatalf("prompt hosted request stop_sequences = %#v", requests[0]["stop_sequences"])
	}
}

func TestGeminiGroundingMetadataBecomesPersistentCitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Grounded answer"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.test","title":"A"}},{"web":{"uri":"https://a.test","title":"duplicate"}},{"retrievedContext":{"uri":"files/x","title":"not web"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	var events []SseEvent
	result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search"}}}},
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"googleSearch":{}}]}`),
		},
	}, nil, func(event SseEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(result.Citations) != 1 || result.Citations[0].URL != "https://a.test" || result.Citations[0].Title != "A" || result.Citations[0].Source != "web" {
		t.Fatalf("grounding citations = %+v", result.Citations)
	}
	citationEvents := 0
	for _, event := range events {
		if event.Type == "citation" {
			citationEvents++
		}
	}
	if citationEvents != 1 {
		t.Fatalf("citation events = %d, want one deduped event: %+v", citationEvents, events)
	}

	// Prompt protocol governs only local Functions; provider-hosted grounding must
	// still use the native request and return the same citations.
	promptResult, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search"}}}},
		Tools:          []ToolDef{{Name: "aivory_web_search", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"googleSearch":{}}]}`),
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("prompt Stream: %v", err)
	}
	if len(promptResult.Citations) != 1 || promptResult.Citations[0].URL != "https://a.test" {
		t.Fatalf("prompt grounding citations = %+v", promptResult.Citations)
	}
}
