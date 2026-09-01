package llm

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestCompactionProviderLogsPairWithoutRawError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-provider-logging.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{failOnCall: 1}
	task := newCompactionTask(t, db, provider)
	var logs bytes.Buffer
	task.logger = log.New(&logs, "", 0)
	ctx := withCompactionTrace(context.Background(), "cmp_log_failure", "manual")

	_, runErr := task.Run(ctx, TaskCompact, "diagnostic prompt must not be logged", RunOpts{
		ConversationID:  "conv_log_failure",
		MaxOutputTokens: 512,
	})
	if runErr == nil {
		t.Fatal("TaskCompact unexpectedly succeeded")
	}
	got := logs.String()
	for _, want := range []string{
		"operation=cmp_log_failure mode=manual stage=provider status=started",
		"operation=cmp_log_failure mode=manual stage=provider status=failed",
		"call_index=1",
		`error_kind="*errors.errorString"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction logs missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		"injected compaction provider failure",
		"diagnostic prompt must not be logged",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("compaction logs leaked %q:\n%s", forbidden, got)
		}
	}
}

func TestCompactionErrorKindNamesContextTermination(t *testing.T) {
	if got := compactionErrorKind(context.Canceled); got != "context_canceled" {
		t.Fatalf("context cancellation kind = %q", got)
	}
	if got := compactionErrorKind(context.DeadlineExceeded); got != "deadline_exceeded" {
		t.Fatalf("context deadline kind = %q", got)
	}
	if got := compactionErrorKind(errors.Join(ErrCompactionFailed, context.Canceled)); got != "context_canceled" {
		t.Fatalf("wrapped cancellation kind = %q", got)
	}
}

func TestCompactionLogsSingleDirectSummaryPlan(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-direct-logging.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	provider := &compactionTestProvider{
		text: strings.Repeat("retained concrete requirement decision and outcome. ", 350),
	}
	task := newCompactionTask(t, db, provider)
	var logs bytes.Buffer
	task.logger = log.New(&logs, "", 0)
	ctx := withCompactionTrace(context.Background(), "cmp_log_direct", "manual")
	_, err = summarizeCompactionText(
		ctx, task, &store.Conversation{ID: "conv_log_direct"},
		"ordered source with a concrete decision and pending follow-up",
		"", "", "", compactionSummaryInstruction, 1024, minimumCompactionRequestMaxTokens,
	)
	if err != nil {
		t.Fatalf("summarizeCompactionText: %v\nlogs:\n%s", err, logs.String())
	}
	got := logs.String()
	for _, want := range []string{
		"stage=summary_plan status=started",
		"stage=summary_plan status=completed",
		"strategy=direct",
		"stage=provider status=started",
		"stage=provider status=completed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("direct compaction logs missing %q:\n%s", want, got)
		}
	}
	if len(provider.reqs) != 1 || strings.Contains(got, "map_part") || strings.Contains(got, "reduce_batch") {
		t.Fatalf("compaction was not single-request direct: requests=%d logs=\n%s", len(provider.reqs), got)
	}
}

func TestManualCompactionLogsTerminalStateAndLeaseRelease(t *testing.T) {
	provider := &compactionTestProvider{
		text: strings.Repeat("retained requirement decision result and pending step. ", 300),
	}
	orchestrator, task, conv, _ := compactionBillingFixture(t, provider)
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	task.logger = logger
	orchestrator.logger = logger

	result, err := orchestrator.CompactConversation(context.Background(), conv.UserID, conv.ID)
	if err != nil {
		t.Fatalf("CompactConversation: result=%+v err=%v\nlogs:\n%s", result, err, logs.String())
	}
	if !result.Compacted {
		t.Fatalf("CompactConversation result=%+v, want compacted", result)
	}
	got := logs.String()
	for _, want := range []string{
		"mode=manual stage=manual status=started",
		"mode=manual stage=lease_acquire status=completed",
		"mode=manual stage=persistence status=completed",
		"mode=manual stage=lease_release status=completed",
		"mode=manual stage=manual status=completed",
		"compacted=true reason=compacted",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("manual compaction logs missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "retained requirement decision result") {
		t.Fatalf("manual compaction logs leaked summary text:\n%s", got)
	}
}
