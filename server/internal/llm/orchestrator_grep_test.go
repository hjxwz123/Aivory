package llm

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/toolnames"
)

type grepModeTestTools struct {
	toolRouteTestTools
}

func (grepModeTestTools) DocumentGrepToolDefs() []ToolDef {
	schema := json.RawMessage(`{"type":"object"}`)
	return []ToolDef{
		{Name: toolnames.DocList, Description: "List docs", InputSchema: schema},
		{Name: toolnames.DocGrep, Description: "Grep docs", InputSchema: schema},
		{Name: toolnames.DocRead, Description: "Read docs", InputSchema: schema},
	}
}

func (g grepModeTestTools) Run(_ context.Context, name string, input []byte, tc *ToolContext) (string, []Citation, error) {
	if name == toolnames.DocGrep && tc != nil && tc.GrepScope != nil {
		var req rag.GrepSearchRequest
		_ = json.Unmarshal(input, &req)
		res, snips, err := rag.SearchGrep(tc.GrepScope, req)
		if err != nil {
			return "", nil, err
		}
		cites := make([]Citation, 0, len(snips))
		for _, s := range snips {
			cites = append(cites, Citation{ID: s.ID, Index: s.Index, Title: s.Title, URL: s.URL, Snippet: s.Snippet, Source: s.Source})
		}
		return res.Formatted, cites, nil
	}
	return g.toolRouteTestTools.Run(context.Background(), name, input, tc)
}

type grepModeCaptureProvider struct {
	requests []UnifiedChatRequest
	callGrep bool
	toolOut  string
}

func (p *grepModeCaptureProvider) ID() string { return "openai" }

func (p *grepModeCaptureProvider) Stream(
	ctx context.Context,
	req UnifiedChatRequest,
	tools ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.requests = append(p.requests, req)
	var cites []Citation
	if p.callGrep && tools != nil {
		out, c, err := tools.Run(ctx, toolnames.DocGrep, []byte(`{"pattern":"GREP_NEEDLE_888"}`))
		if err != nil {
			return nil, err
		}
		p.toolOut = out
		cites = append(cites, c...)
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "Found [1]."}},
		Citations:  cites,
		StopReason: "stop",
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func TestOrchestratorGrepModeToolCallingAndDisabledToolFallback(t *testing.T) {
	ctx := context.Background()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	orchestrator, _, model, conv, _, db := setupToolRouteTest(t)
	if err := store.SetSetting(db, store.DocumentRetrievalModeSettingKey, store.DocumentRetrievalModeGrep); err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	orchestrator.rag = rag.New(db, nil, logger)
	orchestrator.tools = grepModeTestTools{}

	kb, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		ID:     "kb-grep-1",
		UserID: "u1",
		Name:   "Grep Ops Manual",
	})
	if err != nil {
		t.Fatalf("create KB without embedding model: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES('doc-grep-1',?,'ops.md','text/markdown',200,'ready')`,
		kb.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDocumentText(ctx, db, store.DocumentText{
		DocumentID: "doc-grep-1", KBID: kb.ID,
		Content: "# Ops\nLine 2\nAlert: GREP_NEEDLE_888 detected\nLine 4", LineCount: 4, CharCount: 55, TokenEstimate: 20,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. ToolModeEnabled: doc_list / doc_grep / doc_read are declared, tool runs and produces citations.
	provider := &grepModeCaptureProvider{callGrep: true}
	reg := NewRegistry(logger)
	reg.Register(provider)
	orchestrator.reg = reg

	var toolEvents []SseEvent
	res, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		UserText:                         "Find GREP_NEEDLE_888",
		ToolMode:                         ToolModeEnabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{kb.ID},
	}, func(ev SseEvent) { toolEvents = append(toolEvents, ev) })
	if err != nil {
		t.Fatalf("ToolModeEnabled grep run: %v", err)
	}
	if len(provider.requests) != 1 || !toolDefsContain(provider.requests[0].Tools, toolnames.DocGrep) {
		t.Fatalf("expected doc_grep in declared tools, got %+v", provider.requests[0].Tools)
	}
	if !strings.Contains(provider.toolOut, "L3: Alert: GREP_NEEDLE_888 detected") {
		t.Fatalf("unexpected doc_grep output: %q", provider.toolOut)
	}
	var storedCites []Citation
	if err := json.Unmarshal(res.AssistantMessage.Citations, &storedCites); err != nil || len(storedCites) != 1 || storedCites[0].URL != "kbdoc://doc-grep-1" {
		t.Fatalf("stored citations=%s err=%v", string(res.AssistantMessage.Citations), err)
	}
	assertRequestContextMarker(t, provider.requests[0], "<available-documents-for-grep>", true)

	// 2. ToolModeDisabled: doc_* tools are withheld and server FallbackGrep injects matching lines.
	provider.callGrep = false
	provider.requests = nil
	var fallbackEvents []SseEvent
	if _, err := orchestrator.Run(ctx, RunRequest{
		UserID:                           "u1",
		ConversationID:                   conv.ID,
		ModelID:                          model.ID,
		UserText:                         "What is GREP_NEEDLE_888?",
		ToolMode:                         ToolModeDisabled,
		KnowledgeBaseSelectionConfigured: true,
		KnowledgeBaseIDs:                 []string{kb.ID},
	}, func(ev SseEvent) { fallbackEvents = append(fallbackEvents, ev) }); err != nil {
		t.Fatalf("ToolModeDisabled fallback run: %v", err)
	}
	if len(provider.requests) != 1 || toolDefsContain(provider.requests[0].Tools, toolnames.DocGrep) {
		t.Fatalf("expected doc_grep withheld when tools disabled")
	}
	assertRequestContextMarker(t, provider.requests[0], "GREP_NEEDLE_888 detected", true)
	assertRAGEventPresence(t, fallbackEvents, true)
}

func assertRequestContextMarker(t *testing.T, req UnifiedChatRequest, marker string, want bool) {
	t.Helper()
	found := strings.Contains(req.SystemPrompt, marker)
	for _, msg := range req.History {
		for _, block := range msg.Blocks {
			if strings.Contains(block.Text, marker) {
				found = true
			}
		}
	}
	if found != want {
		t.Fatalf("marker %q presence = %v, want %v", marker, found, want)
	}
}
