package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/generationcfg"
	"aivory/server/internal/store"
)

type blockingCompactionProvider struct {
	hits    atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type appendThenCancelMergeProvider struct {
	hits atomic.Int32
}

func (p *appendThenCancelMergeProvider) ID() string { return "openai" }

func (p *appendThenCancelMergeProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	if p.hits.Add(1) > 1 {
		return nil, context.Canceled
	}
	const text = "faithful continuation summary with all prior decisions and facts"
	onEvent(SseEvent{Type: "text_delta", Text: text})
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: text}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 20, OutputTokens: 20},
	}, nil
}

type cancelWithSuccessfulSummaryProvider struct {
	cancel context.CancelFunc
}

func (p *cancelWithSuccessfulSummaryProvider) ID() string { return "openai" }

func (p *cancelWithSuccessfulSummaryProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	const text = "faithful continuation summary returned concurrently with caller cancellation"
	onEvent(SseEvent{Type: "text_delta", Text: text})
	p.cancel()
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: text}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 20, OutputTokens: 20},
	}, nil
}

func (p *blockingCompactionProvider) ID() string { return "openai" }

func (p *blockingCompactionProvider) Stream(
	ctx context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.hits.Add(1)
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "faithful continuation summary with all prior decisions and facts"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
	}, nil
}

func compactionBillingFixture(t *testing.T, provider Provider) (*Orchestrator, *TaskLLM, *store.Conversation, *sql.DB) {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-billing-lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u_compact','compact@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Compaction", provider.ID(), "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "compact", Label: "Compact", Enabled: true,
		PriceInput: 1, PriceOutput: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"context_compaction_model_id": model.ID,
		"credits_per_usd":             1.0,
		"summary_max_tokens":          128,
		"summary_target_percent":      10,
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetPermanentCredits(context.Background(), db, "u_compact", 10); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{
		ID: "c_compact", UserID: "u_compact", Title: "Compact", ModelID: model.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	parentID := ""
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "message content with a concrete decision"}})
		message, createErr := store.CreateMessage(context.Background(), db, store.Message{
			ConversationID: conv.ID, ParentID: parentID, Role: role, AuthorID: "u_compact",
			Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		parentID = message.ID
	}
	if _, err := db.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, parentID, conv.ID); err != nil {
		t.Fatal(err)
	}
	conv.ActiveLeafID = parentID
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	task := NewTaskLLM(db, registry, logger)
	orchestrator := NewOrchestrator(db, registry, nil, nil, cache.NewMemory(), nil, task, nil, logger)
	return orchestrator, task, conv, db
}

func TestStandaloneCompactionBillsCreditsButMessageBoundTaskDefersSettlement(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)

	before, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	standaloneCtx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-standalone")
	if _, err := task.Run(standaloneCtx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	}); err != nil {
		t.Fatal(err)
	}
	afterStandalone, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStandalone.Available != before.Available-2 {
		t.Fatalf("standalone balance = %.2f, want %.2f", afterStandalone.Available, before.Available-2)
	}
	var credits float64
	var messageID string
	if err := db.QueryRow(`SELECT credits, COALESCE(message_id,'') FROM usage_logs WHERE purpose='task.compact' ORDER BY id DESC LIMIT 1`).Scan(&credits, &messageID); err != nil {
		t.Fatal(err)
	}
	if credits != 2 || messageID != "cmp-standalone" {
		t.Fatalf("standalone usage credits/message = %.2f/%q, want 2/cmp-standalone", credits, messageID)
	}

	messageBoundCtx := withTaskBillingMessageID(context.Background(), "assistant-turn")
	if _, err := task.Run(messageBoundCtx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	}); err != nil {
		t.Fatal(err)
	}
	afterMessageBound, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if afterMessageBound.Available != afterStandalone.Available {
		t.Fatalf("message-bound task debited credits directly: before=%.2f after=%.2f", afterStandalone.Available, afterMessageBound.Available)
	}
}

