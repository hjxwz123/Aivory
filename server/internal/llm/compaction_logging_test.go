package llm

import (
	"bytes"
	"context"
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

func TestCompactionMapReduceLogsPartAndBatchIndexes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-map-reduce-logging.db"))
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
	ctx := withCompactionTrace(context.Background(), "cmp_log_map_reduce", "manual")
	parts := []compactionSourcePart{
		{Text: "first ordered source part with a concrete decision"},
		{Text: "second ordered source part with a concrete result"},
		{Text: "third ordered source part with a pending follow-up"},
	}

	_, err = summarizeCompactionParts(
		ctx, task, &store.Conversation{ID: "conv_log_map_reduce"}, parts,
		"", "", "", compactionSummaryInstruction,
		512, 1024, minimumCompactionRequestMaxTokens, nil,
	)
	if err != nil {
		t.Fatalf("summarizeCompactionParts: %v\nlogs:\n%s", err, logs.String())
	}
	got := logs.String()
	for _, want := range []string{
		"stage=map_part status=started",
		"part_index=1 part_count=3",
		"stage=map_part status=completed",
		"stage=reduce_batch status=started",
		"iteration=1 batch_index=1 input_count=3",
		"stage=reduce_batch status=completed",
		"stage=map_reduce status=completed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("map/reduce logs missing %q:\n%s", want, got)
		}
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
