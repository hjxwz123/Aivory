// Package rag implements the simplified document parse/chunk/embed/retrieve
// pipeline described in design.md §4.11. It uses the embedded SQLite store
// and a hash-bag local embedding so the system is fully functional without
// external services. The Embedder interface (and the *Service abstraction)
// make a drop-in replacement trivial — pass a real OpenAI/Voyage embedder
// and nothing else changes.
package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/queue"
	"aurelia/server/internal/storage"
	"aurelia/server/internal/store"
	"aurelia/server/internal/vector"

	"github.com/hibiken/asynq"
)

const (
	ragIngestTaskType = "rag.ingest"
	ragFastQueueName  = "rag-fast"
	ragSlowQueueName  = "rag"
)

var (
	ragFastQueueConcurrency    = envcfg.Int("AURELIA_RAG_RAG_FAST_QUEUE_CONCURRENCY", 4)
	ragSlowQueueConcurrency    = envcfg.Int("AURELIA_RAG_RAG_SLOW_QUEUE_CONCURRENCY", 4)
	ingestPipelineTimeout      = envcfg.Dur("AURELIA_RAG_INGEST_PIPELINE_TIMEOUT", 70*time.Minute)
	ingestTaskTimeout          = envcfg.Dur("AURELIA_RAG_INGEST_TASK_TIMEOUT", 75*time.Minute)
	ingestUniqueTTL            = envcfg.Dur("AURELIA_RAG_INGEST_UNIQUE_TTL", 80*time.Minute)
	ingestHeartbeatInterval    = envcfg.Dur("AURELIA_RAG_INGEST_HEARTBEAT_INTERVAL", 30*time.Second)
	ingestStaleAfter           = envcfg.Dur("AURELIA_RAG_INGEST_STALE_AFTER", 4*time.Minute)
	ingestPendingStaleAfter    = envcfg.Dur("AURELIA_RAG_INGEST_PENDING_STALE_AFTER", ingestUniqueTTL)
	ingestRecoveryInterval     = envcfg.Dur("AURELIA_RAG_INGEST_RECOVERY_INTERVAL", time.Minute)
	ingestFinalizeTimeout      = envcfg.Dur("AURELIA_RAG_INGEST_FINALIZE_TIMEOUT", 30*time.Second)
	ingestAsynqLeaseMaxRetries = envcfg.Int("AURELIA_RAG_INGEST_ASYNQ_LEASE_MAX_RETRIES", 1)
	ingestAsynqRetryDelay      = envcfg.Dur("AURELIA_RAG_INGEST_ASYNQ_RETRY_DELAY", 2*time.Minute)
)

// Env-overridable defaults for inline literals elsewhere in this file. Each
// falls back to the original hardcoded value when the variable is unset.
var (
	ingestQueueClassifyTimeout  = envcfg.Dur("AURELIA_RAG_INGEST_QUEUE_NAME", 2*time.Second)
	runIngestMaxAttempts        = envcfg.Int("AURELIA_RAG_RUN_INGEST_WITH_RETRIES", 3)
	runIngestRetryBackoff       = envcfg.Dur("AURELIA_RAG_RUN_INGEST_WITH_RETRIES_2", 3*time.Second)
	heartbeatWriteTimeout       = envcfg.Dur("AURELIA_RAG_START_INGEST_HEARTBEAT", 5*time.Second)
	finalizeChunkCleanupTimeout = envcfg.Dur("AURELIA_RAG_FINALIZE_CHUNK_CLEANUP_TIMEOUT", 10*time.Second)
	finalizeStatusTimeout       = envcfg.Dur("AURELIA_RAG_FINALIZE_STATUS_TIMEOUT", 10*time.Second)
	extractionFailureReasonCap  = 500
	embeddingErrorTruncate      = 4096
	retrieveDefaultTopK         = 5
	denseSearchLegLimit         = envcfg.Int("AURELIA_RAG_DENSE_SEARCH_LEG_LIMIT", 30)
	keywordSearchLegLimit       = envcfg.Int("AURELIA_RAG_KEYWORD_SEARCH_LEG_LIMIT", 30)
	snippetDefaultMax           = envcfg.Int("AURELIA_RAG_SNIPPET_OF", 240)
	imageAtomSizeThreshold      = envcfg.Int("AURELIA_RAG_SPLIT_PARAGRAPHS_AND_TABLES", 800)
	routerCallTimeout           = envcfg.Dur("AURELIA_RAG_ROUTER_CALL_TIMEOUT", 12*time.Second)
	mapReduceSummaryChars       = envcfg.Int("AURELIA_RAG_MAP_REDUCE_SUMMARISE", 200)
	docHintFirstContentCap      = envcfg.Int("AURELIA_RAG_COLLECT_DOC_HINTS", 120)
	docHintsMaxCount            = envcfg.Int("AURELIA_RAG_COLLECT_DOC_HINTS_2", 12)
)

// Service is the public façade.
type Service struct {
	db           *sql.DB
	queue        queue.Queue
	logger       *log.Logger
	task         TaskRouter
	vec          vector.Store
	asynqClient  *asynq.Client
	asynqServers []*asynq.Server
	// External integration config (§4.11-C/D): embedding HTTP backend + MinerU.
	// All values are env-fallbacks — runtime resolution prefers the admin
	// settings table so the live admin UI controls them without a restart.
	embBaseURL string
	embAPIKey  string
	embModel   string
	embDim     int
	mineruURL  string
	mineruKey  string
	// Sandbox sidecar URL/key — kept for legacy storage-client wiring and env
	// fallback compatibility. MinerU source uploads now use direct Go-side S3/OSS
	// upload and do not require sandbox_base_url.
	sandboxURL string
	sandboxKey string
}

// SetExternalConfig wires the optional embedding HTTP backend + MinerU parser.
// Called by main() after construction. All values may be empty (dev fallback).
// The MinerU URL/token here are env-fallbacks; admin settings take precedence
// at ingest time so the live UI works without a restart.
func (s *Service) SetExternalConfig(embBaseURL, embAPIKey, embModel string, embDim int, mineruURL, mineruKey string) {
	s.embBaseURL, s.embAPIKey, s.embModel, s.embDim = embBaseURL, embAPIKey, embModel, embDim
	s.mineruURL, s.mineruKey = mineruURL, mineruKey
}

// SetSandboxFallback stashes the sandbox sidecar URL/key the env supplied at
// boot. Runtime reads the `sandbox_base_url` / `sandbox_api_key` settings first
// for legacy storage-client compatibility; MinerU direct S3/OSS upload does not
// require these values.
func (s *Service) SetSandboxFallback(url, key string) {
	s.sandboxURL, s.sandboxKey = url, key
}

// TaskRouter is the subset of llm.TaskLLM the RAG service needs (kept as an
// interface to break the import cycle).
type TaskRouter interface {
	RunJSON(ctx context.Context, kind string, prompt string, out any, opts RouterOpts) error
}

// RouterOpts mirrors llm.RunOpts but in this package's vocabulary.
type RouterOpts struct {
	UserID         string
	ConversationID string
}

// New builds the service. The vector backend defaults to Disabled; call
// SetVectorStore to wire Qdrant. When no vector backend is available, retrieval
// injects the full in-scope document text instead of keeping a DB vector copy.
func New(db *sql.DB, q queue.Queue, logger *log.Logger) *Service {
	return &Service{db: db, queue: q, logger: logger, vec: vector.NewDisabled()}
}

// SetVectorStore wires the similarity-search backend (Qdrant in production).
// Called by main() after construction.
func (s *Service) SetVectorStore(v vector.Store) {
	if v != nil {
		s.vec = v
	}
}

// UseAsynq wires Redis/asynq for document ingestion. The rest of the background
// system still uses the closure-based in-process queue; RAG ingest is the part
// that can be expressed as a durable task payload (doc_id) and benefits most
// from surviving restarts and smoothing parsing/embedding bursts.
func (s *Service) UseAsynq(redisURL string) error {
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return err
	}
	s.asynqClient = asynq.NewClient(opt)
	serverConfig := func(queueName string, concurrency int) asynq.Config {
		return asynq.Config{
			Concurrency: concurrency,
			Queues:      map[string]int{queueName: 1},
			RetryDelayFunc: func(_ int, _ error, _ *asynq.Task) time.Duration {
				// A timeout can win the processor select just before the handler finishes
				// its detached cleanup. Keep the replacement task from overlapping it.
				return ingestAsynqRetryDelay
			},
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, taskErr error) {
				s.handleAsynqIngestError(ctx, task, taskErr)
			}),
		}
	}
	for _, lane := range []struct {
		name        string
		concurrency int
	}{
		{name: ragFastQueueName, concurrency: ragFastQueueConcurrency},
		{name: ragSlowQueueName, concurrency: ragSlowQueueConcurrency},
	} {
		srv := asynq.NewServer(opt, serverConfig(lane.name, lane.concurrency))
		s.asynqServers = append(s.asynqServers, srv)
		mux := asynq.NewServeMux()
		mux.HandleFunc(ragIngestTaskType, s.handleAsynqIngest)
		go func(queueName string, server *asynq.Server, handler *asynq.ServeMux) {
			if err := server.Run(handler); err != nil && s.logger != nil {
				s.logger.Printf("rag: asynq server for queue %s stopped: %v", queueName, err)
			}
		}(lane.name, srv, mux)
	}
	if s.logger != nil {
		s.logger.Printf("rag: asynq lanes started fast=%d slow=%d", ragFastQueueConcurrency, ragSlowQueueConcurrency)
	}
	return nil
}

func (s *Service) CloseAsynq() {
	for _, server := range s.asynqServers {
		server.Shutdown()
	}
	if s.asynqClient != nil {
		_ = s.asynqClient.Close()
	}
}

// SetTaskLLM is called by main() after the task helper exists. We accept any
// implementation of TaskRouter to avoid an import cycle.
func (s *Service) SetTaskLLM(t TaskRouter) { s.task = t }

// Embedder is the pluggable embedding backend. The local hash-bag embedder
// satisfies it for development and ensures search is always available; admins
// who configure a real channel of `kind=embedding` can wire in an HTTP-based
// embedder via the orchestrator.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// RequeueIncomplete re-enqueues documents left in a non-terminal state
// (pending/parsing/embedding) by a crash or restart — the in-memory queue
// doesn't survive a restart, so without this a doc would poll "indexing…"
// forever. Best-effort; call once at boot.
func (s *Service) RequeueIncomplete(ctx context.Context) {
	docs, err := store.ListIncompleteDocuments(ctx, s.db)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: requeue scan failed: %v", err)
		}
		return
	}
	for _, d := range docs {
		if err := store.TouchDocumentIngest(ctx, s.db, d.ID); err != nil {
			if s.logger != nil {
				s.logger.Printf("rag: refresh recovery heartbeat for %s: %v", d.ID, err)
			}
			continue
		}
		if s.logger != nil {
			s.logger.Printf("rag: requeueing incomplete document %s (was %s)", d.ID, d.Status)
		}
		s.Ingest(d.ID)
	}
}

// RunIngestRecovery performs the boot recovery and then continuously reclaims
// non-terminal documents whose worker heartbeat stopped. The DB claim is atomic,
// so multiple API replicas can run this loop without enqueueing the same stale
// document in the same recovery window.
func (s *Service) RunIngestRecovery(ctx context.Context) {
	s.RequeueIncomplete(ctx)
	ticker := time.NewTicker(ingestRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RequeueStaleIngests(ctx)
		}
	}
}

