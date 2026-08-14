package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"aivory/server/internal/store"
)

type partialErrorCompactionProvider struct {
	err      error
	usage    Usage
	onStream func()
	hits     int
}

type successfulCompactionWithoutUsageProvider struct{}

func (*successfulCompactionWithoutUsageProvider) ID() string { return "openai" }

func (*successfulCompactionWithoutUsageProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	const summary = "successful summary returned by a compatible relay without terminal usage metadata"
	onEvent(SseEvent{Type: "text_delta", Text: summary})
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: summary}},
		StopReason: "stop",
	}, nil
}

func (p *partialErrorCompactionProvider) ID() string { return "openai" }

func (p *partialErrorCompactionProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	p.hits++
	const partial = "partial summary that must never become durable conversation context"
	onEvent(SseEvent{Type: "text_delta", Text: partial})
	if p.onStream != nil {
		p.onStream()
	}
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: partial}},
		StopReason: "error",
		Usage:      p.usage,
	}, p.err
}

func TestTaskCompactPartialResultErrorStillSettlesConsumedUsage(t *testing.T) {
	providerErr := errors.New("provider stream ended after partial summary")
	provider := &partialErrorCompactionProvider{
		err:   providerErr,
		usage: Usage{InputTokens: 20, OutputTokens: 30},
	}
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "daily_token_limit", 1_000_000); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}

	ctx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-partial-error")
	answer, runErr := task.Run(ctx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	})
	if !errors.Is(runErr, providerErr) {
		t.Fatalf("TaskCompact error = %v, want original provider error", runErr)
	}
	if answer != "" {
		t.Fatalf("TaskCompact returned partial summary %q as a success", answer)
	}
	if provider.hits != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.hits)
	}

	var billingMessageID, billingPurpose string
	var billingInput, billingOutput int
	var billingCostMicros int64
	if err := db.QueryRow(`
		SELECT message_id,purpose,input_tokens,output_tokens,cost_micros
		FROM billing_usage
		WHERE user_id=? AND purpose=?`, conv.UserID, string(TaskCompact)).Scan(
		&billingMessageID, &billingPurpose, &billingInput, &billingOutput, &billingCostMicros,
	); err != nil {
		t.Fatalf("query durable compaction billing: %v", err)
	}
	if billingMessageID != "cmp-partial-error" || billingPurpose != string(TaskCompact) ||
		billingInput != 20 || billingOutput != 30 || billingCostMicros != 50 {
		t.Fatalf("durable billing = message %q purpose %q tokens %d/%d cost %d, want cmp-partial-error task.compact 20/30 cost 50",
			billingMessageID, billingPurpose, billingInput, billingOutput, billingCostMicros)
	}

	var quotaStatus string
	var quotaReservedMicros, quotaActualMicros int64
	if err := db.QueryRow(`
		SELECT status,reserved_micros,actual_micros
		FROM quota_ledger
		WHERE user_id=? AND scope_type=?`, conv.UserID, store.QuotaScopeDailyToken).Scan(
		&quotaStatus, &quotaReservedMicros, &quotaActualMicros,
	); err != nil {
		t.Fatalf("query daily token reservation: %v", err)
	}
	if quotaStatus != store.QuotaStatusFinalized || quotaActualMicros != 50*store.CreditMicrosPerUnit || quotaReservedMicros < quotaActualMicros {
		t.Fatalf("daily token reservation = status %q reserved %d actual %d, want finalized actual %d",
			quotaStatus, quotaReservedMicros, quotaActualMicros, 50*store.CreditMicrosPerUnit)
	}

	var creditStatus string
	var creditActualMicros int64
	if err := db.QueryRow(`
		SELECT status,actual_micros
		FROM credit_reservations
		WHERE user_id=? AND source_type='context_compaction'`, conv.UserID).Scan(
		&creditStatus, &creditActualMicros,
	); err != nil {
		t.Fatalf("query standalone compaction reservation: %v", err)
	}
	if creditStatus != store.CreditReservationSettled || creditActualMicros != 50 {
		t.Fatalf("standalone reservation = status %q actual %d, want settled/50", creditStatus, creditActualMicros)
	}
	after, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if want := before.Available - 0.00005; after.Available < want-0.000000001 || after.Available > want+0.000000001 {
		t.Fatalf("credit balance after partial provider failure = %.8f, want %.8f", after.Available, want)
	}

	var successfulConsumptionRows int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM usage_logs
		WHERE user_id=? AND purpose=? AND status='ok' AND input_tokens=20 AND output_tokens=30 AND credits=0.00005`,
		conv.UserID, string(TaskCompact),
	).Scan(&successfulConsumptionRows); err != nil {
		t.Fatal(err)
	}
	if successfulConsumptionRows != 1 {
		t.Fatalf("consumption analytics rows = %d, want 1", successfulConsumptionRows)
	}
}

func TestTaskCompactSuccessWithoutUsageIsConservativelySettled(t *testing.T) {
	provider := &successfulCompactionWithoutUsageProvider{}
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "daily_token_limit", 1_000_000); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}

	ctx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-missing-usage")
	answer, runErr := task.Run(ctx, TaskCompact, "summarize this conversation", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	})
	if runErr != nil {
		t.Fatalf("TaskCompact success without usage: %v", runErr)
	}
	if answer == "" {
		t.Fatal("TaskCompact discarded a successful summary without usage metadata")
	}

	var inputTokens, outputTokens, billingRows int
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COUNT(1)
		FROM billing_usage WHERE user_id=? AND purpose=?`, conv.UserID, string(TaskCompact),
	).Scan(&inputTokens, &outputTokens, &billingRows); err != nil {
		t.Fatal(err)
	}
	if billingRows != 1 || inputTokens <= 0 || outputTokens <= 0 {
		t.Fatalf("conservative durable usage = rows %d tokens %d/%d, want one non-zero row", billingRows, inputTokens, outputTokens)
	}

	var quotaStatus string
	var quotaActualMicros int64
	if err := db.QueryRow(`
		SELECT status,actual_micros FROM quota_ledger
		WHERE user_id=? AND scope_type=?`, conv.UserID, store.QuotaScopeDailyToken,
	).Scan(&quotaStatus, &quotaActualMicros); err != nil {
		t.Fatal(err)
	}
	if quotaStatus != store.QuotaStatusFinalized || quotaActualMicros <= 0 {
		t.Fatalf("daily quota = status %q actual %d, want finalized non-zero usage", quotaStatus, quotaActualMicros)
	}
	var creditStatus string
	var creditActualMicros int64
	if err := db.QueryRow(`
		SELECT status,actual_micros FROM credit_reservations
		WHERE user_id=? AND source_type='context_compaction'`, conv.UserID,
	).Scan(&creditStatus, &creditActualMicros); err != nil {
		t.Fatal(err)
	}
	if creditStatus != store.CreditReservationSettled || creditActualMicros <= 0 {
		t.Fatalf("credit reservation = status %q actual %d, want settled non-zero charge", creditStatus, creditActualMicros)
	}
	after, err := store.GetCreditBalance(context.Background(), db, conv.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Available >= before.Available {
		t.Fatalf("success without provider usage did not debit credits: before %.8f after %.8f", before.Available, after.Available)
	}
}

