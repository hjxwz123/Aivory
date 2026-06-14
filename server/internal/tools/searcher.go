package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"aurelia/server/internal/llm"
)

// Searcher is the pluggable web-search backend abstraction (§4.4). Swap Serper
// for Brave/Bing/SearXNG by providing another implementation — the tool code
// is backend-agnostic.
type Searcher interface {
	Search(ctx context.Context, query string, topK int) (text string, citations []llm.Citation, err error)
}

// newSearcher builds the configured searcher. SearXNG can run unauthenticated
// (apiKey is empty), but Serper/Brave require a key.
func newSearcher(provider, apiKey, baseURL string) Searcher {
	switch strings.ToLower(provider) {
	case "serper":
		if apiKey == "" {
			return nil
		}
		return &serperSearcher{apiKey: apiKey}
	case "brave":
		if apiKey == "" {
			return nil
		}
		return &braveSearcher{apiKey: apiKey}
	case "searxng":
		if baseURL == "" {
			return nil
		}
		return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/")}
	case "", "auto":
		if apiKey != "" {
			return &serperSearcher{apiKey: apiKey}
		}
		if baseURL != "" {
			return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/")}
		}
		return nil
	default:
		return nil
	}
}

// serperSearcher hits https://google.serper.dev/search.
type serperSearcher struct{ apiKey string }

func (s *serperSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	body, _ := json.Marshal(map[string]any{"q": query, "num": topK})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://google.serper.dev/search", strings.NewReader(string(body)))
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("serper: %s", string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	organic, _ := parsed["organic"].([]any)
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range organic {
		rm, _ := r.(map[string]any)
		title, _ := rm["title"].(string)
		link, _ := rm["link"].(string)
		snippet, _ := rm["snippet"].(string)
		date, _ := rm["date"].(string)
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1, Title: title, URL: link, Snippet: snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, title, link, snippet)
		if date != "" {
			fmt.Fprintf(&out, "(date: %s)\n", date)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// braveSearcher hits https://api.search.brave.com/res/v1/web/search.
type braveSearcher struct{ apiKey string }

func (b *braveSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), topK)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("X-Subscription-Token", b.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bd, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("brave: %s", string(bd))
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range parsed.Web.Results {
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: r.Description, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, r.Description)
		if r.PageAge != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PageAge)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// searxngSearcher queries a self-hosted SearXNG instance over JSON.
type searxngSearcher struct{ baseURL string }

func (s *searxngSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	u := fmt.Sprintf("%s/search?q=%s&format=json&safesearch=1", s.baseURL, url.QueryEscape(query))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AureliaBot/1.0")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bd, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("searxng: %s", string(bd))
	}
	var parsed struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Content     string `json:"content"`
			PublishedAt string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	if len(parsed.Results) > topK {
		parsed.Results = parsed.Results[:topK]
	}
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range parsed.Results {
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: r.Content, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, r.Content)
		if r.PublishedAt != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PublishedAt)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}
