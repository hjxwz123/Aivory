package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/llm"
	"aivory/server/internal/tooldiagnostics"
)

// Keenable search. The settings resolver supplies the effective API key after
// applying saved settings over environment defaults. Without a key it uses the
// keyless public endpoint, which identifies the calling app by X-Keenable-Title
// instead of a credential and is limited to 1,000 requests an hour per IP.
const (
	keenableKeyedEndpoint   = "https://api.keenable.ai/v1/search"
	keenableKeylessEndpoint = "https://api.keenable.ai/v1/search/public"
	keenableClientTitle     = "Aivory"
	keenableResponseLimit   = 4 << 20
	keenableMaxResults      = 20
	keenableSnippetChars    = 400
)

type keenableSearcher struct{ apiKey string }

func newKeenableSearcher(apiKey string) *keenableSearcher {
	return &keenableSearcher{apiKey: strings.TrimSpace(apiKey)}
}

func (k *keenableSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	results, err := k.search(ctx, query, topK, webSearchOptions{})
	text, citations := results.output()
	return text, citations, err
}

func (k *keenableSearcher) newRequest(ctx context.Context, query string, count int) (*http.Request, error) {
	body, _ := json.Marshal(map[string]any{
		"query":              query,
		"max_results":        count,
		"snippet_max_length": keenableSnippetChars,
		"disable_cards":      true,
	})
	endpoint := keenableKeylessEndpoint
	if k.apiKey != "" {
		endpoint = keenableKeyedEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if k.apiKey != "" {
		req.Header.Set("X-API-Key", k.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Required on keyless calls; names the app, carries no user identifier.
	req.Header.Set("X-Keenable-Title", keenableClientTitle)
	return req, nil
}

func (k *keenableSearcher) search(ctx context.Context, query string, topK int, _ webSearchOptions) (webSearchResults, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return webSearchResults{}, &llm.ToolUserError{Message: "query is required"}
	}
	if topK <= 0 {
		topK = defaultWebSearchResults
	}
	if topK > keenableMaxResults {
		topK = keenableMaxResults
	}
	req, err := k.newRequest(ctx, query, topK)
	if err != nil {
		return webSearchResults{}, err
	}
	resp, err := tooldiagnostics.Do(toolHTTPClient, req)
	if err != nil {
		return webSearchResults{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return webSearchResults{}, fmt.Errorf("keenable: HTTP %d: the API key was rejected; check search_api_key or KEENABLE_API_KEY", resp.StatusCode)
	case http.StatusTooManyRequests:
		msg := "keenable: HTTP 429: rate limited"
		if wait, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			msg += fmt.Sprintf("; retry after %s", wait.Round(time.Second))
		}
		if k.apiKey == "" {
			msg += ". The keyless tier allows 1,000 requests an hour per IP; configure an API key for more"
		}
		return webSearchResults{}, fmt.Errorf("%s", msg)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return webSearchResults{}, fmt.Errorf("keenable: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var parsed struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Snippet     string `json:"snippet"`
			Description string `json:"description"`
			PublishedAt string `json:"published_at"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, keenableResponseLimit)).Decode(&parsed); err != nil {
		return webSearchResults{}, err
	}
	results := webSearchResults{items: make([]webSearchResultItem, 0, len(parsed.Results))}
	for _, r := range parsed.Results {
		if len(results.items) >= topK {
			break
		}
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		snippet := r.Snippet
		if strings.TrimSpace(snippet) == "" {
			snippet = r.Description
		}
		results.items = append(results.items, webSearchResultItem{
			citation: llm.Citation{Title: r.Title, URL: r.URL, Snippet: cleanSnippet(snippet), Source: "web"},
			content:  snippet,
			date:     r.PublishedAt,
		})
	}
	if len(results.items) == 0 {
		return webSearchResults{notice: "No web results found for this query."}, nil
	}
	return results, nil
}
