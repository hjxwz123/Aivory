package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestModelModerationUsesDedicatedOutputBudget(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "moderation-output-budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Moderation", "openai", "chat", "https://api.example", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "moderation-model",
		Label:     "Moderation model",
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "moderation_model_id", model.ID); err != nil {
		t.Fatal(err)
	}

	provider := &captureRequestProvider{}
	registry := NewRegistry(log.New(io.Discard, "", 0))
	registry.Register(provider)
	orchestrator := &Orchestrator{
		db:     db,
		task:   NewTaskLLM(db, registry, log.New(io.Discard, "", 0)),
		logger: log.New(io.Discard, "", 0),
	}
	if _, decided, err := orchestrator.moderateByModel(ctx, "hello", "", "", ""); err != nil {
		t.Fatal(err)
	} else if !decided {
		t.Fatal("moderation model did not return a verdict")
	}
	if got := provider.req.MaxOutputTokens; got != 256 {
		t.Fatalf("moderation max output tokens = %d, want 256", got)
	}
}

// An upstream provider whose own content filter trips (e.g. a relay returning
// `sensitive_words_detected` in the error body) must be classified as content
// moderation — a rephrase-and-retry refusal — not a transient provider error.
func TestIsUpstreamModerationError(t *testing.T) {
	moderation := []error{
		errors.New("openai 400: {\"error\":{\"code\":\"sensitive_words_detected\"}}"),
		fmt.Errorf("anthropic 400: %s", `{"message":"SENSITIVE_WORDS_DETECTED"}`), // case-insensitive
		errors.New("upstream: request blocked — sensitive_words_detected"),
	}
	for _, e := range moderation {
		if !isUpstreamModerationError(e) {
			t.Errorf("isUpstreamModerationError(%v) = false, want true", e)
		}
	}

	notModeration := []error{
		nil,
		errors.New("openai 500: rate limited"),
		errors.New("anthropic 529: overloaded"),
		errors.New("dial tcp: connection refused"),
		errors.New("context canceled"),
	}
	for _, e := range notModeration {
		if isUpstreamModerationError(e) {
			t.Errorf("isUpstreamModerationError(%v) = true, want false", e)
		}
	}
}
