package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

// §4.13-B: forcing tool_mode=none drops the whole tool-guidance segment from
// the system prompt (the same gate the orchestrator relies on when NoTools is
// set), while a normal turn with tools keeps it.
func TestComposeSystemPromptNoToolsDropsToolGuidance(t *testing.T) {
	withTools := composeSystemPrompt(systemPromptOpts{
		ModelLabel: "GPT-X", ToolMode: "native", ToolNames: []string{"web_search", "python_execute"},
	})
	if !strings.Contains(withTools, "Tool guidance") {
		t.Fatalf("native turn should include tool guidance:\n%s", withTools)
	}

	none := composeSystemPrompt(systemPromptOpts{
		ModelLabel: "GPT-X", ToolMode: "none", ToolNames: nil,
	})
	if strings.Contains(none, "Tool guidance") {
		t.Fatalf("no-tools turn must NOT include tool guidance:\n%s", none)
	}
	if strings.Contains(none, "web_search for time-sensitive") {
		t.Fatalf("no-tools turn must NOT advertise web_search:\n%s", none)
	}
	// Identity + trust boundary still present (they are tool-independent).
	if !strings.Contains(none, "You are GPT-X") || !strings.Contains(none, "Trust boundary") {
		t.Fatalf("no-tools prompt lost tool-independent segments:\n%s", none)
	}
}

// fakeSearchRegistry implements llm.ToolRegistry, returning canned web_search
// output so forcedWebSearch can be exercised without a live searcher.
type fakeSearchRegistry struct {
	out   string
	cites []Citation
	calls []string // queries seen
}

func (f *fakeSearchRegistry) List(string) []ToolDef { return nil }
func (f *fakeSearchRegistry) Run(_ context.Context, name string, input []byte, _ *ToolContext) (string, []Citation, error) {
	if name != "web_search" {
		return "", nil, nil
	}
	var in struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(input, &in)
	f.calls = append(f.calls, in.Query)
	return f.out, f.cites, nil
}

func TestForcedWebSearchInjectsResults(t *testing.T) {
	reg := &fakeSearchRegistry{
		out:   "Result: Aurelia is a chat app.",
		cites: []Citation{{ID: "w1", Title: "Aurelia", URL: "https://a.example", Source: "web"}},
	}
	o := &Orchestrator{tools: reg} // o.task nil → queries fall back to the raw user text
	var events []SseEvent
	text, cites := o.forcedWebSearch(
		context.Background(),
		RunRequest{UserID: "u1", ConversationID: "c1", ModelID: "m1", UserText: "what is aurelia"},
		&store.Conversation{ID: "c1"},
		nil,
		2, // two KB snippets already numbered this turn → web cite continues at 3
		func(ev SseEvent) { events = append(events, ev) },
	)
	if !strings.Contains(text, "<web-search-result>") || !strings.Contains(text, "Aurelia is a chat app") {
		t.Fatalf("injected block missing search content: %q", text)
	}
	if len(cites) != 1 || cites[0].URL != "https://a.example" || cites[0].Index != 3 {
		t.Fatalf("citations not collected/offset past base index: %+v", cites)
	}
	if len(reg.calls) != 1 || reg.calls[0] != "what is aurelia" {
		t.Fatalf("query fallback wrong: %+v", reg.calls)
	}
	// Progress must stream to the reply area: a tool_start, a tool_result, a citation.
	var start, result, cite bool
	for _, e := range events {
		switch e.Type {
		case "tool_start":
			start = e.Name == "web_search"
		case "tool_result":
			result = e.Name == "web_search" && e.Status == "complete"
		case "citation":
			cite = e.Citation != nil
		}
	}
	if !start || !result || !cite {
		t.Fatalf("missing progress events: start=%v result=%v cite=%v", start, result, cite)
	}
}

