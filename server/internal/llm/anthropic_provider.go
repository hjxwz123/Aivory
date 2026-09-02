package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"aivory/server/internal/envcfg"
)

// Force-use context to avoid "imported and not used" if ever the only ref is removed.
var _ = context.Canceled

// Anthropic provider tunables (env-overridable; defaults preserve prior
// hardcoded behavior).
var (
	anthropicThinkingHeadroomTokens      = envcfg.Int("AIVORY_LLM_APPLY_ANTHROPIC_THINKING_SETTINGS", 2048)
	toolResultSummaryTruncationAnthropic = 240
)

// SSE scanner buffer sizing — low-level transport plumbing, not a tunable in
// practice, so hardcoded rather than env-overridable (unlike the knobs above).
const (
	anthropicScannerBufInit = 64 * 1024
	anthropicScannerBufMax  = 1024 * 1024
	// Anthropic rejects enabled thinking budgets below 1024 tokens.
	anthropicThinkingMinimumTokens = 1024
)

// AnthropicProvider calls the Messages API (`POST /v1/messages`, SSE). The
// channel must carry a real api_key; an empty key is a configuration error.
//
// The implementation is the minimal subset of the Anthropic protocol needed to
// stream a chat reply, parse tool_use blocks, execute them locally and
// continue the loop — exactly the shape described in §4.3.
type AnthropicProvider struct {
	logger *log.Logger
}

// ID returns "anthropic".
func (p *AnthropicProvider) ID() string { return "anthropic" }

// isClaudeModel reports whether the request id names a Claude model. Used to
// scope Claude-specific request handling (sampling-param rejection §4.3, and the
// §4.3-B strip-thinking retry) so a channel proxying a non-Claude model is left
// untouched.
func isClaudeModel(requestID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(requestID)), "claude")
}

// anthropicModelRejectsSampling reports Claude models whose API rejects
// non-default sampling params such as temperature/top_p/top_k.
func anthropicModelRejectsSampling(requestID string) bool {
	return isClaudeModel(requestID)
}

// messagesHaveThinking reports whether any message content block is a thinking /
// redacted_thinking block. Handles both the []map[string]any shape (turns built
// this run) and the []any shape (raw same-vendor replay), matching
// setMessagesCacheBreakpoint. Used to gate the §4.3-B strip-thinking retry.
func messagesHaveThinking(messages []map[string]any) bool {
	isThinking := func(blk map[string]any) bool {
		t, _ := blk["type"].(string)
		return t == "thinking" || t == "redacted_thinking"
	}
	for _, m := range messages {
		switch content := m["content"].(type) {
		case []map[string]any:
			for _, blk := range content {
				if isThinking(blk) {
					return true
				}
			}
		case []any:
			for _, b := range content {
				if blk, ok := b.(map[string]any); ok && isThinking(blk) {
					return true
				}
			}
		}
	}
	return false
}

// stripThinkingFromMessages removes every thinking / redacted_thinking block
// from the conversation (§4.3-B strip-thinking retry). An assistant turn that
// becomes empty after stripping (a rare thinking-only turn) is dropped entirely
// so the API doesn't reject an empty content array — Anthropic merges any
// consecutive same-role turns that leaves behind. historyLen is decremented for
// each dropped message that sat in the history prefix so the run's raw-replay
// slice (`messages[historyLen:]`) stays aligned. Returns the rebuilt slice and
// adjusted historyLen.
func stripThinkingFromMessages(messages []map[string]any, historyLen int) ([]map[string]any, int) {
	isThinking := func(blk map[string]any) bool {
		t, _ := blk["type"].(string)
		return t == "thinking" || t == "redacted_thinking"
	}
	out := make([]map[string]any, 0, len(messages))
	newHistoryLen := historyLen
	for idx, m := range messages {
		empty := false
		switch content := m["content"].(type) {
		case []map[string]any:
			nc := make([]map[string]any, 0, len(content))
			for _, blk := range content {
				if !isThinking(blk) {
					nc = append(nc, blk)
				}
			}
			m["content"] = nc
			empty = len(nc) == 0
		case []any:
			nc := make([]any, 0, len(content))
			for _, b := range content {
				if blk, ok := b.(map[string]any); ok && isThinking(blk) {
					continue
				}
				nc = append(nc, b)
			}
			m["content"] = nc
			empty = len(nc) == 0
		}
		if empty {
			if idx < historyLen {
				newHistoryLen--
			}
			continue
		}
		out = append(out, m)
	}
	return out, newHistoryLen
}

func applyAnthropicThinkingSettings(body map[string]any, requestID string, maxTok *int, strictMax bool) {
	if body == nil || maxTok == nil {
		return
	}
	if anthropicModelRejectsSampling(requestID) {
		removeAnthropicSamplingParams(body)
	}
	cfg, ok := body["thinking"].(map[string]any)
	if !ok {
		return
	}
	typ, _ := cfg["type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))
	if !anthropicThinkingIsActive(typ) {
		return
	}
	removeAnthropicSamplingParams(body)
	removeAnthropicForcedToolChoice(body)
	if typ != "enabled" {
		return
	}
	budget, ok := intFromJSONNumber(cfg["budget_tokens"])
	if !ok || budget <= 0 {
		return
	}
	// max_tokens must exceed budget_tokens because extended thinking spends from
	// the same Anthropic output budget. Most chat calls preserve the historical
	// behavior and enlarge max_tokens. Compaction has a hard complete-request
	// budget, so instead shrink thinking when valid or disable it when there is no
	// room for Anthropic's minimum budget plus visible-summary headroom.
	headroom := anthropicThinkingHeadroomTokens
	if headroom < 1 {
		headroom = 1
	}
	required := budget + headroom
	if *maxTok < required {
		if strictMax {
			available := *maxTok - headroom
			if available < anthropicThinkingMinimumTokens {
				delete(body, "thinking")
				return
			}
			cfg["budget_tokens"] = available
			return
		}
		*maxTok = required
		body["max_tokens"] = *maxTok
	}
}

func anthropicThinkingIsActive(typ string) bool {
	switch typ {
	case "enabled", "adaptive":
		return true
	default:
		return false
	}
}

func removeAnthropicSamplingParams(body map[string]any) {
	for _, key := range []string{"temperature", "top_p", "topP", "top_k", "topK"} {
		delete(body, key)
	}
}

func removeAnthropicForcedToolChoice(body map[string]any) {
	tc, ok := body["tool_choice"]
	if !ok {
		return
	}
	var typ string
	switch x := tc.(type) {
	case map[string]any:
		typ, _ = x["type"].(string)
	case string:
		typ = x
	}
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "any", "tool":
		delete(body, "tool_choice")
	}
}

func intFromJSONNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// Stream runs the Anthropic chat turn (with up to 12 tool iterations).
func (p *AnthropicProvider) Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	if req.Model.APIKey == "" && req.Model.Fallback == nil {
		return nil, errors.New("this channel has no API key configured")
	}
	if !req.Model.Vision {
		req.History = stripImageBlocks(req.History)
	}
	// §4.13 prompt-mode: the model has no native function calling, so drive the
	// text-protocol loop instead of the native tool_use loop.
	if req.ToolModePrompt {
		_, blocks, usage, cites, images, raw, err := RunPromptToolLoopWithRaw(
			ctx, req.SystemPrompt, req.History, req.Tools,
			p.promptRunOnce(req), tools, onEvent,
		)
		if err != nil {
			return promptToolErrorResult(ctx, blocks, raw, usage, cites, images, err)
		}
		return &UnifiedResult{Blocks: blocks, Raw: raw, StopReason: "end_turn", Usage: usage, Citations: cites, GeneratedImages: images}, nil
	}

	maxIter := envcfg.Int("AIVORY_LLM_MAX_ITER", 20)
	messages := historyToAnthropic(req.History, req.Model.Vision)
	historyLen := len(messages) // turns beyond this are this run's raw exchange (§2.3-C)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{} // full ordered content: thinking | text | tool_call (§4.3)
	allCitations := []Citation{}
	totalUsage := Usage{}
	// §4.3-B: set once a 400 forces a thinking-stripped retry; keeps every later
	// iteration this turn thinking-free so the loop can't re-poison itself.
	thinkingStripped := false
	var finalizationPending error
	if signal := toolFinalizationFromContext(ctx); signal != nil {
		finalizationPending = signal
	}

	for i := 0; i < maxIter || finalizationPending != nil; i++ {
		finalizing := finalizationPending != nil
		finalizationSignal := finalizationPending
		finalizationPending = nil
		roundModel := req.Model
		if finalizing {
			roundModel.Fallback = nil
		}
		maxTok := envcfg.Int("AIVORY_LLM_MAX_TOK", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		// buildBody serializes the current messages into a request body. Called
		// again verbatim on the §4.3-B strip-thinking retry, so it must reflect
		// the live `messages` slice and the `thinkingStripped` flag each time.
		// Returns whether the body carries a `thinking` field (used to decide if a
		// 400 is worth retrying without it).
		buildBody := func() ([]byte, bool) {
			// §4.9 prompt caching: cache_control on the system block (stable prefix)
			// and on the last message block (incremental history cache). Exactly two
			// breakpoints, well under the 4-breakpoint limit.
			setMessagesCacheBreakpoint(messages)
			systemPrompt := req.SystemPrompt
			if finalizing {
				systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + toolFinalizationInstruction(finalizationSignal))
			}
			body := map[string]any{
				"model":      req.Model.RequestID,
				"max_tokens": maxTok,
				"stream":     true,
				"system":     anthropicSystemBlocks(systemPrompt),
				"messages":   messages,
			}
			nativeToolsEnabled := len(req.Tools) > 0 && !req.ToolModePrompt && !finalizing
			if nativeToolsEnabled {
				body["tools"] = toAnthropicTools(req.Tools)
			}
			if req.ToolModePrompt {
				body["stop_sequences"] = []string{PromptToolStopSequence()}
			}
			// Apply the model's param_controls (thinking/effort/etc). Claude
			// extended thinking is opt-in: if admins do not explicitly merge a
			// `thinking` object, the provider sends no thinking field.
			body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
			body = StripToolFields(body, nativeToolsEnabled)
			if !finalizing {
				body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
			}
			// §4.3-B: once a strip-thinking retry has fired this turn, every
			// subsequent request drops the thinking param too (the response then
			// carries no thinking blocks, so later replays stay clean).
			if thinkingStripped {
				delete(body, "thinking")
			}
			applyAnthropicThinkingSettings(body, req.Model.RequestID, &maxTok, req.StrictMaxOutputTokens)
			_, hasThinking := body["thinking"]
			buf, _ := json.Marshal(body)
			return buf, hasThinking
		}
		var (
			stopReason     string
			toolCalls      []anthropicToolCall
			hostedCalls    []anthropicHostedToolCall
			text           string
			thinkingBlocks []anthropicThinkingBlock
			citations      []Citation
			nativeContent  []map[string]any
			usage          Usage
		)
		send := func(buf []byte) error {
			stopReason = ""
			toolCalls = nil
			hostedCalls = nil
			text = ""
			thinkingBlocks = nil
			citations = nil
			nativeContent = nil
			usage = Usage{}
			return doProviderParsedRequest(ctx, roundModel, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
				hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.anthropic.com")+"/v1/messages", bytes.NewReader(buf))
				if e != nil {
					return nil, e
				}
				hr.Header.Set("content-type", "application/json")
				hr.Header.Set("anthropic-version", "2023-06-01")
				hr.Header.Set("x-api-key", apiKey)
				hr.Header.Set("accept", "text/event-stream")
				return hr, nil
			}, func(resp *http.Response, emit func(SseEvent)) error {
				stopReason, text, usage = "", "", Usage{}
				toolCalls, hostedCalls, thinkingBlocks, citations = nil, nil, nil, nil
				if statusErr := requireProviderSuccess(resp, "anthropic"); statusErr != nil {
					return statusErr
				}
				var readErr error
				stopReason, toolCalls, hostedCalls, text, thinkingBlocks, citations, nativeContent, usage, readErr = readAnthropicStream(resp.Body, emit)
				// A hidden task may transparently retry on the fallback channel. Attach
				// the primary attempt before the next consume resets usage.
				attachProviderRequestUsage(ctx, usage)
				return readErr
			}, onEvent)
		}
		buf, hadThinking := buildBody()
		err := send(buf)
		// §4.3-B non-genuine-upstream guard. Preserve the existing one-time
		// thinking-strip retry after both configured channels reject the signed
		// payload with HTTP 400. Each send still gets its own channel fallback.
		var statusErr *providerStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusBadRequest &&
			!thinkingStripped && isClaudeModel(req.Model.RequestID) && (hadThinking || messagesHaveThinking(messages)) {
			thinkingStripped = true
			messages, historyLen = stripThinkingFromMessages(messages, historyLen)
			if p.logger != nil {
				p.logger.Printf("anthropic: channel rejected thinking replay (400) for model %q (base=%s) — retried without thinking; this channel may be serving a non-genuine upstream",
					req.Model.ID, providerBaseURL(req.Model.BaseURL, "https://api.anthropic.com"))
			}
			buf, _ = buildBody()
			err = send(buf)
		}
		if err != nil {
			partialBlocks := append([]UnifiedBlock{}, allBlocks...)
			thinkingText := joinThinkingText(thinkingBlocks)
			if thinkingText != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
			}
			partialBlocks = mergeAnthropicHostedUnifiedBlocks(partialBlocks, hostedCalls)
			if text != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "text", Text: text})
			}
			for _, tc := range toolCalls {
				partialBlocks = append(partialBlocks, UnifiedBlock{
					Kind: "tool_call", ToolName: tc.Name, ToolID: tc.ID, Input: tc.Input,
				})
			}
			partialCitations := append(append([]Citation{}, allCitations...), citations...)
			partialUsage := totalUsage
			partialUsage.InputTokens += usage.InputTokens
			partialUsage.OutputTokens += usage.OutputTokens
			partialUsage.CacheReadTokens += usage.CacheReadTokens
			partialUsage.CacheWriteTokens += usage.CacheWriteTokens
			if finalizing && !errors.Is(err, context.Canceled) {
				err = toolFinalizationError(finalizationSignal, err)
			}

			partialMessages := append([]map[string]any{}, messages...)
			currentTurn := buildAssistantTurn(text, thinkingBlocks, completedAnthropicHostedCalls(hostedCalls), nil)
			if content, ok := currentTurn["content"].([]map[string]any); ok && len(content) > 0 {
				// A provider error after tool_start must not leave a native tool_use
				// without its required tool_result in replay history. The canonical
				// block still keeps the visible tool trace for reload.
				partialMessages = append(partialMessages, currentTurn)
			}
			raw, _ := json.Marshal(partialMessages[historyLen:])

			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return &UnifiedResult{
					Blocks: partialBlocks, Raw: raw, StopReason: "stopped",
					Usage: partialUsage, Citations: partialCitations,
				}, err
			}
			visible := providerVisibleOutputFromContext(ctx)
			if len(partialBlocks) > 0 || len(partialCitations) > 0 || usageHasValue(partialUsage) || (visible != nil && visible.Load()) {
				return &UnifiedResult{
					Blocks: partialBlocks, Raw: raw, StopReason: "error",
					Usage: partialUsage, Citations: partialCitations,
				}, err
			}
			return nil, err
		}
		allText.WriteString(text)
		thinkingText := joinThinkingText(thinkingBlocks)
		if thinkingText != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
		}
		if !finalizing {
			allBlocks = mergeAnthropicHostedUnifiedBlocks(allBlocks, hostedCalls)
		}
		if text != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		allCitations = append(allCitations, citations...)
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		totalUsage.CacheReadTokens += usage.CacheReadTokens
		totalUsage.CacheWriteTokens += usage.CacheWriteTokens
		if finalizing {
			assistantTurn := buildAssistantTurn(text, thinkingBlocks, nil, nil)
			if content, ok := assistantTurn["content"].([]map[string]any); ok && len(content) > 0 {
				messages = append(messages, assistantTurn)
			}
			if len(toolCalls) > 0 || len(hostedCalls) > 0 || stopReason == "pause_turn" || strings.TrimSpace(text) == "" {
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{
					Blocks: allBlocks, Raw: raw, StopReason: toolFinalizationStopReason(finalizationSignal),
					Usage: totalUsage, Citations: allCitations,
				}, toolFinalizationError(finalizationSignal, errors.New("model did not return a tool-free final answer"))
			}
			raw, _ := json.Marshal(messages[historyLen:])
			if stopReason == "" {
				stopReason = "end_turn"
			}
			return &UnifiedResult{
				Blocks: allBlocks, Raw: raw, StopReason: stopReason,
				Usage: totalUsage, Citations: allCitations,
			}, nil
		}

		// Append assistant turn (with thinking + tool_use blocks if any) to
		// messages. Thinking blocks must carry their signature or the next
		// iteration's request fails (§4.3 — Claude verifies its own chain).
		assistantTurn := buildAssistantTurn(text, thinkingBlocks, hostedCalls, toolCalls)
		if len(nativeContent) > 0 {
			// Anthropic requires a paused assistant message to be sent back
			// unchanged. The captured native blocks also preserve ordering and
			// response-only fields more faithfully for ordinary local tool loops.
			assistantTurn = map[string]any{"role": "assistant", "content": nativeContent}
		}
		messages = append(messages, assistantTurn)

		if stopReason == "pause_turn" {
			// Provider-hosted tools can pause a long-running server-side loop. No
			// client tool result is needed; replay the exact assistant content in
			// the next request and let Anthropic resume its own tool execution.
			continue
		}

		if stopReason != "tool_use" || len(toolCalls) == 0 {
			// Raw (§2.3-C): the run's full native exchange beyond the supplied
			// history, for same-vendor replay fidelity.
			raw, _ := json.Marshal(messages[historyLen:])
			return &UnifiedResult{
				Blocks:     allBlocks,
				Raw:        raw,
				StopReason: stopReason,
				Usage:      totalUsage,
				Citations:  allCitations,
			}, nil
		}

		// Execute tools concurrently and add tool_result messages in order.
		specs := make([]toolCallSpec, len(toolCalls))
		for i, tc := range toolCalls {
			specs[i] = toolCallSpec{ID: tc.ID, Name: tc.Name, Input: tc.Input}
		}
		results := runToolsConcurrent(ctx, tools, specs, onEvent)
		batchFinalizationErr := toolFinalizationErrorFromResults(results)
		resultBlocks := []map[string]any{}
		for i, tc := range toolCalls {
			r := results[i]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = publicToolErrorOutput(r.Err)
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: tc.Name, ID: tc.ID, Summary: truncate(out, toolResultSummaryTruncationAnthropic), Status: status})
			// Persist the tool round as a block so history reconstruction and the
			// frontend reload keep the full content array (§4.3).
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: tc.Name, ToolID: tc.ID,
				Input: tc.Input, Summary: truncate(out, toolResultSummaryTruncationAnthropic),
			})
			allBlocks = append(allBlocks, canonicalToolOutputBlock(tc.Name, tc.ID, out, status))
			resultBlocks = append(resultBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": tc.ID,
				"content":     out,
				"is_error":    r.Err != nil,
			})
		}
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": resultBlocks,
		})
		if batchFinalizationErr != nil {
			finalizationPending = batchFinalizationErr
		} else if i+1 >= maxIter {
			finalizationPending = &ErrToolBudgetExceeded{Kind: "iterations", Limit: maxIter}
		}
	}
	return nil, errors.New("anthropic: tool loop exhausted")
}

