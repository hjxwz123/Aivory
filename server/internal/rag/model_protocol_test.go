package rag

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelProtocolControlsEmbeddingWireFormat(t *testing.T) {
	for _, protocol := range []string{"openai.embeddings", "dashscope.embeddings"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if protocol == "openai.embeddings" {
					if r.URL.Path != "/api/v1/embeddings" || !strings.Contains(string(raw), `"input":`) {
						t.Errorf("request = %s %s", r.URL.Path, raw)
					}
					io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
				} else {
					if r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" || !strings.Contains(string(raw), `"texts"`) {
						t.Errorf("request = %s %s", r.URL.Path, raw)
					}
					io.WriteString(w, `{"output":{"embeddings":[{"text_index":0,"embedding":[0.1,0.2]}]}}`)
				}
			}))
			defer srv.Close()
			e := httpEmbedder{protocol: protocol, baseURL: srv.URL + "/api/v1", apiKey: "key", model: "test", dim: 2}
			vectors, err := e.Embed(context.Background(), []string{"hello"})
			if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
				t.Fatalf("vectors = %v, %v", vectors, err)
			}
		})
	}
}

func TestAliyunCompatibleModeModelProtocolPreservesEndpoint(t *testing.T) {
	const base = "https://llm-qbihp8zf48j4xmoj.cn-beijing.maas.aliyuncs.com/compatible-mode"
	for _, root := range []string{base, base + "/", base + "/v1", base + "/v1/embeddings"} {
		legacy := httpEmbedder{baseURL: root}
		modelOwned := httpEmbedder{protocol: "openai.embeddings", baseURL: root}
		if got, want := modelOwned.endpoint(), legacy.endpoint(); got != want || got != base+"/v1/embeddings" {
			t.Fatalf("root %s: migrated endpoint=%s legacy=%s", root, got, want)
		}
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/compatible-mode/v1/embeddings" || !strings.Contains(string(raw), `"input"`) || strings.Contains(string(raw), `"texts"`) {
			t.Errorf("wrong compatible-mode request: %s %s", r.URL.Path, raw)
		}
		io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
	}))
	defer srv.Close()
	for _, protocol := range []string{"", "openai.embeddings"} {
		e := httpEmbedder{protocol: protocol, baseURL: srv.URL + "/compatible-mode", model: "text-embedding-v4", apiKey: "key", dim: 2}
		vectors, err := e.Embed(context.Background(), []string{"中文测试"})
		if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
			t.Fatalf("protocol=%s vectors=%v error=%v", protocol, vectors, err)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("unexpected extra upstream calls: %v", paths)
	}
}