func TestStandaloneCompactionSettlesEachRetryOnce(t *testing.T) {
	provider := &emptyThenTextTaskProvider{}
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)
	before, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-retry")
	if _, err := task.Run(ctx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID,
		MaxOutputTokens: 32, EmptyRetryMaxOutputTokens: 64,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture prices input/output at $1 per million. Provider attempt one is
	// 5+32 tokens and attempt two is 5+64 tokens: 106 microcredits in total.
	want := before.Available - 0.000106
	if delta := after.Available - want; delta < -0.0000001 || delta > 0.0000001 {
		t.Fatalf("retry balance = %.6f, want %.6f", after.Available, want)
	}
	var rows int
	var credits float64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(credits),0) FROM usage_logs WHERE purpose='task.compact' AND message_id='cmp-retry'`).Scan(&rows, &credits); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || credits != 0.000106 {
		t.Fatalf("retry usage rows/credits = %d/%.6f, want 2/0.000106", rows, credits)
	}
}

func TestStandaloneCompactionStillEnforcesDailyTokenQuota(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "daily_token_limit", 1); err != nil {
		t.Fatal(err)
	}
	ctx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-daily")
	if _, err := task.Run(ctx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	}); !errors.Is(err, store.ErrDailyTokenQuotaExceeded) {
		t.Fatalf("standalone compaction daily quota error = %v", err)
	}
	var reserved int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_reservations WHERE source_type='context_compaction' AND status='reserved'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("daily-quota rejection leaked %d credit reservation(s)", reserved)
	}
}

func TestManualCompactionRejectsConcurrentProviderInvocation(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, _, conv, _ := compactionBillingFixture(t, provider)
	firstDone := make(chan error, 1)
	go func() {
		_, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
		firstDone <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first compaction never reached provider")
	}
	secondResult, secondErr := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if !errors.Is(secondErr, ErrCompactionInFlight) || secondResult.Reason != "generation_in_progress" {
		t.Fatalf("second compaction result=%+v err=%v", secondResult, secondErr)
	}
	if got := provider.hits.Load(); got != 1 {
		t.Fatalf("provider hits before release = %d, want 1", got)
	}
	close(provider.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first compaction did not finish")
	}
}

func TestManualCompactionTimeoutReleasesLeaseAndBillingReservation(t *testing.T) {
	t.Setenv("AIVORY_API_MAX_GEN_DURATION", "250ms")
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "daily_token_limit", 1_000_000); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Now()
	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out manual compaction error = %v, want context deadline", err)
	}
	if result.Compacted || result.Reason != "timed_out" {
		t.Fatalf("timed-out manual compaction result = %+v", result)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("timed-out manual compaction returned after %v, want under 2s", elapsed)
	}
	if got := provider.hits.Load(); got != 1 {
		t.Fatalf("provider hits = %d, want 1", got)
	}

	replacement, acquired := orchestrator.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("manual compaction timeout left the conversation lease held")
	}
	replacement.Release()

	var reserved int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM credit_reservations WHERE source_type='context_compaction' AND status='reserved'`,
	).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("manual compaction timeout leaked %d credit reservation(s)", reserved)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM quota_ledger WHERE status='reserved'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("manual compaction timeout leaked %d quota reservation(s)", reserved)
	}
	var raw, activeLeafID string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]'), COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, conv.ID).Scan(&raw, &activeLeafID); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("timed-out manual compaction advanced summary frontier: %+v", blocks)
	}
	if activeLeafID != conv.ActiveLeafID {
		t.Fatalf("timed-out manual compaction changed active leaf to %q, want %q", activeLeafID, conv.ActiveLeafID)
	}
}

func TestManualCompactionDoesNotPersistSuccessfulSummaryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelWithSuccessfulSummaryProvider{cancel: cancel}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)

	result, err := orchestrator.CompactConversation(ctx, conv.UserID, conv.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled manual compaction result=%+v err=%v, want context.Canceled", result, err)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("canceled manual compaction persisted successful model output: %+v", blocks)
	}
	replacement, acquired := orchestrator.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("canceled manual compaction left the conversation lease held")
	}
	replacement.Release()
}

func TestManualCompactionReportsPersistenceFailure(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	previousAttempts := summaryBlockCASAttempts
	summaryBlockCASAttempts = 0
	t.Cleanup(func() { summaryBlockCASAttempts = previousAttempts })

	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if !errors.Is(err, ErrCompactionPersist) {
		t.Fatalf("manual persistence failure result=%+v err=%v, want ErrCompactionPersist", result, err)
	}
	if result.Compacted || result.Reason != "persistence_failed" {
		t.Fatalf("manual persistence failure result=%+v", result)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("failed manual persistence wrote summary blocks: %+v", blocks)
	}
}

