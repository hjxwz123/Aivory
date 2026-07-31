package llm

import (
	"context"
	"encoding/json"
	"errors"
)

func usageHasValue(usage Usage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0
}

func validPartialToolInput(input json.RawMessage) json.RawMessage {
	if len(input) > 0 && json.Valid(input) {
		return input
	}
	return nil
}

func promptToolErrorResult(ctx context.Context, blocks []UnifiedBlock, usage Usage, citations []Citation, err error) (*UnifiedResult, error) {
	stopReason := "error"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		stopReason = "stopped"
	}
	visible := providerVisibleOutputFromContext(ctx)
	if stopReason == "stopped" || len(blocks) > 0 || len(citations) > 0 || usageHasValue(usage) || (visible != nil && visible.Load()) {
		return &UnifiedResult{
			Blocks: blocks, StopReason: stopReason, Usage: usage, Citations: citations,
		}, err
	}
	return nil, err
}
