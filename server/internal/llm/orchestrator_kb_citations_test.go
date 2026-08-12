package llm

import (
	"context"
	"strings"
	"testing"
)

type citationToolRegistry struct{}

func (citationToolRegistry) List(string) []ToolDef { return nil }
func (citationToolRegistry) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "first [1], second [2], incidental [9]", []Citation{
		{ID: "web1", Index: 1, Source: "web"},
		{ID: "web2", Index: 2, Source: "web"},
	}, nil
}

func TestResolvedTurnCitationsPrunesOnlyUnusedKnowledgeSources(t *testing.T) {
	ragCitations := []Citation{
		{ID: "kb1", Index: 1, URL: "doc://doc1", Source: "kb"},
		{ID: "kb2", Index: 2, URL: "doc://doc2", Source: "kb"},
		{ID: "web3", Index: 3, URL: "https://example.test/three", Source: "web"},
		{ID: "conversation-doc", Index: 4, URL: "doc://chat-doc", Source: "document"},
	}
	providerCitations := []Citation{{ID: "web4", Index: 1, URL: "https://example.test/four", Source: "web"}}
	blocks := []UnifiedBlock{
		{Kind: "thinking", Text: "discarded reasoning [1]"},
		{Kind: "text", Text: "Supported claim [2]. Web evidence may remain uncited. `literal [1]`\n```txt\n[1]\n```\nEscaped \\[1]. Link [1](https://example.test)."},
	}

	got := resolvedTurnCitations(ragCitations, providerCitations, blocks, true)
	if len(got) != 4 {
		t.Fatalf("citations=%+v, want used KB, conversation document, and both web sources", got)
	}
	if got[0].ID != "kb2" || got[0].Index != 2 {
		t.Fatalf("used KB citation=%+v, want kb2 with stable index 2", got[0])
	}
	if got[1].ID != "web3" || got[1].Index != 3 || got[2].ID != "conversation-doc" || got[2].Index != 4 || got[3].ID != "web4" || got[3].Index != 5 {
		t.Fatalf("web citations lost stable allocation: %+v", got)
	}
	if ragCitations[0].Index != 1 || providerCitations[0].Index != 1 {
		t.Fatalf("citation resolver mutated its inputs: rag=%+v provider=%+v", ragCitations, providerCitations)
	}
}

func TestResolvedTurnCitationsKeepsLegacyRenumberingWithoutKnowledgeBase(t *testing.T) {
	got := resolvedTurnCitations(
		[]Citation{{ID: "conversation-doc", Index: 8, Source: "document"}},
		[]Citation{{ID: "web", Index: 9, Source: "web"}},
		nil,
		false,
	)
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("legacy citations=%+v, want unchanged append-and-renumber behavior", got)
	}
}

func TestFormatRAGContextUsesStableCitationIndexes(t *testing.T) {
	contextText := formatRAGContext([]Citation{{Index: 4, Title: "Policy", Snippet: "Clause"}}, "en")
	if !strings.Contains(contextText, "[4] Policy\nClause") {
		t.Fatalf("RAG context did not preserve source index: %q", contextText)
	}
}

func TestToolCitationsUseKnowledgeBaseTurnIndexNamespace(t *testing.T) {
	orchestrator := &Orchestrator{tools: citationToolRegistry{}}
	events := []SseEvent{}
	runner := &orchToolRunner{
		orch: orchestrator,
		ctx:  &ToolContext{citationIndexes: &citationIndexAllocator{next: 4}},
		onEvent: func(event SseEvent) {
			events = append(events, event)
		},
	}

	out, citations, err := runner.Run(context.Background(), "aivory_web_search", nil)
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if out != "first [5], second [6], incidental [9]" {
		t.Fatalf("remapped tool output=%q", out)
	}
	if len(citations) != 2 || citations[0].Index != 5 || citations[1].Index != 6 ||
		!citations[0].GlobalIndex || !citations[1].GlobalIndex {
		t.Fatalf("tool citations=%+v", citations)
	}
	if len(events) != 2 || events[0].Citation == nil || events[0].Citation.Index != 5 ||
		events[1].Citation == nil || events[1].Citation.Index != 6 {
		t.Fatalf("citation events=%+v", events)
	}

	resolved := resolvedTurnCitations(
		[]Citation{{ID: "kb1", Index: 1, Source: "kb"}, {ID: "kb4", Index: 4, Source: "kb"}},
		citations,
		[]UnifiedBlock{{Kind: "text", Text: "knowledge [4], web [5] and [6]"}},
		true,
	)
	if len(resolved) != 3 || resolved[0].Index != 4 || resolved[1].Index != 5 || resolved[2].Index != 6 {
		t.Fatalf("resolved citation namespace=%+v", resolved)
	}
}

func TestToolCitationOutputIsUnchangedWithoutKnowledgeBase(t *testing.T) {
	orchestrator := &Orchestrator{tools: citationToolRegistry{}}
	runner := &orchToolRunner{orch: orchestrator, ctx: &ToolContext{}}

	out, citations, err := runner.Run(context.Background(), "aivory_web_search", nil)
	if err != nil {
		t.Fatalf("run tool: %v", err)
	}
	if out != "first [1], second [2], incidental [9]" || len(citations) != 2 ||
		citations[0].Index != 1 || citations[1].Index != 2 || citations[0].GlobalIndex || citations[1].GlobalIndex {
		t.Fatalf("legacy no-KB tool result changed: out=%q citations=%+v", out, citations)
	}
}

func TestHostedCitationEventsAndResultsShareGlobalIndex(t *testing.T) {
	allocator := &citationIndexAllocator{next: 3}
	live := allocator.normalize(Citation{ID: "live", Index: 1, URL: "https://source.test/a", Source: "web"})
	resultCopy := allocator.normalize(Citation{ID: "result", Index: 1, URL: "https://source.test/a", Source: "web"})
	next := allocator.normalize(Citation{ID: "next", Index: 2, URL: "https://source.test/b", Source: "web"})

	if live.Index != 4 || resultCopy.Index != 4 || next.Index != 5 ||
		!live.GlobalIndex || !resultCopy.GlobalIndex || !next.GlobalIndex {
		t.Fatalf("hosted citation indexes live=%+v result=%+v next=%+v", live, resultCopy, next)
	}
}
