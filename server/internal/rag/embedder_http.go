package rag

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/store"
)

// Embedding HTTP clients are shared so TLS connections are pooled and reused
// between documents. Re-handshaking a slow / far-away endpoint on every batch is
// exactly what produces "TLS handshake timeout"; keep-alive avoids most of it.
// The transport timeouts bound a hung dial/handshake so a stuck connection
// fails fast (and gets retried) instead of blocking the whole ingest.
var (
	embedHTTPClient          = newEmbeddingHTTPClient(true)
	dashScopeEmbedHTTPClient = newEmbeddingHTTPClient(false)
)

var (
	netDialerTimeout                   = envcfg.Dur("AURELIA_RAG_NET_DIALER_TIMEOUT", 15*time.Second)
	netDialerKeepAlive                 = envcfg.Dur("AURELIA_RAG_NET_DIALER_KEEP_ALIVE", 30*time.Second)
	httpTransportMaxIdleConns          = envcfg.Int("AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS", 50)
	httpTransportMaxIdleConnsPerHost   = envcfg.Int("AURELIA_RAG_HTTP_TRANSPORT_MAX_IDLE_CONNS_PER_HOST", 10)
	httpTransportIdleConnTimeout       = envcfg.Dur("AURELIA_RAG_HTTP_TRANSPORT_IDLE_CONN_TIMEOUT", 90*time.Second)
	httpTransportTLSHandshakeTimeout   = envcfg.Dur("AURELIA_RAG_HTTP_TRANSPORT_TLSHANDSHAKE_TIMEOUT", 20*time.Second)
	httpTransportResponseHeaderTimeout = envcfg.Dur("AURELIA_RAG_HTTP_TRANSPORT_RESPONSE_HEADER_TIMEOUT", 30*time.Second)
	httpTransportExpectContinueTimeout = envcfg.Dur("AURELIA_RAG_HTTP_TRANSPORT_EXPECT_CONTINUE_TIMEOUT", 1*time.Second)
	httpClientTimeout                  = envcfg.Dur("AURELIA_RAG_HTTP_CLIENT_TIMEOUT", 3*time.Minute)

	diagPreviewCharCap = envcfg.Int("AURELIA_RAG_TRUNCATE_AT_N", 1200)
	diagBodyCap        = envcfg.Int("AURELIA_RAG_TRUNCATE_AT_N_2", 16*1024)

	embedErrBodyReadCap    = envcfg.Int64("AURELIA_RAG_IO_LIMIT_READER", 4096)
	embedErrBodyReadCap4xx = envcfg.Int64("AURELIA_RAG_IO_LIMIT_READER_2", 4096)

	embeddingRetryDelayBase = envcfg.Dur("AURELIA_RAG_EMBEDDING_RETRY_DELAY", time.Second)
	embeddingRetryDelayCap  = envcfg.Dur("AURELIA_RAG_EMBEDDING_RETRY_DELAY_2", 30*time.Second)
	embeddingRetryJitter    = envcfg.Dur("AURELIA_RAG_EMBEDDING_RETRY_DELAY_3", 1000*time.Millisecond)
)

func newEmbeddingHTTPClient(allowHTTP2 bool) *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   netDialerTimeout,
			KeepAlive: netDialerKeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     allowHTTP2,
		MaxIdleConns:          httpTransportMaxIdleConns,
		MaxIdleConnsPerHost:   httpTransportMaxIdleConnsPerHost,
		IdleConnTimeout:       httpTransportIdleConnTimeout,
		TLSHandshakeTimeout:   httpTransportTLSHandshakeTimeout,
		ResponseHeaderTimeout: httpTransportResponseHeaderTimeout,
		ExpectContinueTimeout: httpTransportExpectContinueTimeout,
	}
	if !allowHTTP2 {
		// DashScope compatible embedding occasionally stalls on h2 streams behind
		// the provider gateway. HTTP/1.1 keeps each request easier to isolate while
		// retaining keep-alive pooling for the low embedding concurrency we use.
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{
		Timeout:   httpClientTimeout, // overall cap includes reading the vector response body
		Transport: tr,
	}
}

