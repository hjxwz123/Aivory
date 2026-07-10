package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEmbedConcurrencyPreservesOrder verifies that Embed, which now runs its
// ≤embedBatchMax sub-batches CONCURRENTLY, still returns vectors in the exact
// input order — a misalignment here would silently store the wrong vector for
// every chunk. The stub encodes each input "tN" as the 1-dim vector [N].
func TestEmbedConcurrencyPreservesOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var resp embeddingResponse
		for _, in := range body.Input {
			n, _ := strconv.Atoi(strings.TrimPrefix(in, "t"))
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
			}{Embedding: []float32{float32(n)}})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: srv.URL, model: "test"} // dim 0 → no `dimensions` param

	// 25 inputs → 3 concurrent sub-batches (10, 10, 5); result must be in order.
	texts := make([]string, 25)
	for i := range texts {
		texts[i] = "t" + strconv.Itoa(i)
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(texts))
	}
	for i, v := range vecs {
		if len(v) != 1 || int(v[0]) != i {
			t.Fatalf("vector %d misaligned: got %v, want [%d]", i, v, i)
		}
	}

	// Single batch (≤10) still works.
	if v, err := e.Embed(context.Background(), []string{"t7", "t3"}); err != nil || len(v) != 2 || int(v[0][0]) != 7 || int(v[1][0]) != 3 {
		t.Fatalf("small batch wrong: v=%v err=%v", v, err)
	}
}

func TestHTTPEmbedderDashScopeUsesBoundedConcurrency(t *testing.T) {
	e := &httpEmbedder{
		baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		model:   "text-embedding-v4",
	}
	if got := e.concurrency(); got != dashScopeEmbedConcurrency {
		t.Fatalf("DashScope concurrency = %d, want %d", got, dashScopeEmbedConcurrency)
	}
	if dashScopeEmbedConcurrency <= 1 {
		t.Fatalf("DashScope concurrency must be greater than one, got %d", dashScopeEmbedConcurrency)
	}
}

