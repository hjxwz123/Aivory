package llm

import (
	"context"
	"database/sql"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/store"
)

type stoppedEditBranchProvider struct {
	mu           sync.Mutex
	requests     []UnifiedChatRequest
	firstStarted chan struct{}
}

func (p *stoppedEditBranchProvider) ID() string { return "openai" }

func (p *stoppedEditBranchProvider) Stream(
	ctx context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	p.mu.Lock()
	call := len(p.requests)
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if call == 0 {
		close(p.firstStarted)
		onEvent(SseEvent{Type: "text_delta", Text: "partial first branch"})
		<-ctx.Done()
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "text", Text: "partial first branch"}},
			StopReason: "stopped",
			Usage:      Usage{InputTokens: 3, OutputTokens: 2},
		}, ctx.Err()
	}

	answer := "edited branch answer"
	if call == 2 {
		answer = "follow-up answer"
	}
	onEvent(SseEvent{Type: "text_delta", Text: answer})
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: answer}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 4, OutputTokens: 2},
	}, nil
}

func (p *stoppedEditBranchProvider) capturedRequests() []UnifiedChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]UnifiedChatRequest(nil), p.requests...)
}

type stoppedEditBranchTools struct{}

func (stoppedEditBranchTools) List(string) []ToolDef { return nil }

func (stoppedEditBranchTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

type preTokenStopProvider struct {
	calls int
}

type visibleStopWithoutUsageProvider struct {
	started chan struct{}
}

func (p *visibleStopWithoutUsageProvider) ID() string { return "openai" }

func (p *visibleStopWithoutUsageProvider) Stream(
	ctx context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	onEvent(SseEvent{Type: "text_delta", Text: "partially generated answer"})
	close(p.started)
	<-ctx.Done()
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "partially generated answer"}},
		StopReason: "stopped",
	}, ctx.Err()
}

type completeThenCancelProvider struct {
	cancel context.CancelFunc
}

func (p *completeThenCancelProvider) ID() string { return "openai" }

func (p *completeThenCancelProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	onEvent(SseEvent{Type: "text_delta", Text: "complete answer"})
	p.cancel()
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "complete answer"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 3, OutputTokens: 2},
	}, nil
}

func (p *preTokenStopProvider) ID() string { return "openai" }

func (p *preTokenStopProvider) Stream(
	context.Context,
	UnifiedChatRequest,
	ToolRunner,
	func(SseEvent),
) (*UnifiedResult, error) {
	p.calls++
	return &UnifiedResult{Blocks: []UnifiedBlock{{Kind: "text", Text: "unexpected"}}, StopReason: "stop"}, nil
}