// promptRunOnce returns a PromptToolRunner that performs ONE Anthropic call
// (no native tools, stop sequence on </tool_call>) and returns the raw text.
// Text deltas are swallowed here because RunPromptToolLoop emits the visible
// (markup-stripped) portion itself.
func (p *AnthropicProvider) promptRunOnce(req UnifiedChatRequest) PromptToolRunner {
	return func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		ctx = contextWithoutProviderVisibleOutput(ctx)
		finalizing := isToolBudgetFinalization(ctx)
		roundModel := req.Model
		if finalizing {
			roundModel.Fallback = nil
		}
		if len(req.OfficialToolRequests) > 0 && !finalizing {
			round := req
			round.SystemPrompt = system
			round.History = history
			round.Tools = nil
			round.ToolModePrompt = false
			round.ExtraParams = withPromptStopSequence(req.ExtraParams, map[string]any{
				"stop_sequences": []string{PromptToolStopSequence()},
			})
			result, err := p.Stream(
				ctx,
				round,
				toolDefAllowlistRunner{allowed: map[string]bool{}},
				func(SseEvent) {},
			)
			if result == nil {
				return PromptToolRound{}, err
			}
			return PromptToolRound{
				Text: promptRoundText(result.Blocks), Blocks: result.Blocks, Usage: result.Usage,
				Citations: result.Citations, GeneratedImages: result.GeneratedImages,
				Raw:                  result.Raw,
				UsageAlreadyAttached: true,
			}, err
		}

		maxTok := envcfg.Int("AIVORY_LLM_MAX_TOK_2", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		msgs := historyToAnthropic(history, req.Model.Vision)
		setMessagesCacheBreakpoint(msgs)
		body := map[string]any{
			"model":          req.Model.RequestID,
			"max_tokens":     maxTok,
			"stream":         true,
			"system":         anthropicSystemBlocks(system),
			"messages":       msgs,
			"stop_sequences": []string{PromptToolStopSequence()},
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, false)
		if !finalizing {
			body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		}
		applyAnthropicThinkingSettings(body, req.Model.RequestID, &maxTok, req.StrictMaxOutputTokens)
		buf, _ := json.Marshal(body)
		var (
			text  string
			usage Usage
		)
		err := doProviderParsedRequest(ctx, roundModel, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.anthropic.com")+"/v1/messages", bytes.NewReader(buf))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("anthropic-version", "2023-06-01")
			hr.Header.Set("x-api-key", apiKey)
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		}, func(resp *http.Response, emit func(SseEvent)) error {
			text, usage = "", Usage{}
			if statusErr := requireProviderSuccess(resp, "anthropic"); statusErr != nil {
				return statusErr
			}
			var readErr error
			_, _, _, text, _, _, _, usage, readErr = readAnthropicStream(resp.Body, emit)
			return readErr
		}, func(SseEvent) {})
		return PromptToolRound{Text: text, Usage: usage}, err
	}
}

