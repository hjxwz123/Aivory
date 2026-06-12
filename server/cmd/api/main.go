// Aurelia API server — entrypoint.
//
// Runs the HTTP API described in design.md §6. Wires the SQLite-backed store,
// Provider registry, tools registry, async queue, and the SSE-aware chat
// orchestrator into a single net/http handler. Boots clean against an empty
// database (the migration runs at startup) and seeds an initial admin user
// + a "mock" channel so the system is usable end-to-end without any third
// party keys.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aurelia/server/internal/api"
	"aurelia/server/internal/auth"
	"aurelia/server/internal/cache"
	"aurelia/server/internal/config"
	"aurelia/server/internal/llm"
	"aurelia/server/internal/queue"
	"aurelia/server/internal/rag"
	"aurelia/server/internal/store"
	"aurelia/server/internal/tools"
	"aurelia/server/internal/vector"
)

func main() {
	cfg := config.Load()
	logger := log.New(os.Stdout, "aurelia ", log.LstdFlags|log.Lmicroseconds)

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
	// Vector backend: Qdrant in production, Disabled (Postgres brute-force
	// fallback) when QDRANT_URL is unset — search still works without the install.
	if cfg.QdrantURL != "" {
		ragSvc.SetVectorStore(vector.NewQdrant(cfg.QdrantURL, cfg.QdrantAPIKey))
		logger.Printf("vector: qdrant (%s)", redactURL(cfg.QdrantURL))
	} else {
		logger.Printf("vector: disabled (brute-force over %s)", driverName(cfg.DatabaseURL))
	}
	toolRegistry := tools.NewRegistry(db, ragSvc, cfg, logger)

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
		Providers:    providers,
		Tools:        toolRegistry,
		RAG:          ragSvc,
		Orchestrator: orchestrator,
		Logger:       logger,
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           router,
		ReadHeaderTimeout: 15 * time.Second,
		// SSE streams need long write deadlines; 30 minutes matches design.md §11.5.
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  120 * time.Second,
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
		MaxOutputTokens: 256,
	})
}
