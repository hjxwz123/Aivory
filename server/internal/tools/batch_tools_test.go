package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/llm"
)

type batchTestSearcher struct {
	mu      sync.Mutex
	queries []string
	search  func(string) (string, []llm.Citation, error)
}

func (s *batchTestSearcher) Search(_ context.Context, query string, _ int) (string, []llm.Citation, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return s.search(query)
}

func (s *batchTestSearcher) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func TestWebSearchSingleInputKeepsLegacyOutput(t *testing.T) {
	searcher := &batchTestSearcher{search: func(query string) (string, []llm.Citation, error) {
		return "legacy output for " + query, []llm.Citation{{ID: "w1", Index: 1, URL: "https://example.test"}}, nil
	}}
	tool := &webSearchTool{searcher: searcher}
	output, citations, err := tool.Execute(context.Background(), []byte(`{"query":"one query"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if output != "legacy output for one query" || len(citations) != 1 {
		t.Fatalf("single result = %q, %+v", output, citations)
	}
}

func TestWebSearchBatchDeduplicatesAndCombinesCitations(t *testing.T) {
	searcher := &batchTestSearcher{search: func(query string) (string, []llm.Citation, error) {
		return "raw " + query, []llm.Citation{{
			ID: "w1", Index: 1, Title: "Shared", URL: "https://EXAMPLE.test/article#" + query, Snippet: "evidence",
		}}, nil
	}}
	tool := &webSearchTool{searcher: searcher}
	output, citations, err := tool.Execute(context.Background(), []byte(`{
		"query":" Alpha  Query ",
		"queries":["alpha query","Beta Query"]
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := searcher.calls()
	sort.Strings(calls)
	if len(calls) != 2 || calls[0] != "Alpha  Query" || calls[1] != "Beta Query" {
		t.Fatalf("search calls = %#v", calls)
	}
	if len(citations) != 1 || citations[0].Index != 1 {
		t.Fatalf("deduplicated citations = %+v", citations)
	}
	var result webSearchBatchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("batch output is not JSON: %v, %s", err, output)
	}
	if result.Status != "complete" || len(result.Items) != 2 {
		t.Fatalf("batch result = %+v", result)
	}
	for _, item := range result.Items {
		if item.Status != "success" || len(item.CitationIndexes) != 1 || item.CitationIndexes[0] != 1 || !strings.Contains(item.Content, "[1]") {
			t.Fatalf("batch item = %+v", item)
		}
	}
}

func TestWebSearchBatchPreservesPartialSuccess(t *testing.T) {
	searcher := &batchTestSearcher{search: func(query string) (string, []llm.Citation, error) {
		if query == "bad" {
			return "", nil, errors.New("backend unavailable")
		}
		return "useful", []llm.Citation{{URL: "https://example.test/good", Title: "Good"}}, nil
	}}
	tool := &webSearchTool{searcher: searcher}
	output, citations, err := tool.Execute(context.Background(), []byte(`{"queries":["good","bad"]}`), nil)
	if err != nil {
		t.Fatalf("partial batch failed: %v", err)
	}
	var result webSearchBatchResult
	if json.Unmarshal([]byte(output), &result) != nil || result.Status != "partial" || len(citations) != 1 {
		t.Fatalf("partial result = %s, %+v", output, citations)
	}
	if result.Items[0].Status != "success" || result.Items[1].Status != "error" || result.Items[1].Error != "search failed" {
		t.Fatalf("partial items = %+v", result.Items)
	}
}

func TestWebSearchBatchRejectsOversizedInput(t *testing.T) {
	searcher := &batchTestSearcher{search: func(string) (string, []llm.Citation, error) {
		return "unexpected", nil, nil
	}}
	tool := &webSearchTool{searcher: searcher}
	_, _, err := tool.Execute(context.Background(), []byte(`{"queries":["1","2","3","4","5","6"]}`), nil)
	var userErr *llm.ToolUserError
	if !errors.As(err, &userErr) || !strings.Contains(userErr.Message, "at most 5") {
		t.Fatalf("oversized error = %v", err)
	}
	if len(searcher.calls()) != 0 {
		t.Fatalf("oversized batch executed searches: %v", searcher.calls())
	}
}

func TestWebFetchBatchKeepsOldNameAndReturnsStructuredItems(t *testing.T) {
	tool := &webFetchTool{direct: statusTransport{
		code: 200,
		body: "<html><article><h1>Title</h1><p>Batch page body.</p></article></html>",
	}}
	output, citations, err := tool.Execute(context.Background(), []byte(`{
		"url":"http://1.1.1.1/a#fragment",
		"urls":["http://1.1.1.1/a","http://1.0.0.1/b"]
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(citations) != 0 {
		t.Fatalf("web fetch unexpectedly created citations: %+v", citations)
	}
	var result webFetchBatchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("batch output is not JSON: %v, %s", err, output)
	}
	if result.Status != "complete" || len(result.Items) != 2 {
		t.Fatalf("batch fetch result = %+v", result)
	}
	for _, item := range result.Items {
		if item.Status != "success" || !strings.Contains(item.Content, "Batch page body") {
			t.Fatalf("batch fetch item = %+v", item)
		}
	}
}

func TestWebFetchBatchNormalizesURLsBeforeDeduplication(t *testing.T) {
	tool := &webFetchTool{direct: statusTransport{
		code: 200,
		body: "<html><article><p>One normalized page.</p></article></html>",
	}}
	output, _, err := tool.Execute(context.Background(), []byte(`{
		"urls":[
			"https://example.test:443/page?utm_source=one&a=1#first",
			"https://EXAMPLE.test/page?a=1"
		]
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Normalization reduces the request to one item, so the legacy single-item
	// output shape remains in force instead of a one-item batch envelope.
	if output != "One normalized page." {
		t.Fatalf("deduplicated output = %q", output)
	}
}

func TestBatchSchemasExtendExistingTools(t *testing.T) {
	searchSchema := string((&webSearchTool{}).InputSchema())
	fetchSchema := string((&webFetchTool{}).InputSchema())
	if !strings.Contains(searchSchema, `"query"`) || !strings.Contains(searchSchema, `"queries"`) {
		t.Fatalf("search schema = %s", searchSchema)
	}
	if !strings.Contains(fetchSchema, `"url"`) || !strings.Contains(fetchSchema, `"urls"`) {
		t.Fatalf("fetch schema = %s", fetchSchema)
	}
	if (&webSearchTool{}).Name() != "aivory_web_search" || (&webFetchTool{}).Name() != "web_fetch" {
		t.Fatal("batch support changed existing tool names")
	}
}