// RequeueStaleIngests claims and requeues tasks abandoned by a process crash,
// an exhausted asynq lease, a panic, or a worker that could not finalize state.
func (s *Service) RequeueStaleIngests(ctx context.Context) {
	now := time.Now()
	docs, err := store.ClaimStaleIncompleteDocuments(
		ctx,
		s.db,
		now.Add(-ingestPendingStaleAfter).Unix(),
		now.Add(-ingestStaleAfter).Unix(),
	)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: stale ingest scan failed: %v", err)
		}
		return
	}
	for _, d := range docs {
		if s.logger != nil {
			s.logger.Printf("rag: reclaiming stale document %s (last status %s)", d.ID, d.Status)
		}
		// The stale DB claim is the uniqueness guard here. Bypass an old Redis
		// Unique lock that may have survived the worker which owned it.
		s.IngestNow(d.ID)
	}
}

// `failed` with the last error (§4.11-C-3). The pipeline is idempotent —
// repeat calls re-write existing chunks.
func (s *Service) Ingest(docID string) {
	s.enqueueIngest(docID, true)
}

// IngestNow requeues a document without the Redis uniqueness guard. Use this
// for explicit user/admin retry actions: a previous failed task may still have
// a uniqueness lock, but a clicked Retry must actually run.
func (s *Service) IngestNow(docID string) {
	s.enqueueIngest(docID, false)
}

func (s *Service) enqueueIngest(docID string, unique bool) {
	if s.asynqClient != nil {
		queueName := s.ingestQueueName(docID)
		payload, _ := json.Marshal(map[string]string{"doc_id": docID})
		task := asynq.NewTask(ragIngestTaskType, payload)
		opts := []asynq.Option{
			asynq.Queue(queueName),
			asynq.Timeout(ingestTaskTimeout),
			// Handler-level failures already get three bounded attempts and are
			// archived with SkipRetry. These retries are for lease/process loss.
			asynq.MaxRetry(ingestAsynqLeaseMaxRetries),
		}
		if unique {
			opts = append(opts, asynq.Unique(ingestUniqueTTL))
		}
		if _, err := s.asynqClient.Enqueue(task, opts...); err == nil {
			if s.logger != nil {
				s.logger.Printf("rag: enqueued doc=%s queue=%s", docID, queueName)
			}
			return
		} else if errors.Is(err, asynq.ErrDuplicateTask) {
			return
		} else if s.logger != nil {
			s.logger.Printf("rag: asynq enqueue failed for %s, falling back to in-process queue: %v", docID, err)
		}
	}
	s.queue.Enqueue("rag.ingest", func(context.Context) error {
		// The generic in-process queue has a shorter default deadline. RAG owns
		// its longer stage-aware budget so MinerU plus embedding can finish.
		ctx, cancel := context.WithTimeout(context.Background(), ingestPipelineTimeout)
		defer cancel()
		return s.runIngestWithRetries(ctx, docID)
	})
}

func (s *Service) ingestQueueName(docID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ingestQueueClassifyTimeout)
	defer cancel()
	d, err := store.GetDocument(ctx, s.db, docID)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: classify queue for %s: %v", docID, err)
		}
		return ragSlowQueueName
	}
	return ingestQueueNameForDocument(d)
}

func ingestQueueNameForDocument(d *store.Document) string {
	if d != nil && (isSpreadsheetData(d.Filename, d.MimeType) || isProbablyText(d.MimeType, d.StoragePath, d.Filename)) {
		return ragFastQueueName
	}
	return ragSlowQueueName
}

func (s *Service) handleAsynqIngest(ctx context.Context, t *asynq.Task) error {
	var payload struct {
		DocID string `json:"doc_id"`
	}
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return err
	}
	if payload.DocID == "" {
		return fmt.Errorf("rag: missing doc_id")
	}
	pipelineCtx, cancel := context.WithTimeout(ctx, ingestPipelineTimeout)
	defer cancel()
	if err := s.runIngestWithRetries(pipelineCtx, payload.DocID); err != nil {
		// Business/upstream failures were already retried and finalized by the
		// handler. Preserve asynq retries for lease loss instead of multiplying
		// the three-attempt pipeline by another retry layer.
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	return nil
}

func (s *Service) handleAsynqIngestError(ctx context.Context, task *asynq.Task, taskErr error) {
	if task == nil || task.Type() != ragIngestTaskType {
		return
	}
	retried, retryOK := asynq.GetRetryCount(ctx)
	maxRetry, maxOK := asynq.GetMaxRetry(ctx)
	// SkipRetry means runIngestWithRetries already finalized the original error;
	// writing the asynq wrapper would replace the useful user-facing cause.
	if errors.Is(taskErr, asynq.SkipRetry) {
		return
	}
	if !retryOK || !maxOK || retried < maxRetry {
		return
	}
	var payload struct {
		DocID string `json:"doc_id"`
	}
	if json.Unmarshal(task.Payload(), &payload) != nil || payload.DocID == "" {
		return
	}
	s.finalizeIngestFailure(payload.DocID, taskErr)
}

func (s *Service) runIngestWithRetries(ctx context.Context, docID string) error {
	stopHeartbeat := s.startIngestHeartbeat(ctx, docID)
	defer stopHeartbeat()
	var err error
	// Cache the parsed content across retries so a transient embed/DB failure
	// re-runs only the cheap embed step — never the paid MinerU OCR again.
	cache := &parseCache{}
	for attempt := 1; attempt <= runIngestMaxAttempts; attempt++ {
		if err = s.runPipeline(ctx, docID, cache); err == nil {
			return nil
		}
		if s.logger != nil {
			s.logger.Printf("rag: ingest %s attempt %d/%d failed: %v", docID, attempt, runIngestMaxAttempts, err)
		}
		if isNonRetryableIngestError(err) {
			break
		}
		// Back off between whole-pipeline retries so a transient upstream outage
		// (e.g. embeddings TLS timeout) gets a chance to recover instead of being
		// hammered three times in a row.
		if attempt < runIngestMaxAttempts {
			timer := time.NewTimer(time.Duration(attempt) * runIngestRetryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				err = ctx.Err()
				attempt = runIngestMaxAttempts
			case <-timer.C:
			}
		}
	}
	s.finalizeIngestFailure(docID, err)
	return err
}

func (s *Service) startIngestHeartbeat(ctx context.Context, docID string) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	touch := func() {
		writeCtx, writeCancel := context.WithTimeout(context.Background(), heartbeatWriteTimeout)
		defer writeCancel()
		if err := store.TouchDocumentIngest(writeCtx, s.db, docID); err != nil && s.logger != nil {
			s.logger.Printf("rag: heartbeat document %s: %v", docID, err)
		}
	}
	touch()
	go func() {
		ticker := time.NewTicker(ingestHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				touch()
			}
		}
	}()
	return cancel
}

// finalizeIngestFailure never reuses the task context: on timeout/cancellation
// that context is already unusable, which was the original reason rows remained
// forever in parsing/embedding. Cleanup and the terminal status get a fresh,
// tightly-bounded context.
func (s *Service) finalizeIngestFailure(docID string, ingestErr error) {
	if ingestErr == nil {
		ingestErr = errors.New("ingest failed")
	}
	chunkCtx, chunkCancel := context.WithTimeout(context.Background(), finalizeChunkCleanupTimeout)
	if derr := store.DeleteChunksByDocument(chunkCtx, s.db, docID); derr != nil && s.logger != nil {
		s.logger.Printf("rag: cleanup chunks after failed ingest %s: %v", docID, derr)
	}
	chunkCancel()

	if s.vec.Enabled() {
		vectorCtx, vectorCancel := context.WithTimeout(context.Background(), ingestFinalizeTimeout)
		if derr := s.vec.DeleteByDocument(vectorCtx, docID); derr != nil && s.logger != nil {
			s.logger.Printf("rag: cleanup vectors after failed ingest %s: %v", docID, derr)
		}
		vectorCancel()
	}

	// Always allocate a separate status context after cleanup. Even if Qdrant
	// consumed its entire deadline, the terminal DB transition still gets a full
	// chance to complete and unlock the frontend Retry action.
	statusCtx, statusCancel := context.WithTimeout(context.Background(), finalizeStatusTimeout)
	if err := store.UpdateDocumentStatus(statusCtx, s.db, docID, "failed", unwrapNonRetryableIngestError(ingestErr).Error(), 0); err != nil && s.logger != nil {
		s.logger.Printf("rag: finalize failed ingest %s: %v", docID, err)
	}
	statusCancel()
}

type nonRetryableIngestError struct{ err error }

func (e nonRetryableIngestError) Error() string { return e.err.Error() }
func (e nonRetryableIngestError) Unwrap() error { return e.err }

func noRetryIngest(err error) error {
	if err == nil {
		return nil
	}
	return nonRetryableIngestError{err: err}
}

func isNonRetryableIngestError(err error) bool {
	var target nonRetryableIngestError
	return errors.As(err, &target)
}

func unwrapNonRetryableIngestError(err error) error {
	var target nonRetryableIngestError
	if errors.As(err, &target) {
		return target.err
	}
	return err
}

// sanitizeIngestText removes invalid DB text and MinerU-only image markdown
// from parsed document text. Postgres TEXT columns reject NUL/invalid UTF-8
// (SQLSTATE 22021), while `![...](mineru://...)` markers are opaque filenames
// that pollute chunks, embeddings, and keyword search.
func sanitizeIngestText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return stripMinerUMarkdownImages(strings.ToValidUTF8(s, ""))
}

// parseCache memoises a document's parsed content across pipeline retry
// attempts so a later-stage (embed/DB) failure never re-runs paid MinerU OCR.
type parseCache struct {
	ok      bool
	content string
}