// joinThinkingText is the unified-block view of a multi-block thinking stream
// — used purely for SSE/UI/log purposes (we keep the structured signature
// list separately for replay).
func joinThinkingText(blocks []anthropicThinkingBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(t.Text)
	}
	return b.String()
}

// anthropicSystemBlocks renders the system prompt as a single cache-controlled
// text block (§4.9) so the stable system prefix is cached across turns.
func anthropicSystemBlocks(system string) any {
	if strings.TrimSpace(system) == "" {
		return ""
	}
	return []map[string]any{
		{"type": "text", "text": system, "cache_control": map[string]any{"type": "ephemeral"}},
	}
}

// setMessagesCacheBreakpoint clears any existing cache_control markers and sets
// exactly one on the last content block of the newest user message — the
// incremental conversation-cache breakpoint (§4.9). Choosing a user message is
// also required for pause_turn: Anthropic says the paused assistant content must
// be sent back unchanged, so a request-only cache marker must not be injected
// into that native response. Clearing first guarantees we never exceed the
// 4-breakpoint limit as the tool loop appends messages.
//
// History coming from raw-replay arrives as []any (each blk is map[string]any);
// blocks we just built in this loop are []map[string]any. We handle both.
func setMessagesCacheBreakpoint(messages []map[string]any) {
	clearCC := func(blk map[string]any) {
		delete(blk, "cache_control")
	}
	for _, m := range messages {
		switch content := m["content"].(type) {
		case []map[string]any:
			for _, blk := range content {
				clearCC(blk)
			}
		case []any:
			for _, b := range content {
				if blk, ok := b.(map[string]any); ok {
					clearCC(blk)
				}
			}
		}
	}
	var last map[string]any
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			last = messages[i]
			break
		}
	}
	if last == nil {
		return
	}
	setCC := func(blk map[string]any) {
		blk["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	switch content := last["content"].(type) {
	case []map[string]any:
		if len(content) > 0 {
			setCC(content[len(content)-1])
		}
	case []any:
		if len(content) > 0 {
			if blk, ok := content[len(content)-1].(map[string]any); ok {
				setCC(blk)
			}
		}
	}
}