func TestManualCompactionDoesNotPersistPartialProviderResult(t *testing.T) {
	provider := &partialErrorCompactionProvider{
		err:   errors.New("provider failed after returning partial summary"),
		usage: Usage{InputTokens: 20, OutputTokens: 30},
	}
	orchestrator, _, conv, db := compactionBillingFixture(t, provider)

	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if !errors.Is(err, ErrCompactionFailed) {
		t.Fatalf("manual compaction result=%+v err=%v, want ErrCompactionFailed", result, err)
	}
	if result.Compacted || result.Reason != "compaction_failed" {
		t.Fatalf("manual compaction treated partial provider result as success: %+v", result)
	}
	if provider.hits != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.hits)
	}
	var raw string
	if err := db.QueryRow(`SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, conv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if blocks := LoadSummaryBlocks(json.RawMessage(raw)); len(blocks) != 0 {
		t.Fatalf("manual compaction persisted partial provider result: %+v", blocks)
	}
}

func TestTaskCompactJoinsProviderAndDailyQuotaFinalizeErrors(t *testing.T) {
	providerErr := errors.New("provider failed after consuming tokens")
	provider := &partialErrorCompactionProvider{
		err:   providerErr,
		usage: Usage{InputTokens: 11, OutputTokens: 7},
	}
	orchestrator, task, conv, db := compactionBillingFixture(t, provider)
	if err := store.SetSetting(db, "daily_token_limit", 1_000_000); err != nil {
		t.Fatal(err)
	}
	var deleteErr error
	var deleted int64
	provider.onStream = func() {
		var result sql.Result
		result, deleteErr = db.Exec(`
			DELETE FROM quota_ledger
			WHERE user_id=? AND scope_type=? AND status=?`,
			conv.UserID, store.QuotaScopeDailyToken, store.QuotaStatusReserved,
		)
		if deleteErr == nil {
			deleted, deleteErr = result.RowsAffected()
		}
	}

	ctx := withStandaloneCompactionBilling(context.Background(), orchestrator, "cmp-finalize-error")
	answer, runErr := task.Run(ctx, TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID, MaxOutputTokens: 128,
	})
	if deleteErr != nil {
		t.Fatalf("remove daily quota reservation during provider call: %v", deleteErr)
	}
	if deleted != 1 {
		t.Fatalf("daily quota reservations removed = %d, want 1", deleted)
	}
	if answer != "" {
		t.Fatalf("TaskCompact returned partial summary %q despite joined failure", answer)
	}
	if !errors.Is(runErr, providerErr) || !errors.Is(runErr, ErrTaskBillingRecord) {
		t.Fatalf("TaskCompact error = %v, want joined provider and ErrTaskBillingRecord errors", runErr)
	}
	var durableRows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM billing_usage WHERE user_id=? AND purpose=?`,
		conv.UserID, string(TaskCompact)).Scan(&durableRows); err != nil {
		t.Fatal(err)
	}
	if durableRows != 0 {
		t.Fatalf("billing usage rows after quota finalize failure = %d, want 0", durableRows)
	}
	var reservedCredits int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM credit_reservations
		WHERE user_id=? AND source_type='context_compaction' AND status='reserved'`, conv.UserID,
	).Scan(&reservedCredits); err != nil {
		t.Fatal(err)
	}
	if reservedCredits != 1 {
		t.Fatalf("credit reservations kept after accounting failure = %d, want 1", reservedCredits)
	}
}
