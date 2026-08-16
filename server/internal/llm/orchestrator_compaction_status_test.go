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
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/msgcache"
	"aivory/server/internal/queue"
	"aivory/server/internal/store"
)

type compactionStatusProvider struct {
	mainCalls          int
	summaryCalls       int
	failSummary        error
	blockSummary       bool
	summarySawDeadline bool
	summaryStarted     chan struct{}
	summaryRelease     <-chan struct{}
	summaryStartedOnce sync.Once
}

func (*compactionStatusProvider) ID() string { return "openai" }

func (p *compactionStatusProvider) Stream(
	ctx context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	if req.Model.RequestID == "compaction-status-task" {
		p.summaryCalls++
		_, p.summarySawDeadline = ctx.Deadline()
		if p.summaryStarted != nil {
			p.summaryStartedOnce.Do(func() { close(p.summaryStarted) })
		}
		if p.summaryRelease != nil {
			select {
			case <-p.summaryRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if p.blockSummary {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if p.failSummary != nil {
			return nil, p.failSummary
		}
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "text", Text: "Durable summary of the earlier concrete decisions and facts."}},
			StopReason: "stop",
			Usage:      Usage{InputTokens: 8, OutputTokens: 12},
		}, nil
	}
	p.mainCalls++
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "normal chat response"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 12, OutputTokens: 4},
	}, nil
}

type compactionStatusQueue struct {
	jobs []queue.Job
}

type automaticCompactionStatus struct {
	operationID string
	status      string
}

func compactionStatusNames(events []automaticCompactionStatus) string {
	statuses := make([]string, 0, len(events))
	for _, event := range events {
		statuses = append(statuses, event.status)
	}
	return strings.Join(statuses, ",")
}

func assertCompactionStatusPair(t *testing.T, events []automaticCompactionStatus, wantTerminal string) {
	t.Helper()
	if got, want := compactionStatusNames(events), "started,"+wantTerminal; got != want {
		t.Fatalf("automatic compaction statuses = %q, want %q", got, want)
	}
	if events[0].operationID == "" {
		t.Fatal("automatic compaction emitted an empty operation ID")
	}
	if events[1].operationID != events[0].operationID {
		t.Fatalf("automatic compaction operation IDs = %q then %q, want one stable ID", events[0].operationID, events[1].operationID)
	}
}

func (q *compactionStatusQueue) Enqueue(_ string, job queue.Job) {
	q.jobs = append(q.jobs, job)
}

func (*compactionStatusQueue) Close() {}

