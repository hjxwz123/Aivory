// Aivory API server — entrypoint.
//
// Runs the HTTP API described in design.md §6. Wires the SQLite-backed store,
// Provider registry, tools registry, async queue, and the SSE-aware chat
// orchestrator into a single net/http handler. Boots clean against an empty
// database (the migration runs at startup) and seeds an initial admin user.
// An admin must then configure a real provider channel + model before chat
// works — there is no mock fallback.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aivory/server/internal/api"
	"aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/envcfg"
	"aivory/server/internal/llm"
	"aivory/server/internal/mail"
	"aivory/server/internal/queue"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
	"aivory/server/internal/tools"
	"aivory/server/internal/vector"
)

var (
	archiveGcBootSettleDelay  = envcfg.Dur("AIVORY_CMD_ARCHIVE_GC_BOOT_SETTLE_DELAY", 2*time.Minute)
	runPruneCtxTimeout        = envcfg.Dur("AIVORY_CMD_RUN_PRUNE", 5*time.Minute)
	archiveGcSweepInterval    = envcfg.Dur("AIVORY_CMD_ARCHIVE_GC_SWEEP_INTERVAL", 6*time.Hour)
	taskRouterMaxOutputTokens = 256
)

func main() {
	cfg := config.Load()
	logger := log.New(os.Stdout, "aivory ", log.LstdFlags|log.Lmicroseconds)

	// §8.1 production guard — refuse to boot with the dev JWT_SECRET in prod.
	if err := config.Validate(cfg); err != nil {
		logger.Fatalf("config: %v", err)
	}

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		logger.Fatalf("store.Migrate: %v", err)
	}
	if err := store.Seed(db, cfg); err != nil {
		logger.Fatalf("store.Seed: %v", err)
	}

	// Cache: Redis in production (cross-process pub/sub for §11.5 stop-stream and
	// §8.1 realtime bans), in-memory when REDIS_URL is unset (dev, no install).
	var cacheLayer cache.Cache
	if cfg.RedisURL != "" {
		rc, err := cache.NewRedis(cfg.RedisURL)
		if err != nil {
			logger.Fatalf("cache.NewRedis: %v", err)
		}
		cacheLayer = rc
		logger.Printf("cache: redis (%s)", redactURL(cfg.RedisURL))
	} else {
		cacheLayer = cache.NewMemory()
		logger.Printf("cache: in-memory (dev)")
	}

	// §2.4 config-cache invalidation: an admin config write on any instance
	// publishes "cfg:invalidate"; every instance drops its settings cache so a
	// change can't outlive its TTL on another node.
	go func() {
		ch, _ := cacheLayer.Subscribe("cfg:invalidate")
		for range ch {
			store.InvalidateConfig()
		}
	}()

	q := queue.NewInProcess(logger)
	defer q.Close()

	authSvc := auth.New(cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL, cacheLayer)

	providers := llm.NewRegistry(logger)
	ragSvc := rag.New(db, q, logger)
	ragSvc.SetExternalConfig(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim, cfg.MinerUAPIURL, cfg.MinerUAPIKey)
	// MinerU's upload-then-fetch flow needs the sandbox sidecar (which hosts
	// /storage/put + /storage/delete). Admin settings override these at
	// ingest time, but the env defaults make a fresh install Just Work when
	// the operator has only set SANDBOX_BASE_URL.
	ragSvc.SetSandboxFallback(cfg.SandboxBaseURL, cfg.SandboxAPIKey)
	// Vector backend: Qdrant in production; when QDRANT_URL is unset the RAG
	// layer injects full in-scope document text instead of vector retrieval.
	if cfg.QdrantURL != "" {
		ragSvc.SetVectorStore(vector.NewQdrant(cfg.QdrantURL, cfg.QdrantAPIKey))
		logger.Printf("vector: qdrant (%s)", redactURL(cfg.QdrantURL))
	} else {
		logger.Printf("vector: disabled (full-context fallback over %s)", driverName(cfg.DatabaseURL))
	}
	if cfg.RedisURL != "" {
		if err := ragSvc.UseAsynq(cfg.RedisURL); err != nil {
			logger.Fatalf("rag asynq: %v", err)
		}
		defer ragSvc.CloseAsynq()
		logger.Printf("rag queue: asynq/redis")
	} else {
		logger.Printf("rag queue: in-process (dev)")
	}
	toolRegistry := tools.NewRegistry(db, ragSvc, cfg, logger)
	// Surface the sandbox wiring at boot — the #1 reason python_execute silently
	// falls back to "safe-mode" (and the model says it can't run code / host
	// downloads) is an empty SANDBOX_BASE_URL in the API container.
	if cfg.SandboxBaseURL != "" {
		logger.Printf("sandbox: %s", redactURL(cfg.SandboxBaseURL))
	} else {
		logger.Printf("sandbox: disabled (set SANDBOX_BASE_URL; python_execute runs in safe-mode)")
	}

	// §4.5 archived-workspace GC: the sidecar drops one /workspace tarball into
	// object storage per reaped session and never removes it, so the bucket grows
	// without bound. Sweep every 6h and delete archives older than the
	// admin-configured TTL (settings.storage_archive_ttl_days; 0/blank = off).
	go func() {
		time.Sleep(archiveGcBootSettleDelay) // let boot settle; don't sweep on cold start
		// One sweep, with its own recover() so a panic (e.g. a nil deref reached via
		// a malformed settings value or a garbage sidecar response) is contained to
		// this iteration instead of unwinding out of the goroutine and crashing the
		// whole API process — and so the GC loop survives to run again.
		runPrune := func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Printf("archive GC: recovered from panic: %v", r)
				}
			}()
			if days := archiveTTLDays(db); days > 0 {
				ctx, cancel := context.WithTimeout(context.Background(), runPruneCtxTimeout)
				defer cancel()
				deleted, err := toolRegistry.Sandbox().PruneArchives(ctx, time.Duration(days)*24*time.Hour)
				switch {
				case err != nil:
					logger.Printf("archive GC: prune failed: %v", err)
				case deleted > 0:
					logger.Printf("archive GC: removed %d stale workspace archive(s) older than %dd", deleted, days)
				}
			}
		}
		for {
			runPrune()
			time.Sleep(archiveGcSweepInterval)
		}
	}()

	// Recover half-done ingest work at boot and continuously reclaim rows whose
	// persisted worker heartbeat stopped after a crash, timeout, or lost lease.
	ingestRecoveryCtx, stopIngestRecovery := context.WithCancel(context.Background())
	defer stopIngestRecovery()
	go ragSvc.RunIngestRecovery(ingestRecoveryCtx)

	taskLLM := llm.NewTaskLLM(db, providers, logger)
	memoryWorker := llm.NewMemoryWorker(db, taskLLM, logger)
	ragSvc.SetTaskLLM(taskRouterAdapter{t: taskLLM})
	orchestrator := llm.NewOrchestrator(db, providers, toolRegistry, ragSvc, cacheLayer, q, taskLLM, memoryWorker, logger)

	router := api.NewRouter(api.Deps{
		Config:       cfg,
		DB:           db,
		Cache:        cacheLayer,
		Queue:        q,
		Auth:         authSvc,
		Mailer:       mail.NewSMTPSender(db, logger),
		Providers:    providers,
		Tools:        toolRegistry,
		RAG:          ragSvc,
		Orchestrator: orchestrator,
		Logger:       logger,
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           router,
		ErrorLog:          logger,
		ReadHeaderTimeout: 15 * time.Second,
		// SSE streams need long write deadlines; 30 minutes matches design.md §11.5.
		WriteTimeout: 90 * time.Minute,
		// MUST exceed any upstream proxy's origin keep-alive reuse window, or
		// the proxy fires requests down connections we already closed and users
		// see intermittent "site can't be reached" pages that fix on refresh.
		// Cloudflare reuses origin connections for ~15 minutes — 20min clears it.
		IdleTimeout: envcfg.Dur("AIVORY_HTTP_IDLE_TIMEOUT", 20*time.Minute),
	}

	go func() {
		logger.Printf("listening on %s (db=%s, env=%s)", cfg.Listen, cfg.DatabaseURL, cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("listen: %v", err)
		}
	}()

	// Graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}

// redactURL hides credentials in a connection URL before it reaches the log.
// Best-effort: anything that doesn't parse as user:pass@host is returned as-is
// minus an obvious password segment.
func redactURL(raw string) string {
	scheme := ""
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = raw[:i+3]
		rest = raw[i+3:]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return raw
	}
	host := rest[at+1:]
	creds := rest[:at]
	user := creds
	if c := strings.IndexByte(creds, ':'); c >= 0 {
		user = creds[:c]
	}
	return scheme + user + ":***@" + host
}