func (s *Service) runPipeline(ctx context.Context, docID string, cache *parseCache) error {
	pipelineStart := time.Now()
	d, err := store.GetDocument(ctx, s.db, docID)
	if err != nil {
		return err
	}
	if s.logger != nil {
		s.logger.Printf("rag: ingest start doc=%s file=%q kb=%s conv=%s", docID, d.Filename, d.KBID, d.ConversationID)
	}
	// Idempotent re-ingest: drop any chunks AND vectors from a previous, partial,
	// or retried run BEFORE doing anything else. Doing it FIRST (not after parse /
	// embedder-resolve) means a failure later in THIS run — parse error, MinerU
	// outage, KB embedder-resolve error — can't leave stale rows behind; and
	// repeats (RequeueIncomplete, the 3× retry loop, a manual re-Ingest) never
	// duplicate. Unconditional: a previous cleanup may have removed DB chunks but
	// failed to reach Qdrant, so absence of chunk rows is not proof that no stale
	// vectors exist. (§4.11-C-3)
	cleanupStart := time.Now()
	if err := store.DeleteChunksByDocument(ctx, s.db, docID); err != nil {
		return err
	}
	if s.vec.Enabled() {
		if derr := s.vec.DeleteByDocument(ctx, docID); derr != nil && s.logger != nil {
			s.logger.Printf("rag: clear old vectors for %s: %v", docID, derr)
		}
	}
	if s.logger != nil {
		s.logger.Printf("rag: pre-ingest cleanup done doc=%s took=%s", docID, time.Since(cleanupStart).Round(time.Millisecond))
	}
	_ = store.UpdateDocumentStatus(ctx, s.db, docID, "parsing", "", 0)

	// Resolve MinerU + storage from admin settings (live), falling back to env
	// values supplied at boot. MinerU source uploads prefer direct Go-side
	// S3/OSS upload; sandbox_base_url remains only as a legacy sidecar fallback.
	// Any of these can be blank — the parser degrades gracefully (binary docs
	// become a one-line placeholder).
	mineruURL := readSettingString(s.db, "mineru_api_url", s.mineruURL)
	mineruKey := readSettingString(s.db, "mineru_api_token", s.mineruKey)
	// Blank admin setting → fall back to the env/boot default (bundled sandbox).
	sbURL := readSettingString(s.db, "sandbox_base_url", s.sandboxURL)
	if strings.TrimSpace(sbURL) == "" {
		sbURL = s.sandboxURL
	}
	sbKey := readSettingString(s.db, "sandbox_api_key", s.sandboxKey)
	if strings.TrimSpace(sbKey) == "" {
		sbKey = s.sandboxKey
	}
	storageCfg, storageIssues := storageBlockFromSettings(s.db)
	storageClient := storage.New(sbURL, sbKey, storageCfg)
	mineruIssues := minerUConfigIssues(mineruURL, mineruKey, storageCfg, storageIssues)

	// Spreadsheets are data, not prose: never parse or embed them. They stay as
	// conversation files and are analysed in the code sandbox (python_execute
	// stages them to /workspace/uploads). Mark ready with zero chunks so the
	// ingest pipeline completes cleanly instead of vectorising rows of numbers.
	if isSpreadsheetData(d.Filename, d.MimeType) {
		if s.logger != nil {
			s.logger.Printf("rag: ingest ready doc=%s file=%q spreadsheet skipped in %s", docID, d.Filename, time.Since(pipelineStart).Round(time.Millisecond))
		}
		return store.UpdateDocumentStatus(ctx, s.db, docID, "ready", "", 0)
	}

	// Parse: text docs + any PDF/DOC(X)/PPT(X) with a usable text layer locally
	// (instant); only scanned/text-less documents go to MinerU OCR — the cloud
	// pipeline takes minutes (§4.11-C latency-first). parseDocument makes the
	// per-document call from the file's content. Reuse the cached parse on a retry
	// so we never pay for MinerU OCR twice.
	var content string
	if cache != nil && cache.ok {
		content = cache.content
	} else {
		stageStart := time.Now()
		raw, extracted, perr := parseDocument(ctx, d.StoragePath, d.MimeType, d.Filename, mineruURL, mineruKey, storageClient, mineruIssues, s.logger)
		if perr != nil {
			return perr
		}
		// Strip NUL / invalid UTF-8 at the source: parsed binary docs (docx/pdf/ppt)
		// carry bytes Postgres TEXT columns reject (SQLSTATE 22021). This guarantees
		// every downstream write (chunks, parents) is clean regardless of insert path.
		content = sanitizeIngestText(raw)

		// A document whose text couldn't be extracted (e.g. a scan with MinerU
		// unavailable/failing) must NOT be embedded or marked ready — a junk
		// placeholder chunk silently pollutes search and incorrectly unblocks
		// sending. Fail it loudly with the reason instead, and return nil so it
		// isn't retried (re-running MinerU/parse would just fail again until the
		// operator fixes storage/MinerU and re-uploads or rebuilds).
		if !extracted {
			reason := strings.TrimSpace(content)
			if len(reason) > extractionFailureReasonCap {
				reason = reason[:extractionFailureReasonCap]
			}
			if reason == "" {
				reason = "could not extract text"
			}
			if s.logger != nil {
				s.logger.Printf("rag: ingest failed doc=%s file=%q extracted=false reason=%s", docID, d.Filename, reason)
			}
			return store.UpdateDocumentStatus(ctx, s.db, docID, "failed", reason, 0)
		}
		// Only cache real extractions — a placeholder isn't worth reusing, and
		// caching it would skip a (cheap) re-parse that might succeed next time.
		if cache != nil && extracted {
			cache.ok = true
			cache.content = content
		}
		if s.logger != nil {
			s.logger.Printf("rag: parse done doc=%s file=%q chars=%d extracted=%v took=%s", docID, d.Filename, len(content), extracted, time.Since(stageStart).Round(time.Millisecond))
		}
	}

	// Chunk hierarchically (§4.11-C-2 small-to-big): parents carry section
	// context (not embedded), children carry the vectors.
	stageStart := time.Now()
	parents := chunkHierarchical(content)
	if s.logger != nil {
		childCount := 0
		for _, p := range parents {
			childCount += len(p.Children)
		}
		s.logger.Printf("rag: chunk done doc=%s file=%q parents=%d children=%d took=%s", docID, d.Filename, len(parents), childCount, time.Since(stageStart).Round(time.Millisecond))
	}

	// A conversation-scoped doc that fits the full-text window is injected whole,
	// and source/config text is also better provided as exact context instead of
	// dense-vector chunks. KB uploads always embed because a KB is an explicit
	// cross-document search index.
	skipEmbed := d.KBID == "" && d.ConversationID != "" &&
		(isCodeOrConfigText(d.Filename) || estimateTokens(content) <= s.ragSettings().FullTextThreshold)

	// §4.11-B2 lock: ingest into a KB MUST use the KB's locked embedding model;
	// global setting changes never re-route an existing KB's vectors. For pure
	// conversation-scoped docs (no KB), fall through to the global resolver.
	var (
		em     Embedder
		emName string
		dim    int
	)
	if !skipEmbed {
		_ = store.UpdateDocumentStatus(ctx, s.db, docID, "embedding", "", 0)
		if d.KBID != "" {
			em, emName, dim, err = s.resolveEmbedderForKB(ctx, d.KBID)
			if err != nil {
				return err
			}
		} else {
			em, emName, dim = s.resolveEmbedder(ctx)
		}
	}
	// (Old chunks/vectors were already cleared at the top of runPipeline, so a
	// failure between there and here never leaves stale rows.)
	written := 0
	totalTokens := 0
	seq := 0
	points := []vector.Point{}

	// Embed ALL children in ONE call (which now runs its ≤10-text batches
	// concurrently) instead of a serial em.Embed per parent — the old per-parent
	// loop paid one serial round-trip per section, the dominant cost for a large
	// document against a far endpoint.
	var allVecs [][]float32
	if !skipEmbed {
		var allChildren []string
		for _, p := range parents {
			allChildren = append(allChildren, p.Children...)
		}
		if len(allChildren) > 0 {
			stageStart = time.Now()
			if s.logger != nil {
				s.logger.Printf("rag: embedding start doc=%s file=%q chunks=%d model=%s dim=%d", docID, d.Filename, len(allChildren), emName, dim)
			}
			allVecs, err = em.Embed(ctx, allChildren)
			if err != nil {
				s.logEmbeddingError(ctx, d.KBID, d.ConversationID, emName, totalEstimatedTokens(allChildren), err, em, allChildren)
				return noRetryIngest(fmt.Errorf("embedding failed: %w", err))
			}
			if s.logger != nil {
				s.logger.Printf("rag: embedding done doc=%s file=%q vectors=%d model=%s took=%s", docID, d.Filename, len(allVecs), emName, time.Since(stageStart).Round(time.Millisecond))
			}
			// Reconcile the collection dimension with what the model ACTUALLY
			// returned (some endpoints ignore the configured width and emit their
			// native one; wrong-dim vectors make Qdrant reject the whole upsert).
			if len(allVecs) > 0 && len(allVecs[0]) > 0 && len(allVecs[0]) != dim {
				actual := len(allVecs[0])
				s.logger.Printf("rag: embedding model emits %d-dim vectors (config requested %d, unsupported) — adapting to %d (doc %s)", actual, dim, actual, docID)
				dim = actual
				if d.KBID != "" {
					if err := store.SetKBEmbeddingDim(ctx, s.db, d.KBID, actual); err != nil {
						s.logger.Printf("rag: persist corrected embedding_dim for kb %s: %v", d.KBID, err)
					}
				}
			}
		}
	}

	// Build every chunk row (parents + children) with pre-generated ids so a child
	// can reference its parent, then write them all in ONE transaction — one commit
	// instead of one INSERT (and, on SQLite, one fsync) per chunk, which was the
	// dominant cost when indexing a big file.
	inserts := []store.ChunkInsert{}
	vi := 0 // index into the flattened allVecs, in child order
	for _, p := range parents {
		parentID := ""
		if len(parents) > 1 || len(p.Children) > 1 {
			parentID = store.NewChunkID()
			inserts = append(inserts, store.ChunkInsert{
				ID: parentID, DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
				Seq: seq, ChunkType: "parent", Content: p.Content, EmbeddingModel: emName,
			})
			seq++
		}
		for _, child := range p.Children {
			// Classify image_caption strictly: a child chunk must be EXACTLY one
			// `![…](mineru://…)` marker (optionally preceded by the page-number HTML
			// comment). Mixed-with-prose stays chunkType=text.
			chunkType := "text"
			imageRef := ""
			if ref, ok := soleMineruImageMarker(child); ok {
				chunkType = "image_caption"
				imageRef = ref
			}
			var vec []float32
			if !skipEmbed && vi < len(allVecs) {
				vec = allVecs[vi]
			}
			childID := store.NewChunkID()
			inserts = append(inserts, store.ChunkInsert{
				ID: childID, DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
				Seq: seq, ParentID: parentID, ChunkType: chunkType, Content: child,
				ImageRef: imageRef, EmbeddingModel: emName,
			})
			if vec != nil && s.vec.Enabled() {
				points = append(points, vector.Point{
					ChunkID: childID,
					Vector:  vec,
					Payload: vector.Payload{
						DocumentID: docID, KBID: d.KBID, ConversationID: d.ConversationID,
						ParentID: parentID, ChunkType: chunkType, Seq: seq,
						Content: child, Filename: d.Filename,
					},
				})
			}
			vi++
			seq++
			written++
			totalTokens += estimateTokens(child)
		}
	}
	// One transactional batch write for the whole document.
	stageStart = time.Now()
	if err := store.CreateChunksBatch(ctx, s.db, inserts); err != nil {
		return err
	}
	if s.logger != nil {
		s.logger.Printf("rag: chunks stored doc=%s file=%q rows=%d children=%d took=%s", docID, d.Filename, len(inserts), written, time.Since(stageStart).Round(time.Millisecond))
	}
	if s.vec.Enabled() && len(points) > 0 {
		stageStart = time.Now()
		if err := s.vec.Upsert(ctx, dim, points); err != nil {
			return fmt.Errorf("qdrant upsert failed: %w", err)
		}
		if s.logger != nil {
			s.logger.Printf("rag: qdrant upsert done doc=%s file=%q points=%d dim=%d took=%s", docID, d.Filename, len(points), dim, time.Since(stageStart).Round(time.Millisecond))
		}
	}
	// Record embedding spend (§8.3, purpose=embedding) — best-effort. Skipped
	// when we intentionally inject the conversation document in full.
	if !skipEmbed {
		s.logEmbeddingUsage(ctx, d.KBID, d.ConversationID, emName, totalTokens)
	}
	if err := store.UpdateDocumentStatus(ctx, s.db, docID, "ready", "", written); err != nil {
		return err
	}
	if s.logger != nil {
		s.logger.Printf("rag: ingest ready doc=%s file=%q chunks=%d total=%s", docID, d.Filename, written, time.Since(pipelineStart).Round(time.Millisecond))
	}
	return nil
}

