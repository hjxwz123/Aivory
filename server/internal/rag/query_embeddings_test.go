package rag

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type queryTestEmbedder func(context.Context, []string) ([][]float32, error)

func (f queryTestEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func TestQueryEmbeddingBatchDeduplicatesCachesAndIsolatesModels(t *testing.T) {
	ctx := context.Background()
	name := t.Name()
	queryEmbedMu.Lock()
	queryEmbedStore[queryEmbeddingKey(name, "cached")] = queryEmbedEntry{vec: []float32{9, 0}, exp: time.Now().Add(time.Minute).UnixNano()}
	queryEmbedStore[queryEmbeddingKey(name, "expired")] = queryEmbedEntry{vec: []float32{99, 0}, exp: time.Now().Add(-time.Minute).UnixNano()}
	queryEmbedMu.Unlock()
	queries := uniqueRetrievalQueries([]string{" first ", "cached", "first", "", "expired", "last"})
	if !reflect.DeepEqual(queries, []string{"first", "cached", "expired", "last"}) {
		t.Fatalf("queries=%v", queries)
	}
	var calls atomic.Int32
	em := queryTestEmbedder(func(_ context.Context, input []string) ([][]float32, error) {
		calls.Add(1)
		if !reflect.DeepEqual(input, []string{"first", "expired", "last"}) {
			t.Errorf("batch=%v, want only unique cache misses in order", input)
		}
		return [][]float32{{1, 0}, {2, 0}, {3, 0}}, nil
	})
	billed := [][]string{}
	batch := &queryEmbeddingBatch{queries: queries, recordUsage: func(_ string, qs []string) error {
		billed = append(billed, append([]string(nil), qs...))
		return nil
	}}
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			vec, cached, err := batch.embed(ctx, em, name, q)
			want := []float32{1, 9, 2, 3}[i]
			if err != nil || len(vec) != 2 || vec[0] != want || cached != (q == "cached") {
				t.Errorf("query=%s vector=%v cached=%t err=%v", q, vec, cached, err)
			}
		}(i, q)
	}
	wg.Wait()
	if calls.Load() != 1 || !reflect.DeepEqual(billed, [][]string{{"first", "expired", "last"}}) {
		t.Fatalf("calls=%d billed=%v", calls.Load(), billed)
	}
	// A later round reuses every vector without another request or charge.
	next := &queryEmbeddingBatch{queries: queries, recordUsage: batch.recordUsage}
	for _, q := range queries {
		_, cached, err := next.embed(ctx, em, name, q)
		if err != nil || !cached {
			t.Fatalf("second round query=%s cached=%t err=%v", q, cached, err)
		}
	}
	if calls.Load() != 1 || len(billed) != 1 {
		t.Fatalf("cache hit was requested/billed again: calls=%d billed=%v", calls.Load(), billed)
	}
	// A KB using another model gets its own batch, including "cached".
	other := queryTestEmbedder(func(_ context.Context, input []string) ([][]float32, error) {
		if !reflect.DeepEqual(input, queries) {
			t.Errorf("other model input=%v", input)
		}
		return [][]float32{{10}, {20}, {30}, {40}}, nil
	})
	vec, cached, err := batch.embed(ctx, other, name+"-other", "cached")
	if err != nil || cached || len(vec) != 1 || vec[0] != 20 {
		t.Fatalf("model isolation: vector=%v cached=%t err=%v", vec, cached, err)
	}
}

func TestQueryEmbeddingBatchSharesFailureAndPreservesCacheHits(t *testing.T) {
	ctx := context.Background()
	name := t.Name()
	queryEmbedMu.Lock()
	queryEmbedStore[queryEmbeddingKey(name, "cached")] = queryEmbedEntry{vec: []float32{9}, exp: time.Now().Add(time.Minute).UnixNano()}
	queryEmbedMu.Unlock()
	upstreamErr := errors.New("upstream down")
	var calls atomic.Int32
	batch := &queryEmbeddingBatch{queries: []string{"a", "cached", "b"}}
	em := queryTestEmbedder(func(context.Context, []string) ([][]float32, error) {
		calls.Add(1)
		return nil, upstreamErr
	})
	for _, q := range batch.queries {
		_, cached, err := batch.embed(ctx, em, name, q)
		if q == "cached" {
			if err != nil || !cached {
				t.Fatalf("lost cached vector: %v", err)
			}
		} else if !errors.Is(err, upstreamErr) {
			t.Fatalf("query=%s error=%v", q, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("failed batch retried per query: calls=%d", calls.Load())
	}
}

func TestQueryEmbeddingBatchRejectsMalformedVectorsWithoutCaching(t *testing.T) {
	for name, vectors := range map[string][][]float32{
		"missing": {{1}}, "empty": {{1}, {}}, "dimensions": {{1}, {2, 3}},
	} {
		t.Run(name, func(t *testing.T) {
			model := t.Name()
			_, err := embedQueriesCached(context.Background(), queryTestEmbedder(func(context.Context, []string) ([][]float32, error) {
				return vectors, nil
			}), model, []string{"a", "b"})
			if err == nil {
				t.Fatal("accepted malformed batch")
			}
			queryEmbedMu.Lock()
			defer queryEmbedMu.Unlock()
			for _, q := range []string{"a", "b"} {
				if _, ok := queryEmbedStore[queryEmbeddingKey(model, q)]; ok {
					t.Fatal("partially cached malformed batch")
				}
			}
		})
	}
}

func TestQueryEmbeddingBatchCancellationUnblocksAllWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	var calls atomic.Int32
	em := queryTestEmbedder(func(ctx context.Context, _ []string) ([][]float32, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	batch := &queryEmbeddingBatch{queries: []string{"a", "b", "c"}}
	done := make(chan error, 3)
	for _, q := range batch.queries {
		go func(q string) {
			_, _, err := batch.embed(ctx, em, t.Name(), q)
			done <- err
		}(q)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("embedding did not start")
	}
	cancel()
	for range batch.queries {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("worker did not cancel")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
