package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"aurelia/server/internal/store"
)

// httpEmbedder calls an OpenAI-format `/v1/embeddings` endpoint (§4.11-D). Any
// OpenAI-compatible gateway works — the admin configures base_url + key + model
// via a channel/model of kind=embedding, or via the EMBEDDING_* env vars.
type httpEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
}

// embedBatchMax caps texts per upstream request (§4.11-D: 每批 ≤128).
const embedBatchMax = 128

// Embed returns one vector per input text, batching upstream calls at 128.
func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) > embedBatchMax {
		out := make([][]float32, 0, len(texts))
		for start := 0; start < len(texts); start += embedBatchMax {
			end := start + embedBatchMax
			if end > len(texts) {
				end = len(texts)
			}
			part, err := e.Embed(ctx, texts[start:end])
			if err != nil {
				return nil, err
			}
			out = append(out, part...)
		}
		return out, nil
	}
	base := strings.TrimRight(e.baseURL, "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	body, _ := json.Marshal(map[string]any{"model": e.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+e.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embeddings %d: %s", resp.StatusCode, string(b))
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// localEmbedDim is the fixed width of the bundled hash-bag embedder.
const localEmbedDim = 256

// resolveEmbedder picks the embedding backend in priority order:
//  1. admin-configured embedding model (settings.embedding_model_id → model+channel)
//  2. EMBEDDING_* env config
//  3. the bundled local hash-bag embedder (always available)
//
// The returned name is stored on each chunk so retrieval can tell which
// embedder produced a vector; dim selects the Qdrant collection.
//
// NOTE: for a knowledge base, callers MUST prefer `resolveEmbedderForKB(kbID)`
// — it pins the KB's locked embedding_model_id so a global settings change
// doesn't silently produce vectors of a different model into the same
// collection. resolveEmbedder() is reserved for paths that have no KB scope
// (e.g. a freshly-uploaded conversation file that hasn't been promoted yet).
func (s *Service) resolveEmbedder(ctx context.Context) (Embedder, string, int) {
	var id string
	if raw, err := store.GetSetting(s.db, "embedding_model_id"); err == nil {
		_ = json.Unmarshal(raw, &id)
	}
	if id != "" {
		if m, err := store.GetModel(ctx, s.db, id); err == nil && m.Enabled && m.Kind == "embedding" {
			if ch, err := store.GetChannel(ctx, s.db, m.ChannelID); err == nil && ch.APIKey != "" {
				dim := m.Dim
				if dim <= 0 {
					dim = 1536
				}
				return &httpEmbedder{baseURL: ch.BaseURL, apiKey: ch.APIKey, model: m.RequestID, dim: dim}, "emb:" + m.ID, dim
			}
		}
	}
	if s.embAPIKey != "" {
		dim := s.embDim
		if dim <= 0 {
			dim = 1536
		}
		return &httpEmbedder{baseURL: s.embBaseURL, apiKey: s.embAPIKey, model: s.embModel, dim: dim}, "emb:env", dim
	}
	return NewLocalEmbedder(localEmbedDim), "aurelia-local-embed", localEmbedDim
}

// resolveEmbedderForKB picks the embedding backend for a specific knowledge
// base — the one locked at KB creation (§4.11-B2 "embedding model lock"). The
// global setting is ignored: switching settings.embedding_model_id must NEVER
// change vectors written into an existing KB, otherwise the locked Qdrant
// collection dimension diverges from the new model's output and retrieval
// silently regresses. If the KB's locked model is gone (deleted / disabled),
// we return an error so the pipeline surfaces "kb embedding model missing"
// instead of writing wrong-dim vectors.
func (s *Service) resolveEmbedderForKB(ctx context.Context, kbID string) (Embedder, string, int, error) {
	if kbID == "" {
		em, name, dim := s.resolveEmbedder(ctx)
		return em, name, dim, nil
	}
	// Read KB row directly so we don't need a userID round-trip; the orchestrator
	// already gated this caller with KB ownership checks.
	var modelID string
	var dim int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(embedding_model_id,''), COALESCE(embedding_dim,0) FROM knowledge_bases WHERE id=?`, kbID).Scan(&modelID, &dim)
	if err != nil {
		return nil, "", 0, fmt.Errorf("kb lookup: %w", err)
	}
	if modelID == "" {
		// Legacy KBs created before the lock landed — fall through to global
		// resolution, but log so admins can fix it.
		em, name, ddim := s.resolveEmbedder(ctx)
		s.logger.Printf("rag: KB %s has no locked embedding_model_id; using global %s", kbID, name)
		return em, name, ddim, nil
	}
	m, err := store.GetModel(ctx, s.db, modelID)
	if err != nil || !m.Enabled || m.Kind != "embedding" {
		return nil, "", 0, fmt.Errorf("kb %s locked embedding model %s missing/disabled — fix it or re-create the KB", kbID, modelID)
	}
	ch, err := store.GetChannel(ctx, s.db, m.ChannelID)
	if err != nil || ch.APIKey == "" {
		return nil, "", 0, fmt.Errorf("kb %s locked embedding model %s has no API key", kbID, modelID)
	}
	useDim := dim
	if useDim <= 0 {
		useDim = m.Dim
	}
	if useDim <= 0 {
		useDim = 1536
	}
	return &httpEmbedder{baseURL: ch.BaseURL, apiKey: ch.APIKey, model: m.RequestID, dim: useDim}, "emb:" + m.ID, useDim, nil
}
