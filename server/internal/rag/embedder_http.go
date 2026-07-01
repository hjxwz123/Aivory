package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"aurelia/server/internal/store"
)

// embedHTTPClient is shared across every httpEmbedder so TLS connections to the
// embeddings endpoint are pooled and reused between documents. Re-handshaking a
// slow / far-away endpoint (e.g. dashscope.aliyuncs.com) on every batch is
// exactly what produces "TLS handshake timeout"; keep-alive avoids most of it.
// The transport timeouts bound a hung dial/handshake so a stuck connection
// fails fast (and gets retried) instead of blocking the whole ingest.
var embedHTTPClient = &http.Client{
	Timeout: 120 * time.Second, // overall safety net; the request context still applies
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// embeddingResponse is the OpenAI-format /v1/embeddings reply shape.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// embedHTTPError carries the upstream status so the caller can distinguish a
// hard rejection (e.g. 400 on an unsupported `dimensions` value) from transient
// failures and react — like retrying without the dimensions hint.
type embedHTTPError struct {
	status int
	body   string
}

func (e *embedHTTPError) Error() string {
	return fmt.Sprintf("embeddings %d: %s", e.status, e.body)
}

// buildBody marshals the request. dimensions is included only when withDim is
// set and a positive dim is configured (OpenAI-compatible `dimensions` param).
func (e *httpEmbedder) buildBody(texts []string, withDim bool) []byte {
	m := map[string]any{"model": e.model, "input": texts}
	if withDim && e.dim > 0 {
		m["dimensions"] = e.dim
	}
	b, _ := json.Marshal(m)
	return b
}

// httpEmbedder calls an OpenAI-format `/v1/embeddings` endpoint (§4.11-D). Any
// OpenAI-compatible gateway works — the admin configures base_url + key + model
// via a channel/model of kind=embedding, or via the EMBEDDING_* env vars.
type httpEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
}

// embedBatchMax caps texts per upstream request. Kept conservative because some
// OpenAI-compatible providers cap the input array hard — notably Alibaba
// DashScope (Qwen) text-embedding-v3, whose compatible-mode endpoint rejects
// batches over 10 with a 400. OpenAI/others tolerate small batches fine (just
// more requests, cheap now that the HTTP client pools connections), so 10 is the
// safe universal default.
const embedBatchMax = 10

// embedConcurrency caps how many upstream embedding batches run at once. The old
// code did them strictly sequentially, so a 500-chunk doc paid 50 serial
// round-trips (a minute+ against a far endpoint like DashScope). Running a few in
// parallel cuts that ~Nx while staying gentle enough not to trip provider rate
// limits (postEmbeddings already retries any 429 with backoff).
const embedConcurrency = 5

// Embed returns one vector per input text, splitting into ≤embedBatchMax upstream
// requests and running them CONCURRENTLY (bounded, order-preserving).
func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) <= embedBatchMax {
		return e.embedOne(ctx, texts)
	}
	out := make([][]float32, len(texts))
	sem := make(chan struct{}, embedConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for start := 0; start < len(texts); start += embedBatchMax {
		end := start + embedBatchMax
		if end > len(texts) {
			end = len(texts)
		}
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			part, err := e.embedOne(cctx, texts[start:end])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel() // stop the other in-flight batches
				}
				mu.Unlock()
				return
			}
			for i, v := range part {
				out[start+i] = v
			}
		}(start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// embedOne POSTs a single batch (≤embedBatchMax texts) and returns its vectors.
func (e *httpEmbedder) embedOne(ctx context.Context, texts []string) ([][]float32, error) {
	base := strings.TrimRight(e.baseURL, "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	// Build the endpoint. Most configs give the API root (…/compatible-mode or
	// api.openai.com) so we append /v1/embeddings; tolerate a base that already
	// ends in /v1 so we don't produce …/v1/v1/embeddings.
	endpoint := base + "/v1/embeddings"
	if strings.HasSuffix(base, "/v1") {
		endpoint = base + "/embeddings"
	}
	// Ask for the configured width via the `dimensions` param so providers that
	// support it (OpenAI text-embedding-3, DashScope text-embedding-v3/v4) return
	// exactly what the admin set instead of their default. If the provider rejects
	// it (older model / unsupported value — e.g. asking v3 for 1536), retry
	// WITHOUT the hint and let the caller reconcile to whatever native width comes
	// back, so ingestion still succeeds.
	parsed, err := e.postEmbeddings(ctx, endpoint, e.buildBody(texts, e.dim > 0))
	if err != nil && e.dim > 0 {
		var he *embedHTTPError
		if errors.As(err, &he) && he.status == http.StatusBadRequest {
			parsed, err = e.postEmbeddings(ctx, endpoint, e.buildBody(texts, false))
		}
	}
	if err != nil {
		return nil, err
	}
	if len(parsed.Data) != len(texts) {
		// A short/over-long reply means we'd misalign vectors with their chunks
		// (and index out of range downstream). Fail loudly so the doc retries
		// rather than silently storing the wrong vectors.
		return nil, fmt.Errorf("embeddings returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings returned an empty vector at index %d", i)
		}
		out[i] = d.Embedding
	}
	return out, nil
}

// postEmbeddings POSTs one batch, retrying transient failures (TLS handshake
// timeout, dropped connection, 429/5xx) with backoff. The request body is a
// []byte so it can be replayed on each attempt. Hard 4xx (bad key, bad model,
// unsupported dimension) are returned immediately — retrying won't help.
func (e *httpEmbedder) postEmbeddings(ctx context.Context, url string, body []byte) (embeddingResponse, error) {
	const maxAttempts = 3
	var parsed embeddingResponse
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Linear backoff: 2s, 4s. Honor cancellation while waiting.
			timer := time.NewTimer(time.Duration(attempt-1) * 2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return parsed, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return parsed, err
		}
		req.Header.Set("authorization", "Bearer "+e.apiKey)
		req.Header.Set("content-type", "application/json")
		resp, err := embedHTTPClient.Do(req)
		if err != nil {
			// Don't retry if the caller cancelled / timed out the context.
			if ctx.Err() != nil {
				return parsed, ctx.Err()
			}
			lastErr = err // network / TLS / dial error → transient, retry
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("embeddings %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
			continue // rate-limited / server-side → retry
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return parsed, &embedHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
		}
		err = json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("decode embeddings response: %w", err)
			continue // truncated body / transient → retry
		}
		return parsed, nil
	}
	if lastErr == nil {
		lastErr = errors.New("unknown error")
	}
	return parsed, fmt.Errorf("embeddings request failed after %d attempts: %w", maxAttempts, lastErr)
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
