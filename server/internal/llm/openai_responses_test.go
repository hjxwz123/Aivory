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

func TestPrepareResponsesReplayItemsPreservesNativeAndRepairsLegacyItems(t *testing.T) {
	items := []map[string]any{
		{
			"id": "msg_native", "type": "message", "role": "assistant", "status": "completed", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": "native answer", "annotations": []any{}}},
		},
		{
			"id": "msg_legacy", "type": "message", "role": "assistant", "phase": "commentary",
			"content": []any{map[string]any{"type": "output_text", "text": "legacy answer"}},
		},
		{
			"role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "input_text", "text": "old portable answer"}},
		},
		{"id": "fc_old", "type": "function_call", "call_id": "call_old", "name": "lookup", "arguments": `{}`},
		{"id": "ws_old", "type": "web_search_call", "status": "failed", "action": map[string]any{}},
		{"id": "rs_old", "type": "reasoning", "encrypted_content": "opaque"},
	}

	got := prepareResponsesReplayItems(items)
	if len(got) != len(items) {
		t.Fatalf("prepared items = %d, want %d: %#v", len(got), len(items), got)
	}
	native := got[0]
	if native["id"] != "msg_native" || native["status"] != "completed" || native["phase"] != "final_answer" {
		t.Fatalf("native output message metadata changed: %#v", native)
	}
	nativeContent, _ := jsonArrayItems(native["content"])
	nativePart, _ := nativeContent[0].(map[string]any)
	if nativePart["type"] != "output_text" || nativePart["text"] != "native answer" || nativePart["annotations"] == nil {
		t.Fatalf("native output content changed: %#v", native["content"])
	}
	legacy := got[1]
	if legacy["role"] != "assistant" || legacy["phase"] != "commentary" || legacy["content"] != "legacy answer" {
		t.Fatalf("legacy output was not converted to portable history: %#v", legacy)
	}
	for _, field := range []string{"id", "status", "type"} {
		if _, exists := legacy[field]; exists {
			t.Fatalf("portable legacy message retained %s: %#v", field, legacy)
		}
	}
	oldPortable := got[2]
	if oldPortable["content"] != "old portable answer" || oldPortable["phase"] != "final_answer" {
		t.Fatalf("old invalid EasyInputMessage was not repaired: %#v", oldPortable)
	}
	if got[3]["status"] != "completed" || got[4]["status"] != "failed" {
		t.Fatalf("call statuses were not repaired/preserved: %#v %#v", got[3], got[4])
	}
	if _, exists := got[5]["status"]; exists {
		t.Fatalf("reasoning item gained an unsupported status: %#v", got[5])
	}
	if _, exists := items[1]["status"]; exists {
		t.Fatal("preparation mutated the legacy message")
	}
	if _, exists := items[3]["status"]; exists {
		t.Fatal("preparation mutated the function call")
	}
}