func totalEstimatedTokens(texts []string) int {
	total := 0
	for _, text := range texts {
		total += estimateTokens(text)
	}
	return total
}

func (s *Service) logEmbeddingError(ctx context.Context, kbID, convID, embedder string, tokens int, err error, em Embedder, texts []string) {
	if strings.HasPrefix(embedder, "aurelia-local") {
		return
	}
	userID, wsID := "", ""
	if kbID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`, kbID).Scan(&userID, &wsID)
	}
	if userID == "" && convID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID).Scan(&userID, &wsID)
	}
	modelID := strings.TrimPrefix(embedder, "emb:")
	channelID := ""
	if modelID != "" && modelID != "env" {
		if m, merr := store.GetModel(ctx, s.db, modelID); merr == nil {
			channelID = m.ChannelID
		}
	}
	method, url, headers, body := "POST", "", "", ""
	var reqErr *embeddingRequestError
	if errors.As(err, &reqErr) {
		method = reqErr.method
		url = reqErr.url
		headers = reqErr.headers
		body = reqErr.body
	}
	if h, ok := em.(*httpEmbedder); ok {
		if url == "" {
			url = h.endpoint()
		}
		if headers == "" {
			headers = "{\n  \"Authorization\": \"[redacted]\",\n  \"Content-Type\": [\"application/json\"]\n}"
		}
		if body == "" {
			body = h.diagnosticsBody(texts)
		}
	}
	_ = store.LogUsage(ctx, s.db, store.UsageLog{
		UserID:         userID,
		WorkspaceID:    wsID,
		ConversationID: convID,
		ModelID:        modelID,
		Purpose:        "embedding",
		InputTokens:    tokens,
		ChannelID:      channelID,
		Status:         "error",
		Error:          truncateAtN(err.Error(), embeddingErrorTruncate),
		RequestMethod:  method,
		RequestURL:     url,
		RequestHeaders: headers,
		RequestBody:    body,
	})
}

// logEmbeddingUsage writes one usage_logs row for an embedding batch. The
// owning user is resolved through the KB or conversation.
func (s *Service) logEmbeddingUsage(ctx context.Context, kbID, convID, embedder string, tokens int) {
	if tokens == 0 || strings.HasPrefix(embedder, "aurelia-local") {
		return // local hash embedder is free — don't pollute the report
	}
	// §workspaces: shared-KB / shared-conversation indexing is billed to the
	// KB/conversation CREATOR (shared-infrastructure cost — documents carry no
	// uploader column), but the row is attributed to the workspace so the usage
	// pages report it under the right space.
	userID, wsID := "", ""
	if kbID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`, kbID).Scan(&userID, &wsID)
	}
	if userID == "" && convID != "" {
		_ = s.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID).Scan(&userID, &wsID)
	}
	if userID == "" {
		return
	}
	modelID := strings.TrimPrefix(embedder, "emb:")
	_ = store.LogUsage(ctx, s.db, store.UsageLog{
		UserID:         userID,
		WorkspaceID:    wsID,
		ConversationID: convID,
		ModelID:        modelID,
		Purpose:        "embedding",
		InputTokens:    tokens,
	})
}

// Snippet is the slim search hit returned by Retrieve. The orchestrator
// and the `search_knowledge_base` tool convert it to llm.Citation for the
// downstream message + SSE pipeline.
type Snippet struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

// Retrieve runs the hybrid search described in §4.11-E (vector + keyword
// fusion) over the chunks visible to the conversation: own conversation
// uploads ∪ attached KBs (project KBs included via the orchestrator).
//
// Returns up to topK Snippets carrying the snippet and the filename so the
// frontend can render them with the same component as web_search citations.
//
// §4.11-B2 embedding lock: when one or more KBs are in scope, the FIRST KB's
// locked embedding model is used. We refuse cross-model fan-out (multiple KBs
// at different dims) — the orchestrator should split the call instead. With
// no KBs in scope (pure conversation upload), we fall back to the global
// resolver since conversation uploads are ephemeral and not locked.
// §2.4 query-vector cache: identical RAG queries (retries, common questions,
// the same question across users on a shared KB) reuse the embedding instead of
// re-calling the embedding API. Keyed by embedder name + query so different
// models/dims never collide. Process-local, short TTL, bounded size.
var (
	queryEmbedTTL = 10 * time.Minute
	queryEmbedMax = 4096
)

type queryEmbedEntry struct {
	vec []float32
	exp int64
}

var (
	queryEmbedMu    sync.Mutex
	queryEmbedStore = map[string]queryEmbedEntry{}
)

var errVectorBackendUnavailable = errors.New("rag: vector backend unavailable")

func (s *Service) embedQueryCached(ctx context.Context, em Embedder, emName, query string) (vec []float32, cached bool, err error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(emName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(query))
	key := fmt.Sprintf("%x", h.Sum64())

	now := time.Now().UnixNano()
	queryEmbedMu.Lock()
	if e, ok := queryEmbedStore[key]; ok && now < e.exp {
		v := e.vec
		queryEmbedMu.Unlock()
		return v, true, nil
	}
	queryEmbedMu.Unlock()

	vecs, err := em.Embed(ctx, []string{query})
	if err != nil {
		return nil, false, err
	}
	if len(vecs) == 0 {
		return nil, false, fmt.Errorf("rag: embedder returned no vector")
	}
	v := vecs[0]
	queryEmbedMu.Lock()
	if len(queryEmbedStore) >= queryEmbedMax {
		queryEmbedStore = map[string]queryEmbedEntry{} // crude cap; cheap to rebuild
	}
	queryEmbedStore[key] = queryEmbedEntry{vec: v, exp: time.Now().Add(queryEmbedTTL).UnixNano()}
	queryEmbedMu.Unlock()
	return v, false, nil
}

func (s *Service) Retrieve(ctx context.Context, userID, convID string, kbIDs []string, query string, topK int) ([]Snippet, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	terms := tokenize(strings.ToLower(query))
	if topK <= 0 {
		topK = retrieveDefaultTopK
	}
	fullContext := func() ([]Snippet, error) {
		scope, err := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
		if err != nil {
			return nil, err
		}
		return fullTextSnippets(scope), nil
	}
	if !s.vec.Enabled() {
		return fullContext()
	}

	// Resolve the embedding model(s) covering the scope and search each model's
	// chunks with ITS OWN query vector. KB docs use the KB's locked model; a
	// conversation's own (large, embedded) uploads use the GLOBAL model. A single
	// query vector can't meaningfully score — or even reach, since Qdrant
	// collections are per-dimension — vectors from a different model, so when a
	// bound KB and the conversation disagree on the model we search them as two
	// groups and merge. (§4.11 model split)
	var cands []retrievalCandidate
	if len(kbIDs) > 0 {
		kbEm, kbName, kbDim, err := s.resolveEmbedderForKB(ctx, kbIDs[0])
		if err != nil {
			return nil, err
		}
		// Cross-KB consistency: all bound KBs must share ONE embedding model (same
		// dim + same model). A mismatch would score one model's query against
		// another's vectors; erroring surfaces it instead of returning garbage.
		for _, other := range kbIDs[1:] {
			_, otherName, otherDim, oerr := s.resolveEmbedderForKB(ctx, other)
			if oerr != nil {
				return nil, oerr
			}
			if otherDim != kbDim || otherName != kbName {
				return nil, fmt.Errorf("rag: cross-KB query mixes embedding models (%s/%dd vs %s/%dd) — search these KBs separately or re-embed to align", kbName, kbDim, otherName, otherDim)
			}
		}
		gEm, gName, gDim := s.resolveEmbedder(ctx)
		if convID != "" && (gName != kbName || gDim != kbDim) {
			// Two model groups: KBs under the KB model, conversation docs under the
			// global model — each with its own query embedding + per-dim collection.
			kbScope := vector.Scope{KBIDs: kbIDs}
			kbCands, err := s.searchScope(ctx, userID, convID, kbEm, kbName, kbDim, kbScope, query, terms)
			if err != nil {
				if errors.Is(err, errVectorBackendUnavailable) {
					return fullContext()
				}
				return nil, err
			}
			if len(kbCands) == 0 && s.vectorScopeHasEmbeddedChunks(ctx, kbScope) {
				return fullContext()
			}
			cands = kbCands
			convScope := vector.Scope{ConversationID: convID}
			if convCands, cerr := s.searchScope(ctx, userID, convID, gEm, gName, gDim, convScope, query, terms); cerr == nil {
				if len(convCands) == 0 && s.vectorScopeHasEmbeddedChunks(ctx, convScope) {
					return fullContext()
				}
				cands = appendUniqueCandidates(cands, convCands)
			} else if errors.Is(cerr, errVectorBackendUnavailable) {
				return fullContext()
			} else {
				s.logger.Printf("rag: conversation-scope retrieval failed for %s: %v", convID, cerr)
			}
		} else {
			// One model across KBs (+ the conversation when its model matches): a
			// single combined-scope search — exactly the prior behaviour.
			scope := vector.Scope{KBIDs: kbIDs, ConversationID: convID}
			cands, err = s.searchScope(ctx, userID, convID, kbEm, kbName, kbDim, scope, query, terms)
			if err != nil {
				if errors.Is(err, errVectorBackendUnavailable) {
					return fullContext()
				}
				return nil, err
			}
			if len(cands) == 0 && s.vectorScopeHasEmbeddedChunks(ctx, scope) {
				return fullContext()
			}
		}
	} else {
		gEm, gName, gDim := s.resolveEmbedder(ctx)
		var err error
		scope := vector.Scope{ConversationID: convID}
		cands, err = s.searchScope(ctx, userID, convID, gEm, gName, gDim, scope, query, terms)
		if err != nil {
			if errors.Is(err, errVectorBackendUnavailable) {
				return fullContext()
			}
			return nil, err
		}
		if len(cands) == 0 && s.vectorScopeHasEmbeddedChunks(ctx, scope) {
			return fullContext()
		}
	}
	// Surface in-scope chunks that were intentionally left UNEMBEDDED (small
	// conversation docs, code/config — runPipeline's skipEmbed). They live in
	// neither Qdrant nor the dense search set, so without this the
	// search_knowledge_base tool and inject-mode retrieval couldn't find a
	// freshly-uploaded small/code file at all — only auto-mode's pinned injection
	// covered them. Conversation-scoped only (KB docs always embed). (§4.11 skip-embed)
	if convID != "" {
		seen := make(map[string]bool, len(cands))
		for _, c := range cands {
			seen[c.chunkID] = true
		}
		for _, c := range s.keywordOnlyUnembedded(ctx, kbIDs, convID, terms) {
			if !seen[c.chunkID] {
				cands = append(cands, c)
			}
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}

	cfg := s.ragSettings()
	ranked := fuseReciprocalRank(cands)
	if cfg.DynamicTopK {
		// Inject EVERY hit whose cosine similarity clears the cutoff — no fixed K,
		// no cap (§admin RAG). Keyword-only hits (sim 0) don't qualify here.
		cut := float32(cfg.SimThreshold)
		kept := make([]retrievalCandidate, 0, len(ranked))
		for _, c := range ranked {
			if c.sim >= cut {
				kept = append(kept, c)
			}
		}
		ranked = kept
	} else {
		k := cfg.TopK
		if k <= 0 {
			k = topK
		}
		if k > 0 && len(ranked) > k {
			ranked = ranked[:k]
		}
	}

	result := []Snippet{}
	seenParent := map[string]bool{}
	for _, c := range ranked {
		// Small-to-big: inject a parent-window that is guaranteed to include the
		// matched child. Parent chunks are capped at ingest time, so blindly using
		// the parent head can hide a hit that lives deep in a long section; if the
		// child is outside the stored parent window, expandHit falls back to the
		// child itself. One snippet per parent keeps distinct sections from being
		// crowded out.
		snippet := c.content
		if c.parentID != "" {
			if seenParent[c.parentID] {
				continue
			}
			seenParent[c.parentID] = true
			if parent, _ := store.GetChunkContent(ctx, s.db, c.parentID); strings.TrimSpace(parent) != "" {
				snippet = expandHit(parent, c.content, retrievedSnippetChars)
			}
		}
		result = append(result, Snippet{
			ID:      c.chunkID,
			Index:   len(result) + 1,
			Title:   c.filename,
			URL:     "doc://" + c.documentID,
			Snippet: snippet,
			Source:  "kb",
		})
	}
	return result, nil
}

// keywordOnlyUnembedded returns in-scope CHILD chunks that were intentionally not
// embedded (runPipeline's skipEmbed: small conversation docs, code/config),
// scored by keyword overlap only. Such chunks have no vector in Qdrant and are
// skipped by the dense/keyword Qdrant legs, so this is the ONLY query-driven way
// the search_knowledge_base tool / inject-mode retrieval can reach them when
// Qdrant itself is available. Only chunks that actually match the query (bm > 0)
// are surfaced — a query-driven search shouldn't dump every unembedded chunk
// into context.
func (s *Service) keywordOnlyUnembedded(ctx context.Context, kbIDs []string, convID string, terms []string) []retrievalCandidate {
	if len(terms) == 0 {
		return nil
	}
	rows, err := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	if err != nil {
		return nil
	}
	out := []retrievalCandidate{}
	for _, r := range rows {
		// Parents carry no own text vector; embedded children are already covered
		// by the dense/keyword legs above.
		if r.ChunkType == "parent" || strings.TrimSpace(r.EmbeddingModel) != "" {
			continue
		}
		bm := keywordScore(terms, r.Content)
		if bm <= 0 {
			continue
		}
		out = append(out, retrievalCandidate{
			chunkID:    r.ID,
			documentID: r.DocumentID,
			parentID:   r.ParentID,
			filename:   r.Filename,
			content:    r.Content,
			sim:        0,
			bm:         bm,
		})
	}
	return out
}

func (s *Service) vectorScopeHasEmbeddedChunks(ctx context.Context, scope vector.Scope) bool {
	rows, err := store.ListChunksInScope(ctx, s.db, scope.KBIDs, scope.ConversationID)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: check embedded chunks failed: %v", err)
		}
		return false
	}
	for _, r := range rows {
		if r.ChunkType != "parent" && strings.TrimSpace(r.EmbeddingModel) != "" {
			return true
		}
	}
	return false
}

