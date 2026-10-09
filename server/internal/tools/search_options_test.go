package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

const searchOptionsFixture = `{"results":[{"title":"First result","url":"https://example.test/one","content":"First snippet"},{"title":"Second result","url":"https://example.test/two","content":"Second snippet"}]}`

func TestWebSearchFiltersReachSearxng(t *testing.T) {
	var received url.Values
	var language string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mounted/search" {
			t.Errorf("search path = %q", r.URL.Path)
		}
		received, language = r.URL.Query(), r.Header.Get("Accept-Language")
		_, _ = w.Write([]byte(searchOptionsFixture))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: newSettingsSearcher(nil, "searxng", "", srv.URL+"/mounted")}
	output, citations, err := tool.Execute(context.Background(), []byte(`{
		"query":"今日人工智能新闻 & 市场", "top_k":1,
		"categories":[" News ","news"], "time_range":" DAY ", "language":"ZH_cn",
		"pageno":2, "engines":["BING", "bing"], "safesearch":0
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"q": {"今日人工智能新闻 & 市场"}, "format": {"json"}, "safesearch": {"0"},
		"categories": {"news"}, "time_range": {"day"}, "language": {"zh-CN"},
		"pageno": {"2"}, "engines": {"bing"},
	}
	if !reflect.DeepEqual(received, want) || language != "zh-CN" {
		t.Fatalf("request = %#v, language=%q; want %#v", received, language, want)
	}
	if len(citations) != 1 || !strings.Contains(output, "First result") || strings.Contains(output, "Second result") {
		t.Fatalf("top_k did not truncate locally: %q, %+v", output, citations)
	}
}

func TestSearxngUnfilteredSearchPreservesDefaults(t *testing.T) {
	var received url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.URL.Query()
		if got := r.Header.Get("Accept-Language"); got != "en" {
			t.Errorf("legacy language header = %q", got)
		}
		_, _ = w.Write([]byte(searchOptionsFixture))
	}))
	defer srv.Close()
	s := &searxngSearcher{baseURL: srv.URL, engines: []string{"bing", "wikipedia"}}
	if _, _, err := s.Search(context.Background(), "old query", 5); err != nil {
		t.Fatal(err)
	}
	want := url.Values{"q": {"old query"}, "format": {"json"}, "safesearch": {"1"}, "engines": {"bing,wikipedia"}}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("legacy request = %#v, want %#v", received, want)
	}
}

func TestWebSearchBatchSharesFilters(t *testing.T) {
	var mu sync.Mutex
	var requests []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Query())
		mu.Unlock()
		_, _ = w.Write([]byte(searchOptionsFixture))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL}}
	output, citations, err := tool.Execute(context.Background(), []byte(`{
		"queries":["AI research","robotics research"],
		"categories":["science"],"time_range":"year","language":"en-US"
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || len(citations) != 2 {
		t.Fatalf("requests/citations = %d/%d", len(requests), len(citations))
	}
	for _, request := range requests {
		if request.Get("categories") != "science" || request.Get("time_range") != "year" || request.Get("language") != "en-US" {
			t.Fatalf("batch lost filters: %#v", request)
		}
	}
	var result webSearchBatchResult
	if json.Unmarshal([]byte(output), &result) != nil || result.Status != "complete" {
		t.Fatalf("batch output = %q", output)
	}
}

func TestWebSearchRejectsInvalidFiltersBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(searchOptionsFixture))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL, engines: []string{"bing"}}}
	for _, input := range []string{
		`{"query":"x","time_range":"week"}`, `{"query":"x","categories":["unknown"]}`,
		`{"query":"x","categories":"news"}`, `{"query":"x","language":"en-US&engines=other"}`,
		`{"query":"x","language":"not-a-locale"}`, `{"query":"x","pageno":0}`,
		`{"query":"x","pageno":11}`, `{"query":"x","safesearch":3}`,
		`{"query":"x","engines":["bing,other"]}`, `{"query":"x","engines":["google"]}`,
		`{"query":"x","engines":["https://private.example"]}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, _, err := tool.Execute(context.Background(), []byte(input), nil)
			var userErr *llm.ToolUserError
			if !errors.As(err, &userErr) {
				t.Fatalf("expected a repairable input error, got %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d requests", calls.Load())
	}
}

func TestSearxngEngineChoiceOnlyNarrowsAdminSelection(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.URL.Query().Get("engines")
		_, _ = w.Write([]byte(searchOptionsFixture))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL, engines: []string{"bing", "google news"}}}
	if _, _, err := tool.Execute(context.Background(), []byte(`{"query":"x","engines":["Google News"]}`), nil); err != nil {
		t.Fatal(err)
	}
	if received != "google news" {
		t.Fatalf("engine choice = %q", received)
	}
}

func searchSchemaProperties(t *testing.T, tool *webSearchTool) map[string]json.RawMessage {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	return schema.Properties
}

func TestSearchSchemaTracksLiveProviderAndEngineSettings(t *testing.T) {
	// Production uses one database; isolate this temporary database from the
	// process-wide setting cache shared with the other package tests.
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "search-options.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"search_provider": "searxng", "search_base_url": "https://search.example.test", "search_engines": "bing,wikipedia",
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	tool := &webSearchTool{searcher: newSettingsSearcher(db, "", "", "")}
	properties := searchSchemaProperties(t, tool)
	for _, name := range []string{"query", "queries", "top_k", "show_images", "categories", "time_range", "language", "pageno", "engines", "safesearch"} {
		if properties[name] == nil {
			t.Fatalf("schema is missing %s", name)
		}
	}
	var engines struct {
		Items struct {
			Enum []string `json:"enum"`
		} `json:"items"`
	}
	if json.Unmarshal(properties["engines"], &engines) != nil || !reflect.DeepEqual(engines.Items.Enum, []string{"bing", "wikipedia"}) {
		t.Fatalf("engine schema did not respect admin selection: %s", properties["engines"])
	}
	if err := store.SetSetting(db, "search_provider", "duckduckgo"); err != nil {
		t.Fatal(err)
	}
	if properties := searchSchemaProperties(t, tool); len(properties) != 4 || properties["show_images"] == nil || properties["categories"] != nil {
		t.Fatalf("non-SearXNG backend advertised unsupported filters: %+v", properties)
	}
}

func TestSearchFiltersDoNotBreakLegacyBackends(t *testing.T) {
	searcher := &batchTestSearcher{search: func(query string) (string, []llm.Citation, error) { return "legacy " + query, nil, nil }}
	tool := &webSearchTool{searcher: searcher}
	if properties := searchSchemaProperties(t, tool); len(properties) != 4 || properties["show_images"] == nil || properties["categories"] != nil {
		t.Fatalf("legacy backend schema = %+v", properties)
	}
	output, _, err := tool.Execute(context.Background(), []byte(`{"query":"old query"}`), nil)
	if err != nil || output != "legacy old query" {
		t.Fatalf("legacy execution = %q, %v", output, err)
	}
}

func TestSearxngEmptyFilteredSearchSuggestsRefinementWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[]}`))
	}))
	defer srv.Close()
	tool := &webSearchTool{searcher: &searxngSearcher{baseURL: srv.URL}}
	output, citations, err := tool.Execute(context.Background(), []byte(`{"query":"today's news","categories":["news"],"time_range":"day"}`), nil)
	if err != nil || len(citations) != 0 || !strings.Contains(output, "preserving the user's explicit source and date constraints") {
		t.Fatalf("empty filtered result = %q, %+v, %v", output, citations, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("search unexpectedly made %d requests", calls.Load())
	}
}
