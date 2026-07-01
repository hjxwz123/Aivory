package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