// searchScope runs the dense + keyword retrieval legs for ONE embedding model over
// the given vector scope, returning fusion-input candidates. Factored out of
// Retrieve so a query whose scope spans sources embedded by DIFFERENT models (a
// KB's locked model vs the global model used for conversation uploads) searches
// each with its OWN query vector — never scoring one model's vectors against
// another's, nor missing a doc whose vectors sit in a different per-dim
// collection. (§4.11 model split)
func (s *Service) searchScope(ctx context.Context, userID, convID string, em Embedder, emName string, dim int, scope vector.Scope, query string, terms []string) ([]retrievalCandidate, error) {
	if !s.vec.Enabled() {
		return nil, errVectorBackendUnavailable
	}
	qVec, cached, err := s.embedQueryCached(ctx, em, emName, query)
	if err != nil {
		return nil, err
	}
	// Trust the actual query-vector width over the configured dim (a model that
	// emits 1024 despite a 1536 config still hits the right collection).
	if len(qVec) > 0 && len(qVec) != dim {
		dim = len(qVec)
	}
	// Query embedding is billable (§8.3) — but only when we actually called the API
	// (no call on a query-vector cache hit, or for the local embedder).
	if !cached && !strings.HasPrefix(emName, "aurelia-local") && userID != "" {
		var wsID string
		if convID != "" {
			_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID).Scan(&wsID)
		}
		_ = store.LogUsage(ctx, s.db, store.UsageLog{
			UserID: userID, WorkspaceID: wsID, ConversationID: convID,
			ModelID: strings.TrimPrefix(emName, "emb:"),
			Purpose: "embedding", InputTokens: estimateTokens(query),
		})
	}
	// §4.11-E independent legs: 30 dense ∥ 30 keyword, fused later; the same scope
	// so a chunk that hits in only one leg survives.
	live, err := s.liveChildChunks(ctx, scope)
	if err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, nil
	}
	if err := s.ensureVectorIndexComplete(ctx, scope, emName, dim, live); err != nil {
		return nil, err
	}
	hits, err := s.vec.Search(ctx, dim, qVec, scope, denseSearchLegLimit)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: qdrant search failed (%v) — injecting full in-scope text", err)
		}
		return nil, fmt.Errorf("%w: %v", errVectorBackendUnavailable, err)
	}
	kwHits, kwErr := s.vec.SearchKeyword(ctx, dim, query, scope, keywordSearchLegLimit)
	if kwErr != nil && s.logger != nil {
		s.logger.Printf("rag: qdrant keyword search failed (%v) — continuing with dense hits", kwErr)
	}
	merged := map[string]retrievalCandidate{}
	for _, h := range hits {
		row, ok := live[h.Payload.ChunkID]
		if !ok || strings.TrimSpace(row.EmbeddingModel) != emName {
			continue
		}
		merged[row.ID] = retrievalCandidate{
			chunkID:    row.ID,
			documentID: row.DocumentID,
			parentID:   row.ParentID,
			filename:   row.Filename,
			content:    row.Content,
			sim:        h.Score,
			bm:         keywordScore(terms, row.Content),
		}
	}
	for _, h := range kwHits {
		row, ok := live[h.Payload.ChunkID]
		if !ok || strings.TrimSpace(row.EmbeddingModel) != emName {
			continue
		}
		if cur, ok := merged[row.ID]; ok {
			cur.bm += keywordScore(terms, row.Content)
			merged[row.ID] = cur
			continue
		}
		merged[row.ID] = retrievalCandidate{
			chunkID:    row.ID,
			documentID: row.DocumentID,
			parentID:   row.ParentID,
			filename:   row.Filename,
			content:    row.Content,
			sim:        0,
			bm:         keywordScore(terms, row.Content),
		}
	}
	out := make([]retrievalCandidate, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	return out, nil
}

func (s *Service) ensureVectorIndexComplete(ctx context.Context, scope vector.Scope, emName string, dim int, live map[string]store.Chunk) error {
	expected := []string{}
	otherModels := map[string]int{}
	for _, r := range live {
		model := strings.TrimSpace(r.EmbeddingModel)
		if model == "" {
			continue
		}
		if model != emName {
			otherModels[model]++
			continue
		}
		expected = append(expected, r.ID)
	}
	if len(otherModels) > 0 {
		names := make([]string, 0, len(otherModels))
		for name := range otherModels {
			names = append(names, name)
		}
		sort.Strings(names)
		if s.logger != nil {
			s.logger.Printf("rag: scope contains chunks embedded by %v while querying with %s — injecting full in-scope text", names, emName)
		}
		return fmt.Errorf("%w: mixed embedding models in scope", errVectorBackendUnavailable)
	}
	if len(expected) == 0 {
		return nil
	}
	status, err := s.vec.VectorChunkStatuses(ctx, dim, scope)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("rag: qdrant consistency check failed (%v) — injecting full in-scope text", err)
		}
		return fmt.Errorf("%w: %v", errVectorBackendUnavailable, err)
	}
	missing := []string{}
	empty := []string{}
	for _, id := range expected {
		st, ok := status[id]
		if !ok || !st.Exists {
			missing = append(missing, id)
			continue
		}
		if !st.HasVector {
			empty = append(empty, id)
		}
	}
	if len(missing) > 0 || len(empty) > 0 {
		sort.Strings(missing)
		sort.Strings(empty)
		sample := append([]string{}, missing...)
		sample = append(sample, empty...)
		if len(sample) > 5 {
			sample = sample[:5]
		}
		if s.logger != nil {
			s.logger.Printf("rag: qdrant index incomplete for dim=%d scope={kb:%v conv:%s}; missing %d/%d chunks, empty vectors %d/%d (sample %v) — injecting full in-scope text", dim, scope.KBIDs, scope.ConversationID, len(missing), len(expected), len(empty), len(expected), sample)
		}
		return fmt.Errorf("%w: vector index missing %d chunks and has %d empty vectors", errVectorBackendUnavailable, len(missing), len(empty))
	}
	return nil
}

func (s *Service) liveChildChunks(ctx context.Context, scope vector.Scope) (map[string]store.Chunk, error) {
	rows, err := store.ListChunksInScope(ctx, s.db, scope.KBIDs, scope.ConversationID)
	if err != nil {
		return nil, err
	}
	live := make(map[string]store.Chunk, len(rows))
	for _, r := range rows {
		if r.ChunkType == "parent" || strings.TrimSpace(r.EmbeddingModel) == "" {
			continue
		}
		live[r.ID] = r
	}
	return live, nil
}

// appendUniqueCandidates appends candidates from src not already in dst (by chunk
// id), used to merge two model-group searches without double-counting.
func appendUniqueCandidates(dst, src []retrievalCandidate) []retrievalCandidate {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[string]bool, len(dst))
	for _, c := range dst {
		seen[c.chunkID] = true
	}
	for _, c := range src {
		if !seen[c.chunkID] {
			dst = append(dst, c)
			seen[c.chunkID] = true
		}
	}
	return dst
}

// retrievalCandidate is one scored chunk feeding the reciprocal-rank fusion in
// Retrieve. Qdrant dense and keyword legs both produce these so the fusion +
// small-to-big expansion runs identically.
type retrievalCandidate struct {
	chunkID    string
	documentID string
	parentID   string
	filename   string
	content    string
	sim        float32 // raw vector similarity (higher = closer)
	bm         float32 // keyword overlap score
}

// fuseReciprocalRank re-orders candidates by reciprocal-rank fusion of the
// vector-similarity ranking and the keyword ranking (§4.11-E). It returns a new
// slice sorted best-first; the input is left untouched.
func fuseReciprocalRank(cands []retrievalCandidate) []retrievalCandidate {
	k := envcfg.Int("AURELIA_RAG_FUSE_RECIPROCAL_RANK", 60)
	n := len(cands)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	fused := make([]float32, n)
	// Vector leg: rank by similarity, accumulate 1/(rank+k).
	sort.SliceStable(idx, func(a, b int) bool { return cands[idx[a]].sim > cands[idx[b]].sim })
	for rank, i := range idx {
		fused[i] += 1 / float32(rank+k)
	}
	// Keyword leg: rank by BM-ish score, accumulate 1/(rank+k).
	sort.SliceStable(idx, func(a, b int) bool { return cands[idx[a]].bm > cands[idx[b]].bm })
	for rank, i := range idx {
		fused[i] += 1 / float32(rank+k)
	}
	sort.SliceStable(idx, func(a, b int) bool { return fused[idx[a]] > fused[idx[b]] })
	out := make([]retrievalCandidate, n)
	for pos, i := range idx {
		out[pos] = cands[i]
	}
	return out
}