func TestOpenAIResponsesCanonicalAssistantHistoryUsesPortableString(t *testing.T) {
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
	if assistant["role"] != "assistant" || assistant["content"] != "old answer" {
		t.Fatalf("assistant history is not an EasyInputMessage: %#v", assistant)
	}
	for _, field := range []string{"id", "status", "type"} {
		if _, exists := assistant[field]; exists {
			t.Fatalf("canonical assistant history retained %s: %#v", field, assistant)
		}
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

	legacyRaw := json.RawMessage(`[{"id":"msg_old","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"legacy answer"}]}]`)
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
	input, _ := captured["input"].([]any)
	legacy, _ := input[1].(map[string]any)
	if legacy["role"] != "assistant" || legacy["phase"] != "commentary" || legacy["content"] != "legacy answer" {
		t.Fatalf("legacy assistant text or phase was lost: %#v", legacy)
	}
	for _, field := range []string{"id", "status", "type"} {
		if _, exists := legacy[field]; exists {
			t.Fatalf("legacy assistant retained incomplete output field %s: %#v", field, legacy)
		}
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
			input, _ := captured["input"].([]any)
			assistant, _ := input[1].(map[string]any)
			if assistant["role"] != "assistant" || assistant["content"] != "old answer" {
				t.Fatalf("switched history was not rebuilt as portable input: %#v", assistant)
			}
			for _, field := range []string{"id", "status", "type"} {
				if _, exists := assistant[field]; exists {
					t.Fatalf("switched history retained native field %s: %#v", field, assistant)
				}
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

func TestResponsesStreamDoesNotReplayIncompleteEncryptedContentFromItemAdded(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","status":"in_progress","summary":[],"encrypted_content":"partial-ciphertext"}}`,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_1","content_index":0,"text":"inspect"}`,
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"inspect"}],"encrypted_content":null}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"inspect"}],"encrypted_content":null}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	_, _, _, _, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(outputItems) != 1 {
		t.Fatalf("output items = %#v", outputItems)
	}
	if encrypted := outputItems[0]["encrypted_content"]; encrypted != nil {
		t.Fatalf("replayed incomplete encrypted_content from output_item.added: %#v", encrypted)
	}
}

func TestResponsesStreamCapturesIncompleteTerminalSnapshot(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.incomplete","response":{"usage":{"input_tokens":13,"output_tokens":21},"output":[{"id":"rs_1","type":"reasoning","status":"incomplete","summary":[],"encrypted_content":"enc-incomplete"},{"id":"msg_1","type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial answer"}]}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	_, _, _, _, _, usage, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if usage.InputTokens != 13 || usage.OutputTokens != 21 {
		t.Fatalf("usage = %+v, want 13/21", usage)
	}
	if len(outputItems) != 2 || outputItems[0]["encrypted_content"] != "enc-incomplete" || outputItems[1]["status"] != "incomplete" {
		t.Fatalf("incomplete response output was not preserved: %#v", outputItems)
	}
}

func TestResponsesStreamReassemblesMultipleReasoningTextParts(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","status":"in_progress","content":[]}}`,
		`data: {"type":"response.content_part.added","item_id":"rs_1","content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":"inspect "}`,
		`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":"sources"}`,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_1","content_index":0,"text":"inspect sources"}`,
		`data: {"type":"response.content_part.done","item_id":"rs_1","content_index":1,"part":{"type":"reasoning_text","text":"compare evidence"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","status":"completed","content":[]}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","status":"completed","content":[]}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var thinking strings.Builder
	_, _, _, _, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(ev SseEvent) {
		if ev.Type == "thinking_delta" {
			thinking.WriteString(ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if thinking.String() != "inspect sourcescompare evidence" {
		t.Fatalf("streamed thinking = %q", thinking.String())
	}
	if len(outputItems) != 1 {
		t.Fatalf("output items = %#v", outputItems)
	}
	content, _ := jsonArrayItems(outputItems[0]["content"])
	if len(content) != 2 {
		t.Fatalf("reasoning content = %#v, want two parts", outputItems[0]["content"])
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	if first["text"] != "inspect sources" || second["text"] != "compare evidence" {
		t.Fatalf("reasoning parts = %#v", content)
	}
}

func TestResponsesStreamUsesAuthoritativeFunctionArgumentsAndDeduplicatesItems(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	toolStarts := 0
	_, _, calls, _, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(event SseEvent) {
		if event.Type == "tool_start" {
			toolStarts++
		}
	})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if toolStarts != 1 {
		t.Fatalf("tool_start events = %d, want one per item_id", toolStarts)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || string(calls[0].Input) != `{"q":"x"}` {
		t.Fatalf("authoritative call = %#v", calls)
	}
	if len(outputItems) != 1 || outputItems[0]["arguments"] != `{"q":"x"}` || outputItems[0]["status"] != "completed" {
		t.Fatalf("replay item did not retain completed arguments/status: %#v", outputItems)
	}
}

func TestResponsesStreamUsesCompletedOnlyOutputAndFiltersNonCompletedCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2},"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","provider_extension":{"trace":"kept"},"content":[{"type":"output_text","text":"terminal answer","annotations":[]}]},{"id":"fc_ok","type":"function_call","status":"completed","call_id":"call_ok","name":"lookup","arguments":"{}"},{"id":"fc_failed","type":"function_call","status":"failed","call_id":"call_failed","name":"lookup","arguments":"{}"},{"id":"fc_no_id","type":"function_call","status":"completed","name":"lookup","arguments":"{}"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	text, _, calls, _, _, usage, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if text != "terminal answer" || usage.InputTokens != 3 || usage.OutputTokens != 2 {
		t.Fatalf("terminal snapshot text/usage = %q/%+v", text, usage)
	}
	if len(calls) != 1 || calls[0].ID != "call_ok" {
		t.Fatalf("only completed calls with call_id may execute: %#v", calls)
	}
	if len(outputItems) != 4 {
		t.Fatalf("terminal output = %#v", outputItems)
	}
	extension, _ := outputItems[0]["provider_extension"].(map[string]any)
	if extension["trace"] != "kept" {
		t.Fatalf("unknown terminal fields were not retained: %#v", outputItems[0])
	}
}

func TestResponsesStreamIncompleteNeverExecutesTruncatedCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":"}`,
		`data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":9,"output_tokens":4},"output":[{"id":"fc_1","type":"function_call","status":"incomplete","call_id":"call_1","name":"lookup","arguments":"{\"q\":"},{"id":"msg_1","type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial"}]}]}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	text, _, calls, _, _, usage, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if text != "partial" || usage.InputTokens != 9 || usage.OutputTokens != 4 {
		t.Fatalf("incomplete snapshot text/usage = %q/%+v", text, usage)
	}
	if len(calls) != 0 {
		t.Fatalf("truncated call was returned executable: %#v", calls)
	}
	if len(outputItems) != 2 || outputItems[0]["status"] != "incomplete" {
		t.Fatalf("incomplete output was not preserved: %#v", outputItems)
	}
}

func TestResponsesStreamReconcilesTerminalOmissionsInOutputIndexOrder(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","content":[]}}`,
		`data: {"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"must replay"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","content":[]}}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"id":"fc_b","type":"function_call","status":"in_progress","call_id":"call_b","name":"beta","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_b","output_index":2,"arguments":"{}"}`,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"id":"fc_b","type":"function_call","status":"completed","call_id":"call_b","name":"beta","arguments":"{}"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_a","type":"function_call","status":"in_progress","call_id":"call_a","name":"alpha","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_a","output_index":1,"arguments":"{}"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_a","type":"function_call","status":"completed","call_id":"call_a","name":"alpha","arguments":"{}"}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"fc_a","type":"function_call","status":"completed","call_id":"call_a","name":"alpha","arguments":"{}"},{"id":"fc_b","type":"function_call","status":"completed","call_id":"call_b","name":"beta","arguments":"{}"}]}}`,
		``,
	}, "\n\n")

	_, _, calls, _, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[1].ID != "call_b" {
		t.Fatalf("call order = %#v", calls)
	}
	if len(outputItems) != 3 || outputItems[0]["id"] != "rs_1" || outputItems[1]["id"] != "fc_a" || outputItems[2]["id"] != "fc_b" {
		t.Fatalf("reconciled output order = %#v", outputItems)
	}
}

func TestResponsesStreamPreservesDoneOnlySummaryAndRefusal(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"","provider_marker":"kept"}}`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"plan"}`,
		`data: {"type":"response.reasoning_summary_part.done","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"plan","provider_marker":"kept"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[]}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`data: {"type":"response.refusal.done","item_id":"msg_1","output_index":1,"content_index":0,"refusal":"cannot comply"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[]}}`,
		`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[]},{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[]}]}}`,
		``,
	}, "\n\n")

	text, thinking, _, _, _, _, outputItems, err := readOpenAIResponsesStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if text != "cannot comply" || thinking != "plan" {
		t.Fatalf("done-only output = text %q, thinking %q", text, thinking)
	}
	summary, _ := jsonArrayItems(outputItems[0]["summary"])
	content, _ := jsonArrayItems(outputItems[1]["content"])
	if len(summary) != 1 || len(content) != 1 {
		t.Fatalf("done-only parts were not retained: summary=%#v content=%#v", summary, content)
	}
	summaryPart, _ := summary[0].(map[string]any)
	if summaryPart["text"] != "plan" || summaryPart["provider_marker"] != "kept" {
		t.Fatalf("summary part fields = %#v", summaryPart)
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
		OfficialToolRequests: []json.RawMessage{json.RawMessage(`{"tools":[{"type":"web_search","search_context_size":"medium"}],"store":true}`)},
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
	if captured["store"] != false || captured["stream"] != true {
		t.Fatalf("provider-owned Responses fields were overridden: store=%#v stream=%#v", captured["store"], captured["stream"])
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
	hasNativeAssistantMessage := func(input []any, id, phase, text string) bool {
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if item["id"] != id {
				continue
			}
			content, _ := jsonArrayItems(item["content"])
			if len(content) == 0 {
				return false
			}
			part, _ := content[0].(map[string]any)
			return item["type"] == "message" && item["role"] == "assistant" &&
				item["status"] == "completed" && item["phase"] == phase &&
				part["type"] == "output_text" && part["text"] == text
		}
		return false
	}
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
				`data: {"type":"response.output_item.added","item":{"id":"msg_commentary","type":"message","role":"assistant","status":"in_progress","phase":"commentary","content":[]}}`,
				`data: {"type":"response.output_text.delta","item_id":"msg_commentary","delta":"checking"}`,
				`data: {"type":"response.output_item.done","item":{"id":"msg_commentary","type":"message","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"checking","annotations":[]}]}}`,
				`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"q\":\"x\"}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"enc"},{"id":"msg_commentary","type":"message","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"checking","annotations":[]}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		case 2:
			input, _ := captured["input"].([]any)
			if !hasNativeAssistantMessage(input, "msg_commentary", "commentary", "checking") {
				http.Error(w, `{"error":{"message":"Invalid assistant continuation"}}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"done"}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n")))
		case 3:
			input, _ := captured["input"].([]any)
			if !hasNativeAssistantMessage(input, "msg_commentary", "commentary", "checking") ||
				!hasNativeAssistantMessage(input, "msg_1", "final_answer", "done") {
				http.Error(w, `{"error":{"message":"Invalid persisted assistant history"}}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"follow-up done"}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"msg_2","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"follow-up done"}]}]}}`,
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
	var hasReasoning, hasCommentary, hasFunctionCall, hasFunctionOutput bool
	for _, raw := range secondInput {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "reasoning":
			hasReasoning = item["encrypted_content"] == "enc"
		case "message":
			content, _ := jsonArrayItems(item["content"])
			var part map[string]any
			if len(content) > 0 {
				part, _ = content[0].(map[string]any)
			}
			hasCommentary = item["id"] == "msg_commentary" && item["status"] == "completed" &&
				item["phase"] == "commentary" && part["type"] == "output_text" && part["text"] == "checking"
		case "function_call":
			hasFunctionCall = item["call_id"] == "call_1" && item["status"] == "completed"
		case "function_call_output":
			hasFunctionOutput = item["call_id"] == "call_1" && item["output"] == "tool output"
		}
	}
	if !hasReasoning || !hasCommentary || !hasFunctionCall || !hasFunctionOutput {
		t.Fatalf("second request input missing continuation items: %#v", secondInput)
	}
	if !bytes.Contains(result.Raw, []byte(`"output_text"`)) || bytes.Contains(result.Raw, []byte(`"type":"input_text"`)) {
		t.Fatalf("persisted tool-loop raw did not retain valid native output messages: %s", result.Raw)
	}

	followReq := req
	followReq.History = []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use a tool"}}},
		{Role: "assistant", Blocks: result.Blocks, Raw: result.Raw},
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "follow up"}}},
	}
	followResult, err := p.Stream(context.Background(), followReq, staticToolRunner("tool output"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("follow-up stream: %v", err)
	}
	if followResult == nil || len(followResult.Blocks) == 0 || followResult.Blocks[len(followResult.Blocks)-1].Text != "follow-up done" {
		t.Fatalf("follow-up result blocks = %+v, want final text follow-up done", followResult)
	}
	if len(requests) != 3 {
		t.Fatalf("requests after persisted follow-up = %d, want 3", len(requests))
	}
}

