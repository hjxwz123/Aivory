package llm

import (
	"context"
	"encoding/json"
	"sync"
)

// toolCallSpec is a provider-agnostic tool invocation.
type toolCallSpec struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// toolCallResult is the outcome of one invocation (order-preserving).
type toolCallResult struct {
	Output    string
	Citations []Citation
	Err       error
}

// maxConcurrentTools caps how many tools run at once within a single turn so a
// model can't fan out unbounded work (§4.3).
const maxConcurrentTools = 4

// runToolsConcurrent executes all tool calls in a turn concurrently (§4.2/§4.3)
// while preserving result order. tool_start events are emitted up-front from
// the caller's single goroutine; per-tool timeouts are enforced by the runner
// (orchToolRunner.Run wraps each call with a deadline).
func runToolsConcurrent(ctx context.Context, runner ToolRunner, calls []toolCallSpec, onEvent func(SseEvent)) []toolCallResult {
	results := make([]toolCallResult, len(calls))
	// Announce all calls first (serialised — SSE writer isn't concurrent-safe).
	for _, c := range calls {
		onEvent(SseEvent{Type: "tool_start", Name: c.Name, ID: c.ID, Input: c.Input})
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentTools)
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c toolCallSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, cites, err := runner.Run(ctx, c.Name, c.Input)
			results[i] = toolCallResult{Output: out, Citations: cites, Err: err}
		}(i, c)
	}
	wg.Wait()
	return results
}
