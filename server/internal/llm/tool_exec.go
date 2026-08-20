package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"aivory/server/internal/envcfg"
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

const (
	publicToolCanceledMessage = "The operation was canceled."
	publicToolTimeoutMessage  = "The tool timed out. Please try again."
	publicToolFailureMessage  = "Tool execution failed. Please try again."
)

// ToolUserError marks a validation error whose message is intentionally safe
// to return to the model and user. Unknown tool errors are never surfaced: HTTP
// clients commonly include the full request URL (and therefore private hosts,
// ports, paths, or query credentials) in err.Error().
type ToolUserError struct{ Message string }

func (e *ToolUserError) Error() string { return e.Message }

// publicToolErrorOutput is the single user/model-facing boundary for local,
// managed, and MCP tool failures. The original error remains available to
// server-side logging and usage diagnostics; only explicit public error types
// cross this boundary.
func publicToolErrorOutput(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return publicToolCanceledMessage
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return publicToolTimeoutMessage
	}
	var refusal *ToolRefusalError
	if errors.As(err, &refusal) && strings.TrimSpace(refusal.Message) != "" {
		return strings.TrimSpace(refusal.Message)
	}
	var userErr *ToolUserError
	if errors.As(err, &userErr) && strings.TrimSpace(userErr.Message) != "" {
		return strings.TrimSpace(userErr.Message)
	}
	return publicToolFailureMessage
}

// maxConcurrentTools caps how many tools run at once within a single turn so a
// model can't fan out unbounded work (§4.3).
var maxConcurrentTools = envcfg.Int("AIVORY_LLM_MAX_CONCURRENT_TOOLS", 4)

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
			// A panic inside a tool's Execute (e.g. a nil deref while parsing an
			// adversary-influenced sandbox/tool response) unwinds out of THIS child
			// goroutine. The request-scoped recoverMiddleware can't catch it — a
			// recover() only fires for panics in the goroutine that deferred it — so
			// an unrecovered panic here would crash the whole API process and abort
			// every other in-flight generation. Contain it and surface it as a tool
			// error so the turn degrades instead of taking the server down.
			defer func() {
				if r := recover(); r != nil {
					results[i] = toolCallResult{Err: fmt.Errorf("tool %q panicked: %v", c.Name, r)}
				}
			}()
			out, cites, err := runner.Run(ctx, c.Name, c.Input)
			results[i] = toolCallResult{Output: out, Citations: cites, Err: err}
		}(i, c)
	}
	wg.Wait()
	return results
}
