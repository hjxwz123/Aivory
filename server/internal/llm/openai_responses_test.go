package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
)

func TestHostedToolsKeepProviderNamesSeparateFromLocalFunctions(t *testing.T) {
	for input, want := range map[string]string{
		"file_search_call":      "file_search",
		"code_interpreter_call": "code_interpreter",
		"image_generation_call": "image_generation",
	} {
		if got := hostedToolName(input); got != want {
			t.Errorf("hostedToolName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeResponsesReplayItemsUsesPortableAssistantHistory(t *testing.T) {
	items := []map[string]any{
		{
			"id": "msg_old", "type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": "previous answer"}},
		},
		{"id": "fc_old", "type": "function_call", "call_id": "call_old", "name": "lookup", "arguments": `{}`},
		{"id": "ws_old", "type": "web_search_call", "action": map[string]any{}},
		{"id": "rs_old", "type": "reasoning", "encrypted_content": "opaque"},
	}

	got := normalizeResponsesReplayItems(items)
	if len(got) != len(items) {
		t.Fatalf("normalized items = %d, want %d: %#v", len(got), len(items), got)
	}
	message := got[0]
	if message["role"] != "assistant" || message["phase"] != "final_answer" {
		t.Fatalf("assistant EasyInputMessage metadata = %#v", message)
	}
	if _, exists := message["status"]; exists {
		t.Fatalf("EasyInputMessage unexpectedly retained output status: %#v", message)
	}
	if _, exists := message["id"]; exists {
		t.Fatalf("EasyInputMessage unexpectedly retained provider id: %#v", message)
	}
	content, _ := message["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "input_text" || content[0]["text"] != "previous answer" {
		t.Fatalf("assistant EasyInputMessage content = %#v", message["content"])
	}
	for _, index := range []int{1, 2} {
		if got[index]["status"] != "completed" {
			t.Fatalf("terminal call item %d status = %#v", index, got[index])
		}
	}
	if _, exists := got[3]["status"]; exists {
		t.Fatalf("reasoning item gained an unsupported status: %#v", got[3])
	}
	if _, exists := items[1]["status"]; exists {
		t.Fatal("normalization mutated its input")
	}
}

func TestOpenAIResponsesCanonicalAssistantHistoryUsesInputText(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"new answer"}`,
			`data: {"type":"response.completed","response":{"output":[]}}`,
			``,
		}, "\n\n"))
	}))
	defer srv.Close()

	_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "first question"}}},
			{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "old answer"}}},
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "follow up"}}},
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	input, _ := captured["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input = %#v, want three canonical messages", captured["input"])
	}
	assistant, _ := input[1].(map[string]any)
	content, _ := assistant["content"].([]any)
	part, _ := content[0].(map[string]any)
	if assistant["role"] != "assistant" || part["type"] != "input_text" || part["text"] != "old answer" {
		t.Fatalf("assistant history is not an EasyInputMessage: %#v", assistant)
	}
	if _, exists := assistant["status"]; exists {
		t.Fatalf("canonical assistant history must not require output status: %#v", assistant)
	}
}

func TestOpenAIResponsesRepairsLegacyRawMessageWithoutStatus(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[]}}`+"\n\n")
	}))
	defer srv.Close()

	legacyRaw := json.RawMessage(`[{"id":"msg_old","type":"message","role":"assistant","content":[{"type":"output_text","text":"legacy answer"}]}]`)
	_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "first question"}}},
			{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "legacy answer"}}, Raw: legacyRaw},
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "follow up"}}},
		},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	encoded, _ := json.Marshal(captured["input"])
	if bytes.Contains(encoded, []byte(`"output_text"`)) {
		t.Fatalf("legacy output message was replayed without normalization: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"input_text"`)) || !bytes.Contains(encoded, []byte(`"legacy answer"`)) {
		t.Fatalf("legacy assistant text was lost during normalization: %s", encoded)
	}
}