func TestManualCompactionUsesOneReplacementPipeline(t *testing.T) {
	provider := &appendThenCancelMergeProvider{}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 6 {
		t.Fatalf("fixture history = %d messages, want at least 6", len(history))
	}
	seed := []SummaryBlock{{
		Level: 1, FromMessageID: history[0].ID, AnchorMessageID: history[1].ID,
		Text: strings.Repeat("durable earlier detail ", 100), Tokens: 300,
	}}
	encoded, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET summary_blocks=? WHERE id=?`, string(encoded), conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "summary_merge_max_tokens", 256); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	conv.SummaryBlocks = encoded

	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if err != nil {
		t.Fatalf("optional merge reversed successful append: result=%+v err=%v", result, err)
	}
	if !result.Compacted || result.Reason != "compacted" || result.DroppedMessages != 2 {
		t.Fatalf("manual compaction result=%+v, want successful two-message advance", result)
	}
	if got := provider.hits.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want one replacement summary pipeline", got)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	blocks := LoadSummaryBlocks(json.RawMessage(raw))
	if len(blocks) != 1 || blocks[0].Format != continuationSummaryFormatV1 {
		t.Fatalf("manual compaction did not persist one continuation state: %+v", blocks)
	}
	if frontier := summarizedFrontier(filterBlocksForPath(blocks, history), history); frontier != 4 {
		t.Fatalf("durable frontier = %d, want 4", frontier)
	}
}

func TestManualCompactionRejectsActiveLeafChangeDuringSummary(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
		done <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction never reached provider")
	}
	var parentID string
	if err := db.QueryRow(`SELECT parent_id FROM messages WHERE id=?`, conv.ActiveLeafID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET active_leaf_id=? WHERE id=?`, parentID, conv.ID); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrCompactionChanged) {
			t.Fatalf("manual compaction after branch change = %v, want ErrCompactionChanged", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("manual compaction persisted stale-branch summary: %+v", got)
	}
}

func TestManualCompactionRejectsGenerationStartedDuringSummary(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
		done <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction never reached provider")
	}
	blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "partial response"}})
	streaming, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "assistant", AuthorID: conv.UserID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "streaming",
	})
	if err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrCompactionChanged) {
			t.Fatalf("manual compaction after generation started = %v, want ErrCompactionChanged (streaming=%s)", err, streaming.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("manual compaction persisted summary after generation began: %+v", got)
	}
}

func TestManualCompactionRejectsMessageEditDuringSummary(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	done := make(chan error, 1)
	go func() {
		_, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
		done <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction never reached provider")
	}
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 2 {
		t.Fatalf("fixture history too short: %d", len(history))
	}
	oldestID := history[0].ID
	edited, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "edited while the summary model was running"}})
	if err := store.UpdateMessageContent(context.Background(), db, oldestID, edited); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrCompactionChanged) {
			t.Fatalf("manual compaction after message edit = %v, want ErrCompactionChanged", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("manual compaction persisted summary after source edit: %+v", got)
	}
}