func TestOrchestratorStopAfterMessageStartSettlesPlaceholderBeforeFirstToken(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pre-token-stop.db"))
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','pre-token@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	channel, err := store.CreateChannel(ctx, db, "Pre-token stop", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID:         channel.ID,
		Kind:              "chat",
		RequestID:         "pre-token-stop-model",
		Label:             "Pre-token stop model",
		Enabled:           true,
		Stream:            true,
		ToolMode:          "native",
		ModerationEnabled: true,
		ModerationMode:    "keyword",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	if err := store.SetSetting(db, "moderation_keywords", []string{"blocked-before-token"}); err != nil {
		t.Fatalf("set moderation keywords: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c_pre_token_stop", UserID: "u1", Title: "Pre-token stop", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	provider := &preTokenStopProvider{}
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, stoppedEditBranchTools{}, nil, nil, nil, nil, nil, logger)

	turnCtx, cancelTurn := context.WithCancel(ctx)
	assistantID := ""
	_, err = orchestrator.Run(turnCtx, RunRequest{
		UserID:         "u1",
		ConversationID: conversation.ID,
		UserText:       "blocked-before-token",
		Fast:           true,
		ToolMode:       ToolModeEnabled,
	}, func(event SseEvent) {
		if event.Type == "message_start" {
			assistantID = event.MessageID
			cancelTurn()
		}
	})
	if err != nil {
		t.Fatalf("run canceled after message_start: %v", err)
	}
	if assistantID == "" {
		t.Fatal("message_start did not expose an assistant id")
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 before first token", provider.calls)
	}

	persisted, err := store.GetMessage(context.Background(), db, assistantID)
	if err != nil {
		t.Fatalf("load stopped assistant: %v", err)
	}
	if persisted.Status != "stopped" || persisted.StopReason != "stopped" {
		t.Fatalf("assistant status=%q stop_reason=%q, want stopped/stopped", persisted.Status, persisted.StopReason)
	}
	if string(persisted.Blocks) != "[]" {
		t.Fatalf("assistant blocks=%s, want []", persisted.Blocks)
	}
}

func TestOrchestratorVisibleStopWithoutProviderUsageStillChargesCredits(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "visible-stop-billing.db"))
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_paid_stop','Paid stop',0)`,
		`INSERT INTO users(id,email,password_hash,role,group_id,credits_permanent) VALUES('u_paid_stop','paid-stop@example.com','h','user','ug_paid_stop',100)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := store.SetSetting(db, "credits_per_usd", 100.0); err != nil {
		t.Fatalf("set credits rate: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Visible stop", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "visible-stop-model", Label: "Visible stop model",
		Enabled: true, Stream: true, ToolMode: "native", PriceInput: 1, PriceOutput: 2, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c_visible_stop_billing", UserID: "u_paid_stop", Title: "Visible stop billing", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	provider := &visibleStopWithoutUsageProvider{started: make(chan struct{})}
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, stoppedEditBranchTools{}, nil, nil, nil, nil, nil, logger)
	turnCtx, cancelTurn := context.WithCancel(ctx)
	resultCh := make(chan *RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, runErr := orchestrator.Run(turnCtx, RunRequest{
			UserID: "u_paid_stop", ConversationID: conversation.ID, UserText: "generate a paid partial answer",
			ModelID: model.ID, ToolMode: ToolModeEnabled,
		}, func(SseEvent) {})
		resultCh <- result
		errCh <- runErr
	}()
	select {
	case <-provider.started:
		cancelTurn()
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not begin streaming")
	}
	result := <-resultCh
	if runErr := <-errCh; runErr != nil {
		t.Fatalf("run stopped turn: %v", runErr)
	}
	if result == nil || result.AssistantMessage.Status != "stopped" {
		t.Fatalf("stopped result = %+v", result)
	}
	if result.AssistantMessage.InputTokens <= 0 || result.AssistantMessage.OutputTokens <= 0 || result.AssistantMessage.Credits <= 0 {
		t.Fatalf("stopped message was not metered: %+v", result.AssistantMessage)
	}

	balance, err := store.GetCreditBalance(context.Background(), db, "u_paid_stop")
	if err != nil {
		t.Fatalf("load credit balance: %v", err)
	}
	if balance.Permanent >= 100 || balance.Available >= 100 {
		t.Fatalf("stopped turn did not debit credits: %+v", balance)
	}
	var reservationStatus string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM credit_reservations WHERE source_type='llm_turn' AND source_id=?`,
		result.AssistantMessage.ID+":chat",
	).Scan(&reservationStatus); err != nil {
		t.Fatalf("load stopped-turn reservation: %v", err)
	}
	if reservationStatus != store.CreditReservationSettled {
		t.Fatalf("reservation status = %q, want %q", reservationStatus, store.CreditReservationSettled)
	}
}

func TestOrchestratorLateStopDoesNotReplaceCompletedAnswerWithEmptyBlocks(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "late-stop-complete.db"))
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','late-stop@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Late stop", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "late-stop-model", Label: "Late stop model",
		Enabled: true, Stream: true, ToolMode: "native",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c_late_stop", UserID: "u1", Title: "Late stop", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	turnCtx, cancelTurn := context.WithCancel(ctx)
	provider := &completeThenCancelProvider{cancel: cancelTurn}
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, stoppedEditBranchTools{}, nil, nil, nil, nil, nil, logger)
	assistantID := ""
	_, err = orchestrator.Run(turnCtx, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, UserText: "finish at the stop boundary",
		Fast: true, ToolMode: ToolModeEnabled,
	}, func(event SseEvent) {
		if event.Type == "message_start" {
			assistantID = event.MessageID
		}
	})
	if err != nil {
		t.Fatalf("run completed result at stop boundary: %v", err)
	}
	if assistantID == "" {
		t.Fatal("message_start did not expose an assistant id")
	}

	persisted, err := store.GetMessage(context.Background(), db, assistantID)
	if err != nil {
		t.Fatalf("load completed assistant: %v", err)
	}
	if persisted.Status != "complete" || persisted.StopReason != "stop" {
		t.Fatalf("assistant status=%q stop_reason=%q, want complete/stop", persisted.Status, persisted.StopReason)
	}
	if !strings.Contains(string(persisted.Blocks), "complete answer") {
		t.Fatalf("completed assistant blocks were lost: %s", persisted.Blocks)
	}
}