func TestOpenAIResponsesModelAndChannelSwitchesUsePortableHistory(t *testing.T) {
	legacyRaw := json.RawMessage(`[{"id":"msg_old","type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]}]`)
	for _, test := range []struct {
		name             string
		storedProvider   string
		storedModel      string
		currentProvider  string
		currentModel     string
		nativeToolReplay bool
	}{
		{
			name: "different model on another responses channel", storedProvider: "openai", storedModel: "model-a",
			currentProvider: "openai", currentModel: "model-b", nativeToolReplay: true,
		},
		{
			name: "different provider channel", storedProvider: "anthropic", storedModel: "model-a",
			currentProvider: "openai", currentModel: "model-a", nativeToolReplay: true,
		},
		{
			name: "native replay disabled after format or tool-mode switch", storedProvider: "openai", storedModel: "model-a",
			currentProvider: "openai", currentModel: "model-a", nativeToolReplay: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := []store.Message{
				{Role: "user", Blocks: textBlocks("first question"), Status: "complete"},
				{
					Role: "assistant", Provider: test.storedProvider, ModelID: test.storedModel,
					Blocks: textBlocks("old answer"), Raw: legacyRaw, Status: "complete",
				},
				{Role: "user", Blocks: textBlocks("follow up"), Status: "complete"},
			}
			history := storeToUnified(
				stored, test.currentProvider, test.currentModel, test.nativeToolReplay,
			)
			if len(history) != 3 || len(history[1].Raw) != 0 {
				t.Fatalf("switched history retained incompatible native Raw: %+v", history)
			}

			var captured map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"output":[]}}`+"\n\n")
			}))
			defer srv.Close()

			_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
				Model: ModelInfo{
					ID: test.currentModel, RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses",
				},
				History: history,
			}, nil, func(SseEvent) {})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			encoded, _ := json.Marshal(captured["input"])
			if bytes.Contains(encoded, []byte(`"output_text"`)) || !bytes.Contains(encoded, []byte(`"input_text"`)) {
				t.Fatalf("switched history was not rebuilt as portable input: %s", encoded)
			}
		})
	}
}

func TestOpenAIResponsesRetriesUnexpectedEOFBeforeRoundEvents(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			// Claim a longer body so net/http reports io.ErrUnexpectedEOF when
			// this deliberately truncated stream closes.
			w.Header().Set("Content-Length", "4096")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"recovered"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":1}}}`,
			``,
		}, "\n\n"))
	}))
	defer srv.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want one retry", hits.Load())
	}
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Text != "recovered" {
		t.Fatalf("result = %+v, want recovered text", result)
	}
}