func historyToAnthropic(h []UnifiedMessage, vision bool) []map[string]any {
	if !vision {
		h = stripImageBlocks(h)
	}
	out := []map[string]any{}
	for _, m := range h {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Same-vendor raw replay (§2.3-C): the stored native exchange contains
		// the assistant turn(s) + tool_result turns exactly as Anthropic emitted
		// them — splice them in verbatim for maximal fidelity.
		if m.Role == "assistant" && len(m.Raw) > 2 && !isPromptToolRawEnvelope(m.Raw) {
			var turns []map[string]any
			if err := json.Unmarshal(m.Raw, &turns); err == nil && len(turns) > 0 {
				out = append(out, turns...)
				continue
			}
		}
		content := []map[string]any{}
		// Image attachments resolved by the orchestrator (§4.6). Document
		// attachments are intentionally excluded: PDFs/DOCX/PPTX/etc. always enter
		// the model through the RAG text path, never native provider file blocks.
		for _, b := range m.Blocks {
			switch b.Kind {
			case "image":
				// Image blocks are only valid on the user role; assistant content may
				// only be text/tool_use/thinking. Drop images that rode onto a non-user
				// turn (share/fork history) so the request isn't rejected.
				if vision && m.Role == "user" && b.Data != "" {
					content = append(content, map[string]any{
						"type":   "image",
						"source": map[string]any{"type": "base64", "media_type": b.MimeType, "data": b.Data},
					})
				}
			}
		}
		text := renderBlocksAsText(m.Blocks)
		if text != "" || len(content) == 0 {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		out = append(out, map[string]any{"role": m.Role, "content": content})
	}
	return out
}

func toAnthropicTools(defs []ToolDef) []map[string]any {
	out := []map[string]any{}
	for _, d := range defs {
		out = append(out, map[string]any{
			"name":         d.Name,
			"description":  d.Description,
			"input_schema": json.RawMessage(d.InputSchema),
		})
	}
	return out
}

type anthropicToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// anthropicHostedToolCall is a provider-executed server tool round. Anthropic
// distinguishes these from client Functions on the wire: server_tool_use is
// executed by Anthropic, while tool_use must be executed by Aivory. Keeping a
// separate type prevents an official web_search from ever reaching the local
// registry as if it were aivory_web_search.
type anthropicHostedToolCall struct {
	ID           string
	Name         string
	Input        json.RawMessage
	Status       string
	Summary      string
	ResultBlocks []map[string]any
}

func anthropicToolInput(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("{}")
	}
	raw, err := json.Marshal(value)
	if err != nil || !json.Valid(raw) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

func anthropicHostedResultMeta(name string, block map[string]any) (string, string) {
	status := "complete"
	if isError, _ := block["is_error"].(bool); isError {
		status = "error"
	}
	if raw, err := json.Marshal(block["content"]); err == nil {
		lower := strings.ToLower(string(raw))
		if strings.Contains(lower, "error_code") || strings.Contains(lower, "tool_result_error") {
			status = "error"
		}
	}
	if status == "error" {
		return status, name + " failed"
	}
	return status, name + " completed"
}

func anthropicHostedUnifiedBlocks(calls []anthropicHostedToolCall) []UnifiedBlock {
	blocks := make([]UnifiedBlock, 0, len(calls))
	for _, call := range calls {
		blocks = append(blocks, UnifiedBlock{
			Kind: "tool_call", ToolName: call.Name, ToolID: call.ID,
			Input: append(json.RawMessage(nil), call.Input...), Summary: call.Summary,
		})
	}
	return blocks
}

// mergeAnthropicHostedUnifiedBlocks folds a later result-only continuation into
// the server-tool block emitted by an earlier pause_turn response. Anthropic can
// return server_tool_use in one assistant message and its *_tool_result in the
// next; persisting both as separate canonical cards would duplicate one hosted
// call after reload even though the native history correctly contains two turns.
func mergeAnthropicHostedUnifiedBlocks(blocks []UnifiedBlock, calls []anthropicHostedToolCall) []UnifiedBlock {
	for _, incoming := range anthropicHostedUnifiedBlocks(calls) {
		merged := false
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Kind != "tool_call" || blocks[i].ToolID != incoming.ToolID || blocks[i].ToolName != incoming.ToolName {
				continue
			}
			if len(incoming.Input) > 0 && string(incoming.Input) != "{}" {
				blocks[i].Input = incoming.Input
			}
			if incoming.Summary != "" {
				blocks[i].Summary = incoming.Summary
			}
			merged = true
			break
		}
		if !merged {
			blocks = append(blocks, incoming)
		}
	}
	return blocks
}

