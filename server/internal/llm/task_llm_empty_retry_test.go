package llm

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type emptyThenTextTaskProvider struct {
	maxOutputTokens []int
	alwaysEmpty     bool
}

func (p *emptyThenTextTaskProvider) ID() string { return "openai" }

func (p *emptyThenTextTaskProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.maxOutputTokens = append(p.maxOutputTokens, req.MaxOutputTokens)
	result := &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "thinking", Text: "internal reasoning"}},
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: 5, OutputTokens: req.MaxOutputTokens},
	}
	if !p.alwaysEmpty && len(p.maxOutputTokens) > 1 {
		result.Blocks = append(result.Blocks, UnifiedBlock{Kind: "text", Text: `{"use_tools":false}`})
	}
	return result, nil
}

func newEmptyRetryTaskLLM(t *testing.T, provider *emptyThenTextTaskProvider) (*TaskLLM, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "task-empty-retry.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	channel, err := store.CreateChannel(context.Background(), db, "Task", provider.ID(), "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "reasoning-task", Label: "Task", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	registry := NewRegistry(log.New(io.Discard, "", 0))
	registry.Register(provider)
	return NewTaskLLM(db, registry, log.New(io.Discard, "", 0)), model.ID
}

func TestTaskLLMRetriesEmptyVisibleOutputWithLargerBudget(t *testing.T) {
	provider := &emptyThenTextTaskProvider{}
	task, modelID := newEmptyRetryTaskLLM(t, provider)
	var decision struct {
		UseTools bool `json:"use_tools"`
	}
	if err := task.RunJSON(context.Background(), TaskToolRoute, "route", &decision, RunOpts{
		ModelID: modelID, MaxOutputTokens: 32,
	}); err != nil {
		t.Fatalf("RunJSON: %v", err)
	}
	if decision.UseTools {
		t.Fatal("decoded retry response incorrectly")
	}
	if len(provider.maxOutputTokens) != 2 || provider.maxOutputTokens[0] != 32 || provider.maxOutputTokens[1] != taskEmptyRetryMaxOutputTokens {
		t.Fatalf("max output token attempts = %v", provider.maxOutputTokens)
	}
}

func TestTaskLLMReportsDiagnosticAfterEmptyRetry(t *testing.T) {
	provider := &emptyThenTextTaskProvider{alwaysEmpty: true}
	task, modelID := newEmptyRetryTaskLLM(t, provider)
	_, err := task.Run(context.Background(), TaskRouter, "route", RunOpts{ModelID: modelID, MaxOutputTokens: 64})
	if err == nil {
		t.Fatal("Run unexpectedly accepted empty visible output")
	}
	for _, detail := range []string{"task llm returned empty output", "model=" + modelID, "stop_reason=end_turn", "output_tokens=4096", "max_output_tokens=4096"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("error %q missing %q", err, detail)
		}
	}
	if len(provider.maxOutputTokens) != 2 {
		t.Fatalf("attempts = %d, want 2", len(provider.maxOutputTokens))
	}
}
