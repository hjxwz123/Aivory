package llm

import (
	"context"
	"sync/atomic"
)

// providerVisibleOutputKey stores the flag shared by every provider request in
// one user turn. Once set, replaying on another channel would visibly duplicate
// or mix an answer/tool trace.
type (
	providerVisibleOutputKey       struct{}
	providerTextDeltaVisibilityKey struct{}
)

func contextWithProviderVisibleOutput(ctx context.Context, visible *atomic.Bool) context.Context {
	if visible == nil {
		return ctx
	}
	return context.WithValue(ctx, providerVisibleOutputKey{}, visible)
}

func providerVisibleOutputFromContext(ctx context.Context) *atomic.Bool {
	if ctx == nil {
		return nil
	}
	visible, _ := ctx.Value(providerVisibleOutputKey{}).(*atomic.Bool)
	return visible
}

// contextWithoutProviderVisibleOutput detaches a nested internal model call
// from the outer chat turn's commit state. TaskLLM output is never streamed to
// the user, so it must keep its own transparent full-response channel fallback
// even when the surrounding chat has already emitted a tool event.
func contextWithoutProviderVisibleOutput(ctx context.Context) context.Context {
	visible, _ := ctx.Value(providerVisibleOutputKey{}).(*atomic.Bool)
	if visible == nil {
		return ctx
	}
	return context.WithValue(ctx, providerVisibleOutputKey{}, (*atomic.Bool)(nil))
}

// contextWithProviderTextDeltaVisibility tells the provider buffer whether a
// text delta can reach the user. Non-streaming models suppress those deltas and
// emit one final answer later, so their raw deltas must not release buffered
// metadata or commit the channel early.
func contextWithProviderTextDeltaVisibility(ctx context.Context, visible bool) context.Context {
	return context.WithValue(ctx, providerTextDeltaVisibilityKey{}, visible)
}

func providerEventCommitsVisibleOutputInContext(ctx context.Context, ev SseEvent) bool {
	if ev.Type == "text_delta" {
		if visible, ok := ctx.Value(providerTextDeltaVisibilityKey{}).(bool); ok && !visible {
			return false
		}
	}
	return providerEventCommitsVisibleOutput(ev)
}

// providerEventCommitsVisibleOutput deliberately excludes protocol metadata,
// citations, RAG progress, and message_start. Only content/tool events that the
// user can actually see make a channel retry unsafe.
func providerEventCommitsVisibleOutput(ev SseEvent) bool {
	switch ev.Type {
	case "text_delta", "thinking_delta":
		return ev.Text != ""
	case "tool_start", "tool_input", "tool_result":
		return true
	default:
		return false
	}
}

// observeProviderVisibleOutput marks an event only after the downstream
// callback has accepted it. This is important for prompt-tool provider calls:
// their raw token callback is intentionally a no-op, so hidden protocol text
// must not commit the user-visible turn.
func observeProviderVisibleOutput(onEvent func(SseEvent), visible *atomic.Bool) func(SseEvent) {
	if onEvent == nil {
		onEvent = func(SseEvent) {}
	}
	return func(ev SseEvent) {
		onEvent(ev)
		if visible != nil && providerEventCommitsVisibleOutput(ev) {
			visible.Store(true)
		}
	}
}