func completedAnthropicHostedCalls(calls []anthropicHostedToolCall) []anthropicHostedToolCall {
	completed := make([]anthropicHostedToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Status != "" && len(call.ResultBlocks) > 0 {
			completed = append(completed, call)
		}
	}
	return completed
}

// anthropicThinkingBlock captures a thinking block as it streams in so we can
// replay it verbatim in the next loop turn (§4.3 — extended thinking + tools
// REQUIRES the thinking block AND its signature in the assistant turn or the
// API rejects the request with 400 "invalid_request_error: thinking block …").
type anthropicThinkingBlock struct {
	Text      string
	Signature string
}

// readAnthropicStream consumes the SSE response, forwards text/thinking/tool
// deltas as canonical events, and returns visible content plus separate local
// Function and provider-hosted call collections.
//
// Returns thinking as a structured slice (each redacted/normal block with its
// signature) so the next tool-loop iteration can replay them in the assistant
// turn.
func readAnthropicStream(body io.Reader, onEvent func(SseEvent)) (string, []anthropicToolCall, []anthropicHostedToolCall, string, []anthropicThinkingBlock, []Citation, []map[string]any, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, anthropicScannerBufInit), anthropicScannerBufMax)
	stopReason := "end_turn"
	text := strings.Builder{}
	thinkingBlocks := []anthropicThinkingBlock{}
	currentThinking := strings.Builder{}
	currentThinkingActive := false
	toolCalls := []anthropicToolCall{}
	hostedCalls := []anthropicHostedToolCall{}
	hostedIndexByID := map[string]int{}
	citations := []Citation{}
	seenCitations := map[string]bool{}
	nativeBlocks := map[int]map[string]any{}
	nativeOrder := []int{}
	usage := Usage{}
	sawEvent := false
	terminal := false
	streamEnded := false

	var currentTool *anthropicToolCall
	currentHostedIndex := -1
	var partialJSON strings.Builder
	snapshotToolCalls := func() []anthropicToolCall {
		out := append([]anthropicToolCall{}, toolCalls...)
		if currentTool == nil {
			return out
		}
		partial := *currentTool
		if input := partialJSON.String(); json.Valid([]byte(input)) {
			partial.Input = json.RawMessage(input)
		}
		return append(out, partial)
	}
	snapshotHostedCalls := func() []anthropicHostedToolCall {
		out := append([]anthropicHostedToolCall{}, hostedCalls...)
		if currentHostedIndex < 0 || currentHostedIndex >= len(out) {
			return out
		}
		if input := partialJSON.String(); json.Valid([]byte(input)) {
			out[currentHostedIndex].Input = json.RawMessage(input)
		}
		return out
	}
	snapshotThinkingBlocks := func() []anthropicThinkingBlock {
		out := append([]anthropicThinkingBlock{}, thinkingBlocks...)
		if !currentThinkingActive || currentThinking.Len() == 0 {
			return out
		}
		current := currentThinking.String()
		if len(out) > 0 && out[len(out)-1].Signature != "" && strings.HasPrefix(current, out[len(out)-1].Text) {
			out[len(out)-1].Text = current
			return out
		}
		if len(out) == 0 || out[len(out)-1].Text != current {
			out = append(out, anthropicThinkingBlock{Text: current})
		}
		return out
	}
	snapshotNativeContent := func() []map[string]any {
		out := make([]map[string]any, 0, len(nativeOrder))
		for _, index := range nativeOrder {
			if block := nativeBlocks[index]; block != nil {
				out = append(out, block)
			}
		}
		return out
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimPrefix(line, "data:")
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			terminal = true
			break
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return stopReason, snapshotToolCalls(), snapshotHostedCalls(), text.String(), snapshotThinkingBlocks(), citations, snapshotNativeContent(), usage,
				fmt.Errorf("anthropic stream invalid JSON: %w", err)
		}
		if streamErr := providerEventError("anthropic", ev); streamErr != nil {
			return stopReason, snapshotToolCalls(), snapshotHostedCalls(), text.String(), snapshotThinkingBlocks(), citations, snapshotNativeContent(), usage, streamErr
		}
		switch ev["type"] {
		case "content_block_start":
			sawEvent = true
			block, _ := ev["content_block"].(map[string]any)
			blockIndex := intOf(ev["index"])
			if block != nil {
				if _, exists := nativeBlocks[blockIndex]; !exists {
					nativeOrder = append(nativeOrder, blockIndex)
				}
				nativeBlocks[blockIndex] = block
			}
			t, _ := block["type"].(string)
			if t == "tool_use" {
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				currentTool = &anthropicToolCall{ID: id, Name: name, Input: anthropicToolInput(block["input"])}
				currentHostedIndex = -1
				partialJSON.Reset()
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: id})
			} else if t == "server_tool_use" {
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				hostedCalls = append(hostedCalls, anthropicHostedToolCall{
					ID: id, Name: name, Input: anthropicToolInput(block["input"]),
				})
				currentHostedIndex = len(hostedCalls) - 1
				if id != "" {
					hostedIndexByID[id] = currentHostedIndex
				}
				currentTool = nil
				partialJSON.Reset()
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: id})
			} else if strings.HasSuffix(t, "_tool_result") {
				toolUseID, _ := block["tool_use_id"].(string)
				index, ok := hostedIndexByID[toolUseID]
				if !ok {
					name := strings.TrimSuffix(t, "_tool_result")
					hostedCalls = append(hostedCalls, anthropicHostedToolCall{ID: toolUseID, Name: name})
					index = len(hostedCalls) - 1
					hostedIndexByID[toolUseID] = index
					onEvent(SseEvent{Type: "tool_start", Name: name, ID: toolUseID})
				}
				hosted := &hostedCalls[index]
				hosted.ResultBlocks = append(hosted.ResultBlocks, block)
				hosted.Status, hosted.Summary = anthropicHostedResultMeta(hosted.Name, block)
				onEvent(SseEvent{
					Type: "tool_result", Name: hosted.Name, ID: hosted.ID,
					Status: hosted.Status, Summary: hosted.Summary,
				})
				currentTool = nil
				currentHostedIndex = -1
				partialJSON.Reset()
			} else if t == "thinking" || t == "redacted_thinking" {
				currentTool = nil
				currentHostedIndex = -1
				currentThinking.Reset()
				if initial, _ := block["thinking"].(string); initial != "" {
					currentThinking.WriteString(initial)
					onEvent(SseEvent{Type: "thinking_delta", Text: initial})
				}
				currentThinkingActive = true
			} else if t == "text" {
				if initial, _ := block["text"].(string); initial != "" {
					text.WriteString(initial)
					onEvent(SseEvent{Type: "text_delta", Text: initial})
				}
			}
		case "content_block_delta":
			sawEvent = true
			delta, _ := ev["delta"].(map[string]any)
			nativeBlock := nativeBlocks[intOf(ev["index"])]
			switch delta["type"] {
			case "text_delta":
				if s, _ := delta["text"].(string); s != "" {
					text.WriteString(s)
					if nativeBlock != nil {
						current, _ := nativeBlock["text"].(string)
						nativeBlock["text"] = current + s
					}
					onEvent(SseEvent{Type: "text_delta", Text: s})
				}
			case "thinking_delta":
				if s, _ := delta["thinking"].(string); s != "" {
					currentThinking.WriteString(s)
					if nativeBlock != nil {
						current, _ := nativeBlock["thinking"].(string)
						nativeBlock["thinking"] = current + s
					}
					onEvent(SseEvent{Type: "thinking_delta", Text: s})
				}
			case "signature_delta":
				// §4.3: signature_delta carries the cryptographic seal for the
				// thinking block we're currently reading. Without it the next
				// turn's request is rejected as tampered.
				if s, _ := delta["signature"].(string); s != "" {
					if nativeBlock != nil {
						current, _ := nativeBlock["signature"].(string)
						nativeBlock["signature"] = current + s
					}
					if currentThinkingActive {
						if len(thinkingBlocks) == 0 || thinkingBlocks[len(thinkingBlocks)-1].Signature != "" {
							thinkingBlocks = append(thinkingBlocks, anthropicThinkingBlock{Text: currentThinking.String(), Signature: s})
						} else {
							thinkingBlocks[len(thinkingBlocks)-1].Signature = s
							thinkingBlocks[len(thinkingBlocks)-1].Text = currentThinking.String()
						}
					}
				}
			case "input_json_delta":
				if s, _ := delta["partial_json"].(string); s != "" {
					if currentTool == nil && (currentHostedIndex < 0 || currentHostedIndex >= len(hostedCalls)) {
						break
					}
					partialJSON.WriteString(s)
					ev := SseEvent{Type: "tool_input", PartialJson: s}
					if currentTool != nil {
						ev.Name = currentTool.Name
						ev.ID = currentTool.ID
					} else {
						ev.Name = hostedCalls[currentHostedIndex].Name
						ev.ID = hostedCalls[currentHostedIndex].ID
					}
					onEvent(ev)
				}
			case "citations_delta":
				citation, _ := delta["citation"].(map[string]any)
				if nativeBlock != nil && citation != nil {
					existing, _ := nativeBlock["citations"].([]any)
					nativeBlock["citations"] = append(existing, citation)
				}
				url, _ := citation["url"].(string)
				if url == "" {
					url, _ = citation["uri"].(string)
				}
				url = strings.TrimSpace(url)
				if url != "" && !seenCitations[url] {
					seenCitations[url] = true
					title, _ := citation["title"].(string)
					snippet, _ := citation["cited_text"].(string)
					item := Citation{
						ID: fmt.Sprintf("ac%d", len(citations)+1), Index: len(citations) + 1,
						Title: strings.TrimSpace(title), URL: url, Snippet: strings.TrimSpace(snippet), Source: "web",
					}
					citations = append(citations, item)
					onEvent(SseEvent{Type: "citation", Citation: &item})
				}
			}
		case "content_block_stop":
			sawEvent = true
			blockIndex := intOf(ev["index"])
			if input := partialJSON.String(); json.Valid([]byte(input)) {
				var decoded any
				if json.Unmarshal([]byte(input), &decoded) == nil && nativeBlocks[blockIndex] != nil {
					nativeBlocks[blockIndex]["input"] = decoded
				}
			}
			if currentTool != nil {
				if input := partialJSON.String(); json.Valid([]byte(input)) {
					currentTool.Input = json.RawMessage(input)
				}
				if len(currentTool.Input) == 0 || !json.Valid(currentTool.Input) {
					currentTool.Input = json.RawMessage("{}")
				}
				toolCalls = append(toolCalls, *currentTool)
				currentTool = nil
				partialJSON.Reset()
			}
			if currentHostedIndex >= 0 && currentHostedIndex < len(hostedCalls) {
				if input := partialJSON.String(); json.Valid([]byte(input)) {
					hostedCalls[currentHostedIndex].Input = json.RawMessage(input)
				}
				if len(hostedCalls[currentHostedIndex].Input) == 0 {
					hostedCalls[currentHostedIndex].Input = json.RawMessage("{}")
				}
				currentHostedIndex = -1
				partialJSON.Reset()
			}
			if currentThinkingActive {
				// Finalize this thinking block. If signature_delta never came
				// (e.g. display=omitted), still record the text so we don't
				// silently lose the chain of thought.
				if len(thinkingBlocks) == 0 || thinkingBlocks[len(thinkingBlocks)-1].Text != currentThinking.String() {
					thinkingBlocks = append(thinkingBlocks, anthropicThinkingBlock{Text: currentThinking.String()})
				}
				currentThinkingActive = false
				currentThinking.Reset()
			}
		case "message_delta":
			sawEvent = true
			if delta, ok := ev["delta"].(map[string]any); ok {
				if sr, _ := delta["stop_reason"].(string); sr != "" {
					stopReason = sr
					terminal = true
				}
			}
			if u, ok := ev["usage"].(map[string]any); ok {
				if tokens := intOf(u["output_tokens"]); tokens > 0 {
					usage.OutputTokens = tokens
				}
				if tokens := intOf(u["cache_read_input_tokens"]); tokens > 0 {
					usage.CacheReadTokens = tokens
				}
				if tokens := intOf(u["cache_creation_input_tokens"]); tokens > 0 {
					usage.CacheWriteTokens = tokens
				}
			}
		case "message_start":
			sawEvent = true
			if msg, ok := ev["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["input_tokens"])
					usage.CacheReadTokens = intOf(u["cache_read_input_tokens"])
					usage.CacheWriteTokens = intOf(u["cache_creation_input_tokens"])
				}
			}
		case "message_stop":
			sawEvent = true
			terminal = true
			streamEnded = true
		}
		if streamEnded {
			break
		}
	}
	if err := scanner.Err(); err != nil && !terminal {
		// Return whatever was accumulated before the error (e.g. on context cancel)
		// rather than discarding it — partial text must survive a stop signal.
		return stopReason, snapshotToolCalls(), snapshotHostedCalls(), text.String(), snapshotThinkingBlocks(), citations, snapshotNativeContent(), usage, err
	}
	if !sawEvent {
		return stopReason, toolCalls, hostedCalls, text.String(), thinkingBlocks, citations, snapshotNativeContent(), usage, invalidProviderStream("anthropic", "empty response")
	}
	if !terminal {
		return stopReason, snapshotToolCalls(), snapshotHostedCalls(), text.String(), snapshotThinkingBlocks(), citations, snapshotNativeContent(), usage, invalidProviderStream("anthropic", "response ended before a terminal event")
	}
	return stopReason, toolCalls, hostedCalls, text.String(), thinkingBlocks, citations, snapshotNativeContent(), usage, nil
}

