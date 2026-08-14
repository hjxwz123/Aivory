package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/vector"
)

func TestRerankEndpointNormalizesOpenAICompatibleBase(t *testing.T) {
	for _, test := range []struct {
		name string
		base string
		want string
	}{
		{name: "host root", base: "https://rerank.example", want: "https://rerank.example/v1/rerank"},
		{name: "versioned", base: "https://rerank.example/openai/v1", want: "https://rerank.example/openai/v1/rerank"},
		{name: "trailing slash", base: "https://rerank.example/openai/v1/", want: "https://rerank.example/openai/v1/rerank"},
		{name: "full endpoint", base: "https://rerank.example/openai/v1/rerank", want: "https://rerank.example/openai/v1/rerank"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := rerankEndpoint(test.base)
			if err != nil {
				t.Fatalf("endpoint: %v", err)
			}
			if got != test.want {
				t.Fatalf("endpoint=%q, want %q", got, test.want)
			}
		})
	}
}

func TestRerankSendsOpenAICompatibleRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/rerank" {
			t.Errorf("request=%s %s, want POST /openai/v1/rerank", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization=%q", got)
		}
		var request rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.Model != "BAAI/bge-reranker-v2-m3" || request.Query != "target question" || request.TopN != 2 {
			t.Errorf("unexpected request: %+v", request)
		}
		if len(request.Documents) != 3 || request.Documents[2] != "third" {
			t.Errorf("documents=%q", request.Documents)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.2},{"index":2,"relevance_score":0.9}]}`)
	}))
	defer server.Close()

	got, err := rerank(context.Background(), rerankConfig{
		BaseURL: server.URL + "/openai/v1/",
		APIKey:  "secret",
		Model:   "BAAI/bge-reranker-v2-m3",
	}, "target question", []string{"first", "second", "third"}, 2)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Fatalf("order=%v, want [2 0]", got)
	}
}

func TestRerankKnowledgeBaseCandidatesRequiresAttachedKB(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`)
	}))
	defer server.Close()
	svc := newConfiguredRerankService(t, server.URL)
	ranked := []retrievalCandidate{
		{chunkID: "kb-first", content: "first KB candidate"},
		{chunkID: "conversation", content: "conversation attachment"},
		{chunkID: "kb-second", content: "second KB candidate"},
	}
	chunkKBIDs := map[string]string{"kb-first": "kb1", "kb-second": "kb1"}

	withoutKB := svc.rerankKnowledgeBaseCandidates(context.Background(), nil, "question", ranked, chunkKBIDs, 2)
	if calls.Load() != 0 || candidateIDs(withoutKB)[0] != "kb-first" {
		t.Fatalf("rerank ran without an attached KB: calls=%d order=%v", calls.Load(), candidateIDs(withoutKB))
	}
	withKB := svc.rerankKnowledgeBaseCandidates(context.Background(), []string{"kb1"}, "question", ranked, chunkKBIDs, 2)
	if calls.Load() != 1 {
		t.Fatalf("rerank calls=%d, want 1", calls.Load())
	}
	want := []string{"kb-second", "conversation", "kb-first"}
	if got := candidateIDs(withKB); !equalStrings(got, want) {
		t.Fatalf("reranked order=%v, want %v", got, want)
	}
}

func TestRetrieveAppliesRerankToKnowledgeBaseResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"index":1,"relevance_score":0.95},{"index":0,"relevance_score":0.1}]}`)
	}))
	defer server.Close()
	svc := newConfiguredRerankService(t, server.URL)
	if _, err := svc.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','',256)`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES('doc1','kb1','kb.txt','text/plain',10,'ready')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES
			('chunk-first','doc1','kb1',0,'text','first candidate','aivory-local-embed'),
			('chunk-second','doc1','kb1',1,'text','second candidate','aivory-local-embed')`,
	} {
		if _, err := svc.db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{
			{Score: 0.99, Payload: vector.Payload{ChunkID: "chunk-first"}},
			{Score: 0.90, Payload: vector.Payload{ChunkID: "chunk-second"}},
		},
		existingIDs: map[string]bool{"chunk-first": true, "chunk-second": true},
	})

	got, err := svc.Retrieve(context.Background(), "u1", "", []string{"kb1"}, "question", 8)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 || got[0].ID != "chunk-second" || got[1].ID != "chunk-first" {
		t.Fatalf("retrieval did not apply rerank order: %+v", got)
	}
}

func TestRerankKnowledgeBaseCandidatesCapsPoolAndFallsBackOnInvalidResponse(t *testing.T) {
	var documentCount atomic.Int32
	validServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		documentCount.Store(int32(len(request.Documents)))
		results := make([]rerankResult, request.TopN)
		for i := range results {
			index := len(request.Documents) - 1 - i
			score := float64(request.TopN - i)
			results[i] = rerankResult{Index: &index, RelevanceScore: &score}
		}
		_ = json.NewEncoder(w).Encode(rerankResponse{Results: results})
	}))
	defer validServer.Close()
	svc := newConfiguredRerankService(t, validServer.URL)
	ranked := make([]retrievalCandidate, 30)
	chunkKBIDs := make(map[string]string, len(ranked))
	for i := range ranked {
		id := fmt.Sprintf("chunk-%02d", i)
		ranked[i] = retrievalCandidate{chunkID: id, content: fmt.Sprintf("document %d", i)}
		chunkKBIDs[id] = "kb1"
	}
	got := svc.rerankKnowledgeBaseCandidates(context.Background(), []string{"kb1"}, "question", ranked, chunkKBIDs, 8)
	if documentCount.Load() != rerankCandidateLimit {
		t.Fatalf("rerank documents=%d, want cap %d", documentCount.Load(), rerankCandidateLimit)
	}
	if got[0].chunkID != "chunk-23" || got[24].chunkID != "chunk-24" {
		t.Fatalf("candidate pool or tail order is wrong: %v", candidateIDs(got))
	}

	invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"index":999,"relevance_score":1}]}`)
	}))
	defer invalidServer.Close()
	svc = newConfiguredRerankService(t, invalidServer.URL)
	fallback := svc.rerankKnowledgeBaseCandidates(context.Background(), []string{"kb1"}, "question", ranked[:3], chunkKBIDs, 1)
	if got, want := candidateIDs(fallback), candidateIDs(ranked[:3]); !equalStrings(got, want) {
		t.Fatalf("invalid response changed RRF order: got %v want %v", got, want)
	}
}

func newConfiguredRerankService(t *testing.T, baseURL string) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rerank.db"))
	if err != nil {
		t.Fatalf("open rerank database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		store.InvalidateConfig()
	})
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate rerank database: %v", err)
	}
	settings := map[string]any{
		"rag_rerank_enabled": true,
		"rag_rerank_api_url": baseURL,
		"rag_rerank_api_key": "secret",
		"rag_rerank_model":   "BAAI/bge-reranker-v2-m3",
	}
	for key, value := range settings {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	return New(db, nil, log.New(io.Discard, "", 0))
}

func candidateIDs(candidates []retrievalCandidate) []string {
	out := make([]string, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.chunkID
	}
	return out
}
