package rag

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingChannelHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("A") != "a" || r.Header.Get("Authorization") != "Bearer override" {
			t.Errorf("headers = %v", r.Header)
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer server.Close()
	embedder := httpEmbedder{baseURL: server.URL, apiKey: "key", model: "test-embedding", dim: 2, headers: map[string]string{"A": "a", "Authorization": "Bearer override"}}
	result, err := embedder.Embed(context.Background(), []string{"test"})
	if err != nil || len(result) != 1 || len(result[0]) != 2 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}