func TestOpenAIResponsesDoesNotRetryUnexpectedEOFAfterRoundEvent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "4096")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"partial"}`+"\n\n")
	}))
	defer srv.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, func(SseEvent) {})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Stream error = %v, want unexpected EOF", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, visible partial stream must not retry", hits.Load())
	}
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Text != "partial" {
		t.Fatalf("result = %+v, want preserved partial text", result)
	}
}

func testPNGBytes(payloadSize int) []byte {
	data := append([]byte(nil), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}...)
	return append(data, bytes.Repeat([]byte{0}, payloadSize)...)
}

func TestOpenAIResponsesHostedImageLargeEventReturnsBinaryWithoutRawBase64(t *testing.T) {
	imageData := testPNGBytes(900_000)
	encoded := base64.StdEncoding.EncodeToString(imageData)
	if len(encoded) <= 1<<20 {
		t.Fatalf("fixture must exceed the old Scanner limit, got %d bytes", len(encoded))
	}
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"ig_1","type":"image_generation_call"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + encoded + `"}}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":3},"output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"` + encoded + `"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:                ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History:              []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "draw"}}}},
		OfficialToolNames:    []string{"image_generation"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"image_generation"}]}`)},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(result.GeneratedImages) != 1 || !bytes.Equal(result.GeneratedImages[0].Data, imageData) || result.GeneratedImages[0].MimeType != "image/png" {
		t.Fatalf("generated images = %#v", result.GeneratedImages)
	}
	if bytes.Contains(result.Raw, []byte(`"result"`)) || bytes.Contains(result.Raw, []byte(encoded[:128])) {
		t.Fatal("multi-megabyte image result leaked into persisted Responses raw")
	}
}

func TestOpenAIResponsesHostedImageContinuesFromPreviousArtifact(t *testing.T) {
	imageData := testPNGBytes(48)
	encoded := base64.StdEncoding.EncodeToString(imageData)
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"edited"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":1},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"edited"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer server.Close()

	_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses",
		},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "draw a room"}}},
			{Role: "assistant", Blocks: []UnifiedBlock{{
				Kind: "artifact", FileRef: "art_previous", Data: encoded, MimeType: "image/png",
			}}},
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "only change the wall color"}}},
		},
		OfficialToolNames:    []string{"image_generation"},
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"image_generation"}]}`)},
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	tools, _ := requestBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("hosted image tool missing from request: %#v", requestBody["tools"])
	}
	input, _ := requestBody["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input turns = %d, want original user + edit user without an empty assistant turn: %#v", len(input), input)
	}
	editTurn, _ := input[1].(map[string]any)
	if editTurn["role"] != "user" {
		t.Fatalf("edit turn role = %#v", editTurn["role"])
	}
	content, _ := editTurn["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("edit content = %#v, want text + previous image", editTurn["content"])
	}
	imagePart, _ := content[1].(map[string]any)
	if imagePart["type"] != "input_image" || imagePart["image_url"] != "data:image/png;base64,"+encoded {
		t.Fatalf("previous generated image was not sent as edit input: %#v", imagePart)
	}
}

func TestResponsesHostedImageUsesLatestPartialWhenFinalResultIsOmitted(t *testing.T) {
	imageData := testPNGBytes(32)
	encoded := base64.StdEncoding.EncodeToString(imageData)
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"ig_partial","type":"image_generation_call"}}`,
		`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_partial","partial_image_index":0,"partial_image":"aW52YWxpZA=="}`,
		`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_partial","partial_image_index":1,"partial_image_b64":"` + encoded + `"}`,
		`data: {"type":"response.output_item.done","item":{"id":"ig_partial","type":"image_generation_call","status":"completed"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_partial","type":"image_generation_call","status":"completed"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	_, _, _, hosted, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	hosted, images, err := decodeHostedGeneratedImages(hosted)
	if err != nil {
		t.Fatalf("decode partial image: %v", err)
	}
	if len(images) != 1 || !bytes.Equal(images[0].Data, imageData) {
		t.Fatalf("partial fallback images = %#v", images)
	}
	if hosted[0].ImageBase64 != "" {
		t.Fatal("decoded base64 was retained in hosted tool state")
	}
	raw, _ := json.Marshal(outputItems)
	if bytes.Contains(raw, []byte(`"result"`)) {
		t.Fatalf("output items retained image result: %s", raw)
	}
}

func TestResponsesHostedImageCompletedWithoutUsableResultFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		encoded string
	}{
		{name: "missing"},
		{name: "invalid base64", encoded: "%%%"},
		{name: "unsupported bytes", encoded: base64.StdEncoding.EncodeToString([]byte("plain text"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, images, err := decodeHostedGeneratedImages([]hostedToolCall{{
				ID: "ig_bad", Name: "image_generation", Status: "completed", ImageBase64: tc.encoded,
			}})
			if err == nil || len(images) != 0 {
				t.Fatalf("images=%#v err=%v, want a decoding failure", images, err)
			}
		})
	}
}

func TestResponsesHostedItemWithoutIDDoesNotPanic(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"image_generation_call"}}`,
		`data: {"type":"response.completed","response":{"output":[]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	_, _, _, hosted, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil || len(hosted) != 0 {
		t.Fatalf("hosted=%#v err=%v", hosted, err)
	}
}

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

func TestResponsesPromptModePreservesHostedSearchAndImageResults(t *testing.T) {
	imageData := testPNGBytes(32)
	encoded := base64.StdEncoding.EncodeToString(imageData)
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"ws_prompt","type":"web_search_call"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"ws_prompt","type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://prompt.test","title":"Prompt source"}]}}}`,
		`data: {"type":"response.output_item.added","item":{"id":"ig_prompt","type":"image_generation_call"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"ig_prompt","type":"image_generation_call","status":"completed","result":"` + encoded + `"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hosted answer"}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":3},"output":[{"id":"ws_prompt","type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://prompt.test","title":"Prompt source"}]}},{"id":"ig_prompt","type":"image_generation_call","status":"completed","result":"` + encoded + `"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	var events []SseEvent
	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search and draw"}}}},
		Tools:          []ToolDef{{Name: "aivory_web_search", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search"}]}`),
			json.RawMessage(`{"tools":[{"type":"image_generation"}]}`),
		},
	}, nil, func(event SseEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(result.Citations) != 1 || result.Citations[0].URL != "https://prompt.test" {
		t.Fatalf("prompt hosted citations = %+v", result.Citations)
	}
	if len(result.GeneratedImages) != 1 || !bytes.Equal(result.GeneratedImages[0].Data, imageData) {
		t.Fatalf("prompt hosted images = %#v", result.GeneratedImages)
	}
	toolNames := map[string]bool{}
	for _, block := range result.Blocks {
		if block.Kind == "tool_call" {
			toolNames[block.ToolName] = true
		}
	}
	if !toolNames["web_search"] || !toolNames["image_generation"] || toolNames["aivory_web_search"] {
		t.Fatalf("prompt hosted/local block separation = %+v", result.Blocks)
	}
	wireTools, _ := requestBody["tools"].([]any)
	if len(wireTools) != 2 {
		t.Fatalf("prompt wire tools = %#v, want only two administrator-hosted tools", requestBody["tools"])
	}
	for _, eventType := range []string{"tool_start", "tool_result", "citation", "text_delta"} {
		if !hasSSEEvent(events, eventType) {
			t.Errorf("prompt hosted events missing %s: %+v", eventType, events)
		}
	}
}

