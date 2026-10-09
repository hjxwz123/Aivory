package tools

import (
	"context"
	"regexp"
	"strings"

	"aivory/server/internal/llm"
)

// Optional filters leave the original Searcher contract intact. Backends that
// cannot apply them do not advertise them in the model-facing tool schema.
type searcherWithOptions interface {
	SearchWithOptions(context.Context, string, int, webSearchOptions) (string, []llm.Citation, error)
	searchOptionsProperties() map[string]any
}

type webSearchOptions struct {
	Categories []string `json:"categories,omitempty"`
	TimeRange  string   `json:"time_range,omitempty"`
	Language   string   `json:"language,omitempty"`
	PageNo     *int     `json:"pageno,omitempty"`
	Engines    []string `json:"engines,omitempty"`
	SafeSearch *int     `json:"safesearch,omitempty"`
}

var searchCategories = []string{
	"general", "news", "science", "it", "images", "videos", "music", "files", "map", "social media",
}

var searchLanguagePattern = regexp.MustCompile(`^(all|[a-z]{2,3}(-[a-z0-9]{2,8}){0,2})$`)
var searchEnginePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9 _.\-]{0,79}$`)

func (s *searxngSearcher) searchOptionsProperties() map[string]any {
	engineItems := map[string]any{"type": "string", "minLength": 1, "maxLength": 80}
	if len(s.engines) > 0 {
		engineItems["enum"] = s.engines
	}
	return map[string]any{
		"categories": map[string]any{
			"type": "array", "maxItems": 10, "uniqueItems": true,
			"items":       map[string]any{"type": "string", "enum": searchCategories},
			"description": "SearXNG categories for the user's intent: images for existing photos, people/portraits, places/scenery, product photos, or visual references (returned image URLs are displayed in chat); news for news reports; science for papers; it for technical/code search; videos for videos. Omit for a broad search; use general to broaden a narrow category that returned no useful results. Available categories depend on the instance.",
		},
		"time_range": map[string]any{
			"type": "string", "enum": []string{"day", "month", "year"},
			"description": "SearXNG freshness filter. Use day for today/the last 24 hours, month for a recent month, year for a recent year. Omit for timeless or historical questions. For other or exact date intervals, include dates in query and check source dates; do not invent week or a custom range. Engine support varies.",
		},
		"language": map[string]any{
			"type": "string", "maxLength": 32,
			"description": "Source language/locale, e.g. zh-CN, zh-TW, en-US, ja-JP, fr-FR, or all. Select for the requested sources, not necessarily the reply language; English can be useful for technical sources. Omit when a language restriction would exclude useful evidence. Use a locale supported by the instance.",
		},
		"pageno": map[string]any{
			"type": "integer", "minimum": 1, "maximum": 10,
			"description": "Starting result page, default 1. The backend may fetch subsequent pages to reach the administrator's configured result count. Request a later starting page only if previous results lack decisive evidence; do not fetch all pages.",
		},
		"engines": map[string]any{
			"type": "array", "maxItems": 10, "uniqueItems": true, "items": engineItems,
			"description": "Optional known SearXNG engine names or shortcuts. Omit to use administrator/instance defaults. If an enum is provided, only that configured subset is allowed. Do not guess engine names or pick engines incompatible with the selected category.",
		},
		"safesearch": map[string]any{
			"type": "integer", "enum": []int{0, 1, 2},
			"description": "Content filter: 0 off, 1 moderate, 2 strict. Omit to keep the default moderate filter; change only for an explicit user requirement. Engine support varies.",
		},
	}
}

func normalizeWebSearchOptions(in webSearchOptions) (webSearchOptions, error) {
	in.TimeRange = strings.ToLower(strings.TrimSpace(in.TimeRange))
	switch in.TimeRange {
	case "", "day", "month", "year":
	default:
		return in, &llm.ToolUserError{Message: "time_range must be day, month, or year; omit it for an unrestricted search"}
	}
	in.Language = strings.ReplaceAll(strings.TrimSpace(in.Language), "_", "-")
	if len(in.Language) > 32 || (in.Language != "" && !searchLanguagePattern.MatchString(strings.ToLower(in.Language))) {
		return in, &llm.ToolUserError{Message: "language must be a language code such as zh-CN, en-US, or all"}
	}
	if in.Language != "" {
		parts := strings.Split(strings.ToLower(in.Language), "-")
		for i := 1; i < len(parts); i++ {
			if len(parts[i]) == 2 {
				parts[i] = strings.ToUpper(parts[i])
			} else if len(parts[i]) == 4 {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		in.Language = strings.Join(parts, "-")
	}
	if in.PageNo != nil && (*in.PageNo < 1 || *in.PageNo > 10) {
		return in, &llm.ToolUserError{Message: "pageno must be between 1 and 10"}
	}
	if in.SafeSearch != nil && (*in.SafeSearch < 0 || *in.SafeSearch > 2) {
		return in, &llm.ToolUserError{Message: "safesearch must be 0, 1, or 2"}
	}
	var err error
	in.Categories, err = normalizeSearchOptionList(in.Categories, "categories", func(value string) bool {
		for _, category := range searchCategories {
			if value == category {
				return true
			}
		}
		return false
	})
	if err != nil {
		return in, err
	}
	in.Engines, err = normalizeSearchOptionList(in.Engines, "engines", searchEnginePattern.MatchString)
	return in, err
}

func normalizeSearchOptionList(values []string, field string, valid func(string) bool) ([]string, error) {
	if len(values) > 10 {
		return nil, &llm.ToolUserError{Message: field + " must contain at most 10 items"}
	}
	var result []string
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.Join(strings.Fields(value), " "))
		if !valid(value) {
			return nil, &llm.ToolUserError{Message: "invalid " + field + " value; use values from the search tool schema"}
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func searchWithOptions(ctx context.Context, searcher Searcher, query string, topK int, options webSearchOptions) (string, []llm.Citation, error) {
	if configurable, ok := searcher.(searcherWithOptions); ok {
		return configurable.SearchWithOptions(ctx, query, topK, options)
	}
	return searcher.Search(ctx, query, topK)
}