// archiveTTLDays reads settings.storage_archive_ttl_days — the age after which
// an archived workspace tarball is GC'd. Accepts a JSON number or the string the
// admin text input saves ("30"). Returns 0 (disabled) when unset/blank/invalid
// or negative, so a fat-fingered value can never trigger a mass delete.
func archiveTTLDays(db *sql.DB) int {
	raw, err := store.GetSetting(db, "storage_archive_ttl_days")
	if err != nil {
		return 0
	}
	days := 0
	if json.Unmarshal(raw, &days) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			days, _ = strconv.Atoi(strings.TrimSpace(s))
		}
	}
	if days < 0 {
		return 0
	}
	return days
}

// driverName names the relational backend for a startup log line.
func driverName(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

// taskRouterAdapter is a small bridge so internal/rag (which can't import
// internal/llm without a cycle) can call into TaskLLM. rag.TaskRouter takes a
// string `kind`; we forward to llm.TaskLLM.RunJSONString.
type taskRouterAdapter struct {
	t *llm.TaskLLM
}

// RunJSON satisfies rag.TaskRouter.
func (a taskRouterAdapter) RunJSON(ctx context.Context, kind, prompt string, out any, opts rag.RouterOpts) error {
	return a.t.RunJSONString(ctx, kind, prompt, out, llm.RunOpts{
		UserID:          opts.UserID,
		ConversationID:  opts.ConversationID,
		JSONOutput:      true,
		MaxOutputTokens: taskRouterMaxOutputTokens,
	})
}
