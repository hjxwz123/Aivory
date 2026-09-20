package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResponsesMessageIDRepairBeforeFallback(t *testing.T) {
	const badID = "4991bdf7-7458-44c6-a633-2d252c2831dd"
	const idError = "Invalid 'id': message id must be a string starting with 'msg_', got '4991bdf7-7458-44c6-a633-2d252c2831dd'."
	for _, tc := range []struct {
		name                                                            string
		httpError, rejectAgain, otherError, sticky, finalizing, visible bool
	}{
		{name: "SSE validation"},
		{name: "HTTP validation", httpError: true},
		{name: "persistent ID rejection", rejectAgain: true},
		{name: "unrelated failure still falls back", otherError: true},
		{name: "already using fallback", sticky: true},
		{name: "tool finalization", finalizing: true},
		{name: "do not replay output", visible: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits, fallbackHits atomic.Int32
			success := func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2},\"output\":[]}}\n\n")
			}
			handle := func(w http.ResponseWriter, r *http.Request, n int32) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				items, _ := body["input"].([]any)
				message, _ := items[1].(map[string]any)
				want := badID
				if n > 1 {
					want = "msg_" + badID
				}
				if message["id"] != want || message["phase"] != "final_answer" {
					t.Errorf("request %d message = %#v", n, message)
				}
				if n == 1 || tc.rejectAgain {
					if tc.httpError {
						w.WriteHeader(http.StatusBadRequest)
						_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": idError}})
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if tc.visible {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
					}
					_, _ = fmt.Fprintf(w, "data: {\"code\":\"InvalidParameter\",\"message\":%q}\n\n", idError)
					return
				}
				if tc.otherError {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, "temporarily unavailable")
					return
				}
				success(w)
			}
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handle(w, r, primaryHits.Add(1)) }))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := fallbackHits.Add(1)
				if tc.sticky {
					handle(w, r, n)
				} else {
					success(w)
				}
			}))
			defer fallback.Close()
			rec := newProviderRequestRecorder("openai")
			ctx := contextWithProviderRequestRecorder(context.Background(), rec)
			ctx = contextWithProviderVisibleOutput(ctx, new(atomic.Bool))
			if tc.finalizing {
				ctx = contextWithToolFinalization(ctx, &ErrToolBudgetExceeded{Kind: "iterations", Limit: 1})
			}
			flag := new(atomic.Bool)
			flag.Store(tc.sticky)
			raw := json.RawMessage(fmt.Sprintf(`[{"id":%q,"type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"previous","annotations":[]}]}]`, badID))
			result, err := (&OpenAIProvider{}).Stream(ctx, UnifiedChatRequest{
				Model:        ModelInfo{RequestID: "deepseek-v4-flash", APIFormat: "responses", BaseURL: primary.URL, APIKey: "primary", Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback"}},
				FallbackUsed: flag,
				History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "first"}}}, {Role: "assistant", Raw: raw}, {Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "next"}}}},
			}, nil, func(SseEvent) {})
			wantError := tc.rejectAgain || tc.visible
			if (err != nil) != wantError {
				t.Fatalf("err=%v, wantError=%v", err, wantError)
			}
			wantPrimary, wantFallback := int32(2), int32(0)
			if tc.visible {
				wantPrimary = 1
			}
			if tc.otherError {
				wantFallback = 1
			}
			if tc.sticky {
				wantPrimary, wantFallback = 0, 2
			}
			if primaryHits.Load() != wantPrimary || fallbackHits.Load() != wantFallback {
				t.Fatalf("hits primary=%d fallback=%d; want %d/%d", primaryHits.Load(), fallbackHits.Load(), wantPrimary, wantFallback)
			}
			if flag.Load() != (tc.sticky || tc.otherError) {
				t.Fatal("ID rejection changed fallback attribution")
			}
			failures := 0
			for _, snap := range rec.snapshots() {
				if snap.Error != "" {
					failures++
				}
			}
			wantFailures := 0
			if tc.rejectAgain || tc.visible || tc.otherError {
				wantFailures = 1
			}
			if failures != wantFailures {
				t.Fatalf("failure snapshots=%d, want %d", failures, wantFailures)
			}
			if !wantError {
				if result.Usage != (Usage{InputTokens: 5, OutputTokens: 2}) {
					t.Fatalf("usage = %+v", result.Usage)
				}
				if got := providerRequestUsageTotal(rec.snapshots()); got != result.Usage {
					t.Fatalf("recorder usage=%+v, result=%+v", got, result.Usage)
				}
			}
			if strings.Contains(string(raw), "msg_"+badID) {
				t.Fatal("stored history mutated")
			}
		})
	}
}