func TestDashScopeProviderSlotsBoundAggregateConcurrency(t *testing.T) {
	e := &httpEmbedder{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"}
	releases := make([]func(), 0, dashScopeGlobalEmbedConcurrency)
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < dashScopeGlobalEmbedConcurrency; i++ {
		release, err := e.acquireProviderSlot(context.Background())
		if err != nil {
			t.Fatalf("acquire slot %d: %v", i, err)
		}
		releases = append(releases, release)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.acquireProviderSlot(ctx); err == nil {
		t.Fatal("acquired a slot above the global DashScope cap")
	}

	releases[0]()
	releases = releases[1:]
	release, err := e.acquireProviderSlot(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release()
}

func TestEmbeddingRetryDelayHonorsRetryAfterWithJitter(t *testing.T) {
	delay := embeddingRetryDelay(1, "5")
	if delay < 5*time.Second || delay >= 6*time.Second {
		t.Fatalf("retry delay = %s, want [5s, 6s)", delay)
	}
}

func TestEmbeddingResponseHeaderTimeoutFailsFast(t *testing.T) {
	transport, ok := embedHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("embedding transport type = %T", embedHTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("response header timeout = %s, want 30s", transport.ResponseHeaderTimeout)
	}
}

func TestDashScopeEmbeddingHTTPClientDisablesHTTP2(t *testing.T) {
	dash := &httpEmbedder{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"}
	if got := dash.httpClient(); got != dashScopeEmbedHTTPClient {
		t.Fatalf("DashScope client = %p, want %p", got, dashScopeEmbedHTTPClient)
	}
	transport, ok := dashScopeEmbedHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("DashScope embedding transport type = %T", dashScopeEmbedHTTPClient.Transport)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("DashScope embedding transport should not force HTTP/2")
	}
	if transport.TLSNextProto == nil {
		t.Fatal("DashScope embedding transport should explicitly disable HTTP/2 via TLSNextProto")
	}

	other := &httpEmbedder{baseURL: "https://api.openai.com/v1"}
	if got := other.httpClient(); got != embedHTTPClient {
		t.Fatalf("non-DashScope client = %p, want %p", got, embedHTTPClient)
	}
	otherTransport, ok := embedHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("embedding transport type = %T", embedHTTPClient.Transport)
	}
	if !otherTransport.ForceAttemptHTTP2 {
		t.Fatal("non-DashScope embedding transport should keep HTTP/2 enabled")
	}
}

func TestDashScopeEmbedderUsesBoundedAttemptTimeout(t *testing.T) {
	e := &httpEmbedder{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"}
	if got := e.requestTimeout(); got != dashScopeEmbedAttemptTimeout {
		t.Fatalf("DashScope request timeout = %s, want %s", got, dashScopeEmbedAttemptTimeout)
	}
	other := &httpEmbedder{baseURL: "https://api.openai.com/v1"}
	if got := other.requestTimeout(); got != 0 {
		t.Fatalf("non-DashScope request timeout = %s, want 0", got)
	}
}

func TestDashScopeRequestTimeoutDoesNotRetrySameHungRequest(t *testing.T) {
	oldTimeout := dashScopeEmbedAttemptTimeout
	dashScopeEmbedAttemptTimeout = 20 * time.Millisecond
	defer func() { dashScopeEmbedAttemptTimeout = oldTimeout }()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	_, err := e.postEmbeddings(context.Background(), srv.URL, []byte(`{"model":"text-embedding-v4","input":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "did not return a response within") {
		t.Fatalf("err = %v, want bounded timeout", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("request hits = %d, want no retry after a hung provider request", got)
	}
}

func TestHTTPEmbedderDashScopeNativeRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{
				"embeddings": []map[string]any{
					{"embedding": []float32{1, 2, 3}},
				},
			},
		})
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: srv.URL + "/api/v1", apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	vecs, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("vectors = %+v", vecs)
	}
	if got["model"] != "text-embedding-v4" {
		t.Fatalf("model = %v", got["model"])
	}
	input, ok := got["input"].(map[string]any)
	if !ok {
		t.Fatalf("input shape = %#v", got["input"])
	}
	texts, ok := input["texts"].([]any)
	if !ok || len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("texts = %#v", input["texts"])
	}
	params, ok := got["parameters"].(map[string]any)
	if !ok || int(params["dimension"].(float64)) != 1024 {
		t.Fatalf("parameters = %#v", got["parameters"])
	}
}

func TestHTTPEmbedderOpenAICompatibleRequestShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compatible-mode/v1/embeddings" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{1, 2}},
				{"embedding": []float32{3, 4}},
			},
		})
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: srv.URL + "/compatible-mode/v1", apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("vectors = %+v", vecs)
	}
	if got["model"] != "text-embedding-v4" || int(got["dimensions"].(float64)) != 1024 || got["encoding_format"] != "float" {
		t.Fatalf("request body = %#v", got)
	}
	input, ok := got["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "a" || input[1] != "b" {
		t.Fatalf("input = %#v", got["input"])
	}
}

func TestEmbeddingRequestErrorIncludesBatchDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary upstream outage", http.StatusBadGateway)
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: srv.URL, apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	_, err := e.Embed(context.Background(), []string{"alpha", "案例98"})
	if err == nil {
		t.Fatal("Embed unexpectedly succeeded")
	}
	msg := err.Error()
	for _, want := range []string{"batch inputs=2", "chars=", "approx_tokens="} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
	var reqErr *embeddingRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error type = %T, want embeddingRequestError", err)
	}
	for _, want := range []string{"\"input_count\": 2", "\"input_chars\""} {
		if !strings.Contains(reqErr.body, want) {
			t.Fatalf("diagnostic body %q missing %q", reqErr.body, want)
		}
	}
}

func TestHTTPEmbedderRejectsLegacyDashScopeCompatibleURL(t *testing.T) {
	e := &httpEmbedder{baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil || !strings.Contains(err.Error(), "invalid DashScope embedding base_url") {
		t.Fatalf("err = %v, want workspace URL guidance", err)
	}
}

func TestHTTPEmbedderRejectsOversizeV4InputBeforeHTTP(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	e := &httpEmbedder{baseURL: srv.URL, apiKey: "sk", model: "text-embedding-v4", dim: 1024}
	_, err := e.Embed(context.Background(), []string{strings.Repeat("测", textEmbeddingV4MaxTokens+1)})
	if err == nil || !strings.Contains(err.Error(), "exceeding text-embedding-v4 limit") {
		t.Fatalf("err = %v, want local oversize error", err)
	}
	if called {
		t.Fatal("provider should not be called for an oversized v4 input")
	}
}
