package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type runtimeCompactionModelFallbackProvider struct {
	dedicatedID    string
	conversationID string
	failed         int
	succeeded      int
}

type noVisibleCompactionProvider struct {
	dedicatedID    string
	conversationID string
	calls          []string
	mode           string
}

func (p *noVisibleCompactionProvider) ID() string { return "openai" }

func (p *noVisibleCompactionProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.calls = append(p.calls, req.Model.ID)
	if req.Model.ID == p.dedicatedID {
		if p.mode == "provider-error" {
			return nil, errors.New("upstream service unavailable")
		}
		// A normal protocol completion containing only hidden reasoning is not
		// a stale model configuration. TaskLLM may retry this same model once,
		// but must not jump to the conversation model.
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "thinking", Text: "internal reasoning only"}},
			StopReason: "end_turn",
		}, nil
	}
	if req.Model.ID == p.conversationID {
		const summary = "fallback must not be called for a normal thinking-only completion"
		return &UnifiedResult{Blocks: []UnifiedBlock{{Kind: "text", Text: summary}}, Usage: Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return nil, errors.New("unexpected compaction model")
}

func (p *runtimeCompactionModelFallbackProvider) ID() string { return "openai" }

func (p *runtimeCompactionModelFallbackProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	switch req.Model.ID {
	case p.dedicatedID:
		p.failed++
		// Model the provider-level signal for a stale/missing credential. This is
		// intentionally different from a generic upstream outage: only the former
		// is eligible for the next configured compaction model.
		return nil, errors.New("this channel has no API key configured")
	case p.conversationID:
		p.succeeded++
		const summary = "conversation model supplied the durable continuation summary"
		onEvent(SseEvent{Type: "text_delta", Text: summary})
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "text", Text: summary}},
			StopReason: "stop",
			Usage:      Usage{InputTokens: 8, OutputTokens: 12},
		}, nil
	default:
		return nil, errors.New("unexpected compaction model")
	}
}