// §4.13-B wire contract: a no-tools turn reaches the orchestrator as an
// empty-Tools UnifiedChatRequest (NoTools forces toolMode=none, so toolDefs is
// never populated) — and with empty Tools, NONE of the four provider wire
// formats may emit a tool-calling field in the upstream request body.
func TestEmptyToolsCarriesNoToolFieldsOnWire(t *testing.T) {
	toolFields := []string{`"tools"`, `"tool_choice"`, `"functions"`, `"function_call"`, `"toolConfig"`, `"tool_config"`}
	assertNoToolFields := func(t *testing.T, body []byte) {
		t.Helper()
		for _, f := range toolFields {
			if strings.Contains(string(body), f) {
				t.Fatalf("no-tools request leaked %s onto the wire\nbody: %s", f, string(body))
			}
		}
	}
	capture := func(stream string) (*httptest.Server, *[]byte) {
		var captured []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(stream))
		}))
		return srv, &captured
	}
	history := []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}}
	openAIChatStream := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"
	openAIRespStream := `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n"

	t.Run("anthropic", func(t *testing.T) {
		srv, captured := capture(anthropicTextStream("ok"))
		defer srv.Close()
		p := &AnthropicProvider{}
		req := UnifiedChatRequest{Model: ModelInfo{RequestID: "claude-test", BaseURL: srv.URL, APIKey: "k"}, History: history}
		if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertNoToolFields(t, *captured)
	})
	t.Run("openai-chat", func(t *testing.T) {
		srv, captured := capture(openAIChatStream)
		defer srv.Close()
		p := &OpenAIProvider{}
		req := UnifiedChatRequest{Model: ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k"}, History: history}
		if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertNoToolFields(t, *captured)
	})
	t.Run("openai-responses", func(t *testing.T) {
		srv, captured := capture(openAIRespStream)
		defer srv.Close()
		p := &OpenAIProvider{}
		req := UnifiedChatRequest{Model: ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"}, History: history}
		if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertNoToolFields(t, *captured)
	})
	t.Run("google", func(t *testing.T) {
		srv, captured := capture(geminiTextStream("ok"))
		defer srv.Close()
		p := &GoogleProvider{}
		req := UnifiedChatRequest{Model: ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"}, History: history}
		if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		assertNoToolFields(t, *captured)
	})
	// Positive control: WITH a tool the field appears — proving the assertions
	// above would catch a leak rather than passing vacuously.
	t.Run("anthropic-with-tools-control", func(t *testing.T) {
		srv, captured := capture(anthropicTextStream("ok"))
		defer srv.Close()
		p := &AnthropicProvider{}
		req := UnifiedChatRequest{
			Model:   ModelInfo{RequestID: "claude-test", BaseURL: srv.URL, APIKey: "k"},
			History: history,
			Tools:   []ToolDef{{Name: "web_search", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		}
		if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(*captured), `"tools"`) {
			t.Fatalf("control failed: tools field missing when tools ARE configured\nbody: %s", string(*captured))
		}
	})
}

// §4.13-B history compat: a turn that declares NO native tools (disable-tools,
// tool_mode none/prompt) must not replay the stored native tool exchange —
// providers 400 on tool_use/tool_result blocks without a tools param. The tool
// rounds instead degrade to their text trace via the block path.
func TestStoreToUnifiedStripsRawWithoutNativeTools(t *testing.T) {
	raw := json.RawMessage(`[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"web_search","input":{"query":"q"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"result"}]},{"role":"assistant","content":[{"type":"text","text":"answer"}]}]`)
	msgs := []store.Message{
		{Role: "user", Blocks: json.RawMessage(`[{"kind":"text","text":"question"}]`), Status: "complete"},
		{Role: "assistant", Provider: "anthropic", Raw: raw, Status: "complete",
			Blocks: json.RawMessage(`[{"kind":"tool_call","tool_name":"web_search","summary":"searched the web"},{"kind":"text","text":"answer"}]`)},
	}

	// Native tools declared → raw replays verbatim (unchanged behavior).
	with := storeToUnified(msgs, "anthropic", true)
	if len(with) != 2 || len(with[1].Raw) == 0 {
		t.Fatalf("native-tool turn must keep raw replay: %+v", with)
	}
	if body, _ := json.Marshal(historyToAnthropic(with)); !strings.Contains(string(body), `"tool_use"`) {
		t.Fatalf("raw replay should splice tool_use into the wire history: %s", body)
	}

	// No native tools → raw stripped, wire history has NO tool blocks, and the
	// tool round survives as readable text.
	without := storeToUnified(msgs, "anthropic", false)
	if len(without) != 2 {
		t.Fatalf("stripped history lost a turn: %+v", without)
	}
	if len(without[1].Raw) != 0 {
		t.Fatalf("raw must be stripped on a no-native-tools turn: %s", without[1].Raw)
	}
	body, _ := json.Marshal(historyToAnthropic(without))
	for _, banned := range []string{`"tool_use"`, `"tool_result"`} {
		if strings.Contains(string(body), banned) {
			t.Fatalf("no-native-tools history leaked %s: %s", banned, body)
		}
	}
	if !strings.Contains(string(body), "web_search") || !strings.Contains(string(body), "answer") {
		t.Fatalf("tool round must degrade to a text trace: %s", body)
	}
	// The caller's slice must not be mutated by the strip.
	if len(msgs[1].Raw) == 0 {
		t.Fatal("storeToUnified must not mutate the caller's messages")
	}
}

func TestForcedWebSearchUnconfiguredInjectsNothing(t *testing.T) {
	reg := &fakeSearchRegistry{out: "Search not yet configured. Reply based on training knowledge."}
	o := &Orchestrator{tools: reg}
	text, cites := o.forcedWebSearch(
		context.Background(),
		RunRequest{UserID: "u1", ConversationID: "c1", UserText: "hi"},
		&store.Conversation{ID: "c1"},
		nil,
		0,
		func(SseEvent) {},
	)
	if text != "" || cites != nil {
		t.Fatalf("unconfigured search must inject nothing, got text=%q cites=%+v", text, cites)
	}
}