func automaticCompactionStatusFixture(
	t *testing.T,
	messageCount int,
	q queue.Queue,
) (*Orchestrator, *compactionStatusProvider, *store.Conversation, *store.Model, *sql.DB) {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	db, err := store.Open(filepath.Join(t.TempDir(), "automatic-compaction-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,is_default) VALUES(?,?,1)`, store.DefaultGroupID, "Free"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u_status','status@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Status", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	taskModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "compaction-status-task", Label: "Status task", Enabled: true, ToolMode: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "compaction-status-chat", Label: "Status chat", Enabled: true, Stream: true, ToolMode: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, modelID := range []string{taskModel.ID, chatModel.ID} {
		if err := store.SetModelQuotas(ctx, db, modelID, []store.ModelGroupQuota{{
			GroupID: store.DefaultGroupID, LimitType: "count", LimitValue: 0,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]any{
		"context_compaction_model_id":     taskModel.ID,
		"keep_recent_rounds":              1,
		"compaction_retention_percentage": 10,
		"summary_max_tokens":              512,
		"summary_target_percent":          30,
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	conv, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c_status", UserID: "u_status", Title: "Existing title", ModelID: chatModel.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	parentID := ""
	for i := 0; i < messageCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "historical message with a concrete decision"}})
		message, createErr := store.CreateMessage(ctx, db, store.Message{
			ConversationID: conv.ID, ParentID: parentID, Role: role, AuthorID: conv.UserID,
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

	provider := &compactionStatusProvider{}
	registry := NewRegistry(log.New(io.Discard, "", 0))
	registry.Register(provider)
	task := NewTaskLLM(db, registry, log.New(io.Discard, "", 0))
	orchestrator := NewOrchestrator(db, registry, nil, nil, cache.NewMemory(), q, task, nil, log.New(io.Discard, "", 0))
	return orchestrator, provider, conv, chatModel, db
}

func TestManualCompactionReportsSummaryFailureReason(t *testing.T) {
	orchestrator, provider, conv, _, _ := automaticCompactionStatusFixture(t, 6, nil)
	provider.failSummary = errors.New("summary provider unavailable")
	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if !errors.Is(err, ErrCompactionFailed) {
		t.Fatalf("manual compaction result=%+v err=%v, want ErrCompactionFailed", result, err)
	}
	if result.Compacted || result.Reason != "compaction_failed" {
		t.Fatalf("manual compaction summary failure result=%+v", result)
	}
}

func runAutomaticCompactionStatusTurn(t *testing.T, orchestrator *Orchestrator, conv *store.Conversation, model *store.Model) *RunResult {
	t.Helper()
	result, err := orchestrator.Run(context.Background(), RunRequest{
		UserID: "u_status", ConversationID: conv.ID, ModelID: model.ID,
		UserText: "Please continue this discussion.", ToolMode: ToolModeDisabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if result == nil || result.UserMessage == nil {
		t.Fatalf("run result = %+v", result)
	}
	return result
}

func TestAutomaticInlineCompactionEmitsLifecycleStatuses(t *testing.T) {
	// Six earlier messages plus this turn's new user message exceed the inline
	// backlog threshold (3 * one retained round), so no queue is involved.
	orchestrator, provider, conv, model, _ := automaticCompactionStatusFixture(t, 6, nil)
	var statuses []automaticCompactionStatus
	terminalObservedWithLeaseHeld := false
	orchestrator.SetCompactionStatusHandler(func(userID, conversationID, operationID, status string) {
		if userID != conv.UserID || conversationID != conv.ID {
			t.Fatalf("status target = %q/%q, want %q/%q", userID, conversationID, conv.UserID, conv.ID)
		}
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
		if status != "started" {
			probe, acquired := orchestrator.acquireCompactionLease(conv.ID)
			if acquired {
				probe.Release()
			} else {
				terminalObservedWithLeaseHeld = true
			}
		}
	})

	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)

	if got, want := compactionStatusNames(statuses), "started,completed"; got != want {
		history, historyErr := msgcache.ListMessages(context.Background(), orchestrator.cache, orchestrator.db, conv.ID, "")
		updated, updatedErr := store.GetConversation(context.Background(), orchestrator.db, conv.ID, conv.UserID)
		requestTokens := estimateRequestTokens(UnifiedChatRequest{History: storeToUnified(history, "openai", model.ID, false)})
		_, _, action := PlanCompactionForRequest(orchestrator.db, updated, history, requestTokens, model.CompactionTokenThreshold)
		t.Fatalf("inline statuses = %q, want %q; history=%d action=%d summaryCalls=%d updated=%+v getErr=%v histErr=%v", got, want, len(history), action, provider.summaryCalls, updated, updatedErr, historyErr)
	}
	assertCompactionStatusPair(t, statuses, "completed")
	if !terminalObservedWithLeaseHeld {
		t.Fatal("inline terminal notification was published after releasing the compaction lease")
	}
	if provider.summaryCalls == 0 {
		t.Fatal("inline compaction never called the summary model")
	}
}

func TestInlineCompactionSettlesBeforeChatAdmissionRefusal(t *testing.T) {
	// Exercise the real orchestrator ordering: the inline summary is generated
	// before the main chat reservation. There must be no path where the summary
	// provider has consumed tokens but a later low-credit chat refusal leaves that
	// cost attached to an un-settled assistant-message turn.
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 6, nil)
	ctx := context.Background()

	// Remove both free quota rows so each call is credit-admitted independently.
	// Keep the summary model inexpensive and make the chat model deliberately
	// expensive enough that the small balance covers only the summary.
	if err := store.SetModelQuotas(ctx, db, model.ID, nil); err != nil {
		t.Fatal(err)
	}
	var taskModelID string
	if err := db.QueryRow(`SELECT id FROM models WHERE request_id=?`, "compaction-status-task").Scan(&taskModelID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetModelQuotas(ctx, db, taskModelID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET price_input=?, price_output=? WHERE id=?`, 1000.0, 1000.0, model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET price_input=?, price_output=? WHERE id=?`, 1.0, 1.0, taskModelID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "credits_per_usd", 1.0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermanentCredits(ctx, db, conv.UserID, 0.1); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetCreditBalance(ctx, db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := orchestrator.Run(ctx, RunRequest{
		UserID: conv.UserID, ConversationID: conv.ID, ModelID: model.ID,
		UserText: "Continue after compaction.", ToolMode: ToolModeDisabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("inline low-credit run returned error: %v", err)
	}
	if result == nil || result.AssistantMessage == nil {
		t.Fatalf("inline low-credit result = %+v", result)
	}
	if provider.summaryCalls == 0 {
		t.Fatal("inline compaction did not call the summary provider")
	}
	if provider.mainCalls != 0 {
		t.Fatalf("main provider calls = %d, want zero after admission refusal", provider.mainCalls)
	}

	assistant, err := store.GetMessage(ctx, db, result.AssistantMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assistant.StopReason != "insufficient_credits" || assistant.Status != "complete" {
		t.Fatalf("assistant terminal state = status=%q stop_reason=%q, want complete/insufficient_credits", assistant.Status, assistant.StopReason)
	}
	var taskRows int
	var taskCredits float64
	var taskMessageID string
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(credits),0), COALESCE(MAX(message_id),'')
		FROM usage_logs WHERE purpose=?`, string(TaskCompact)).Scan(&taskRows, &taskCredits, &taskMessageID); err != nil {
		t.Fatal(err)
	}
	if taskRows == 0 || taskCredits <= 0 || !strings.HasPrefix(taskMessageID, "cmp") {
		t.Fatalf("compaction usage rows/credits/message = %d/%.9f/%q, want a positive independent cmp row", taskRows, taskCredits, taskMessageID)
	}
	if taskMessageID == result.AssistantMessage.ID {
		t.Fatalf("compaction usage was attached to assistant message %q", taskMessageID)
	}
	var billingTaskRows, billingChatRows int
	var billingTaskMessageID string
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(MAX(message_id),'')
		FROM billing_usage WHERE purpose=?`, string(TaskCompact)).Scan(&billingTaskRows, &billingTaskMessageID); err != nil {
		t.Fatal(err)
	}
	if billingTaskRows == 0 || billingTaskMessageID != taskMessageID {
		t.Fatalf("compaction billing rows/message = %d/%q, want the independent usage operation %q", billingTaskRows, billingTaskMessageID, taskMessageID)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM billing_usage WHERE purpose='chat'`).Scan(&billingChatRows); err != nil {
		t.Fatal(err)
	}
	if billingChatRows != 0 {
		t.Fatalf("refused main chat wrote %d billing row(s), want zero", billingChatRows)
	}
	after, err := store.GetCreditBalance(ctx, db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !(after.Available < before.Available) {
		t.Fatalf("credit balance did not decrease after summary settlement: before=%.9f after=%.9f", before.Available, after.Available)
	}
	var reservedCredits, reservedQuota int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_reservations WHERE status='reserved'`).Scan(&reservedCredits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM quota_ledger WHERE status='reserved'`).Scan(&reservedQuota); err != nil {
		t.Fatal(err)
	}
	if reservedCredits != 0 || reservedQuota != 0 {
		t.Fatalf("billing reservations leaked: credits=%d quota=%d", reservedCredits, reservedQuota)
	}
}

func TestAutomaticAsyncCompactionValidatesLeafAndLeaseBeforeNotifying(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	result := runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}

	// A lease held by another automatic pass means this job must silently skip
	// before it advertises progress or calls the summary model.
	lease, acquired := orchestrator.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("seed compaction lease was not acquired")
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("queued compaction with held lease: %v", err)
	}
	lease.Release()
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("lease-blocked job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}

	// Delete the branch leaf that scheduled the job. store.ListMessages normally
	// repairs a dangling leaf to the newest surviving path; an async compaction
	// must instead leave silently without summarising that other branch.
	if _, err := store.DeleteRound(context.Background(), db, conv.ID, conv.UserID, result.UserMessage.ID); err != nil {
		t.Fatalf("delete queued branch leaf: %v", err)
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("queued compaction with deleted leaf: %v", err)
	}
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("deleted-leaf job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}
}

func TestAutomaticCompactionSkipsUnavoidableRequestOverflow(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	// Force even the system prompt plus the minimum retained tail over the token
	// trigger. Old rounds exist, but summarizing them cannot satisfy this threshold,
	// so token pressure must not create a compaction task on every turn.
	if err := store.SetSetting(db, "compaction_token_trigger", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})

	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 0 {
		t.Fatalf("queued jobs = %d, want zero when the minimum request still exceeds the token trigger", len(q.jobs))
	}
	if len(statuses) != 0 {
		t.Fatalf("automatic compaction statuses = %v, want none", statuses)
	}
	if provider.summaryCalls != 0 {
		t.Fatalf("summary provider calls = %d, want zero", provider.summaryCalls)
	}
}

func TestAutomaticAsyncCompactionSkipsAfterActiveLeafSwitchesToSibling(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	result := runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}

	// Fork from the pre-turn leaf. The queued user message remains durable on its
	// sibling branch, but it is no longer an ancestor of conversations.active_leaf_id.
	// The old existence-only guard would still call and bill the summary model.
	blocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "sibling branch message"}})
	siblingUser, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "user", AuthorID: conv.UserID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	})
	if err != nil {
		t.Fatalf("create sibling user message: %v", err)
	}
	if _, err := store.CreateMessage(context.Background(), db, store.Message{
		ConversationID: conv.ID, ParentID: siblingUser.ID, Role: "assistant", AuthorID: conv.UserID,
		Blocks: blocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	}); err != nil {
		t.Fatalf("create sibling assistant message: %v", err)
	}
	if _, err := store.GetMessage(context.Background(), db, result.UserMessage.ID); err != nil {
		t.Fatalf("queued branch message unexpectedly disappeared: %v", err)
	}

	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("queued compaction after sibling switch: %v", err)
	}
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("inactive-branch job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}
	var taskBillingRows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM billing_usage WHERE purpose=?`, string(TaskCompact)).Scan(&taskBillingRows); err != nil {
		t.Fatal(err)
	}
	if taskBillingRows != 0 {
		t.Fatalf("inactive-branch job created %d compaction billing row(s), want none", taskBillingRows)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("inactive-branch job persisted summary blocks: %+v", blocks)
	}
}

func TestAutomaticAsyncCompactionFailsIfActiveBranchChangesDuringSummary(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	ctx := context.Background()

	// Make the queued summary exercise the standalone paid-compaction lifecycle.
	// The chat model remains on its unlimited fixture quota, so only the summary
	// call can create context_compaction credit and task.compact usage records.
	var taskModelID string
	if err := db.QueryRow(`SELECT id FROM models WHERE request_id=?`, "compaction-status-task").Scan(&taskModelID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetModelQuotas(ctx, db, taskModelID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET price_input=1, price_output=1, currency='USD' WHERE id=?`, taskModelID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "credits_per_usd", 1.0); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "daily_token_limit", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermanentCredits(ctx, db, conv.UserID, 10); err != nil {
		t.Fatal(err)
	}

	provider.summaryStarted = make(chan struct{})
	summaryRelease := make(chan struct{})
	provider.summaryRelease = summaryRelease
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	result := runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}

	jobDone := make(chan error, 1)
	go func() {
		jobDone <- q.jobs[0](ctx)
	}()
	select {
	case <-provider.summaryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("queued compaction never reached the summary provider")
	}
	if got := compactionStatusNames(statuses); got != "started" {
		t.Fatalf("statuses while provider is blocked = %q, want started", got)
	}

	// The queued user row remains durable, but switch the active leaf to a sibling
	// after generation began and before the persistence transaction revalidates it.
	siblingBlocks, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: "sibling branch created during summary"}})
	siblingUser, err := store.CreateMessage(ctx, db, store.Message{
		ConversationID: conv.ID, ParentID: conv.ActiveLeafID, Role: "user", AuthorID: conv.UserID,
		Blocks: siblingBlocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	})
	if err != nil {
		t.Fatalf("create sibling user message: %v", err)
	}
	siblingAssistant, err := store.CreateMessage(ctx, db, store.Message{
		ConversationID: conv.ID, ParentID: siblingUser.ID, Role: "assistant", AuthorID: conv.UserID,
		Blocks: siblingBlocks, Attachments: json.RawMessage("[]"), Citations: json.RawMessage("[]"), Status: "complete",
	})
	if err != nil {
		t.Fatalf("create sibling assistant message: %v", err)
	}
	if _, err := store.GetMessage(ctx, db, result.UserMessage.ID); err != nil {
		t.Fatalf("queued branch message unexpectedly disappeared: %v", err)
	}
	close(summaryRelease)
	select {
	case err := <-jobDone:
		if err != nil {
			t.Fatalf("queued compaction after branch switch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued compaction did not finish after releasing the provider")
	}

	assertCompactionStatusPair(t, statuses, "failed")
	if provider.summaryCalls != 1 {
		t.Fatalf("summary provider calls = %d, want exactly one", provider.summaryCalls)
	}
	var activeLeafID, raw string
	if err := db.QueryRow(`SELECT COALESCE(active_leaf_id,''), COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&activeLeafID, &raw); err != nil {
		t.Fatal(err)
	}
	if activeLeafID != siblingAssistant.ID {
		t.Fatalf("active leaf = %q, want sibling %q", activeLeafID, siblingAssistant.ID)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("inactive queued branch persisted summary blocks: %+v", blocks)
	}

	operationID := statuses[0].operationID
	var usageRows, usageTokens int
	var usageCredits float64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(input_tokens+output_tokens),0), COALESCE(SUM(credits),0)
		FROM usage_logs WHERE purpose=? AND message_id=?`, string(TaskCompact), operationID).
		Scan(&usageRows, &usageTokens, &usageCredits); err != nil {
		t.Fatal(err)
	}
	if usageRows != 1 || usageTokens <= 0 || usageCredits <= 0 {
		t.Fatalf("compaction usage rows/tokens/credits = %d/%d/%.9f, want one positive settled call", usageRows, usageTokens, usageCredits)
	}
	var billingRows, billingTokens int
	var billingMicros int64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(input_tokens+output_tokens),0), COALESCE(SUM(cost_micros),0)
		FROM billing_usage WHERE purpose=? AND message_id=?`, string(TaskCompact), operationID).
		Scan(&billingRows, &billingTokens, &billingMicros); err != nil {
		t.Fatal(err)
	}
	if billingRows != 1 || billingTokens <= 0 || billingMicros <= 0 {
		t.Fatalf("compaction billing rows/tokens/cost_micros = %d/%d/%d, want one positive provider call", billingRows, billingTokens, billingMicros)
	}

	var creditRows, reservedCredits, settledCreditMicros int
	if err := db.QueryRow(`SELECT COUNT(1),
		COALESCE(SUM(CASE WHEN status='reserved' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='settled' THEN actual_micros ELSE 0 END),0)
		FROM credit_reservations WHERE source_type='context_compaction'`).
		Scan(&creditRows, &reservedCredits, &settledCreditMicros); err != nil {
		t.Fatal(err)
	}
	if creditRows != 1 || reservedCredits != 0 || settledCreditMicros <= 0 {
		t.Fatalf("compaction credit reservations rows/reserved/settled_micros = %d/%d/%d", creditRows, reservedCredits, settledCreditMicros)
	}
	var reservedQuota int
	if err := db.QueryRow(`SELECT COUNT(1) FROM quota_ledger WHERE status='reserved'`).Scan(&reservedQuota); err != nil {
		t.Fatal(err)
	}
	if reservedQuota != 0 {
		t.Fatalf("branch-changed async compaction leaked %d quota reservation(s)", reservedQuota)
	}
}

func TestAutomaticAsyncCompactionSkipsAfterModelConfigChanges(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}

	// Keep the same model and channel IDs but change the request contract while
	// the job is waiting. The worker must discard its old projection before it
	// calls the summary model or emits a misleading lifecycle event.
	if _, err := db.Exec(`UPDATE models SET extra_params=? WHERE id=?`,
		`{"temperature":0.1}`, model.ID); err != nil {
		t.Fatalf("change queued model config: %v", err)
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("stale config job: %v", err)
	}
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("stale config job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("stale config job persisted summary blocks: %+v", blocks)
	}
}

func TestAutomaticAsyncCompactionSkipsAfterSummaryCandidateConfigChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, db *sql.DB, taskModelID string)
	}{
		{
			name: "model request parameters",
			mutate: func(t *testing.T, db *sql.DB, taskModelID string) {
				t.Helper()
				if _, err := db.Exec(`UPDATE models SET extra_params=? WHERE id=?`, `{"temperature":0.2}`, taskModelID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "primary channel credentials",
			mutate: func(t *testing.T, db *sql.DB, taskModelID string) {
				t.Helper()
				if _, err := db.Exec(`UPDATE channels SET base_url=?, api_key=? WHERE id=(SELECT channel_id FROM models WHERE id=?)`,
					"https://changed.example.invalid", "changed-key", taskModelID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fallback channel credentials",
			mutate: func(t *testing.T, db *sql.DB, taskModelID string) {
				t.Helper()
				ctx := context.Background()
				fallback, err := store.CreateChannel(ctx, db, "Status fallback", "openai", "chat", "https://fallback.example.invalid", "fallback-key")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE models SET fallback_channel_id=? WHERE id=?`, fallback.ID, taskModelID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &compactionStatusQueue{}
			orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
			var statuses []automaticCompactionStatus
			orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
				statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
			})
			runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
			if len(q.jobs) != 1 {
				t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
			}
			var taskModelID string
			if err := db.QueryRow(`SELECT id FROM models WHERE request_id=?`, "compaction-status-task").Scan(&taskModelID); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, db, taskModelID)

			if err := q.jobs[0](context.Background()); err != nil {
				t.Fatalf("stale summary candidate job: %v", err)
			}
			if len(statuses) != 0 || provider.summaryCalls != 0 {
				t.Fatalf("stale candidate job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
			}
			var raw string
			if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
				t.Fatalf("stale candidate job persisted summary blocks: %+v", blocks)
			}
		})
	}
}

func TestAutomaticAsyncCompactionRunsForFastModelWithoutOverwritingConversationModel(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, advancedModel, db := automaticCompactionStatusFixture(t, 4, q)
	ctx := context.Background()
	fastModel, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: advancedModel.ChannelID,
		Kind:      "chat",
		RequestID: "compaction-status-fast",
		Label:     "Status fast",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "none",
		Vision:    advancedModel.Vision,
	})
	if err != nil {
		t.Fatalf("create fast model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, fastModel.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	if err := store.SetModelQuotas(ctx, db, fastModel.ID, []store.ModelGroupQuota{{
		GroupID: store.DefaultGroupID, LimitType: "count", LimitValue: 0,
	}}); err != nil {
		t.Fatalf("set fast model quota: %v", err)
	}

	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	result, err := orchestrator.Run(ctx, RunRequest{
		UserID: conv.UserID, ConversationID: conv.ID, ModelID: advancedModel.ID,
		Fast: true, UserText: "Continue this fast discussion.", ToolMode: ToolModeDisabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("fast turn: %v", err)
	}
	if result == nil || result.UserMessage == nil {
		t.Fatalf("fast turn result = %+v", result)
	}
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}
	fresh, err := store.GetConversation(ctx, db, conv.ID, conv.UserID)
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if fresh.ModelID != advancedModel.ID || !fresh.Fast {
		t.Fatalf("fast turn conversation state = model=%q fast=%v, want advanced model %q and fast=true", fresh.ModelID, fresh.Fast, advancedModel.ID)
	}
	if err := q.jobs[0](ctx); err != nil {
		t.Fatalf("fast async compaction: %v", err)
	}
	assertCompactionStatusPair(t, statuses, "completed")
	if provider.summaryCalls == 0 {
		t.Fatal("fast async compaction was skipped before calling the summary model")
	}
}

func TestAutomaticAsyncCompactionUsesPrimaryWhenSummaryFallbackIsMissing(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	ctx := context.Background()
	var taskModelID string
	if err := db.QueryRow(`SELECT id FROM models WHERE request_id=?`, "compaction-status-task").Scan(&taskModelID); err != nil {
		t.Fatalf("load summary model: %v", err)
	}
	const staleFallbackID = "ch_missing_compaction_fallback"
	if _, err := db.Exec(`UPDATE models SET fallback_channel_id=? WHERE id=?`, staleFallbackID, taskModelID); err != nil {
		t.Fatalf("set stale summary fallback: %v", err)
	}

	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}
	if fingerprint := compactionRuntimeFingerprint(ctx, db, model.ID); fingerprint == "" {
		t.Fatal("stale fallback made the compaction fingerprint unusable")
	}
	if err := q.jobs[0](ctx); err != nil {
		t.Fatalf("stale fallback async compaction: %v", err)
	}
	assertCompactionStatusPair(t, statuses, "completed")
	if provider.summaryCalls == 0 {
		t.Fatal("summary model was not called through its primary channel")
	}
}

func TestAutomaticAsyncCompactionSkipsAfterMissingSummaryFallbackBindingChanges(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	ctx := context.Background()
	var taskModelID string
	if err := db.QueryRow(`SELECT id FROM models WHERE request_id=?`, "compaction-status-task").Scan(&taskModelID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET fallback_channel_id=? WHERE id=?`, "missing-fallback-a", taskModelID); err != nil {
		t.Fatal(err)
	}
	queuedFingerprint := compactionRuntimeFingerprint(ctx, db, model.ID)
	if queuedFingerprint == "" {
		t.Fatal("initial missing summary fallback made the compaction fingerprint unusable")
	}

	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one asynchronous compaction", len(q.jobs))
	}
	if _, err := db.Exec(`UPDATE models SET fallback_channel_id=? WHERE id=?`, "missing-fallback-b", taskModelID); err != nil {
		t.Fatal(err)
	}
	currentFingerprint := compactionRuntimeFingerprint(ctx, db, model.ID)
	if currentFingerprint == "" {
		t.Fatal("updated missing summary fallback made the compaction fingerprint unusable")
	}
	if currentFingerprint == queuedFingerprint {
		t.Fatal("changing a missing fallback binding did not change the compaction fingerprint")
	}

	if err := q.jobs[0](ctx); err != nil {
		t.Fatalf("stale missing-fallback job: %v", err)
	}
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("stale missing-fallback job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("stale missing-fallback job persisted summary blocks: %+v", blocks)
	}
}

func TestAutomaticAsyncCompactionEmitsTerminalFailure(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, _ := automaticCompactionStatusFixture(t, 4, q)
	provider.failSummary = errors.New("summary provider unavailable")
	var statuses []automaticCompactionStatus
	terminalObservedWithLeaseHeld := false
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
		if status != "started" {
			probe, acquired := orchestrator.acquireCompactionLease(conv.ID)
			if acquired {
				probe.Release()
			} else {
				terminalObservedWithLeaseHeld = true
			}
		}
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one", len(q.jobs))
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("automatic compaction normalizes provider errors: %v", err)
	}

	// Task errors are deliberately non-fatal to the chat turn, so the queue job
	// completes cleanly. The lifecycle still reports failure because no summary
	// frontier was persisted.
	assertCompactionStatusPair(t, statuses, "failed")
	if !terminalObservedWithLeaseHeld {
		t.Fatal("async terminal notification was published after releasing the compaction lease")
	}
}

func TestAutomaticAsyncCompactionAppliesGenerationDeadline(t *testing.T) {
	t.Setenv("AIVORY_API_MAX_GEN_DURATION", "250ms")
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	provider.blockSummary = true
	var statuses []automaticCompactionStatus
	terminalObservedWithLeaseHeld := false
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
		if status != "started" {
			probe, acquired := orchestrator.acquireCompactionLease(conv.ID)
			if acquired {
				probe.Release()
			} else {
				terminalObservedWithLeaseHeld = true
			}
		}
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one", len(q.jobs))
	}

	startedAt := time.Now()
	err := q.jobs[0](context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("async compaction error = %v, want context deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("async compaction deadline elapsed = %v, want under 2s", elapsed)
	}
	if !provider.summarySawDeadline {
		t.Fatal("async compaction provider did not receive a deadline")
	}
	assertCompactionStatusPair(t, statuses, "failed")
	if !terminalObservedWithLeaseHeld {
		t.Fatal("async timeout notification was published after releasing the compaction lease")
	}
	probe, acquired := orchestrator.acquireCompactionLease(conv.ID)
	if !acquired {
		t.Fatal("async timeout left the compaction lease held")
	}
	probe.Release()

	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("async timeout advanced summary frontier: %+v", blocks)
	}
	var reserved int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_reservations WHERE source_type='context_compaction' AND status='reserved'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("async timeout leaked %d credit reservation(s)", reserved)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM quota_ledger WHERE status='reserved'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("async timeout leaked %d quota reservation(s)", reserved)
	}
}

func TestAutomaticAsyncCompactionEmitsStableOperationIDOnSuccess(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, _, conv, model, _ := automaticCompactionStatusFixture(t, 4, q)
	var statuses []automaticCompactionStatus
	terminalObservedWithLeaseHeld := false
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
		if status != "started" {
			probe, acquired := orchestrator.acquireCompactionLease(conv.ID)
			if acquired {
				probe.Release()
			} else {
				terminalObservedWithLeaseHeld = true
			}
		}
	})
	runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one", len(q.jobs))
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("automatic compaction: %v", err)
	}
	assertCompactionStatusPair(t, statuses, "completed")
	if !terminalObservedWithLeaseHeld {
		t.Fatal("async terminal notification was published after releasing the compaction lease")
	}
}

func TestAutomaticAsyncCompactionSkipsQuietlyAfterAnotherPassAdvancesFrontier(t *testing.T) {
	q := &compactionStatusQueue{}
	orchestrator, provider, conv, model, db := automaticCompactionStatusFixture(t, 4, q)
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})
	result := runAutomaticCompactionStatusTurn(t, orchestrator, conv, model)
	if len(q.jobs) != 1 {
		t.Fatalf("queued jobs = %d, want one", len(q.jobs))
	}
	history, err := msgcache.ListMessages(context.Background(), orchestrator.cache, db, conv.ID, result.UserMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 2 {
		t.Fatalf("history = %d messages, want at least two", len(history))
	}
	// Simulate an earlier worker having already covered every message this queued
	// pass could summarize. The delayed job should disappear silently: no model
	// call and no misleading started -> failed lifecycle.
	block := SummaryBlock{
		Level: 1, FromMessageID: history[0].ID,
		AnchorMessageID: history[len(history)-2].ID,
		Text:            "summary written by an earlier worker", Tokens: 12,
	}
	encoded, _ := json.Marshal([]SummaryBlock{block})
	if _, err := db.Exec(`UPDATE conversations SET summary_blocks=? WHERE id=?`, string(encoded), conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.jobs[0](context.Background()); err != nil {
		t.Fatalf("redundant queued compaction: %v", err)
	}
	if len(statuses) != 0 || provider.summaryCalls != 0 {
		t.Fatalf("redundant job notified/called summary: statuses=%v calls=%d", statuses, provider.summaryCalls)
	}
}

func TestManualCompactionDoesNotEmitAutomaticStatus(t *testing.T) {
	orchestrator, _, conv, _, _ := automaticCompactionStatusFixture(t, 6, nil)
	var statuses []automaticCompactionStatus
	orchestrator.SetCompactionStatusHandler(func(_, _, operationID, status string) {
		statuses = append(statuses, automaticCompactionStatus{operationID: operationID, status: status})
	})

	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if err != nil {
		t.Fatalf("manual compaction: %v", err)
	}
	if !result.Compacted {
		t.Fatalf("manual result = %+v, want compaction", result)
	}
	if len(statuses) != 0 {
		t.Fatalf("manual /compact emitted automatic statuses: %v", statuses)
	}
}
