package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"aivory/server/internal/llm"
	"aivory/server/internal/rag"
	"aivory/server/internal/toolnames"
)

func snippetsToCitations(snippets []rag.Snippet) []llm.Citation {
	if len(snippets) == 0 {
		return nil
	}
	cites := make([]llm.Citation, 0, len(snippets))
	for _, s := range snippets {
		cites = append(cites, llm.Citation{
			ID:      s.ID,
			Index:   s.Index,
			Title:   s.Title,
			URL:     s.URL,
			Snippet: s.Snippet,
			Source:  s.Source,
		})
	}
	return cites
}

type docListTool struct{}

func (t *docListTool) Name() string { return toolnames.DocList }
func (t *docListTool) Description() string {
	return "List the documents currently accessible in this conversation (knowledge base documents and attached files), including document_id, filename, line count, and section headings. Use this first when you need to discover available documents or inspect their structure before searching or reading."
}
func (t *docListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *docListTool) Execute(_ context.Context, _ []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	if tc == nil || tc.GrepScope == nil {
		return "No documents are available in the current scope.", nil, nil
	}
	return rag.FormatGrepDocumentList(tc.GrepScope), nil, nil
}

type docGrepTool struct{}

func (t *docGrepTool) Name() string { return toolnames.DocGrep }
func (t *docGrepTool) Description() string {
	return "Search across accessible documents using exact text or RE2 regular expressions (similar to grep -n -C). Returns matching lines with 1-based line numbers, surrounding context, and citation indexes [1], [2], ... Prefer concise patterns; filter by document_ids when targeting specific files."
}
func (t *docGrepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Search keyword or RE2 regular expression."
    },
    "regex": {
      "type": "boolean",
      "description": "Set true to interpret pattern as an RE2 regular expression; default false (literal substring match)."
    },
    "case_sensitive": {
      "type": "boolean",
      "description": "Set true for case-sensitive matching; default false."
    },
    "document_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional list of document_id values to restrict the search scope."
    },
    "context_lines": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10,
      "description": "Number of context lines before and after each match."
    },
    "max_matches": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Optional cap on returned match lines."
    }
  },
  "required": ["pattern"]
}`)
}
func (t *docGrepTool) Execute(_ context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	if tc == nil || tc.GrepScope == nil {
		return "No documents are available in the current scope.", nil, nil
	}
	var req rag.GrepSearchRequest
	if err := json.Unmarshal(input, &req); err != nil {
		return "", nil, &llm.ToolUserError{Message: "invalid doc_grep input"}
	}
	if strings.TrimSpace(req.Pattern) == "" {
		return "", nil, &llm.ToolUserError{Message: "pattern is required"}
	}
	res, snippets, err := rag.SearchGrep(tc.GrepScope, req)
	if err != nil {
		return "", nil, &llm.ToolUserError{Message: err.Error()}
	}
	return res.Formatted, snippetsToCitations(snippets), nil
}

type docReadTool struct{}

func (t *docReadTool) Name() string { return toolnames.DocRead }
func (t *docReadTool) Description() string {
	return "Read a range of 1-based lines from one accessible document by document_id. Use this after doc_list or doc_grep to inspect full paragraphs, tables, code blocks, or neighboring lines."
}
func (t *docReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "document_id": {
      "type": "string",
      "description": "The document_id to read from."
    },
    "start_line": {
      "type": "integer",
      "minimum": 1,
      "description": "1-based starting line number (default 1)."
    },
    "line_count": {
      "type": "integer",
      "minimum": 1,
      "description": "Number of lines to read (bounded by server limit)."
    }
  },
  "required": ["document_id"]
}`)
}
func (t *docReadTool) Execute(_ context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	if tc == nil || tc.GrepScope == nil {
		return "No documents are available in the current scope.", nil, nil
	}
	var req struct {
		DocumentID string `json:"document_id"`
		StartLine  int    `json:"start_line"`
		LineCount  int    `json:"line_count"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return "", nil, &llm.ToolUserError{Message: "invalid doc_read input"}
	}
	if strings.TrimSpace(req.DocumentID) == "" {
		return "", nil, &llm.ToolUserError{Message: "document_id is required"}
	}
	res, snippet, err := rag.ReadGrepLines(tc.GrepScope, req.DocumentID, req.StartLine, req.LineCount)
	if err != nil {
		return "", nil, &llm.ToolUserError{Message: fmt.Sprintf("read document failed: %v", err)}
	}
	var cites []llm.Citation
	if snippet != nil {
		cites = snippetsToCitations([]rag.Snippet{*snippet})
	}
	return res.Formatted, cites, nil
}
