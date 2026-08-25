package llm

import (
	"testing"
	"time"
)

func TestThinkingDurationTrackerAccumulatesSeparateThoughtRuns(t *testing.T) {
	base := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	now := base
	tracker := newThinkingDurationTracker()
	tracker.now = func() time.Time { return now }

	tracker.Observe(SseEvent{Type: "thinking_delta", Text: "First thought"})
	now = now.Add(1200 * time.Millisecond)
	tracker.Observe(SseEvent{Type: "tool_start", Name: "aivory_web_search"})

	// Tool execution is intentionally outside the reasoning-time total.
	now = now.Add(8 * time.Second)
	tracker.Observe(SseEvent{Type: "tool_result", Name: "aivory_web_search"})

	tracker.Observe(SseEvent{Type: "thinking_delta", Text: "Second thought"})
	now = now.Add(2500 * time.Millisecond)
	tracker.Observe(SseEvent{Type: "text_delta", Text: "Final answer"})

	now = now.Add(time.Second)
	if got := tracker.Observe(SseEvent{Type: "done"}); got != 3700 {
		t.Fatalf("terminal thinking duration = %dms, want 3700ms", got)
	}
	if got := tracker.FinishMs(); got != 3700 {
		t.Fatalf("final thinking duration = %dms, want 3700ms", got)
	}
}

func TestAttachThinkingDurationAnnotatesOnlyFirstThinkingBlock(t *testing.T) {
	blocks := []UnifiedBlock{
		{Kind: "thinking", Text: "one"},
		{Kind: "tool_call", ToolName: "aivory_web_search"},
		{Kind: "thinking", Text: "two"},
	}

	got := attachThinkingDuration(blocks, 3200)
	if got[0].ThinkingMs != 3200 {
		t.Fatalf("first thinking block duration = %d, want 3200", got[0].ThinkingMs)
	}
	if got[2].ThinkingMs != 0 {
		t.Fatalf("second thinking block duration = %d, want 0", got[2].ThinkingMs)
	}
	if blocks[0].ThinkingMs != 0 {
		t.Fatalf("attachThinkingDuration mutated caller blocks: %#v", blocks)
	}
}

func TestThinkingDurationTrackerIgnoresTurnsWithoutThoughtOutput(t *testing.T) {
	tracker := newThinkingDurationTracker()
	tracker.Observe(SseEvent{Type: "text_delta", Text: "Direct answer"})
	if got := tracker.Observe(SseEvent{Type: "done"}); got != 0 {
		t.Fatalf("duration without thought output = %dms, want 0", got)
	}
}
