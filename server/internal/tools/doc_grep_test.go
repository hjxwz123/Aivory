package tools

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/rag"
	"aivory/server/internal/toolnames"
)

func TestDocumentGrepToolsHiddenFromCatalogAndExecuteWithinScope(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))

	for _, def := range registry.List("") {
		if toolnames.IsDocumentTool(def.Name) {
			t.Fatalf("document tool %q leaked into regular Registry.List", def.Name)
		}
	}
	for _, def := range registry.ListRegistered() {
		if toolnames.IsDocumentTool(def.Name) {
			t.Fatalf("document tool %q leaked into Registry.ListRegistered", def.Name)
		}
	}

	docDefs := registry.DocumentGrepToolDefs()
	if len(docDefs) != 3 {
		t.Fatalf("DocumentGrepToolDefs count = %d, want 3", len(docDefs))
	}

	scope := &rag.GrepScope{
		Settings: rag.GrepSettings{
			MaxMatches:   20,
			ContextLines: 1,
			ReadMaxLines: 50,
			ReadMaxChars: 4000,
		},
		Documents: []rag.GrepDocument{
			{
				DocumentID: "doc-1",
				KBID:       "kb-1",
				Filename:   "runbook.md",
				Source:     "kb",
				URL:        "kbdoc://doc-1",
				LineCount:  3,
				Content:    "# Runbook\nRotate token daily.\nCheck alert_code_99.",
				Lines:      []string{"# Runbook", "Rotate token daily.", "Check alert_code_99."},
			},
		},
	}
	scope.ByID = map[string]*rag.GrepDocument{"doc-1": &scope.Documents[0]}

	tc := &llm.ToolContext{
		BuiltinTools: map[string]bool{
			toolnames.DocList: true,
			toolnames.DocGrep: true,
			toolnames.DocRead: true,
		},
		GrepScope: scope,
	}

	listOut, listCites, err := registry.Run(context.Background(), toolnames.DocList, []byte(`{}`), tc)
	if err != nil || len(listCites) != 0 || !strings.Contains(listOut, "doc-1") {
		t.Fatalf("doc_list out=%q cites=%d err=%v", listOut, len(listCites), err)
	}

	grepOut, grepCites, err := registry.Run(context.Background(), toolnames.DocGrep, []byte(`{"pattern":"alert_code_99"}`), tc)
	if err != nil || len(grepCites) != 1 || !strings.Contains(grepOut, "L3: Check alert_code_99.") {
		t.Fatalf("doc_grep out=%q cites=%+v err=%v", grepOut, grepCites, err)
	}
	if grepCites[0].URL != "kbdoc://doc-1" || grepCites[0].Source != "kb" {
		t.Fatalf("unexpected doc_grep citation: %+v", grepCites[0])
	}

	readOut, readCites, err := registry.Run(context.Background(), toolnames.DocRead, []byte(`{"document_id":"doc-1","start_line":2,"line_count":2}`), tc)
	if err != nil || len(readCites) != 1 || !strings.Contains(readOut, "L2: Rotate token daily.") {
		t.Fatalf("doc_read out=%q cites=%+v err=%v", readOut, readCites, err)
	}

	if _, _, err := registry.Run(context.Background(), toolnames.DocRead, []byte(`{"document_id":"doc-forbidden"}`), tc); err == nil {
		t.Fatal("doc_read allowed access to out-of-scope document")
	}
}