func buildAssistantTurn(text string, thinkingBlocks []anthropicThinkingBlock, hosted []anthropicHostedToolCall, calls []anthropicToolCall) map[string]any {
	content := []map[string]any{}
	// §4.3 thinking-with-tools: the thinking block MUST come first in the
	// content array and carry its signature, or Anthropic rejects the next
	// request as tampered. Blocks with no signature (display="omitted") are
	// dropped because the API doesn't accept signature-less thinking on replay.
	for _, t := range thinkingBlocks {
		if t.Signature == "" {
			continue
		}
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  t.Text,
			"signature": t.Signature,
		})
	}
	// Provider-hosted server tools and their results belong to the assistant
	// content returned by Anthropic. Preserve them before any client tool_use so
	// a mixed hosted+Function turn can be replayed verbatim enough for the next
	// local tool-loop request without asking Aivory to execute the server tool.
	for _, hostedCall := range hosted {
		input := map[string]any{}
		_ = json.Unmarshal(hostedCall.Input, &input)
		content = append(content, map[string]any{
			"type": "server_tool_use", "id": hostedCall.ID,
			"name": hostedCall.Name, "input": input,
		})
		content = append(content, hostedCall.ResultBlocks...)
	}
	if strings.TrimSpace(text) != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, c := range calls {
		input := map[string]any{}
		_ = json.Unmarshal(c.Input, &input)
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    c.ID,
			"name":  c.Name,
			"input": input,
		})
	}
	return map[string]any{"role": "assistant", "content": content}
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func jsonEscape(s string) string {
	b := bytes.Buffer{}
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