// OnDocumentDeleted removes a document's vectors from the search backend so it
// stays in sync with the relational chunk rows the store deletes. No-op when
// the vector backend is disabled.
func (s *Service) OnDocumentDeleted(ctx context.Context, documentID string) error {
	return s.vec.DeleteByDocument(ctx, documentID)
}

// PromoteDocument moves a conversation-scoped document into a KB and RE-EMBEDS
// it with that KB's locked embedder (§C5). The old chunks/vectors were embedded
// with the conversation's (possibly different model/dim) embedder; keeping them
// would silently break retrieval, so we drop them and re-run the pipeline, which
// resolves the destination KB's embedder. Re-ingest is async (Ingest enqueues).
func (s *Service) PromoteDocument(ctx context.Context, docID, kbID string) error {
	if err := store.PromoteDocumentToKB(ctx, s.db, docID, kbID); err != nil {
		return err
	}
	_ = s.vec.DeleteByDocument(ctx, docID)
	if err := store.DeleteChunksByDocument(ctx, s.db, docID); err != nil {
		return err
	}
	s.Ingest(docID) // re-parse + re-embed with the KB's locked model
	return nil
}

// OnKBDeleted removes every vector belonging to a knowledge base.
func (s *Service) OnKBDeleted(ctx context.Context, kbID string) error {
	return s.vec.DeleteByKB(ctx, kbID)
}

// OnConversationDeleted removes every vector belonging to a conversation's
// uploads.
func (s *Service) OnConversationDeleted(ctx context.Context, conversationID string) error {
	return s.vec.DeleteByConversation(ctx, conversationID)
}

func snippetOf(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if max <= 0 {
		max = snippetDefaultMax
	}
	if len(s) > max {
		// Rune-safe cut — a byte slice mid-rune produces invalid UTF-8 (mojibake /
		// a Postgres-rejected trailing byte) for CJK content.
		return s[:clampRune(s, max-1)] + "…"
	}
	return s
}

// clampRune clamps a byte offset into [0,len(s)] and snaps it forward to a UTF-8
// rune boundary so substring slices never split a multi-byte (e.g. CJK) rune.
func clampRune(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// Budget (bytes) for a retrieved chunk's injected snippet. Sized to hold a full
// child chunk (childTargetChars) plus a little surrounding section context, so a
// hit deep in a long section is shown in full rather than truncated to the
// section head. (§4.11 retrieval fidelity)
var retrievedSnippetChars = envcfg.Int("AURELIA_RAG_RETRIEVED_SNIPPET_CHARS", 2000)

// expandHit returns a snippet that is GUARANTEED to contain the matched child
// chunk, with surrounding section context when available. The old small-to-big
// path returned snippetOf(parent, …) — i.e. always the SECTION HEAD — so a hit
// located deep in a long section, or past the parent's truncation, was dropped
// from what the model saw (it "matched 案例98 but couldn't answer"). Here we find
// the child inside its parent section and center a budget-sized window on it;
// when the child lies beyond the parent's truncation we return the child itself.
func expandHit(parent, child string, budget int) string {
	child = strings.TrimSpace(child)
	if parent != "" {
		needle := stripBreadcrumb(child)
		if needle != "" {
			if idx := strings.Index(parent, needle); idx >= 0 {
				win := windowAround(parent, idx, idx+len(needle), budget)
				if bc := breadcrumbOf(child); bc != "" {
					win = bc + " " + win
				}
				return win
			}
		}
	}
	// No parent, or the child sits past the parent's truncation → the child IS
	// the hit; return it directly.
	return snippetOf(child, budget)
}

// windowAround returns a rune-safe, budget-sized window of s spanning the byte
// range [start,end], centered on it, with ellipses where trimmed and newlines
// collapsed.
func windowAround(s string, start, end, budget int) string {
	if end-start >= budget {
		return snippetOf(s[clampRune(s, start):], budget)
	}
	pad := (budget - (end - start)) / 2
	ws := clampRune(s, start-pad)
	we := clampRune(s, end+pad)
	out := strings.TrimSpace(strings.ReplaceAll(s[ws:we], "\n", " "))
	if ws > 0 {
		out = "…" + out
	}
	if we < len(s) {
		out = out + "…"
	}
	return out
}

// stripBreadcrumb removes the leading "[breadcrumb]\n" prefix added to a child at
// ingest, so the child text can be located inside its (un-prefixed) parent.
func stripBreadcrumb(s string) string {
	if strings.HasPrefix(s, "[") {
		if nl := strings.IndexByte(s, '\n'); nl > 0 && strings.IndexByte(s[:nl], ']') > 0 {
			return strings.TrimSpace(s[nl+1:])
		}
	}
	return s
}

// breadcrumbOf returns the "[breadcrumb]" prefix of a child chunk (heading path),
// or "" when absent.
func breadcrumbOf(s string) string {
	if strings.HasPrefix(s, "[") {
		if nl := strings.IndexByte(s, '\n'); nl > 0 && strings.IndexByte(s[:nl], ']') > 0 {
			return strings.TrimSpace(s[:nl])
		}
	}
	return ""
}

// tokenize splits text into lexical terms for keyword scoring AND the hashed
// local embedder. ASCII/Latin words are kept whole (so existing Latin indexes +
// embeddings are byte-for-byte unchanged), but spaceless CJK is segmented into
// overlapping bigrams (plus any embedded digits/Latin).
//
// Why: the old `[\p{L}\p{N}_]+` regex collapsed an entire Chinese phrase into ONE
// token (Han chars are \p{L} with no spaces between them). `keywordScore` then did
// `strings.Count(doc, term)` with that whole-phrase term, which never matched, and
// the FNV-hashed embedder put the whole phrase in one bucket — so a reference
// buried inside the phrase, e.g. "案例98" inside "讲解案例98及相关知识点", was
// unretrievable. Bigram segmentation makes "案例"/"98"/"知识"… matchable units that
// a query and a document share. (§4.11 CJK retrieval)
func tokenize(s string) []string {
	re := regexp.MustCompile(`[\p{L}\p{N}_]+`)
	runs := re.FindAllString(s, -1)
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		if !hasCJK(run) {
			out = append(out, run) // Latin/alphanumeric — unchanged
			continue
		}
		out = append(out, cjkGrams(run)...)
	}
	return out
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func hasCJK(s string) bool {
	for _, r := range s {
		if isCJKRune(r) {
			return true
		}
	}
	return false
}

// estimateTokens approximates a string's token count. The plain byte-len/4
// heuristic is tuned for English and badly UNDER-counts CJK: each Han/Kana/Hangul
// char is ~3 UTF-8 bytes but ~1 token, so len/4 scores it ~0.75 tokens — which let
// long Chinese documents be misjudged as "fits the full-text window" and silently
// overflow the prompt. We count CJK runes as ~1 token each and apply byte/4 to the
// rest. (§4.11 token budgeting)
func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJKRune(r) {
			cjk++
		} else {
			other += utf8.RuneLen(r)
		}
	}
	return cjk + other/4
}

// cjkGrams segments a mixed CJK run: maximal CJK spans become overlapping bigrams
// (a lone CJK char → itself), and ASCII/Latin/digit spans are kept whole (so an
// id like "98" inside "案例98" survives as its own matchable token).
func cjkGrams(run string) []string {
	runes := []rune(run)
	out := []string{}
	for i := 0; i < len(runes); {
		if isCJKRune(runes[i]) {
			j := i
			for j < len(runes) && isCJKRune(runes[j]) {
				j++
			}
			seg := runes[i:j]
			if len(seg) == 1 {
				out = append(out, string(seg))
			} else {
				for k := 0; k+1 < len(seg); k++ {
					out = append(out, string(seg[k:k+2]))
				}
			}
			i = j
		} else {
			j := i
			for j < len(runes) && !isCJKRune(runes[j]) {
				j++
			}
			out = append(out, string(runes[i:j]))
			i = j
		}
	}
	return out
}

func keywordScore(terms []string, doc string) float32 {
	if len(terms) == 0 || doc == "" {
		return 0
	}
	low := strings.ToLower(doc)
	score := float32(0)
	for _, t := range terms {
		count := strings.Count(low, t)
		if count > 0 {
			score += float32(math.Log(float64(1 + count)))
		}
	}
	return score
}

// LocalEmbedder hashes tokens into a fixed-dimension feature vector. The
// result is deterministic, fast and good enough to make local search work
// without external services.
type LocalEmbedder struct{ Dim int }

// NewLocalEmbedder returns a LocalEmbedder at the given dimension.
func NewLocalEmbedder(dim int) *LocalEmbedder { return &LocalEmbedder{Dim: dim} }

// Embed returns one vector per text.
func (l *LocalEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = featureVector(t, l.Dim)
	}
	return out, nil
}

func featureVector(s string, dim int) []float32 {
	v := make([]float32, dim)
	terms := tokenize(strings.ToLower(s))
	for _, t := range terms {
		h := fnv.New32a()
		_, _ = h.Write([]byte(t))
		idx := int(h.Sum32() % uint32(dim))
		v[idx] += 1
	}
	// L2 normalise.
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	if norm > 0 {
		inv := 1 / float32(math.Sqrt(float64(norm)))
		for i := range v {
			v[i] *= inv
		}
	}
	return v
}

// parentChunk groups one large section with its embedded child chunks
// (§4.11-C-2 small-to-big: search the small, return the big).
type parentChunk struct {
	Content  string
	Children []string
	// Breadcrumb is the heading path leading to this section (e.g.
	// "Manual > Setup > Networking"). Prepended to each child so embeddings
	// carry context the bare body would otherwise lose (§4.11-C-1).
	Breadcrumb string
	// ChunkType marks atomic units we MUST NOT split mid-stream (table, code).
	// "text" = default; "table" = preserve whole row block; "code" = preserve
	// fenced block.
	ChunkType string
}

// Target child chunk size in characters. The design specifies 400-800 tokens;
// at ~4 chars/token the safe range is ~1600-3200 chars. Defaulting at 2000
// gives the embedder enough context per vector without diluting precision.
var (
	childTargetChars  = envcfg.Int("AURELIA_RAG_CHILD_TARGET_CHARS", 2000)
	parentTargetChars = envcfg.Int("AURELIA_RAG_PARENT_TARGET_CHARS", 4800)
	// Overlap between consecutive children (~12%) keeps boundary information
	// retrievable from either side (§4.11-C-1 "10-15% overlap").
	chunkOverlapChars = envcfg.Int("AURELIA_RAG_CHUNK_OVERLAP_CHARS", 250)
)