func TestResponsesHostedAndAivoryToolNamesStaySeparate(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"ws_1","type":"web_search_call","status":"completed"}}`,
		`data: {"type":"response.output_item.added","item":{"id":"fn_1","type":"function_call","call_id":"fn_1","name":"aivory_web_search","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fn_1","arguments":"{\"query\":\"local\"}"}`,
		`data: {"type":"response.output_item.done","item":{"id":"fn_1","type":"function_call","call_id":"fn_1","name":"aivory_web_search","arguments":"{\"query\":\"local\"}"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"ws_1","type":"web_search_call","status":"completed"},{"id":"fn_1","type":"function_call","call_id":"fn_1","name":"aivory_web_search","arguments":"{\"query\":\"local\"}"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	_, _, local, hosted, _, _, _, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read responses stream: %v", err)
	}
	if len(local) != 1 || local[0].Name != "aivory_web_search" || string(local[0].Input) != `{"query":"local"}` {
		t.Fatalf("local calls = %+v, want only aivory_web_search", local)
	}
	if len(hosted) != 1 || hosted[0].Name != "web_search" || hosted[0].ID != "ws_1" {
		t.Fatalf("hosted calls = %+v, want official web_search", hosted)
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
			hasFunctionCall = item["call_id"] == "call_1" && item["status"] == "completed"
		case "function_call_output":
			hasFunctionOutput = item["call_id"] == "call_1" && item["output"] == "tool output"
		}
	}
	if !hasReasoning || !hasFunctionCall || !hasFunctionOutput {
		t.Fatalf("second request input missing continuation items: %#v", secondInput)
	}
	if bytes.Contains(result.Raw, []byte(`"output_text"`)) || !bytes.Contains(result.Raw, []byte(`"input_text"`)) {
		t.Fatalf("persisted tool-loop raw retained a status-dependent output message: %s", result.Raw)
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
