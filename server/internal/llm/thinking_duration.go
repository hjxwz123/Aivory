package llm

import (
	"strings"
	"sync"
	"time"
)

// thinkingDurationTracker measures only the observable reasoning phase. Tool
// execution and final-answer streaming are deliberately excluded: they have
// their own timing surfaces and must not inflate the model's thinking time.
//
// Providers can emit more than one thought run around tools, so each run is
// accumulated independently. It is safe to observe events from concurrent
// tool callbacks.
type thinkingDurationTracker struct {
	mu       sync.Mutex
	now      func() time.Time
	started  time.Time
	total    time.Duration
	observed bool
}

func newThinkingDurationTracker() *thinkingDurationTracker {
	return &thinkingDurationTracker{now: time.Now}
}

// Observe consumes a user-visible event. Terminal events receive the current
// accumulated duration so the UI can settle before its post-stream reload.
func (t *thinkingDurationTracker) Observe(event SseEvent) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if event.Type == "thinking_delta" && strings.TrimSpace(event.Text) != "" {
		t.observed = true
		if t.started.IsZero() {
			t.started = now
		}
		return 0
	}

	if thinkingPhaseEnds(event) {
		t.finishLocked(now)
	}
	if event.Type == "done" || event.Type == "error" {
		return t.millisecondsLocked()
	}
	return 0
}

func (t *thinkingDurationTracker) FinishMs() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.finishLocked(t.now())
	return t.millisecondsLocked()
}

func (t *thinkingDurationTracker) finishLocked(now time.Time) {
	if t.started.IsZero() {
		return
	}
	if now.After(t.started) {
		t.total += now.Sub(t.started)
	}
	t.started = time.Time{}
}

func (t *thinkingDurationTracker) millisecondsLocked() int64 {
	if !t.observed {
		return 0
	}
	// A provider can synchronously emit a thought and final text in the same
	// clock millisecond. Preserve that a thought phase existed instead of making
	// the persisted marker disappear because of JSON `omitempty`.
	if ms := t.total.Milliseconds(); ms > 0 {
		return ms
	}
	return 1
}

func thinkingPhaseEnds(event SseEvent) bool {
	switch event.Type {
	case "text_delta":
		return strings.TrimSpace(event.Text) != ""
	case "tool_start", "tool_input", "tool_result", "artifact", "refusal", "done", "error":
		return true
	default:
		return false
	}
}

// attachThinkingDuration annotates one block rather than every thinking block.
// The total applies to the whole assistant turn, not each individual run.
func attachThinkingDuration(blocks []UnifiedBlock, thinkingMs int64) []UnifiedBlock {
	if thinkingMs <= 0 {
		return blocks
	}
	for i := range blocks {
		if blocks[i].Kind != "thinking" {
			continue
		}
		out := append([]UnifiedBlock(nil), blocks...)
		out[i].ThinkingMs = thinkingMs
		return out
	}
	return blocks
}