func TestOrchestratorStoppedRootEditBranchAllowsFollowUp(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "stopped-edit-branch-followup.db"))
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','branch@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	channel, err := store.CreateChannel(ctx, db, "Branch flow", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "branch-flow-model",
		Label:     "Branch flow model",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	if err := store.SetSetting(db, "fallback_ttft_sec", 0); err != nil {
		t.Fatalf("disable TTFT fallback: %v", err)
	}
	if err := store.SetSetting(db, "fallback_model_id", ""); err != nil {
		t.Fatalf("clear fallback model: %v", err)
	}
	if err := store.SetSetting(db, "disabled_tools", []string{}); err != nil {
		t.Fatalf("reset disabled tools: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Stopped edit branch regression", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	provider := &stoppedEditBranchProvider{firstStarted: make(chan struct{})}
	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, stoppedEditBranchTools{}, nil, nil, nil, nil, nil, logger)

	type runOutcome struct {
		result *RunResult
		err    error
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	firstDone := make(chan runOutcome, 1)
	go func() {
		result, runErr := orchestrator.Run(firstCtx, RunRequest{
			UserID:         "u1",
			ConversationID: conversation.ID,
			UserText:       "original question",
			Fast:           true,
			ToolMode:       ToolModeEnabled,
		}, func(SseEvent) {})
		firstDone <- runOutcome{result: result, err: runErr}
	}()

	select {
	case <-provider.firstStarted:
		cancelFirst()
	case <-time.After(5 * time.Second):
		cancelFirst()
		t.Fatal("first provider call did not start")
	}

	var first runOutcome
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled first turn did not finish")
	}
	if first.err != nil {
		t.Fatalf("cancelled first turn returned an error: %v", first.err)
	}
	assertCompleteRunResult(t, "cancelled first turn", first.result)
	if first.result.UserMessage.ParentID != "" {
		t.Fatalf("first user parent = %q, want root", first.result.UserMessage.ParentID)
	}
	if first.result.AssistantMessage.ParentID != first.result.UserMessage.ID {
		t.Fatalf("first assistant parent = %q, want first user %q", first.result.AssistantMessage.ParentID, first.result.UserMessage.ID)
	}
	if first.result.AssistantMessage.Status != "stopped" || first.result.AssistantMessage.StopReason != "stopped" {
		t.Fatalf("cancelled assistant state = status %q stop_reason %q, want stopped/stopped",
			first.result.AssistantMessage.Status, first.result.AssistantMessage.StopReason)
	}

	second, err := orchestrator.Run(ctx, RunRequest{
		UserID:         "u1",
		ConversationID: conversation.ID,
		UserText:       "edited root question",
		ParentID:       first.result.UserMessage.ParentID,
		Branch:         true,
		Fast:           true,
		ToolMode:       ToolModeEnabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run edited root branch: %v", err)
	}
	assertCompleteRunResult(t, "edited root branch", second)
	if second.UserMessage.ParentID != "" {
		t.Fatalf("edited user parent = %q, want a sibling root", second.UserMessage.ParentID)
	}
	if second.AssistantMessage.ParentID != second.UserMessage.ID {
		t.Fatalf("edited assistant parent = %q, want edited user %q", second.AssistantMessage.ParentID, second.UserMessage.ID)
	}

	// A normal composer send does not supply a parent. Run must resolve the
	// committed active leaf from the edited branch and append there.
	followUp, err := orchestrator.Run(ctx, RunRequest{
		UserID:         "u1",
		ConversationID: conversation.ID,
		UserText:       "follow up on the edited branch",
		Fast:           true,
		ToolMode:       ToolModeEnabled,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run edited-branch follow-up: %v", err)
	}
	assertCompleteRunResult(t, "edited-branch follow-up", followUp)
	if followUp.UserMessage.ParentID != second.AssistantMessage.ID {
		t.Fatalf("follow-up user parent = %q, want edited branch assistant %q",
			followUp.UserMessage.ParentID, second.AssistantMessage.ID)
	}
	if followUp.AssistantMessage.ParentID != followUp.UserMessage.ID {
		t.Fatalf("follow-up assistant parent = %q, want follow-up user %q",
			followUp.AssistantMessage.ParentID, followUp.UserMessage.ID)
	}

	updated, err := store.GetConversation(ctx, db, conversation.ID, "u1")
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if updated.ActiveLeafID != followUp.AssistantMessage.ID {
		t.Fatalf("active leaf = %q, want edited branch tail %q", updated.ActiveLeafID, followUp.AssistantMessage.ID)
	}

	firstPath, err := store.ListMessages(ctx, db, conversation.ID, first.result.AssistantMessage.ID)
	if err != nil {
		t.Fatalf("load stopped first branch: %v", err)
	}
	assertMessagePath(t, "stopped first branch", firstPath,
		first.result.UserMessage.ID, first.result.AssistantMessage.ID)

	secondPath, err := store.ListMessages(ctx, db, conversation.ID, followUp.AssistantMessage.ID)
	if err != nil {
		t.Fatalf("load edited branch: %v", err)
	}
	assertMessagePath(t, "edited branch", secondPath,
		second.UserMessage.ID, second.AssistantMessage.ID,
		followUp.UserMessage.ID, followUp.AssistantMessage.ID)

	roots, err := store.SiblingsOf(ctx, db, *first.result.UserMessage)
	if err != nil {
		t.Fatalf("load root siblings: %v", err)
	}
	if !sameMessageIDs(roots, []string{first.result.UserMessage.ID, second.UserMessage.ID}) {
		t.Fatalf("root siblings = %v, want stopped and edited roots", roots)
	}

	all, err := store.ListAllMessages(ctx, db, conversation.ID)
	if err != nil {
		t.Fatalf("load complete message tree: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("message count = %d, want 6: %+v", len(all), all)
	}
	assertAllMessageParentsValid(t, db, conversation.ID)

	requests := provider.capturedRequests()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(requests))
	}
	assertHistoryText(t, "stopped first request", requests[0].History, []string{"original question"})
	assertHistoryText(t, "edited branch request", requests[1].History, []string{"edited root question"})
	assertHistoryText(t, "edited branch follow-up request", requests[2].History,
		[]string{"edited root question", "edited branch answer", "follow up on the edited branch"})
}

func assertCompleteRunResult(t *testing.T, label string, result *RunResult) {
	t.Helper()
	if result == nil || result.UserMessage == nil || result.AssistantMessage == nil {
		t.Fatalf("%s returned an incomplete result: %+v", label, result)
	}
}

func assertMessagePath(t *testing.T, label string, got []store.Message, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d, want %d: %+v", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", label, i, got[i].ID, want[i])
		}
	}
}

func sameMessageIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]int, len(want))
	for _, id := range want {
		set[id]++
	}
	for _, id := range got {
		set[id]--
	}
	for _, count := range set {
		if count != 0 {
			return false
		}
	}
	return true
}

func assertAllMessageParentsValid(t *testing.T, db *sql.DB, conversationID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT child.id, child.parent_id
		FROM messages child
		LEFT JOIN messages parent ON parent.id=child.parent_id
		WHERE child.conversation_id=?
		  AND child.parent_id IS NOT NULL
		  AND (parent.id IS NULL OR parent.conversation_id<>child.conversation_id)`, conversationID)
	if err != nil {
		t.Fatalf("query invalid parent links: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var childID, parentID string
		if err := rows.Scan(&childID, &parentID); err != nil {
			t.Fatalf("scan invalid parent link: %v", err)
		}
		t.Fatalf("message %q has invalid parent %q", childID, parentID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invalid parent links: %v", err)
	}

	fkRows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign_key_check: %v", err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		var table string
		var rowID int64
		var parent string
		var fkID int
		if err := fkRows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("scan foreign_key_check: %v", err)
		}
		t.Fatalf("foreign key violation: table=%s rowid=%d parent=%s fk=%d", table, rowID, parent, fkID)
	}
	if err := fkRows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_check: %v", err)
	}
}

func assertHistoryText(t *testing.T, label string, history []UnifiedMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(history))
	for _, message := range history {
		text := ""
		for _, block := range message.Blocks {
			if block.Kind == "text" {
				text += block.Text
			}
		}
		got = append(got, text)
	}
	if len(got) != len(want) {
		t.Fatalf("%s history = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s history[%d] = %q, want %q (full history: %v)", label, i, got[i], want[i], got)
		}
	}
}