func TestOpenAIResponsesToolLoopReplaysStreamedReasoningText(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, captured)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","status":"in_progress","content":[]}}`,
				`data: {"type":"response.content_part.added","item_id":"rs_1","content_index":0,"part":{"type":"reasoning_text","text":""}}`,
				`data: {"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":"inspect sources"}`,
				`data: {"type":"response.reasoning_text.done","item_id":"rs_1","content_index":0,"text":"inspect sources"}`,
				`data: {"type":"response.content_part.done","item_id":"rs_1","content_index":0,"part":{"type":"reasoning_text","text":"inspect sources"}}`,
				// A compact terminal item may omit content. The content events above
				// remain authoritative and must survive this later snapshot.
				`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","status":"completed","content":[]}}`,
				`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"q\":\"x\"}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","status":"completed","content":[]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}

		input, _ := captured["input"].([]any)
		var hasReasoningText, hasCall, hasOutput bool
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			switch item["type"] {
			case "reasoning":
				content, _ := jsonArrayItems(item["content"])
				if len(content) > 0 {
					part, _ := content[0].(map[string]any)
					hasReasoningText = part["type"] == "reasoning_text" && part["text"] == "inspect sources"
				}
			case "function_call":
				hasCall = item["call_id"] == "call_1"
			case "function_call_output":
				hasOutput = item["call_id"] == "call_1" && item["output"] == "tool output"
			}
		}
		if !hasReasoningText || !hasCall || !hasOutput {
			http.Error(w, `{"error":{"message":"reasoning_text was not passed back"}}`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"done"}`,
			`data: {"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer srv.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "deepseek-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use a tool"}}}},
		Tools:   []ToolDef{{Name: "lookup", Description: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, staticToolRunner("tool output"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if result == nil || len(result.Blocks) == 0 || result.Blocks[len(result.Blocks)-1].Text != "done" {
		t.Fatalf("result = %+v, want final text done", result)
	}

	followReq := UnifiedChatRequest{
		Model: ModelInfo{RequestID: "deepseek-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use a tool"}}},
			{Role: "assistant", Blocks: result.Blocks, Raw: result.Raw},
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "follow up"}}},
		},
	}
	if _, err := (&OpenAIProvider{}).Stream(context.Background(), followReq, nil, func(SseEvent) {}); err != nil {
		t.Fatalf("persisted follow-up: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests after persisted follow-up = %d, want 3", len(requests))
	}
}

func TestOpenAIResponsesToolLoopReplaysAllInterleavedReasoningAndCallsInOrder(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, captured)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"id":"rs_a","type":"reasoning","status":"in_progress","content":[]}}`,
				`data: {"type":"response.reasoning_text.done","item_id":"rs_a","content_index":0,"text":"plan first lookup"}`,
				`data: {"type":"response.output_item.done","item":{"id":"rs_a","type":"reasoning","status":"completed","content":[]}}`,
				`data: {"type":"response.output_item.added","item":{"id":"fc_a","type":"function_call","call_id":"call_a","name":"lookup_a","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_a","arguments":"{\"q\":\"a\"}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_a","type":"function_call","call_id":"call_a","name":"lookup_a","arguments":"{\"q\":\"a\"}"}}`,
				`data: {"type":"response.output_item.added","item":{"id":"rs_b","type":"reasoning","status":"in_progress","content":[]}}`,
				`data: {"type":"response.content_part.done","item_id":"rs_b","content_index":0,"part":{"type":"reasoning_text","text":"plan second lookup"}}`,
				`data: {"type":"response.output_item.done","item":{"id":"rs_b","type":"reasoning","status":"completed","content":[]}}`,
				`data: {"type":"response.output_item.added","item":{"id":"fc_b","type":"function_call","call_id":"call_b","name":"lookup_b","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_b","arguments":"{\"q\":\"b\"}"}`,
				`data: {"type":"response.output_item.done","item":{"id":"fc_b","type":"function_call","call_id":"call_b","name":"lookup_b","arguments":"{\"q\":\"b\"}"}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"rs_a","type":"reasoning","status":"completed","content":[]},{"id":"fc_a","type":"function_call","call_id":"call_a","name":"lookup_a","arguments":"{\"q\":\"a\"}"},{"id":"rs_b","type":"reasoning","status":"completed","content":[]},{"id":"fc_b","type":"function_call","call_id":"call_b","name":"lookup_b","arguments":"{\"q\":\"b\"}"}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}

		input, _ := captured["input"].([]any)
		var types []string
		var reasoningTexts, callIDs, outputIDs []string
		for _, raw := range input[1:] {
			item, _ := raw.(map[string]any)
			itemType, _ := item["type"].(string)
			types = append(types, itemType)
			switch itemType {
			case "reasoning":
				content, _ := jsonArrayItems(item["content"])
				if len(content) > 0 {
					part, _ := content[0].(map[string]any)
					reasoningTexts = append(reasoningTexts, part["text"].(string))
				}
			case "function_call":
				callIDs = append(callIDs, item["call_id"].(string))
			case "function_call_output":
				outputIDs = append(outputIDs, item["call_id"].(string))
			}
		}
		wantTypes := "reasoning,function_call,reasoning,function_call,function_call_output,function_call_output"
		if strings.Join(types, ",") != wantTypes || strings.Join(reasoningTexts, ",") != "plan first lookup,plan second lookup" ||
			strings.Join(callIDs, ",") != "call_a,call_b" || strings.Join(outputIDs, ",") != "call_a,call_b" {
			http.Error(w, `{"error":{"message":"continuation items were incomplete or reordered"}}`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"done"}`,
			`data: {"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer srv.Close()

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "deepseek-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use two tools"}}}},
		Tools: []ToolDef{
			{Name: "lookup_a", Description: "Lookup A", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "lookup_b", Description: "Lookup B", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}, staticToolRunner("tool output"), func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 || result == nil || len(result.Blocks) == 0 || result.Blocks[len(result.Blocks)-1].Text != "done" {
		t.Fatalf("requests=%d result=%+v", len(requests), result)
	}
}

func TestOpenAIResponsesToolLoopDeduplicatesCallAndReplaysReasoningWithFinalArguments(t *testing.T) {
	var requestCount atomic.Int32
	var toolStarts atomic.Int32
	var secondInput atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestCount.Add(1) {
		case 1:
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","content":[]}}`,
				`data: {"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"inspect"}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","content":[]}}`,
				`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"q\":"}`,
				`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":1,"arguments":"{\"q\":\"x\"}"}`,
				`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}]}}`,
				``,
			}, "\n\n"))
		case 2:
			input, _ := captured["input"].([]any)
			secondInput.Store(input)
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"done"}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}}`,
				``,
			}, "\n\n"))
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	runner := &responsesRecordingToolRunner{}
	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "deepseek-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use lookup"}}}},
		Tools:   []ToolDef{{Name: "lookup", Description: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, runner, func(event SseEvent) {
		if event.Type == "tool_start" {
			toolStarts.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if result == nil || len(result.Blocks) == 0 || result.Blocks[len(result.Blocks)-1].Text != "done" {
		t.Fatalf("result = %+v", result)
	}
	// One start comes from the provider stream and one from the executor with
	// the finalized input. A duplicate output_item.added must not add a third.
	if requestCount.Load() != 2 || runner.count.Load() != 1 || toolStarts.Load() != 2 {
		t.Fatalf("requests/tools/starts = %d/%d/%d, want 2/1/2", requestCount.Load(), runner.count.Load(), toolStarts.Load())
	}
	if got, _ := runner.input.Load().(string); got != `{"q":"x"}` {
		t.Fatalf("runner input = %q", got)
	}

	input, _ := secondInput.Load().([]any)
	var replayTypes []string
	var reasoningText, replayArguments string
	var outputCount int
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		itemType, _ := item["type"].(string)
		if itemType == "" {
			continue
		}
		replayTypes = append(replayTypes, itemType)
		switch itemType {
		case "reasoning":
			content, _ := jsonArrayItems(item["content"])
			if len(content) > 0 {
				part, _ := content[0].(map[string]any)
				reasoningText, _ = part["text"].(string)
			}
		case "function_call":
			replayArguments, _ = item["arguments"].(string)
		case "function_call_output":
			outputCount++
		}
	}
	if strings.Join(replayTypes, ",") != "reasoning,function_call,function_call_output" ||
		reasoningText != "inspect" || replayArguments != `{"q":"x"}` || outputCount != 1 {
		t.Fatalf("second request replay = types %v reasoning %q args %q outputs %d", replayTypes, reasoningText, replayArguments, outputCount)
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

type responsesRecordingToolRunner struct {
	count atomic.Int32
	input atomic.Value
}

func (runner *responsesRecordingToolRunner) Run(_ context.Context, _ string, input []byte) (string, []Citation, error) {
	runner.count.Add(1)
	runner.input.Store(string(input))
	return "tool output", nil, nil
}
