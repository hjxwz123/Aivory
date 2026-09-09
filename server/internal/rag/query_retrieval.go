package rag

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"aivory/server/internal/envcfg"
)

var retrievalQueryConcurrency = min(3, max(1, envcfg.Int("AIVORY_RAG_QUERY_CONCURRENCY", 3)))

// Search concurrently but preserve query order for the existing round-robin
// merge and first-error semantics. Cancellation stops queued work and reaches
// every active embedder/vector call through the original request context.
func (s *Service) retrieveQueries(ctx context.Context, userID, convID string, kbIDs, queries []string, topK int, opts retrieveOptions) ([][]Snippet, error) {
	opts.queryEmbeddings = &queryEmbeddingBatch{
		queries: queries,
		recordUsage: func(emName string, queries []string) error {
			if userID == "" || strings.HasPrefix(emName, "aivory-local") {
				return nil
			}
			tokens := 0
			for _, query := range queries {
				tokens += estimateTokens(query)
			}
			if err := s.logEmbeddingUsage(ctx, "", convID, emName, tokens); err != nil {
				return fmt.Errorf("%w: %v", ErrBillingRecord, err)
			}
			return nil
		},
	}
	subsets := make([][]Snippet, len(queries))
	errs := make([]error, len(queries))
	jobs := make(chan int, len(queries))
	for i := range queries {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < min(retrievalQueryConcurrency, len(queries)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					errs[i] = err
					continue
				}
				started := time.Now()
				s.logRetrievalStage(ctx, convID, "fallback_query", "started", time.Time{},
					fmt.Sprintf(" query_index=%d query_count=%d", i+1, len(queries)))
				subset, err := s.retrieve(ctx, userID, convID, kbIDs, queries[i], topK, opts)
				status := "completed"
				if err != nil {
					status = "failed"
					errs[i] = err
				} else {
					subsets[i] = subset
				}
				s.logRetrievalStage(ctx, convID, "fallback_query", status, started,
					fmt.Sprintf(" query_index=%d sources=%d error_kind=%q", i+1, len(subset), retrievalStageErrorKind(err)))
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return subsets, err
	}
	for _, err := range errs {
		if err != nil {
			return subsets, err
		}
	}
	return subsets, nil
}
