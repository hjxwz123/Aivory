package rag

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

// Short-lived, process-local cache shared with single-query retrieval. A batch
// keeps its own vectors too, so concurrent cache eviction cannot trigger a new
// embedding request halfway through a retrieval round.
var (
	queryEmbedTTL   = 10 * time.Minute
	queryEmbedMax   = 4096
	queryEmbedMu    sync.Mutex
	queryEmbedStore = map[string]queryEmbedEntry{}
)

type queryEmbedEntry struct {
	vec []float32
	exp int64
}

type queryEmbeddingResult struct {
	vec    []float32
	cached bool
}

func queryEmbeddingKey(emName, query string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(emName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(query))
	return fmt.Sprintf("%x", h.Sum64())
}

func uniqueRetrievalQueries(queries []string) []string {
	out := make([]string, 0, len(queries))
	seen := make(map[string]bool, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" && !seen[query] {
			seen[query] = true
			out = append(out, query)
		}
	}
	return out
}

// Cache hits remain usable if embedding the other queries fails. Results are
// keyed by query so cache hits and misses can be interleaved without shifting
// a vector onto the wrong query. Only valid complete batches enter the cache.
func embedQueriesCached(ctx context.Context, em Embedder, emName string, queries []string) (map[string]queryEmbeddingResult, error) {
	results := make(map[string]queryEmbeddingResult, len(queries))
	if err := ctx.Err(); err != nil {
		return results, err
	}
	missing := make([]string, 0, len(queries))
	now := time.Now().UnixNano()
	queryEmbedMu.Lock()
	for _, query := range queries {
		if entry, ok := queryEmbedStore[queryEmbeddingKey(emName, query)]; ok && now < entry.exp {
			results[query] = queryEmbeddingResult{vec: entry.vec, cached: true}
		} else {
			missing = append(missing, query)
		}
	}
	queryEmbedMu.Unlock()
	if len(missing) == 0 {
		return results, nil
	}

	vectors, err := em.Embed(ctx, missing)
	if err != nil {
		return results, err
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}
	if len(vectors) != len(missing) {
		return results, fmt.Errorf("rag: embedder returned %d vectors for %d queries", len(vectors), len(missing))
	}
	for _, vec := range vectors {
		if len(vec) == 0 || len(vec) != len(vectors[0]) {
			return results, fmt.Errorf("rag: embedder returned empty or inconsistent query vectors")
		}
	}

	queryEmbedMu.Lock()
	defer queryEmbedMu.Unlock()
	for i, query := range missing {
		if len(queryEmbedStore) >= queryEmbedMax {
			queryEmbedStore = map[string]queryEmbedEntry{}
		}
		queryEmbedStore[queryEmbeddingKey(emName, query)] = queryEmbedEntry{
			vec: vectors[i], exp: time.Now().Add(queryEmbedTTL).UnixNano(),
		}
		results[query] = queryEmbeddingResult{vec: vectors[i]}
	}
	return results, nil
}

// Each model is resolved by the existing scoped retrieval path. The first
// search for that model embeds all uncached queries; the other workers wait
// for that same batch. KB and conversation models never share query vectors.
// Errors are also shared so an upstream failure is not retried per query.
type queryEmbeddingBatch struct {
	queries     []string
	recordUsage func(string, []string) error
	mu          sync.Mutex
	models      map[string]*modelQueryEmbeddings
}

type modelQueryEmbeddings struct {
	once    sync.Once
	results map[string]queryEmbeddingResult
	err     error
}

func (b *queryEmbeddingBatch) embed(ctx context.Context, em Embedder, emName, query string) ([]float32, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	b.mu.Lock()
	if b.models == nil {
		b.models = make(map[string]*modelQueryEmbeddings)
	}
	model := b.models[emName]
	if model == nil {
		model = &modelQueryEmbeddings{}
		b.models[emName] = model
	}
	b.mu.Unlock()
	model.once.Do(func() {
		model.results, model.err = embedQueriesCached(ctx, em, emName, b.queries)
		if b.recordUsage != nil {
			uncached := make([]string, 0, len(b.queries))
			for _, q := range b.queries {
				if result, ok := model.results[q]; ok && !result.cached {
					uncached = append(uncached, q)
				}
			}
			if len(uncached) > 0 {
				if err := b.recordUsage(emName, uncached); err != nil {
					model.err = err
					// Billing failures must surface for all queries in this batch.
					model.results = nil
				}
			}
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if result, ok := model.results[query]; ok {
		return result.vec, result.cached, nil
	}
	if model.err != nil {
		return nil, false, model.err
	}
	return nil, false, fmt.Errorf("rag: query missing from embedding batch")
}
