package rag

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"aivory/server/internal/store"
)

const (
	rerankCandidateLimit = 24
	rerankRequestTimeout = 5 * time.Second
	rerankResponseLimit  = 1 << 20
)

var rerankHTTPClient = &http.Client{Timeout: rerankRequestTimeout}

type rerankConfig struct {
	Enabled bool
	BaseURL string
	APIKey  string
	Model   string
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

type rerankResult struct {
	Index          *int     `json:"index"`
	RelevanceScore *float64 `json:"relevance_score"`
}

func (s *Service) rerankConfig() rerankConfig {
	if s == nil || s.db == nil {
		return rerankConfig{}
	}
	cfg := rerankConfig{}
	if raw, err := store.GetSetting(s.db, "rag_rerank_enabled"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg.Enabled)
	}
	cfg.BaseURL = stringSetting(s.db, "rag_rerank_api_url")
	cfg.APIKey = stringSetting(s.db, "rag_rerank_api_key")
	cfg.Model = stringSetting(s.db, "rag_rerank_model")
	return cfg
}

func stringSetting(db *sql.DB, key string) string {
	if db == nil {
		return ""
	}
	raw, err := store.GetSetting(db, key)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *Service) rerankKnowledgeBaseCandidates(
	ctx context.Context,
	kbIDs []string,
	query string,
	ranked []retrievalCandidate,
	chunkKBIDs map[string]string,
	topN int,
) []retrievalCandidate {
	if len(kbIDs) == 0 || len(ranked) < 2 {
		return ranked
	}
	cfg := s.rerankConfig()
	if !cfg.Enabled || cfg.BaseURL == "" || cfg.Model == "" {
		return ranked
	}

	positions := make([]int, 0, minInt(len(ranked), rerankCandidateLimit))
	pool := make([]retrievalCandidate, 0, minInt(len(ranked), rerankCandidateLimit))
	for i, candidate := range ranked {
		if strings.TrimSpace(chunkKBIDs[candidate.chunkID]) == "" {
			continue
		}
		positions = append(positions, i)
		pool = append(pool, candidate)
		if len(pool) == rerankCandidateLimit {
			break
		}
	}
	if len(pool) < 2 {
		return ranked
	}
	if topN <= 0 || topN > len(pool) {
		topN = len(pool)
	}

	documents := make([]string, len(pool))
	for i, candidate := range pool {
		documents[i] = candidate.content
	}
	order, err := rerank(ctx, cfg, query, documents, topN)
	if err != nil {
		// Reranking is an optional quality pass. Retrieval must remain available
		// when the independently configured service is slow, unavailable or sends
		// an incompatible response, so preserve the established RRF order exactly.
		if s.logger != nil {
			s.logger.Printf("rag: rerank failed (falling back to RRF): %v", err)
		}
		return ranked
	}

	reordered := make([]retrievalCandidate, 0, len(pool))
	used := make([]bool, len(pool))
	for _, index := range order {
		reordered = append(reordered, pool[index])
		used[index] = true
	}
	for i, candidate := range pool {
		if !used[i] {
			reordered = append(reordered, candidate)
		}
	}
	out := append([]retrievalCandidate(nil), ranked...)
	for i, position := range positions {
		out[position] = reordered[i]
	}
	return out
}

func rerank(ctx context.Context, cfg rerankConfig, query string, documents []string, topN int) ([]int, error) {
	if len(documents) == 0 || topN <= 0 {
		return nil, errors.New("rag: rerank requires documents and top_n")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("rag: rerank model is empty")
	}
	if topN > len(documents) {
		topN = len(documents)
	}
	endpoint, err := rerankEndpoint(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(rerankRequest{
		Model:     strings.TrimSpace(cfg.Model),
		Query:     query,
		Documents: documents,
		TopN:      topN,
	})
	if err != nil {
		return nil, fmt.Errorf("rag: encode rerank request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, rerankRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rag: create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(cfg.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := rerankHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rag: call rerank service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, rerankResponseLimit))
		return nil, fmt.Errorf("rag: rerank service returned HTTP %d", resp.StatusCode)
	}

	var decoded rerankResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, rerankResponseLimit))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("rag: decode rerank response: %w", err)
	}
	if len(decoded.Results) < topN {
		return nil, fmt.Errorf("rag: rerank response returned %d results, want at least %d", len(decoded.Results), topN)
	}
	seen := make(map[int]struct{}, len(decoded.Results))
	for _, result := range decoded.Results {
		if result.Index == nil || result.RelevanceScore == nil {
			return nil, errors.New("rag: rerank response is missing index or relevance_score")
		}
		if *result.Index < 0 || *result.Index >= len(documents) {
			return nil, fmt.Errorf("rag: rerank response index %d is out of range", *result.Index)
		}
		if _, exists := seen[*result.Index]; exists {
			return nil, fmt.Errorf("rag: rerank response repeated index %d", *result.Index)
		}
		if math.IsNaN(*result.RelevanceScore) || math.IsInf(*result.RelevanceScore, 0) {
			return nil, errors.New("rag: rerank response contains a non-finite score")
		}
		seen[*result.Index] = struct{}{}
	}
	sort.SliceStable(decoded.Results, func(i, j int) bool {
		if *decoded.Results[i].RelevanceScore != *decoded.Results[j].RelevanceScore {
			return *decoded.Results[i].RelevanceScore > *decoded.Results[j].RelevanceScore
		}
		return *decoded.Results[i].Index < *decoded.Results[j].Index
	})
	order := make([]int, topN)
	for i := 0; i < topN; i++ {
		order[i] = *decoded.Results[i].Index
	}
	return order, nil
}

func rerankEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", errors.New("rag: rerank base URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("rag: rerank base URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("rag: rerank base URL must use http or https")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/rerank"):
	case strings.HasSuffix(path, "/v1"):
		path += "/rerank"
	default:
		path += "/v1/rerank"
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
