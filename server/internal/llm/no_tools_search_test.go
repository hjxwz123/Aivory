package llm

import (
	"context"
	"encoding/json"
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
