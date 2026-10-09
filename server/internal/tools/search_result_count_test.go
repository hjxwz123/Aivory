package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func searchCountResults(query string, start, count int, snippet string) []searxngResult {
	results := make([]searxngResult, count)
	for i := range results {
		results[i] = searxngResult{
			Title: fmt.Sprintf("%s result %d", query, start+i),
			URL:   fmt.Sprintf("https://example.test/%s/%d", query, start+i), Content: snippet,
		}
	}
	return results
}

func TestSearxngResultCountSettingIsLiveAndOverridesOldTopK(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "search-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	snippet := strings.Repeat("完整摘要", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(searxngPage{Results: searchCountResults("live", 0, 25, " \n"+snippet+"\t end ")})
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: newSettingsSearcher(db, "searxng", "", srv.URL)}
	if searchSchemaProperties(t, tool)["top_k"] == nil {
		t.Fatal("legacy installation lost top_k")
	}
	_, citations, err := tool.Execute(t.Context(), []byte(`{"query":"live","top_k":5}`), nil)
	if err != nil || len(citations) != 5 {
		t.Fatalf("legacy count = %d, err=%v", len(citations), err)
	}
	for _, count := range []int{20, 2, 50} {
		if err := store.SetSetting(db, "search_result_count", count); err != nil {
			t.Fatal(err)
		}
		if searchSchemaProperties(t, tool)["top_k"] != nil {
			t.Fatal("model can still select top_k after admin configured count")
		}
		before := requests.Load()
		output, citations, err := tool.Execute(t.Context(), []byte(`{"query":"live","top_k":5}`), nil)
		want := min(count, 25)
		if err != nil || len(citations) != want {
			t.Fatalf("configured count %d = %d, err=%v", count, len(citations), err)
		}
		if citations[0].Snippet != snippet+" end" || !strings.Contains(output, snippet+" end") {
			t.Fatal("SearXNG snippet was truncated or not normalized")
		}
		wantRequests := int32(1)
		if count == 50 {
			wantRequests = 2 // Repeated page ends pagination.
		}
		if requests.Load()-before != wantRequests {
			t.Fatalf("configured count %d requested %d pages", count, requests.Load()-before)
		}
	}
	// A stale caller cannot exceed the administrator's result count either.
	if err := store.SetSetting(db, "search_result_count", 2); err != nil {
		t.Fatal(err)
	}
	_, citations, err = tool.Execute(t.Context(), []byte(`{"query":"live","top_k":100}`), nil)
	if err != nil || len(citations) != 2 {
		t.Fatalf("stale caller bypassed admin count: %d, %v", len(citations), err)
	}
	if err := store.SetSetting(db, "search_provider", "duckduckgo"); err != nil {
		t.Fatal(err)
	}
	if searchSchemaProperties(t, tool)["top_k"] == nil {
		t.Fatal("SearXNG setting affected another provider's schema")
	}
	if _, _, err := tool.Execute(t.Context(), []byte(`{"query":"live","top_k":11}`), nil); err == nil {
		t.Fatal("another provider lost its existing top_k validation")
	}
}

func TestSearxngConfiguredCountPaginatesAndPreservesFilters(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		pages = append(pages, params.Get("pageno"))
		for key, want := range map[string]string{
			"q": "今日新闻 & 市场", "engines": "bing", "categories": "news",
			"time_range": "day", "language": "zh-CN", "safesearch": "2", "format": "json",
		} {
			if params.Get(key) != want {
				t.Errorf("page %s %s=%q, want %q", params.Get("pageno"), key, params.Get(key), want)
			}
		}
		if r.URL.Path != "/mounted/search" || r.Header.Get("Accept-Language") != "zh-CN" {
			t.Errorf("lost instance subpath or language: %s, %s", r.URL, r.Header.Get("Accept-Language"))
		}
		page, _ := strconv.Atoi(params.Get("pageno"))
		// First page 0..9, second 5..14, third 10..19.
		_ = json.NewEncoder(w).Encode(searxngPage{Results: searchCountResults("filtered", (page-2)*5, 10, "full snippet")})
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL + "/mounted", engines: []string{"bing", "wikipedia"}, resultCount: 20}}
	_, citations, err := tool.Execute(t.Context(), []byte(`{
		"query":"今日新闻 & 市场","top_k":5,"pageno":2,"engines":["bing"],
		"categories":["news"],"time_range":"day","language":"zh-CN","safesearch":2
	}`), nil)
	if err != nil || len(citations) != 20 || strings.Join(pages, ",") != "2,3,4" {
		t.Fatalf("paginated search: %d citations, pages=%v, err=%v", len(citations), pages, err)
	}
	for i, citation := range citations {
		if citation.Index != i+1 || citation.URL != fmt.Sprintf("https://example.test/filtered/%d", i) {
			t.Fatalf("duplicate or misindexed citation: %+v", citation)
		}
	}
}