func seedExistingCompactionPrefix(t *testing.T, db *sql.DB, conv *store.Conversation, history []store.Message) {
	t.Helper()
	if len(history) < 4 {
		t.Fatalf("fixture history too short for an existing prefix: %d", len(history))
	}
	blocks := []SummaryBlock{{
		Level:           1,
		FromMessageID:   history[0].ID,
		AnchorMessageID: history[1].ID,
		Text:            "durable summary of the first conversation round",
	}}
	blocks[0].Tokens = estimateTokens(blocks[0].Text)
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET summary_blocks=? WHERE id=?`, string(encoded), conv.ID); err != nil {
		t.Fatal(err)
	}
	conv.SummaryBlocks = encoded
}

func editCoveredCompactionMessage(t *testing.T, db *sql.DB, messageID string) {
	t.Helper()
	edited, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "edited after the existing summary was loaded"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateMessageContent(context.Background(), db, messageID, edited); err != nil {
		t.Fatal(err)
	}
}

func assertNoDisconnectedCompactionBlock(t *testing.T, db *sql.DB, conversationID string) {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conversationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 0 {
		t.Fatalf("compaction persisted a block after its prerequisite prefix was pruned: %+v", got)
	}
}

func TestAutomaticCompactionKeepsHistoryWhenExistingFrontierRetreatsDuringSummary(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "keep_recent_rounds", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_retention_percentage", 10); err != nil {
		t.Fatal(err)
	}
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	seedExistingCompactionPrefix(t, db, conv, history)

	type result struct {
		keep   []store.Message
		blocks []SummaryBlock
		err    error
	}
	done := make(chan result, 1)
	go func() {
		keep, blocks, compactErr := MaybeCompact(context.Background(), db, task, conv, history, 0, conv.UserID)
		done <- result{keep: keep, blocks: blocks, err: compactErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction never reached provider")
	}

	// This edit invalidates only the already-durable prefix. The messages used to
	// generate the new block remain unchanged, isolating the frontier-retreat race.
	editCoveredCompactionMessage(t, db, history[0].ID)
	close(provider.release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.keep) != len(history) || len(got.blocks) != 0 {
			t.Fatalf("frontier retreat omitted request history: keep=%d/%d blocks=%+v", len(got.keep), len(history), got.blocks)
		}
		if len(got.keep) == 0 || got.keep[0].ID != history[0].ID {
			t.Fatalf("frontier retreat did not restore the uncovered prefix: keep=%+v", got.keep)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction did not finish")
	}
	assertNoDisconnectedCompactionBlock(t, db, conv.ID)
}

func TestAutomaticCompactionSnapshotFailureDoesNotDuplicateExistingPrefix(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "keep_recent_rounds", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_retention_percentage", 10); err != nil {
		t.Fatal(err)
	}
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	seedExistingCompactionPrefix(t, db, conv, history)

	type result struct {
		keep   []store.Message
		blocks []SummaryBlock
		err    error
	}
	done := make(chan result, 1)
	go func() {
		keep, blocks, compactErr := MaybeCompact(context.Background(), db, task, conv, history, 0, conv.UserID)
		done <- result{keep: keep, blocks: blocks, err: compactErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction never reached provider")
	}

	// Change only the newly summarized range without invoking the production edit
	// helper's summary-pruning transaction. This isolates the snapshot mismatch
	// branch: the durable prefix remains valid while the generated suffix is stale.
	edited, err := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "changed after the summary snapshot"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET blocks=?, raw='' WHERE id=?`, string(edited), history[2].ID); err != nil {
		t.Fatal(err)
	}
	close(provider.release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.blocks) != 1 || got.blocks[0].AnchorMessageID != history[1].ID {
			t.Fatalf("snapshot failure lost durable prefix: %+v", got.blocks)
		}
		if len(got.keep) != len(history)-2 || got.keep[0].ID != history[2].ID {
			t.Fatalf("snapshot failure duplicated durable prefix: keep=%+v", got.keep)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction did not finish")
	}

	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := LoadSummaryBlocks(json.RawMessage(raw)); len(got) != 1 || got[0].AnchorMessageID != history[1].ID {
		t.Fatalf("snapshot failure persisted stale suffix: %+v", got)
	}
}

func TestAutomaticCompactionRejectsInactiveBranchAtPersistence(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "keep_recent_rounds", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "compaction_retention_percentage", 10); err != nil {
		t.Fatal(err)
	}
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 6 {
		t.Fatalf("fixture history = %d messages, want at least 6", len(history))
	}
	queuedMessageID := history[len(history)-2].ID

	type result struct {
		keep   []store.Message
		blocks []SummaryBlock
		err    error
	}
	done := make(chan result, 1)
	go func() {
		keep, blocks, compactErr := MaybeCompactForRequest(
			context.Background(), db, task, conv, history, 0, 0, 0,
			conv.ModelID, conv.UserID, queuedMessageID,
		)
		done <- result{keep: keep, blocks: blocks, err: compactErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction never reached provider")
	}

	// Switch to a sibling below the previous completed round after the worker's
	// preflight check but before it can append the generated summary. The queued
	// user message remains durable, but it is no longer on the active path.
	siblingBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "new sibling branch"}})
	siblingUser, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conv.ID, ParentID: history[len(history)-3].ID,
		Role: "user", AuthorID: conv.UserID, Blocks: siblingBlocks,
		Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conv.ID, ParentID: siblingUser.ID,
		Role: "assistant", AuthorID: conv.UserID, Blocks: siblingBlocks,
		Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	close(provider.release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.keep) != len(history) || len(got.blocks) != 0 {
			t.Fatalf("inactive branch returned compacted context: keep=%d/%d blocks=%+v", len(got.keep), len(history), got.blocks)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automatic compaction did not finish")
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("inactive branch persisted summary blocks: %+v", blocks)
	}
}

