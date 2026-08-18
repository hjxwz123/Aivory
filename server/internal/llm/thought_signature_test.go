package llm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestGeminiRawCallsAllSigned guards the gate that decides whether a stored
// Gemini tool turn can be replayed verbatim. Any bare functionCall part must
// flip it to false so the caller downgrades to block→text instead of 400ing the
// whole request (§ thought-signatures).
func TestGeminiRawCallsAllSigned(t *testing.T) {
	parse := func(s string) []map[string]any {
		var turns []map[string]any
		if err := json.Unmarshal([]byte(s), &turns); err != nil {
			t.Fatalf("bad fixture: %v", err)
		}
		return turns
	}

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "signed call (camelCase) passes",
			raw:  `[{"role":"model","parts":[{"functionCall":{"name":"python_execute"},"thoughtSignature":"abc"}]},{"role":"user","parts":[{"functionResponse":{"name":"python_execute"}}]}]`,
			want: true,
		},
		{
			name: "signed call (snake_case) passes",
			raw:  `[{"role":"model","parts":[{"functionCall":{"name":"x"},"thought_signature":"abc"}]}]`,
			want: true,
		},
		{
			name: "bare call fails (pre-fix stored Raw → position-N 400)",
			raw:  `[{"role":"model","parts":[{"text":"sure"},{"functionCall":{"name":"python_execute"}}]},{"role":"user","parts":[{"functionResponse":{"name":"python_execute"}}]}]`,
			want: false,
		},
		{
			name: "empty-string signature counts as bare",
			raw:  `[{"role":"model","parts":[{"functionCall":{"name":"x"},"thoughtSignature":""}]}]`,
			want: false,
		},
		{
			name: "text-only turn (no calls) passes",
			raw:  `[{"role":"model","parts":[{"text":"hello"}]}]`,
			want: true,
		},
		{
			name: "one signed + one bare across turns fails",
			raw:  `[{"role":"model","parts":[{"functionCall":{"name":"a"},"thoughtSignature":"sig"}]},{"role":"user","parts":[{"functionResponse":{"name":"a"}}]},{"role":"model","parts":[{"functionCall":{"name":"b"}}]}]`,
			want: false,
		},
	}
	for _, c := range cases {
		if got := geminiRawCallsAllSigned(parse(c.raw)); got != c.want {
			t.Errorf("%s: geminiRawCallsAllSigned = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestGeminiFunctionCallPartSignature locks the emit-path guarantee: a
// functionCall part is NEVER replayed bare. Real signatures win; the most-recent
// fallback is used next; and as a last resort the documented bypass sentinel is
// injected so Gemini 3 doesn't hard-400 the live tool loop.
func TestGeminiFunctionCallPartSignature(t *testing.T) {
	fc := map[string]any{"name": "python_execute"}

	// Real part-level signature is preferred and echoed verbatim.
	got := geminiFunctionCallPart(map[string]any{"thoughtSignature": "real-sig"}, fc, "fallback")
	if got["thoughtSignature"] != "real-sig" {
		t.Errorf("part sig: got %v, want real-sig", got["thoughtSignature"])
	}

	// No part sig → fall back to the most-recent signature seen this turn.
	got = geminiFunctionCallPart(map[string]any{}, fc, "fallback-sig")
	if got["thoughtSignature"] != "fallback-sig" {
		t.Errorf("fallback sig: got %v, want fallback-sig", got["thoughtSignature"])
	}

	// No sig anywhere → sentinel, never bare.
	got = geminiFunctionCallPart(map[string]any{}, fc, "")
	if got["thoughtSignature"] != geminiSkipSigSentinel {
		t.Errorf("sentinel: got %v, want %s", got["thoughtSignature"], geminiSkipSigSentinel)
	}
	if _, ok := got["thoughtSignature"]; !ok {
		t.Error("functionCall part went back bare (no thoughtSignature key)")
	}

	// Sentinel must decode to Google's documented literal.
	dec, err := base64.StdEncoding.DecodeString(geminiSkipSigSentinel)
	if err != nil || string(dec) != "skip_thought_signature_validator" {
		t.Errorf("sentinel decode = %q (err %v), want skip_thought_signature_validator", string(dec), err)
	}
}

func TestGeminiRawReplayDropsStandaloneThoughtSignaturePart(t *testing.T) {
	history := []UnifiedMessage{{
		Role: "assistant",
		Blocks: []UnifiedBlock{{
			Kind: "tool_call", ToolName: "python_execute", ToolID: "python_execute",
		}},
		Raw: json.RawMessage(`[
			{"role":"model","parts":[
				{"thoughtSignature":"orphan-metadata"},
				{"functionCall":{"name":"python_execute","args":{}},"thoughtSignature":"real-sig"}
			]},
			{"role":"user","parts":[{"functionResponse":{"name":"python_execute","response":{"content":"ok"}}}]}
		]`),
	}}

	wire, err := json.Marshal(historyToGemini(history, false))
	if err != nil {
		t.Fatal(err)
	}
	body := string(wire)
	if strings.Contains(body, "orphan-metadata") {
		t.Fatalf("metadata-only Gemini part survived raw replay: %s", body)
	}
	if !strings.Contains(body, `"functionCall"`) || !strings.Contains(body, `"functionResponse"`) {
		t.Fatalf("valid native tool history was lost while sanitizing: %s", body)
	}
}

func TestReadGeminiStreamDoesNotPersistStandaloneThoughtSignaturePart(t *testing.T) {
	stream := `data: {"candidates":[{"content":{"parts":[` +
		`{"thoughtSignature":"stream-sig"},` +
		`{"functionCall":{"name":"python_execute","args":{}}}` +
		`]},"finishReason":"STOP"}]}` + "\n\n"

	_, _, calls, modelParts, _, _, err := readGeminiStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("readGeminiStream: %v", err)
	}
	if len(calls) != 1 || len(modelParts) != 1 {
		t.Fatalf("calls=%#v modelParts=%#v, want one signed function call part", calls, modelParts)
	}
	if _, exists := modelParts[0]["functionCall"]; !exists {
		t.Fatalf("functionCall missing after signature filtering: %#v", modelParts[0])
	}
	if modelParts[0]["thoughtSignature"] != "stream-sig" {
		t.Fatalf("standalone signature was not transferred to functionCall: %#v", modelParts[0])
	}
	if !geminiPartHasData(modelParts[0]) {
		t.Fatalf("persisted Gemini part has no data oneof: %#v", modelParts[0])
	}
}
