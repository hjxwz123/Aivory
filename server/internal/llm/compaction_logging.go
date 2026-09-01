package llm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

type compactionTraceContextKey struct{}

type compactionTraceContext struct {
	operationID  string
	mode         string
	providerCall atomic.Uint64
}

func withCompactionTrace(ctx context.Context, operationID, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	operationID = strings.TrimSpace(operationID)
	mode = strings.TrimSpace(mode)
	if existing := compactionTraceFromContext(ctx); existing != nil &&
		existing.operationID == operationID && existing.mode == mode {
		return ctx
	}
	return context.WithValue(ctx, compactionTraceContextKey{}, &compactionTraceContext{
		operationID: operationID,
		mode:        mode,
	})
}

func compactionTraceFromContext(ctx context.Context) *compactionTraceContext {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(compactionTraceContextKey{}).(*compactionTraceContext)
	return trace
}

func compactionTraceIdentity(ctx context.Context) (operationID, mode string) {
	if trace := compactionTraceFromContext(ctx); trace != nil {
		return trace.operationID, trace.mode
	}
	// Older tests and any package-local callers may install standalone billing
	// directly. Preserve its operation identifier even without an explicit trace.
	if ctx != nil {
		if billing := standaloneCompactionBillingFromContext(ctx); billing != nil {
			return billing.operationID, "unknown"
		}
	}
	return "", "unknown"
}

func nextCompactionProviderCall(ctx context.Context) uint64 {
	if trace := compactionTraceFromContext(ctx); trace != nil {
		return trace.providerCall.Add(1)
	}
	if billing := standaloneCompactionBillingFromContext(ctx); billing != nil {
		return billing.sequence.Load()
	}
	return 0
}

func compactionErrorKind(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrCompactionDisabled):
		return "compaction_disabled"
	case errors.Is(err, ErrCompactionInFlight):
		return "compaction_in_flight"
	case errors.Is(err, ErrCompactionChanged):
		return "compaction_changed"
	case errors.Is(err, ErrCompactionPersist):
		return "compaction_persist"
	case errors.Is(err, ErrCompactionFailed):
		return "compaction_failed"
	}
	return fmt.Sprintf("%T", err)
}

// logCompactionStage records structural diagnostics only. Callers must never
// place conversation text, prompts, summaries, filenames, provider URLs, or raw
// error strings in details.
func logCompactionStage(
	logger *log.Logger,
	db *sql.DB,
	ctx context.Context,
	conversationID, stage, status string,
	started time.Time,
	details string,
) {
	if logger == nil {
		return
	}
	duration := time.Duration(0)
	if !started.IsZero() {
		duration = time.Since(started).Round(time.Millisecond)
	}
	operationID, mode := compactionTraceIdentity(ctx)
	var open, inUse, idle int
	var waitCount int64
	var waitDuration time.Duration
	if db != nil {
		stats := db.Stats()
		open = stats.OpenConnections
		inUse = stats.InUse
		idle = stats.Idle
		waitCount = stats.WaitCount
		waitDuration = stats.WaitDuration
	}
	ctxErrorKind := ""
	if ctx != nil {
		ctxErrorKind = compactionErrorKind(ctx.Err())
	}
	logger.Printf(
		"compaction: stage (conv=%s operation=%s mode=%s stage=%s status=%s duration=%s db_open=%d db_in_use=%d db_idle=%d db_wait_count=%d db_wait_ms=%d ctx_error_kind=%q%s)",
		conversationID, operationID, mode, stage, status, duration,
		open, inUse, idle, waitCount, waitDuration.Milliseconds(), ctxErrorKind, details,
	)
}

func (o *Orchestrator) logCompactionStage(
	ctx context.Context,
	conversationID, stage, status string,
	started time.Time,
	details string,
) {
	if o == nil {
		return
	}
	logCompactionStage(o.logger, o.db, ctx, conversationID, stage, status, started, details)
}

func (t *TaskLLM) logCompactionStage(
	ctx context.Context,
	conversationID, stage, status string,
	started time.Time,
	details string,
) {
	if t == nil {
		return
	}
	logCompactionStage(t.logger, t.db, ctx, conversationID, stage, status, started, details)
}