func TestManualCompactionRejectsExistingFrontierRetreatDuringSummary(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, task, conv, db := compactionBillingFixture(t, provider)
	history, err := store.ListMessages(context.Background(), db, conv.ID, conv.ActiveLeafID)
	if err != nil {
		t.Fatal(err)
	}
	seedExistingCompactionPrefix(t, db, conv, history)

	done := make(chan error, 1)
	go func() {
		_, _, compactErr := CompactConversationNow(context.Background(), db, task, conv, history, conv.ModelID, conv.UserID)
		done <- compactErr
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction never reached provider")
	}

	editCoveredCompactionMessage(t, db, history[0].ID)
	close(provider.release)

	select {
	case err := <-done:
		if !errors.Is(err, ErrCompactionChanged) {
			t.Fatalf("manual compaction after frontier retreat = %v, want ErrCompactionChanged", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	assertNoDisconnectedCompactionBlock(t, db, conv.ID)
}

func TestCompactionLeaseIsSharedAcrossOrchestrators(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	first, _, conv, db := compactionBillingFixture(t, provider)
	// These represent two application replicas that deliberately do NOT share a
	// Redis cache. The database lease must still exclude the second worker.
	first.cache = cache.NewMemory()
	second := &Orchestrator{db: db, cache: cache.NewMemory()}
	lease, acquired := first.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("first orchestrator did not acquire lease")
	}
	if competing, acquired := second.acquireCompactionLease(conv.ID); acquired {
		competing.Release()
		t.Fatal("second orchestrator acquired the same conversation lease")
	}
	lease.Release()
	replacement, acquired := second.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("released lease remained stuck")
	}
	replacement.Release()
}

func TestCompactionLeaseTTLAlwaysOutlivesGenerationWindow(t *testing.T) {
	previousTTL := compactionLeaseTTL
	t.Cleanup(func() { compactionLeaseTTL = previousTTL })
	t.Setenv("AIVORY_API_MAX_GEN_DURATION", "3h")

	compactionLeaseTTL = time.Minute
	if got, want := effectiveCompactionLeaseTTL(), 3*time.Hour+generationcfg.FinalizationMargin; got != want {
		t.Fatalf("short lease TTL = %v, want protected generation window %v", got, want)
	}

	compactionLeaseTTL = 4 * time.Hour
	if got := effectiveCompactionLeaseTTL(); got != 4*time.Hour {
		t.Fatalf("explicit longer lease TTL = %v, want 4h", got)
	}
}

func TestCompactionLeaseReleaseDoesNotDeleteNewOwner(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)
	if acquired, err := store.TryAcquireConversationCompactionLease(context.Background(), db, conv.ID, "old-token", time.Minute); err != nil || !acquired {
		t.Fatalf("seed lease acquired=%v err=%v", acquired, err)
	}
	// Simulate a worker that outlived its lease. The new owner may replace an
	// expired row, but the original owner's deferred Release must not remove it.
	if _, err := db.Exec(`UPDATE conversation_compaction_leases SET expires_at=0 WHERE conversation_id=?`, conv.ID); err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.TryAcquireConversationCompactionLease(context.Background(), db, conv.ID, "new-token", time.Minute); err != nil || !acquired {
		t.Fatalf("replacement lease acquired=%v err=%v", acquired, err)
	}
	(&compactionLease{orchestrator: orchestrator, conversationID: conv.ID, token: "old-token"}).Release()
	var value string
	if err := db.QueryRow(`SELECT owner_token FROM conversation_compaction_leases WHERE conversation_id=?`, conv.ID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "new-token" {
		t.Fatalf("stale release removed replacement owner: value=%q", value)
	}
}

func TestCompactionHistoryAtLeafNeverRepairsToAnotherBranch(t *testing.T) {
	provider := &blockingCompactionProvider{started: make(chan struct{}), release: make(chan struct{})}
	close(provider.release)
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)

	path, current, err := orchestrator.compactionHistoryAtLeaf(context.Background(), conv.ID, conv.ActiveLeafID)
	if err != nil || !current || len(path) == 0 || path[len(path)-1].ID != conv.ActiveLeafID {
		t.Fatalf("current leaf history = %#v current=%v err=%v", path, current, err)
	}

	// Delete the leaf but leave older messages behind. store.ListMessages has a
	// deliberate render-time fallback to the newest surviving branch; a queued
	// compaction job must instead silently discard its stale branch snapshot.
	if _, err := db.Exec(`DELETE FROM messages WHERE id=?`, conv.ActiveLeafID); err != nil {
		t.Fatal(err)
	}
	path, current, err = orchestrator.compactionHistoryAtLeaf(context.Background(), conv.ID, conv.ActiveLeafID)
	if err != nil || current || path != nil {
		t.Fatalf("deleted leaf was repaired into another path: path=%#v current=%v err=%v", path, current, err)
	}
}