func TestSearxngBatchCountIsPerKeywordAndKeepsFullSnippets(t *testing.T) {
	var requests atomic.Int32
	snippet := strings.Repeat("完整摘要", 150)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("pageno"))
		if page == 0 {
			page = 1
		}
		_ = json.NewEncoder(w).Encode(searxngPage{Results: searchCountResults(r.URL.Query().Get("q"), (page-1)*10, 10, snippet)})
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL, resultCount: 20}}
	output, citations, err := tool.Execute(t.Context(), []byte(`{"queries":["first","second"],"top_k":5}`), nil)
	var batch webSearchBatchResult
	if err != nil || json.Unmarshal([]byte(output), &batch) != nil || len(citations) != 40 || requests.Load() != 4 {
		t.Fatalf("batch result: citations=%d requests=%d err=%v output=%s", len(citations), requests.Load(), err, output)
	}
	for _, item := range batch.Items {
		if len(item.CitationIndexes) != 20 || !strings.Contains(item.Content, snippet) {
			t.Fatalf("per-keyword count/snippet lost for %s: %d indexes", item.Query, len(item.CitationIndexes))
		}
	}
}

func TestSearxngPaginationKeepsPartialResults(t *testing.T) {
	for _, mode := range []string{"empty", "repeated", "http_error", "bad_json", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := requests.Add(1)
				if call == 1 || mode == "repeated" {
					_ = json.NewEncoder(w).Encode(searxngPage{Results: searchCountResults("partial", 0, 2, "retained")})
					return
				}
				switch mode {
				case "empty":
					_, _ = w.Write([]byte(`{"results":[]}`))
				case "http_error":
					w.WriteHeader(http.StatusTooManyRequests)
				case "bad_json":
					_, _ = w.Write([]byte(`<html>proxy error</html>`))
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			searcher := &searxngSearcher{baseURL: srv.URL, resultCount: 20}
			output, citations, err := searcher.Search(ctx, "partial", 5)
			if err != nil || len(citations) != 2 || requests.Load() != 2 || !strings.Contains(output, "Returned 2 of 20 requested results") {
				t.Fatalf("partial results lost: %d, requests=%d, err=%v output=%q", len(citations), requests.Load(), err, output)
			}
		})
	}
}

func TestSearxngPaginationStopsAtPageLimit(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(searxngPage{Results: searchCountResults(r.URL.Query().Get("pageno"), 0, 1, "snippet")})
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL, resultCount: 50}}
	output, citations, err := tool.Execute(t.Context(), []byte(`{"query":"limited"}`), nil)
	if err != nil || len(citations) != 10 || requests.Load() != 10 || !strings.Contains(output, "page limit") {
		t.Fatalf("pagination did not stop: citations=%d requests=%d err=%v output=%s", len(citations), requests.Load(), err, output)
	}
}

func TestSearxngConfiguredCountKeepsDistinctImagesOnOnePage(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"results":[
			{"url":"https://source.test/gallery","img_src":"https://images.test/1.jpg"},
			{"url":"https://source.test/gallery","img_src":"https://images.test/1.jpg"},
			{"url":"https://source.test/gallery","img_src":"https://images.test/2.jpg"}
		]}`))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL, resultCount: 2}}
	_, citations, err := tool.Execute(t.Context(), []byte(`{"query":"photos","categories":["images"]}`), nil)
	if err != nil || len(citations) != 2 || requests.Load() != 1 || citations[0].ImageURL == citations[1].ImageURL ||
		citations[0].ImageDisplay == nil || !*citations[0].ImageDisplay {
		t.Fatalf("distinct gallery images lost: %+v, requests=%d, err=%v", citations, requests.Load(), err)
	}
}