// chunkHierarchical splits content into parent sections, each subdivided into
// overlapping child chunks. Children are embedded; the parent is returned at
// retrieval time for fuller context. The structural splitter respects
// headings → paragraphs → sentences as natural break points and protects
// tables / fenced code blocks as atomic units.
func chunkHierarchical(content string) []parentChunk {
	sections := splitByHeadings(content)
	out := []parentChunk{}
	for _, sec := range sections {
		atoms := splitProtectedAtoms(sec.body)
		merged := mergeAtomsIntoChildren(atoms, childTargetChars)
		if len(merged) == 0 {
			continue
		}
		// Sliding-window overlap between adjacent children.
		children := withOverlap(merged, chunkOverlapChars)
		// Prefix each child with the breadcrumb so its embedding captures the
		// heading path (§4.11-C-1).
		breadcrumb := sec.breadcrumb
		labeled := make([]string, len(children))
		for i, c := range children {
			if breadcrumb != "" {
				labeled[i] = "[" + breadcrumb + "]\n" + c
			} else {
				labeled[i] = c
			}
		}
		parent := truncateAt(sec.body, parentTargetChars)
		out = append(out, parentChunk{
			Content: parent, Children: labeled,
			Breadcrumb: breadcrumb, ChunkType: "text",
		})
	}
	return out
}

// section represents a body of text under a heading path.
type section struct {
	breadcrumb string
	body       string
}

// headingRe matches ATX-style markdown headings (#, ##, … up to ######) at the
// start of a line. We also accept "Section N:" style headings as a fallback.
var headingRe = regexp.MustCompile(`(?m)^(\s{0,3})(#{1,6})\s+(.+)$`)

// splitByHeadings cuts content at heading lines, building a heading-path
// breadcrumb (e.g. "Manual > Setup > Networking") for each resulting body.
func splitByHeadings(content string) []section {
	matches := headingRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return []section{{breadcrumb: "", body: content}}
	}
	out := []section{}
	stack := []string{}
	prevDepth := 0
	cursor := 0
	for _, m := range matches {
		// Body BEFORE this heading line. For the first heading this is the document
		// preamble (frontmatter / abstract / intro / title blurb) — keep it with an
		// empty breadcrumb (stack is empty here) instead of dropping it; for later
		// headings it's the previous section's body under the current stack.
		headingStart := m[0]
		body := content[cursor:headingStart]
		if strings.TrimSpace(body) != "" {
			out = append(out, section{breadcrumb: strings.Join(stack, " > "), body: body})
		}
		depth := m[5] - m[4] // length of the # run (group 2)
		title := strings.TrimSpace(content[m[6]:m[7]])
		// Maintain heading-depth stack: pop until our depth fits, then push.
		if depth <= prevDepth {
			pops := prevDepth - depth + 1
			if pops > len(stack) {
				pops = len(stack)
			}
			stack = stack[:len(stack)-pops]
		}
		stack = append(stack, title)
		prevDepth = depth
		cursor = m[1]
	}
	// Tail body.
	if cursor < len(content) {
		tail := content[cursor:]
		if strings.TrimSpace(tail) != "" {
			out = append(out, section{breadcrumb: strings.Join(stack, " > "), body: tail})
		}
	}
	if len(out) == 0 {
		out = append(out, section{breadcrumb: "", body: content})
	}
	return out
}

// atom is one chunkable unit: a paragraph, a table block, or a fenced code
// block. Tables and code are marked atomic so mergeAtomsIntoChildren never
// splits them across two children (§4.11-C-1 "保护表格/代码").
type atom struct {
	text   string
	atomic bool
}

// fenced code fence ``` … ``` plus pipe-table runs are recognised as atomics.
var (
	fenceRe = regexp.MustCompile("(?ms)^(\\s{0,3}```[^\n]*\\n.*?```)")
	tableRe = regexp.MustCompile(`(?m)^\|.*\|\s*$`)
	mathRe  = regexp.MustCompile(`(?ms)^\\\$\\\$.*?\\\$\\\$`)
	imageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
)

// splitProtectedAtoms returns paragraph + table + code-block atoms in document
// order. Anything inside a fenced code block or a contiguous table run is kept
// in a single atom so a child chunk can never end mid-row or mid-statement.
func splitProtectedAtoms(body string) []atom {
	if body == "" {
		return nil
	}
	out := []atom{}
	rest := body
	// First, peel off fenced code blocks (they win over paragraphs).
	for {
		loc := fenceRe.FindStringIndex(rest)
		if loc == nil {
			break
		}
		before := rest[:loc[0]]
		code := rest[loc[0]:loc[1]]
		if strings.TrimSpace(before) != "" {
			out = append(out, splitParagraphsAndTables(before)...)
		}
		out = append(out, atom{text: code, atomic: true})
		rest = rest[loc[1]:]
	}
	if strings.TrimSpace(rest) != "" {
		out = append(out, splitParagraphsAndTables(rest)...)
	}
	return out
}

// splitParagraphsAndTables splits a non-code chunk by blank lines and groups
// consecutive pipe-table lines into one atomic atom.
func splitParagraphsAndTables(s string) []atom {
	out := []atom{}
	for _, para := range regexp.MustCompile(`\n{2,}`).Split(s, -1) {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		// If every line of this paragraph looks like a table row, treat the
		// whole paragraph as atomic.
		lines := strings.Split(p, "\n")
		allTable := true
		for _, l := range lines {
			if !tableRe.MatchString(strings.TrimRight(l, " \t")) {
				allTable = false
				break
			}
		}
		if allTable && len(lines) >= 2 {
			out = append(out, atom{text: p, atomic: true})
			continue
		}
		// math/image blocks are kept whole too.
		if mathRe.MatchString(p) || (imageRe.MatchString(p) && len(p) < imageAtomSizeThreshold) {
			out = append(out, atom{text: p, atomic: true})
			continue
		}
		// Otherwise long paragraphs are sub-split on sentence boundaries inside
		// mergeAtomsIntoChildren so we never split mid-sentence.
		out = append(out, atom{text: p, atomic: false})
	}
	return out
}

// mergeAtomsIntoChildren accumulates atoms into children of ~target chars,
// splitting long non-atomic paragraphs on sentence boundaries when necessary.
func mergeAtomsIntoChildren(atoms []atom, target int) []string {
	out := []string{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	for _, a := range atoms {
		if a.atomic {
			if cur.Len() > 0 {
				flush()
			}
			out = append(out, a.text)
			continue
		}
		if cur.Len()+len(a.text) > target && cur.Len() > 0 {
			flush()
		}
		// If a paragraph alone exceeds target, sentence-split it.
		if len(a.text) > target {
			sentences := splitSentences(a.text)
			for _, s := range sentences {
				if len(s) > target {
					if cur.Len() > 0 {
						flush()
					}
					for _, part := range splitLongTextByChars(s, target) {
						out = append(out, part)
					}
					continue
				}
				if cur.Len()+len(s) > target && cur.Len() > 0 {
					flush()
				}
				if cur.Len() > 0 {
					cur.WriteString(" ")
				}
				cur.WriteString(s)
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(a.text)
	}
	flush()
	return out
}

func splitLongTextByChars(s string, target int) []string {
	if target <= 0 || len(s) <= target {
		return []string{s}
	}
	out := []string{}
	rest := strings.TrimSpace(s)
	for len(rest) > target {
		cut := clampRune(rest, target)
		out = append(out, strings.TrimSpace(rest[:cut]))
		rest = strings.TrimSpace(rest[cut:])
	}
	if rest != "" {
		out = append(out, rest)
	}
	return out
}

// withOverlap re-emits children so each (except the first) starts with the
// tail of the previous one. Skip atomics: tables / code don't need overlap.
func withOverlap(children []string, overlap int) []string {
	if overlap <= 0 || len(children) <= 1 {
		return children
	}
	out := make([]string, 0, len(children))
	for i, c := range children {
		if i == 0 {
			out = append(out, c)
			continue
		}
		prev := children[i-1]
		// Don't overlap into a code/table block (it would be a fragment).
		if strings.Contains(prev, "```") || strings.HasPrefix(strings.TrimSpace(prev), "|") {
			out = append(out, c)
			continue
		}
		tail := prev
		if len(tail) > overlap {
			tail = tail[clampRune(tail, len(tail)-overlap):]
			// Pull back to a word boundary.
			if i := strings.IndexAny(tail, " \n。.；;！!？?"); i > 0 && i < len(tail)-1 {
				tail = tail[i+1:]
			}
		}
		out = append(out, strings.TrimSpace(tail)+"\n\n"+c)
	}
	return out
}

// splitSentences breaks a paragraph on common sentence-ending punctuation,
// supporting CJK (。 ！ ？ ；) plus ASCII.
func splitSentences(p string) []string {
	// Insert a NUL after each end-of-sentence punctuation then split on NUL.
	endRunes := map[rune]bool{'.': true, '!': true, '?': true, ';': true, '。': true, '！': true, '？': true, '；': true}
	var b strings.Builder
	for _, r := range p {
		b.WriteRune(r)
		if endRunes[r] {
			b.WriteByte(0)
		}
	}
	raw := strings.Split(b.String(), "\x00")
	out := []string{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{p}
	}
	return out
}

func truncateAt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:clampRune(s, max)]
}

// mineruImageMarker matches the markdown image syntax parser.go emits for
// MinerU image extractions: `![caption](mineru://filename)`. The capture
// groups are (1) caption, (2) filename.
var mineruImageMarker = regexp.MustCompile(`!\[([^\]]*)\]\(mineru://([^)]+)\)`)

// soleMineruImageMarker reports whether `chunk` consists of exactly one
// MinerU image marker (after trimming whitespace and an optional leading
// `<!-- mineru-image … -->` page-number comment that parser.go appends).
// Returns the image filename on hit.
//
// Rationale: a child chunk that mixes prose with an image marker should
// stay `text` — we don't want to collapse the prose under an image_ref or
// classify the chunk against just one of multiple image filenames it may
// contain. This is the strict check the chunker uses for classification;
// the broader `mineruImageMarker` regex is still used elsewhere.
func soleMineruImageMarker(chunk string) (string, bool) {
	s := strings.TrimSpace(chunk)
	// Strip an optional leading page-number comment we emit when we append
	// fallback image markers in parser.go.
	if strings.HasPrefix(s, "<!--") {
		if end := strings.Index(s, "-->"); end > 0 {
			s = strings.TrimSpace(s[end+3:])
		}
	}
	m := mineruImageMarker.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	// The whole match must cover the trimmed chunk — any trailing prose or
	// a second marker would leave residue.
	if m[0] != s {
		return "", false
	}
	return m[2], true
}

// RouteDecision is the structured output of the query router (§4.11-B).
type RouteDecision struct {
	Strategy string   `json:"strategy"` // retrieve | full_doc | none
	Queries  []string `json:"queries"`
}

// RAG knobs (§4.11-B) are admin-tunable from the Documents settings page and read
// live from the settings table (no restart). Defaults inject only genuinely small
// docs in full and send everything larger to RETRIEVAL — a whole medium file
// injected every turn is what blew the prompt to 70k+.
const (
	// defaultRAGFullTextThreshold: a conversation doc whose estimated tokens are
	// at/below this is injected in FULL (and not vectorised); above it the doc is
	// vectorised and only relevant chunks are retrieved.
	defaultRAGFullTextThreshold = 8000
	defaultRAGTopK              = 8
	defaultRAGSimThreshold      = 0.5
)

// ragSettings holds the live RAG configuration.
type ragSettings struct {
	FullTextThreshold int     // ≤ this (est. tokens) → inject whole; above → retrieve
	TopK              int     // chunks retrieved when DynamicTopK is off
	DynamicTopK       bool    // inject ALL hits with cosine sim ≥ SimThreshold instead of a fixed K
	SimThreshold      float64 // dynamic-topK cutoff (cosine similarity)
}

// ragSettings reads the admin-tunable RAG knobs (with safe defaults).
func (s *Service) ragSettings() ragSettings {
	c := ragSettings{FullTextThreshold: defaultRAGFullTextThreshold, TopK: defaultRAGTopK, SimThreshold: defaultRAGSimThreshold}
	if raw, err := store.GetSetting(s.db, "rag_full_text_threshold"); err == nil && len(raw) > 0 {
		var v int
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.FullTextThreshold = v
		}
	}
	if raw, err := store.GetSetting(s.db, "rag_top_k"); err == nil && len(raw) > 0 {
		var v int
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.TopK = v
		}
	}
	if raw, err := store.GetSetting(s.db, "rag_dynamic_topk"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.DynamicTopK)
	}
	if raw, err := store.GetSetting(s.db, "rag_similarity_threshold"); err == nil && len(raw) > 0 {
		var v float64
		if json.Unmarshal(raw, &v) == nil && v > 0 {
			c.SimThreshold = v
		}
	}
	return c
}

