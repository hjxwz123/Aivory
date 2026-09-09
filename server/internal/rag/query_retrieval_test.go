package rag

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/store"
	"aivory/server/internal/vector"
)

// Search can be held open to prove overlap and cancellation without timing a
// fake fast dependency. The embedded store provides the other vector methods.
type concurrentQueryStore struct {
	testVectorStore
	started chan vector.Scope
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
	calls   atomic.Int32
}

func (v *concurrentQueryStore) Search(ctx context.Context, _ int, _ []float32, scope vector.Scope, _ int) ([]vector.Hit, error) {
	v.calls.Add(1)
	active := v.active.Add(1)
	defer v.active.Add(-1)
	for old := v.max.Load(); active > old; old = v.max.Load() {
		if v.max.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case v.started <- scope:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-v.release:
		return v.hits, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRoutedQueriesBatchHTTPAndSearchConcurrentlyWithStableScopeAndBilling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	var requestMu sync.Mutex
	var batches [][]string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad body", 400)
			return
		}
		var texts []string
		if err := json.Unmarshal(body.Input, &texts); err != nil {
			var single string
			if err := json.Unmarshal(body.Input, &single); err != nil {
				t.Error(err)
			}
			texts = []string{single}
		}
		requestMu.Lock()
		batches = append(batches, append([]string(nil), texts...))
		requestMu.Unlock()
		data := make([]map[string]any, len(texts))
		for i := range texts {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer endpoint.Close()
	channel, err := store.CreateChannel(ctx, db, "Batch embedding", "openai", "embedding", endpoint.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "embedding", RequestID: "test-embedding", Label: "Test embedding", Enabled: true, Dim: 2, Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"embedding_model_id": model.ID, "rag_full_text_threshold": 1} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE chunks SET embedding_model=?`, "emb:"+model.ID); err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(&recordingRouter{decision: RouteDecision{Strategy: "retrieve", Queries: []string{"alpha query", " beta query ", "gamma query", "alpha query", " "}}})
	vec := &concurrentQueryStore{
		testVectorStore: testVectorStore{
			existingIDs: map[string]bool{"ch1": true, "ch2": true},
			hits:        []vector.Hit{{Score: 1, Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"}}},
		},
		started: make(chan vector.Scope, 8), release: make(chan struct{}, 1),
	}
	svc.SetVectorStore(vec)
	em, name, _ := svc.resolveEmbedder(ctx)
	if _, _, err := svc.embedQueryCached(ctx, em, name, "beta query"); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snippets []Snippet
		decision RouteDecision
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		snippets, decision, err := svc.RouteAndRetrieveDocumentScope(ctx, "u1", "c1", nil, []string{"d1"}, nil, "first full chunk", nil, 8)
		done <- outcome{snippets, decision, err}
	}()
	for i := 0; i < retrievalQueryConcurrency; i++ {
		select {
		case scope := <-vec.started:
			if scope.ConversationID != "c1" || !reflect.DeepEqual(scope.DocumentIDs, []string{"d1"}) || len(scope.KBIDs) != 0 {
				t.Fatalf("scope changed: %+v", scope)
			}
		case <-ctx.Done():
			t.Fatal("queries did not run concurrently")
		}
	}
	requestMu.Lock()
	gotBatches := append([][]string(nil), batches...)
	requestMu.Unlock()
	wantBatches := [][]string{{"beta query"}, {"first full chunk", "alpha query", "gamma query"}}
	if !reflect.DeepEqual(gotBatches, wantBatches) {
		t.Fatalf("HTTP requests=%v, want warmup then one batch %v", gotBatches, wantBatches)
	}
	if vec.active.Load() != 3 || vec.calls.Load() != 3 {
		t.Fatalf("active=%d calls=%d; fourth query must remain queued", vec.active.Load(), vec.calls.Load())
	}
	vec.release <- struct{}{}
	select {
	case <-vec.started: // The fourth query starts only after a worker finishes.
	case <-ctx.Done():
		t.Fatal("fourth query did not start")
	}
	close(vec.release)
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("retrieval did not finish")
	}
	if result.err != nil || !reflect.DeepEqual(result.decision.Queries, []string{"first full chunk", "alpha query", "beta query", "gamma query"}) {
		t.Fatalf("result=%+v", result)
	}
	if vec.max.Load() != 3 || len(result.snippets) != 2 || result.snippets[0].ID != "ch1" || result.snippets[1].ID != "ch2" {
		t.Fatalf("max=%d snippets=%+v, want bounded concurrency and deduplicated evidence", vec.max.Load(), result.snippets)
	}
	// Same request again: no new embedding calls and no duplicate charges.
	_, _, err = svc.RouteAndRetrieveDocumentScope(ctx, "u1", "c1", nil, []string{"d1"}, nil, "first full chunk", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	requestMu.Lock()
	requestCount := len(batches)
	requestMu.Unlock()
	var charged, tokens int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(input_tokens),0) FROM usage_logs WHERE purpose='embedding'`).Scan(&charged, &tokens); err != nil {
		t.Fatal(err)
	}
	wantTokens := estimateTokens("first full chunk") + estimateTokens("alpha query") + estimateTokens("gamma query")
	if requestCount != 2 || charged != 1 || tokens != wantTokens {
		t.Fatalf("requests=%d charged=%d tokens=%d want 2/1/%d", requestCount, charged, tokens, wantTokens)
	}
}

func TestConcurrentQueryCancellationStopsActiveAndQueuedSearches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	svc := New(db, nil, log.New(io.Discard, "", 0))
	vec := &concurrentQueryStore{
		testVectorStore: testVectorStore{existingIDs: map[string]bool{"ch1": true, "ch2": true}},
		started:         make(chan vector.Scope, 6), release: make(chan struct{}),
	}
	svc.SetVectorStore(vec)
	done := make(chan error, 1)
	go func() {
		_, err := svc.retrieveQueries(ctx, "u1", "c1", nil, []string{"a", "b", "c", "d", "e"}, 8, retrieveOptions{strict: true, restrictDocuments: true, documentIDs: []string{"d1"}})
		done <- err
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-vec.started:
		case <-time.After(2 * time.Second):
			t.Fatal("search did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("search did not stop")
	}
	if vec.active.Load() != 0 || vec.calls.Load() != 3 {
		t.Fatalf("active=%d calls=%d, queued searches must not start", vec.active.Load(), vec.calls.Load())
	}
	// An already cancelled request must not call any dependency.
	_, err := svc.retrieveQueries(ctx, "u1", "c1", nil, []string{"f"}, 8, retrieveOptions{})
	if !errors.Is(err, context.Canceled) || vec.calls.Load() != 3 {
		t.Fatalf("already cancelled: error=%v calls=%d", err, vec.calls.Load())
	}
}