type messageIDFinalizationToolRunner struct{ calls atomic.Int32 }

func (r *messageIDFinalizationToolRunner) Run(_ context.Context, _ string, input []byte) (string, []Citation, error) {
	r.calls.Add(1)
	return "evidence " + string(input), nil, nil
}

func TestResponsesTwentyToolRoundsFinalizeAfterMessageIDRepair(t *testing.T) {
	t.Setenv("AIVORY_LLM_MAX_ITER_3", "20")
	const badID = "4991bdf7-7458-44c6-a633-2d252c2831dd"
	var requests atomic.Int32
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		http.Error(w, "must not use fallback", http.StatusInternalServerError)
	}))
	defer fallback.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		body := decodeBudgetTestRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 20 {
			if _, ok := body["tools"]; !ok {
				t.Errorf("tools removed before limit at round %d", n)
			}
			items := []map[string]any{}
			if n == 20 {
				items = append(items, map[string]any{
					"id": badID, "type": "message", "role": "assistant", "status": "completed", "phase": "commentary",
					"content": []map[string]any{{"type": "output_text", "text": "Collecting the last result.", "annotations": []any{}}},
				})
			}
			items = append(items, map[string]any{
				"id": fmt.Sprintf("fc_%d", n), "type": "function_call", "call_id": fmt.Sprintf("call_%d", n),
				"name": "lookup", "arguments": fmt.Sprintf(`{"step":%d}`, n), "status": "completed",
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
				"type": "response.completed", "response": map[string]any{
					"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}, "output": items,
				},
			}))
			return
		}
		assertToolFieldsRemoved(t, body)
		if !strings.Contains(stringValue(body["instructions"]), toolBudgetFinalInstruction) {
			t.Error("finalization did not request an answer from existing results")
		}
		items, _ := body["input"].([]any)
		results, messageID := 0, ""
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["type"] == "function_call_output" {
				results++
			}
			if item["type"] == "message" {
				messageID, _ = item["id"].(string)
				if item["phase"] != "commentary" {
					t.Error("repair changed message phase")
				}
			}
		}
		if results != 20 {
			t.Errorf("finalization has %d results, want all 20", results)
		}
		if n == 21 {
			if messageID != badID {
				t.Errorf("initial finalization message ID = %q", messageID)
			}
			_, _ = io.WriteString(w, "data: {\"code\":\"InvalidParameter\",\"message\":\"Invalid 'id': message id must be a string starting with 'msg_', got '4991bdf7-7458-44c6-a633-2d252c2831dd'.\"}\n\n")
			return
		}
		if n != 22 || messageID != "msg_"+badID {
			t.Errorf("repair request %d, message ID %q", n, messageID)
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Answer based on the 20 collected results.\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2},\"output\":[]}}\n\n")
	}))
	defer server.Close()
	rec := newProviderRequestRecorder("openai")
	ctx := contextWithProviderRequestRecorder(context.Background(), rec)
	ctx = contextWithProviderVisibleOutput(ctx, new(atomic.Bool))
	runner := &messageIDFinalizationToolRunner{}
	flag := new(atomic.Bool)
	req := budgetTestRequest(ModelInfo{
		RequestID: "deepseek-v4-flash", BaseURL: server.URL, APIKey: "primary", APIFormat: "responses",
		Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback"},
	})
	req.FallbackUsed = flag
	var visible strings.Builder
	result, err := (&OpenAIProvider{}).Stream(ctx, req, runner, func(ev SseEvent) {
		if ev.Type == "error" {
			t.Errorf("frontend received error: %+v", ev)
		}
		if ev.Type == "text_delta" {
			visible.WriteString(ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("Stream failed at finalization: %v", err)
	}
	if requests.Load() != 22 || runner.calls.Load() != 20 || fallbackHits.Load() != 0 || flag.Load() {
		t.Fatalf("requests=%d tools=%d fallback=%d used=%v", requests.Load(), runner.calls.Load(), fallbackHits.Load(), flag.Load())
	}
	if result.StopReason != "end_turn" || unifiedResultText(result) != "Answer based on the 20 collected results." || visible.String() != unifiedResultText(result) {
		t.Fatalf("final result=%+v, visible=%q", result, visible.String())
	}
	for _, snapshot := range rec.snapshots() {
		if snapshot.Error != "" {
			t.Fatalf("successful finalization left a failure: %s", snapshot.Error)
		}
	}
	if result.Usage != (Usage{InputTokens: 25, OutputTokens: 22}) || providerRequestUsageTotal(rec.snapshots()) != result.Usage {
		t.Fatalf("finalization usage double-counted: %+v", result.Usage)
	}
}