// RouteAndRetrieve runs the §4.11-B router pipeline:
//
//  1. Small scope (≤ FullTextThreshold) → inject the full text directly, no
//     router call at all.
//  2. Otherwise ask the task model: none | retrieve (with rewritten queries) |
//     full_doc.
//  3. full_doc over ContextBudget → map-reduce: summarise chunk groups via the
//     task model, then merge (§4.11-B).
//
// When the task model is unavailable it falls back to "retrieve" using the
// user's text as the query (safest).
func (s *Service) RouteAndRetrieve(ctx context.Context, userID, convID string, kbIDs []string, userText string, history []string, topK int) ([]Snippet, RouteDecision, error) {
	decision := RouteDecision{Strategy: "retrieve", Queries: []string{userText}}
	cfg := s.ragSettings()

	// Step 0: size check + pinned docs. "Pinned" = in-scope child chunks with no
	// embedding — small conversation docs we intentionally did NOT vectorise (see
	// runPipeline). They can't be semantically retrieved, so they are ALWAYS
	// injected in full whenever we don't take the whole-scope full-text path.
	scope, _ := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	if len(scope) > 0 && !s.vec.Enabled() {
		decision.Strategy = "full_text"
		return fullTextSnippets(scope), decision, nil
	}
	pinned := []store.Chunk{}
	embeddedTokens := 0
	pinnedTokens := 0
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue // parents duplicate child text
		}
		if strings.TrimSpace(c.EmbeddingModel) == "" {
			pinned = append(pinned, c)
			pinnedTokens += estimateTokens(c.Content)
		} else {
			embeddedTokens += estimateTokens(c.Content)
		}
	}
	// Whole scope fits (or nothing is embedded) → inject everything in full.
	if len(scope) > 0 && (embeddedTokens == 0 || pinnedTokens+embeddedTokens <= cfg.FullTextThreshold) {
		decision.Strategy = "full_text"
		return fullTextSnippets(scope), decision, nil
	}
	// Otherwise retrieve over the embedded chunks, but ALWAYS prepend the pinned
	// (unembedded) docs in full so they're never dropped from a large, mixed-size
	// conversation. No injection cap (§admin RAG) — cost is guarded by the
	// pre-flight credit check, not by truncation.
	pinnedSnips := fullTextSnippets(pinned)
	withPinned := func(out []Snippet) []Snippet {
		if len(pinnedSnips) == 0 {
			return out
		}
		merged := make([]Snippet, 0, len(pinnedSnips)+len(out))
		seen := map[string]bool{}
		for _, sn := range pinnedSnips {
			if seen[sn.ID] {
				continue
			}
			seen[sn.ID] = true
			merged = append(merged, sn)
		}
		for _, sn := range out {
			if seen[sn.ID] {
				continue
			}
			seen[sn.ID] = true
			merged = append(merged, sn)
		}
		for i := range merged {
			merged[i].Index = i + 1
		}
		return merged
	}

	// Build a list of (filename, ~first sentence) so the router can resolve
	// pronouns like "this report" / "the second doc" (§4.11-B router prompt).
	docHints := s.collectDocHints(ctx, kbIDs, convID)

	if s.task == nil {
		out, err := s.Retrieve(ctx, userID, convID, kbIDs, userText, cfg.TopK)
		return withPinned(out), decision, err
	}
	prompt := buildRouterPrompt(userText, history, docHints)
	var d RouteDecision
	// The router is a small-model JSON call on the FIRST-TOKEN hot path — bound
	// it. A slow or hung task-model channel must degrade to plain retrieval with
	// the original query (the same fallback as s.task == nil), not stall the
	// user's reply for minutes.
	rctx, cancelRouter := context.WithTimeout(ctx, routerCallTimeout)
	err := s.task.RunJSON(rctx, "task.router", prompt, &d, RouterOpts{UserID: userID, ConversationID: convID})
	cancelRouter()
	if err == nil {
		if d.Strategy != "" {
			decision = d
		}
	} else if s.logger != nil {
		s.logger.Printf("rag: router call failed (falling back to retrieve): %v", err)
	}
	switch decision.Strategy {
	case "none":
		// Even when the router sees no need to retrieve, the pinned (small,
		// always-on) docs are still injected — the user uploaded them on purpose.
		return withPinned(nil), decision, nil
	case "full_doc":
		// Whole-document question → inject the entire document in order. No cap
		// (§admin RAG); the pre-flight credit check governs cost.
		return fullTextSnippets(scope), decision, nil
	default:
		// retrieve: run each rewritten query, merge + dedupe. With dynamic top-K
		// the per-query result is already similarity-bounded, so don't cap the
		// merge; with fixed K, cap the merged set at K.
		seen := map[string]struct{}{}
		merged := []Snippet{}
		queries := decision.Queries
		if len(queries) == 0 {
			queries = []string{userText}
		}
		for _, q := range queries {
			subset, err := s.Retrieve(ctx, userID, convID, kbIDs, q, cfg.TopK)
			if err != nil {
				continue
			}
			for _, sn := range subset {
				if _, ok := seen[sn.ID]; ok {
					continue
				}
				seen[sn.ID] = struct{}{}
				merged = append(merged, sn)
			}
			if !cfg.DynamicTopK && len(merged) >= cfg.TopK {
				break
			}
		}
		if !cfg.DynamicTopK && len(merged) > cfg.TopK {
			merged = merged[:cfg.TopK]
		}
		return withPinned(merged), decision, nil
	}
}

// fullTextSnippets returns the scope's child chunks in document order, each in
// FULL — no token budget / truncation (§admin RAG: "删除封顶"). Cost on a credit
// turn is bounded by the pre-flight estimate, not here.
func fullTextSnippets(scope []store.Chunk) []Snippet {
	out := []Snippet{}
	idx := 1
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue
		}
		out = append(out, Snippet{
			ID:      c.ID,
			Index:   idx,
			Title:   c.Filename,
			URL:     "doc://" + c.DocumentID,
			Snippet: c.Content,
			Source:  "kb",
		})
		idx++
	}
	return out
}

// mapReduceSummarise condenses an over-budget corpus (§4.11-B): chunk groups
// are summarised by the task model with the user's question as focus (map),
// and the partial summaries are returned as snippets (reduce happens in the
// answer model's context).
func (s *Service) mapReduceSummarise(ctx context.Context, userID, convID string, scope []store.Chunk, userText string) ([]Snippet, error) {
	if s.task == nil {
		return nil, nil
	}
	groupTokens := envcfg.Int("AURELIA_RAG_MAPREDUCE_GROUPTOKENS", 6000)
	maxGroups := envcfg.Int("AURELIA_RAG_MAPREDUCE_MAXGROUPS", 8)
	groups := [][]store.Chunk{}
	cur := []store.Chunk{}
	used := 0
	for _, c := range scope {
		if c.ChunkType == "parent" {
			continue
		}
		t := estimateTokens(c.Content)
		if used+t > groupTokens && len(cur) > 0 {
			groups = append(groups, cur)
			cur, used = nil, 0
			if len(groups) >= maxGroups {
				break
			}
		}
		cur = append(cur, c)
		used += t
	}
	if len(cur) > 0 && len(groups) < maxGroups {
		groups = append(groups, cur)
	}

	out := []Snippet{}
	for gi, g := range groups {
		var b strings.Builder
		fmt.Fprintf(&b, "针对问题「%s」，提炼下面文档片段中相关的事实与数据，≤%d字。无关内容忽略。\n\n", userText, mapReduceSummaryChars)
		for _, c := range g {
			b.WriteString(c.Content)
			b.WriteString("\n\n")
		}
		var summary struct {
			Summary string `json:"summary"`
		}
		text := ""
		if err := s.task.RunJSON(ctx, "task.router", b.String()+`\n以 JSON 回复: {"summary":"..."}`, &summary, RouterOpts{UserID: userID, ConversationID: convID}); err == nil {
			text = summary.Summary
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, Snippet{
			ID:      g[0].ID,
			Index:   gi + 1,
			Title:   g[0].Filename + " (摘要)",
			URL:     "doc://" + g[0].DocumentID,
			Snippet: text,
			Source:  "kb",
		})
	}
	return out, nil
}

func buildRouterPrompt(userText string, history []string, docHints []string) string {
	b := strings.Builder{}
	b.WriteString("Decide whether the user's question needs retrieval from their uploaded documents.\n\n")
	if len(docHints) > 0 {
		b.WriteString("Documents in scope (filename — first words):\n")
		for _, d := range docHints {
			b.WriteString("- ")
			b.WriteString(d)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(history) > 0 {
		b.WriteString("Recent conversation:\n")
		for _, h := range history {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Latest user message:\n")
	b.WriteString(userText)
	b.WriteString("\n\n")
	b.WriteString(`Rules:
- Use "none" when the question is unrelated to the documents (general chit-chat, math, code unrelated to files).
- Use "retrieve" for fact-finding inside the documents — return 2-4 rewritten queries that disambiguate pronouns ("this report" → use the filename) and split compound questions.
- Use "full_doc" when the user asks for a summary, an overview, or a high-level comparison spanning the WHOLE document(s).
Reply with strict JSON: {"strategy":"retrieve|full_doc|none","queries":["<rewritten query 1>","<rewritten query 2>"]}`)
	return b.String()
}

// collectDocHints returns up to ~12 "filename — first ~120 chars" lines so the
// router can resolve "this report", "the second one", etc. without a separate
// look-up. Chunks of type=parent are preferred since they carry the section
// heading.
func (s *Service) collectDocHints(ctx context.Context, kbIDs []string, convID string) []string {
	seen := map[string]bool{}
	hints := []string{}
	chunks, _ := store.ListChunksInScope(ctx, s.db, kbIDs, convID)
	for _, c := range chunks {
		if seen[c.DocumentID] {
			continue
		}
		seen[c.DocumentID] = true
		first := c.Content
		if len(first) > docHintFirstContentCap {
			first = first[:docHintFirstContentCap]
		}
		first = strings.ReplaceAll(first, "\n", " ")
		hints = append(hints, c.Filename+" — "+strings.TrimSpace(first))
		if len(hints) >= docHintsMaxCount {
			break
		}
	}
	return hints
}
