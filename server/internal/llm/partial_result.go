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

// stoppedTurnUsage supplies a conservative usage estimate when a streaming
// provider only reports token counts in its terminal frame and a user stop
// prevents that frame from arriving. Once provider output was visible, the
// request was consumed upstream and must not become free merely because its
// exact usage metadata was lost. Each non-zero provider-reported field wins;
// only fields missing at the cancellation boundary are estimated.
func stoppedTurnUsage(usage Usage, req UnifiedChatRequest, blocks []UnifiedBlock, visible bool, requests []providerRequestSnapshot) (Usage, bool) {
	if !visible {
		return usage, usageHasValue(usage)
	}

	requestInput := 0
	requestOutput := 0
	eligible := 0
	missingInput := false
	missingOutput := false
	for _, request := range requests {
		if request.Error != "" {
			continue
		}
		if request.HasUsage && request.Usage.InputTokens > 0 {
			requestInput += request.Usage.InputTokens
		} else if eligible == 0 {
			missingInput = true
			requestInput += estimateRequestTokens(req)
		} else {
			missingInput = true
			requestInput += request.EstimatedInputTokens
		}
		if request.HasUsage && request.Usage.OutputTokens > 0 {
			requestOutput += request.Usage.OutputTokens
		} else {
			missingOutput = true
			requestOutput += request.EstimatedOutputTokens
		}
		eligible++
	}
	if eligible == 0 {
		if usage.InputTokens == 0 {
			usage.InputTokens = estimateRequestTokens(req)
		}
	} else if missingInput {
		usage.InputTokens = maxInt(usage.InputTokens, requestInput)
	}

	if (eligible == 0 && usage.OutputTokens == 0) || (eligible > 0 && missingOutput) {
		blockOutput := 0
		for _, block := range blocks {
			blockOutput += estimateTokens(block.Text)
			blockOutput += estimateTokens(block.Summary)
			if len(block.Input) > 0 {
				blockOutput += estimateTokens(string(block.Input))
			}
		}
		usage.OutputTokens = maxInt(usage.OutputTokens, maxInt(requestOutput, blockOutput))
	}
	// A visible delta can race ahead of a provider's partial-result accumulator.
	// Keep such a stopped generation billable even if no block survived locally.
	if usage.OutputTokens == 0 {
		usage.OutputTokens = 1
	}
	return usage, true
}

func validPartialToolInput(input json.RawMessage) json.RawMessage {
	if len(input) > 0 && json.Valid(input) {
		return input
	}
	return nil
}

func promptToolErrorResult(ctx context.Context, blocks []UnifiedBlock, raw json.RawMessage, usage Usage, citations []Citation, images []GeneratedImage, err error) (*UnifiedResult, error) {
	stopReason := "error"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		stopReason = "stopped"
	}
	visible := providerVisibleOutputFromContext(ctx)
	if stopReason == "stopped" || len(blocks) > 0 || len(raw) > 0 || len(citations) > 0 || len(images) > 0 || usageHasValue(usage) || (visible != nil && visible.Load()) {
		return &UnifiedResult{
			Blocks: blocks, Raw: raw, StopReason: stopReason, Usage: usage, Citations: citations, GeneratedImages: images,
		}, err
	}
	return nil, err
}