func TestTaskLLMCompactionFallsBackWhenDedicatedProviderFails(t *testing.T) {
	provider := &runtimeCompactionModelFallbackProvider{}
	_, task, conv, db := compactionBillingFixture(t, provider)
	// The fixture's model is the conversation model. Add a separate dedicated
	// model on the same channel so both candidates share the provider family but
	// have independently observable runtime behavior.
	dedicated, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: convModelChannelID(t, db, conv.ModelID),
		Kind:      "chat",
		RequestID: "dedicated-compaction",
		Label:     "Dedicated compaction",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create dedicated model: %v", err)
	}
	provider.dedicatedID = dedicated.ID
	provider.conversationID = conv.ModelID
	if err := store.SetSetting(db, "context_compaction_model_id", dedicated.ID); err != nil {
		t.Fatalf("set dedicated model: %v", err)
	}

	answer, err := task.Run(context.Background(), TaskCompact, "summarize the conversation", RunOpts{
		UserID:          conv.UserID,
		ConversationID:  conv.ID,
		FallbackModelID: conv.ModelID,
		MaxOutputTokens: 128,
		MessageID:       "compaction-fallback-test",
	})
	if err != nil {
		t.Fatalf("TaskCompact fallback: %v", err)
	}
	if answer != "conversation model supplied the durable continuation summary" {
		t.Fatalf("answer = %q, want conversation-model summary", answer)
	}
	if provider.failed != 1 || provider.succeeded != 1 {
		t.Fatalf("provider attempts dedicated/conversation = %d/%d, want 1/1", provider.failed, provider.succeeded)
	}

	var errorRows, successRows int
	if err := db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN status='error' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),0)
		FROM usage_logs WHERE user_id=? AND purpose=?`,
		conv.UserID, string(TaskCompact)).Scan(&errorRows, &successRows); err != nil {
		t.Fatalf("query compaction usage rows: %v", err)
	}
	if errorRows != 1 || successRows != 1 {
		t.Fatalf("compaction usage rows error/success = %d/%d, want 1/1", errorRows, successRows)
	}
}

func TestRegistryGetRejectsMissingProviderInsteadOfReturningNil(t *testing.T) {
	registry := &Registry{providers: map[string]Provider{}}
	provider, err := registry.Get("openai")
	if provider != nil {
		t.Fatalf("missing provider = %#v, want nil", provider)
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("missing provider error = %v, want ErrUnknownProvider", err)
	}

	var nilRegistry *Registry
	if provider, err := nilRegistry.Get("openai"); provider != nil || !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("nil registry result/provider = %#v/%v, want nil/ErrUnknownProvider", provider, err)
	}
}

func TestCompactionBudgetExtraParamsUsesLargestMergedRequest(t *testing.T) {
	compact := json.RawMessage(`{"temperature":0.2}`)
	large := json.RawMessage(`{"metadata":{"instructions":"` + strings.Repeat("fallback context ", 700) + `"}}`)
	selected := compactionBudgetExtraParams([]json.RawMessage{compact, large})
	if string(selected) != string(large) {
		t.Fatalf("selected extra params = %d bytes, want fallback payload with %d bytes", len(selected), len(large))
	}

	const requestMax = minimumCompactionRequestMaxTokens
	const outputCap = 512
	const target = 384
	budget := compactionPayloadBudget(
		requestMax, outputCap, "", compactionSummaryInstruction, target, selected,
	)
	for index, params := range []json.RawMessage{compact, large} {
		base := compactionTaskInputTokens("", compactionSummaryInstruction, target, params)
		if total := base + budget + outputCap + compactionRequestSafetyTokens; total > requestMax {
			t.Fatalf("candidate %d total budget = %d, want <= %d", index, total, requestMax)
		}
	}
}

func TestTaskLLMCompactionDoesNotFallbackForThinkingOnlyCompletion(t *testing.T) {
	provider := &noVisibleCompactionProvider{}
	_, task, conv, db := compactionBillingFixture(t, provider)
	dedicated, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: convModelChannelID(t, db, conv.ModelID), Kind: "chat", RequestID: "dedicated-thinking-only",
		Label: "Dedicated thinking-only", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create dedicated model: %v", err)
	}
	provider.dedicatedID = dedicated.ID
	provider.conversationID = conv.ModelID
	if err := store.SetSetting(db, "context_compaction_model_id", dedicated.ID); err != nil {
		t.Fatalf("set dedicated model: %v", err)
	}

	_, err = task.Run(context.Background(), TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID,
		MaxOutputTokens: 128, EmptyRetryMaxOutputTokens: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "task llm returned empty output") {
		t.Fatalf("thinking-only error = %v, want empty-output error", err)
	}
	if len(provider.calls) != 1 || provider.calls[0] != dedicated.ID {
		t.Fatalf("thinking-only model calls = %v, want dedicated only", provider.calls)
	}
}

func TestTaskLLMCompactionDoesNotFallbackForGenericProviderError(t *testing.T) {
	provider := &noVisibleCompactionProvider{mode: "provider-error"}
	_, task, conv, db := compactionBillingFixture(t, provider)
	dedicated, err := store.CreateModel(context.Background(), db, store.Model{
		ChannelID: convModelChannelID(t, db, conv.ModelID), Kind: "chat", RequestID: "dedicated-upstream-error",
		Label: "Dedicated upstream error", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create dedicated model: %v", err)
	}
	provider.dedicatedID = dedicated.ID
	provider.conversationID = conv.ModelID
	if err := store.SetSetting(db, "context_compaction_model_id", dedicated.ID); err != nil {
		t.Fatalf("set dedicated model: %v", err)
	}

	_, err = task.Run(context.Background(), TaskCompact, "summarize", RunOpts{
		UserID: conv.UserID, ConversationID: conv.ID, FallbackModelID: conv.ModelID,
		MaxOutputTokens: 128,
	})
	if err == nil || !strings.Contains(err.Error(), "upstream service unavailable") {
		t.Fatalf("generic provider error = %v, want upstream error", err)
	}
	if len(provider.calls) != 1 || provider.calls[0] != dedicated.ID {
		t.Fatalf("generic provider error model calls = %v, want dedicated only", provider.calls)
	}
}

// convModelChannelID keeps the fixture's model/channel lookup out of the
// production API while making the test resilient to generated channel IDs.
func convModelChannelID(t *testing.T, db *sql.DB, modelID string) string {
	t.Helper()
	var channelID string
	if err := db.QueryRowContext(context.Background(), `SELECT channel_id FROM models WHERE id=?`, modelID).Scan(&channelID); err != nil {
		t.Fatalf("lookup conversation channel: %v", err)
	}
	return channelID
}