// embeddingResponse is the OpenAI-format /v1/embeddings reply shape.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// dashscopeEmbeddingResponse is the native DashScope text embedding reply shape
// from /api/v1/services/embeddings/text-embedding/text-embedding.
type dashscopeEmbeddingResponse struct {
	Output struct {
		Embeddings []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"embeddings"`
	} `json:"output"`
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

type embeddingRequestError struct {
	err     error
	method  string
	url     string
	headers string
	body    string
}

func (e *embeddingRequestError) Error() string { return e.err.Error() }
func (e *embeddingRequestError) Unwrap() error { return e.err }

// buildOpenAIBody marshals an OpenAI-compatible embedding request. dimensions is
// included only when withDim is set and a positive dim is configured.
func (e *httpEmbedder) buildOpenAIBody(texts []string, withDim bool) []byte {
	input := any(texts)
	if len(texts) == 1 {
		// DashScope's examples use a single string for one input; arrays work too,
		// but the string form reduces one common compatibility edge.
		input = texts[0]
	}
	m := map[string]any{"model": e.model, "input": input, "encoding_format": "float"}
	if withDim && e.dim > 0 {
		m["dimensions"] = e.dim
	}
	b, _ := json.Marshal(m)
	return b
}

// buildDashScopeBody marshals a native DashScope text embedding request. Native
// API uses `input.texts` + `parameters.dimension`, not OpenAI's top-level
// `input` + `dimensions`.
func (e *httpEmbedder) buildDashScopeBody(texts []string, withDim bool) []byte {
	m := map[string]any{
		"model": e.model,
		"input": map[string]any{"texts": texts},
	}
	if withDim && e.dim > 0 {
		m["parameters"] = map[string]any{"dimension": e.dim}
	}
	b, _ := json.Marshal(m)
	return b
}

// httpEmbedder calls either an OpenAI-compatible `/embeddings` endpoint or the
// native DashScope text embedding endpoint. Admins configure base_url + key +
// model via a channel/model of kind=embedding, or via EMBEDDING_* env vars.
type httpEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
}

const (
	// embedBatchMax caps texts per upstream request. text-embedding-v4's official
	// limit is 10, so keep the generic cap at that limit.
	embedBatchMax = 10
	// textEmbeddingV4MaxTokens is the documented per-text limit for DashScope
	// text-embedding-v4. The chunker targets far smaller inputs; this is a final
	// guard that reports a clear local error instead of waiting on a provider
	// timeout when OCR returns one enormous atom.
	textEmbeddingV4MaxTokens = 8192
)

// DashScope remains concurrent, but two batches per document avoids a single
// OCR-heavy ingest monopolising the workspace. A process-wide cap also bounds
// the aggregate when several RAG workers reach embedding at once.
var (
	dashScopeEmbedConcurrency       = envcfg.Int("AURELIA_RAG_DASH_SCOPE_EMBED_CONCURRENCY", 2)
	dashScopeGlobalEmbedConcurrency = envcfg.Int("AURELIA_RAG_DASH_SCOPE_GLOBAL_EMBED_CONCURRENCY", 2)
)

// DashScope compatible-mode can occasionally accept the request and then sit
// behind provider-side queueing without returning headers. Bound each attempt
// below the shared client's broad 3-minute safety cap so indexing fails with a
// retryable, visible error instead of looking stuck for many minutes. Variable
// for tests.
var dashScopeEmbedAttemptTimeout = envcfg.Dur("AURELIA_RAG_DASH_SCOPE_EMBED_ATTEMPT_TIMEOUT", 60*time.Second)

// embedConcurrency caps how many upstream embedding batches run at once. The old
// code did them strictly sequentially, so a 500-chunk doc paid 50 serial
// round-trips. Keep this moderate because the RAG queue can process multiple
// documents at once and each document gets its own concurrency allowance.
var embedConcurrency = envcfg.Int("AURELIA_RAG_EMBED_CONCURRENCY", 4)

var dashScopeEmbeddingSlots = make(chan struct{}, dashScopeGlobalEmbedConcurrency)

// Embed returns one vector per input text, splitting into ≤embedBatchMax upstream
// requests and running them CONCURRENTLY (bounded, order-preserving).
func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = e.normalizeInputs(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	if err := e.validateInputs(texts); err != nil {
		return nil, err
	}
	if len(texts) <= embedBatchMax {
		return e.embedOne(ctx, texts)
	}
	out := make([][]float32, len(texts))
	sem := make(chan struct{}, e.concurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
batchLoop:
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
		select {
		case sem <- struct{}{}:
		case <-cctx.Done():
			break batchLoop
		}
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
	endpoint := e.endpoint()
	// Ask for the configured width via the `dimensions` param so providers that
	// support it (OpenAI text-embedding-3, DashScope text-embedding-v3/v4) return
	// exactly what the admin set instead of their default. If the provider rejects
	// it (older model / unsupported value — e.g. asking v3 for 1536), retry
	// WITHOUT the hint and let the caller reconcile to whatever native width comes
	// back, so ingestion still succeeds.
	body := e.buildBody(texts, e.dim > 0)
	parsed, err := e.postEmbeddings(ctx, endpoint, body)
	if err != nil && e.dim > 0 {
		var he *embedHTTPError
		if errors.As(err, &he) && he.status == http.StatusBadRequest {
			body = e.buildBody(texts, false)
			parsed, err = e.postEmbeddings(ctx, endpoint, body)
		}
	}
	if err != nil {
		err = fmt.Errorf("%w (batch inputs=%d chars=%d approx_tokens=%d)", err, len(texts), totalTextChars(texts), totalEstimatedTokens(texts))
		return nil, &embeddingRequestError{
			err:     err,
			method:  http.MethodPost,
			url:     endpoint,
			headers: "{\n  \"Authorization\": \"[redacted]\",\n  \"Content-Type\": [\"application/json\"]\n}",
			body:    e.diagnosticsBody(texts),
		}
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

func (e *httpEmbedder) endpoint() string {
	base := strings.TrimRight(e.baseURL, "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	if strings.Contains(base, "dashscope.aliyuncs.com/compatible-mode") {
		// DashScope compatible embedding models are served from the Bailian
		// workspace region endpoint,
		// e.g. https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1.
		// The legacy global dashscope host can accept the TCP connection but never
		// return headers for newer embedding models, which looks like a random
		// timeout.
		return base + "/__invalid_dashscope_embedding_base_url__"
	}
	if strings.HasSuffix(base, "/embeddings") || strings.Contains(base, "/services/embeddings/") {
		return base
	}
	if strings.HasSuffix(base, "/api/v1") || strings.Contains(base, ".maas.aliyuncs.com/api/v1") {
		return base + "/services/embeddings/text-embedding/text-embedding"
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/embeddings"
	}
	return base + "/v1/embeddings"
}

func (e *httpEmbedder) buildBody(texts []string, withDim bool) []byte {
	if e.nativeDashScope() {
		return e.buildDashScopeBody(texts, withDim)
	}
	return e.buildOpenAIBody(texts, withDim)
}

func (e *httpEmbedder) nativeDashScope() bool {
	base := strings.TrimRight(e.baseURL, "/")
	return strings.Contains(base, "/services/embeddings/text-embedding/") ||
		strings.HasSuffix(base, "/api/v1") ||
		strings.Contains(base, ".maas.aliyuncs.com/api/v1")
}

func (e *httpEmbedder) isDashScope() bool {
	base := strings.ToLower(strings.TrimSpace(e.baseURL))
	return strings.Contains(base, "aliyuncs.com") || strings.Contains(base, "dashscope")
}

func (e *httpEmbedder) concurrency() int {
	if e.isDashScope() {
		return dashScopeEmbedConcurrency
	}
	return embedConcurrency
}

func (e *httpEmbedder) requestTimeout() time.Duration {
	if e.isDashScope() {
		return dashScopeEmbedAttemptTimeout
	}
	return 0
}

func (e *httpEmbedder) httpClient() *http.Client {
	if e.isDashScope() {
		return dashScopeEmbedHTTPClient
	}
	return embedHTTPClient
}

func (e *httpEmbedder) acquireProviderSlot(ctx context.Context) (func(), error) {
	if !e.isDashScope() {
		return func() {}, nil
	}
	select {
	case dashScopeEmbeddingSlots <- struct{}{}:
		return func() { <-dashScopeEmbeddingSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *httpEmbedder) normalizeInputs(texts []string) []string {
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

func (e *httpEmbedder) maxTokensPerInput() int {
	if strings.EqualFold(strings.TrimSpace(e.model), "text-embedding-v4") {
		return textEmbeddingV4MaxTokens
	}
	return 0
}

func (e *httpEmbedder) validateInputs(texts []string) error {
	limit := e.maxTokensPerInput()
	if limit <= 0 {
		return nil
	}
	for i, text := range texts {
		if n := estimateTokens(text); n > limit {
			return fmt.Errorf("embedding input %d is about %d tokens, exceeding %s limit %d; reduce chunk size or fix OCR paragraph splitting", i, n, e.model, limit)
		}
	}
	return nil
}

func (e *httpEmbedder) diagnosticsBody(texts []string) string {
	previews := make([]string, 0, 2)
	totalChars := 0
	for i, text := range texts {
		totalChars += len(text)
		if i < 2 {
			previews = append(previews, truncateAtN(text, diagPreviewCharCap))
		}
	}
	body := map[string]any{
		"model":         e.model,
		"input_count":   len(texts),
		"input_chars":   totalChars,
		"input_preview": previews,
	}
	if e.nativeDashScope() {
		body["parameters"] = map[string]any{"dimension": e.dim}
	} else {
		body["dimensions"] = e.dim
		body["encoding_format"] = "float"
	}
	b, _ := json.MarshalIndent(body, "", "  ")
	return truncateAtN(string(b), diagBodyCap)
}

// postEmbeddings POSTs one batch, retrying transient failures (TLS handshake
// timeout, dropped connection, 429/5xx) with backoff. The request body is a
// []byte so it can be replayed on each attempt. Hard 4xx (bad key, bad model,
// unsupported dimension) are returned immediately — retrying won't help.
func (e *httpEmbedder) postEmbeddings(ctx context.Context, url string, body []byte) (embeddingResponse, error) {
	maxAttempts := envcfg.Int("AURELIA_RAG_MAX_ATTEMPTS", 2)
	var parsed embeddingResponse
	var lastErr error
	var retryAfter string
	client := e.httpClient()
	if strings.Contains(url, "__invalid_dashscope_embedding_base_url__") {
		return parsed, fmt.Errorf("invalid DashScope embedding base_url: use a Bailian workspace regional URL such as https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1 or https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/api/v1, not https://dashscope.aliyuncs.com/compatible-mode/v1")
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Exponential backoff plus jitter prevents every concurrent batch from
			// timing out and retrying against the workspace in the same millisecond.
			timer := time.NewTimer(embeddingRetryDelay(attempt-1, retryAfter))
			select {
			case <-ctx.Done():
				timer.Stop()
				return parsed, ctx.Err()
			case <-timer.C:
			}
		}
		reqCtx := ctx
		cancelReq := func() {}
		if timeout := e.requestTimeout(); timeout > 0 {
			reqCtx, cancelReq = context.WithTimeout(ctx, timeout)
		}
		req, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
		if err != nil {
			cancelReq()
			return parsed, err
		}
		req.Header.Set("authorization", "Bearer "+e.apiKey)
		req.Header.Set("content-type", "application/json")
		release, err := e.acquireProviderSlot(ctx)
		if err != nil {
			cancelReq()
			return parsed, err
		}
		resp, err := client.Do(req)
		if err != nil {
			release()
			cancelReq()
			// Don't retry if the caller cancelled / timed out the context.
			if ctx.Err() != nil {
				return parsed, ctx.Err()
			}
			if reqCtx.Err() != nil {
				lastErr = fmt.Errorf("embedding provider did not return a response within %s: %w", e.requestTimeout().Round(time.Second), err)
				break
			}
			lastErr = err // network / TLS / dial error → transient, retry
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryAfter = resp.Header.Get("Retry-After")
			b, _ := io.ReadAll(io.LimitReader(resp.Body, embedErrBodyReadCap))
			resp.Body.Close()
			release()
			cancelReq()
			lastErr = fmt.Errorf("embeddings %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
			continue // rate-limited / server-side → retry
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, embedErrBodyReadCap4xx))
			resp.Body.Close()
			release()
			cancelReq()
			return parsed, &embedHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
		}
		parsed = embeddingResponse{}
		err = e.decodeResponse(resp.Body, &parsed)
		resp.Body.Close()
		release()
		cancelReq()
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

func totalTextChars(texts []string) int {
	total := 0
	for _, text := range texts {
		total += len(text)
	}
	return total
}

func embeddingRetryDelay(failedAttempt int, retryAfter string) time.Duration {
	base := time.Duration(1<<min(failedAttempt, 4)) * embeddingRetryDelayBase
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		base = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(strings.TrimSpace(retryAfter)); err == nil {
		if wait := time.Until(when); wait > 0 {
			base = wait
		}
	}
	if base > embeddingRetryDelayCap {
		base = embeddingRetryDelayCap
	}
	return base + time.Duration(rand.IntN(int(max(1, embeddingRetryJitter/time.Millisecond))))*time.Millisecond
}

func (e *httpEmbedder) decodeResponse(r io.Reader, parsed *embeddingResponse) error {
	if !e.nativeDashScope() {
		return json.NewDecoder(r).Decode(parsed)
	}
	var ds dashscopeEmbeddingResponse
	if err := json.NewDecoder(r).Decode(&ds); err != nil {
		return err
	}
	parsed.Data = make([]struct {
		Embedding []float32 `json:"embedding"`
	}, len(ds.Output.Embeddings))
	for i, item := range ds.Output.Embeddings {
		parsed.Data[i].Embedding = item.Embedding
	}
	return nil
}

// localEmbedDim is the fixed width of the bundled hash-bag embedder.
const localEmbedDim = 256

func defaultEmbeddingDim() int {
	return 1536
}

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
					dim = defaultEmbeddingDim()
				}
				return &httpEmbedder{baseURL: ch.BaseURL, apiKey: ch.APIKey, model: m.RequestID, dim: dim}, "emb:" + m.ID, dim
			}
		}
	}
	if s.embAPIKey != "" {
		dim := s.embDim
		if dim <= 0 {
			dim = defaultEmbeddingDim()
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
		useDim = defaultEmbeddingDim()
	}
	return &httpEmbedder{baseURL: ch.BaseURL, apiKey: ch.APIKey, model: m.RequestID, dim: useDim}, "emb:" + m.ID, useDim, nil
}
