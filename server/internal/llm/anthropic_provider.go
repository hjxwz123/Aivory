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

// anthropicModelRejectsSampling reports Claude models whose API rejects
// non-default sampling params such as temperature/top_p/top_k.
func anthropicModelRejectsSampling(requestID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(requestID)), "claude")
}

func applyAnthropicThinkingSettings(body map[string]any, requestID string, maxTok *int) {
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
	// max_tokens must exceed budget_tokens because extended thinking spends
	// from the same Anthropic output budget.
	if *maxTok < budget+anthropicThinkingHeadroomTokens {
		*maxTok = budget + anthropicThinkingHeadroomTokens
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
	if req.Model.APIKey == "" {
		return nil, errors.New("this channel has no API key configured")
	}
	// §4.13 prompt-mode: the model has no native function calling, so drive the
	// text-protocol loop instead of the native tool_use loop.
	if req.ToolModePrompt {
		_, blocks, usage, cites, err := RunPromptToolLoop(
			ctx, req.SystemPrompt, req.History, req.Tools,
			p.promptRunOnce(req), tools, onEvent,
		)
		if err != nil {
			return nil, err
		}
		return &UnifiedResult{Blocks: blocks, StopReason: "end_turn", Usage: usage, Citations: cites}, nil
	}

	maxIter := envcfg.Int("AIVORY_LLM_MAX_ITER", 20)
	messages := historyToAnthropic(req.History)
	historyLen := len(messages) // turns beyond this are this run's raw exchange (§2.3-C)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{} // full ordered content: thinking | text | tool_call (§4.3)
	allCitations := []Citation{}
	totalUsage := Usage{}

	for i := 0; i < maxIter; i++ {
		maxTok := envcfg.Int("AIVORY_LLM_MAX_TOK", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		// §4.9 prompt caching: cache_control on the system block (stable prefix)
		// and on the last message block (incremental history cache). Exactly two
		// breakpoints, well under the 4-breakpoint limit.
		setMessagesCacheBreakpoint(messages)
		body := map[string]any{
			"model":      req.Model.RequestID,
			"max_tokens": maxTok,
			"stream":     true,
			"system":     anthropicSystemBlocks(req.SystemPrompt),
			"messages":   messages,
		}
		if len(req.Tools) > 0 && !req.ToolModePrompt {
			body["tools"] = toAnthropicTools(req.Tools)
		}
		if req.ToolModePrompt {
			body["stop_sequences"] = []string{PromptToolStopSequence()}
		}
		// Apply the model's param_controls (thinking/effort/etc). Claude
		// extended thinking is opt-in: if admins do not explicitly merge a
		// `thinking` object, the provider sends no thinking field.
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		applyAnthropicThinkingSettings(body, req.Model.RequestID, &maxTok)
		buf, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.anthropic.com")+"/v1/messages", bytes.NewReader(buf))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("anthropic-version", "2023-06-01")
			hr.Header.Set("x-api-key", apiKey)
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		})
		if err != nil {
			// Context cancel (stop button): return partial result + err so the
			// orchestrator persists what we got so far (§6.2 partial-content rule).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{
					Blocks: allBlocks, Raw: raw, StopReason: "stopped",
					Usage: totalUsage, Citations: allCitations,
				}, err
			}
			return nil, err
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(b))
		}
		stopReason, toolCalls, text, thinkingBlocks, citations, usage, err := readAnthropicStream(resp.Body, onEvent)
		resp.Body.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				allText.WriteString(text)
				thinkingText := joinThinkingText(thinkingBlocks)
				if thinkingText != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
				}
				if text != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
				}
				allCitations = append(allCitations, citations...)
				totalUsage.OutputTokens += usage.OutputTokens
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{
					Blocks: allBlocks, Raw: raw, StopReason: "stopped",
					Usage: totalUsage, Citations: allCitations,
				}, err
			}
			return nil, err
		}
		allText.WriteString(text)
		thinkingText := joinThinkingText(thinkingBlocks)
		if thinkingText != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
		}
		if text != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		allCitations = append(allCitations, citations...)
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		totalUsage.CacheReadTokens += usage.CacheReadTokens
		totalUsage.CacheWriteTokens += usage.CacheWriteTokens

		// Append assistant turn (with thinking + tool_use blocks if any) to
		// messages. Thinking blocks must carry their signature or the next
		// iteration's request fails (§4.3 — Claude verifies its own chain).
		messages = append(messages, buildAssistantTurn(text, thinkingBlocks, toolCalls))

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
		resultBlocks := []map[string]any{}
		for i, tc := range toolCalls {
			r := results[i]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = "Error: " + r.Err.Error()
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: tc.Name, ID: tc.ID, Summary: truncate(out, toolResultSummaryTruncationAnthropic), Status: status})
			// Persist the tool round as a block so history reconstruction and the
			// frontend reload keep the full content array (§4.3).
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: tc.Name, ToolID: tc.ID,
				Input: tc.Input, Summary: truncate(out, toolResultSummaryTruncationAnthropic),
			})
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
	}
	return nil, errors.New("anthropic: tool loop exhausted")
}

// promptRunOnce returns a PromptToolRunner that performs ONE Anthropic call
// (no native tools, stop sequence on </tool_call>) and returns the raw text.
// Text deltas are swallowed here because RunPromptToolLoop emits the visible
// (markup-stripped) portion itself.
func (p *AnthropicProvider) promptRunOnce(req UnifiedChatRequest) PromptToolRunner {
	return func(ctx context.Context, history []UnifiedMessage, system string) (string, Usage, error) {
		maxTok := envcfg.Int("AIVORY_LLM_MAX_TOK_2", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		msgs := historyToAnthropic(history)
		setMessagesCacheBreakpoint(msgs)
		body := map[string]any{
			"model":          req.Model.RequestID,
			"max_tokens":     maxTok,
			"stream":         true,
			"system":         anthropicSystemBlocks(system),
			"messages":       msgs,
			"stop_sequences": []string{PromptToolStopSequence()},
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		applyAnthropicThinkingSettings(body, req.Model.RequestID, &maxTok)
		buf, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.anthropic.com")+"/v1/messages", bytes.NewReader(buf))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("anthropic-version", "2023-06-01")
			hr.Header.Set("x-api-key", apiKey)
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		})
		if err != nil {
			return "", Usage{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return "", Usage{}, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(b))
		}
		_, _, text, _, _, usage, err := readAnthropicStream(resp.Body, func(SseEvent) {})
		return text, usage, err
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
// exactly one on the last content block of the last message — the incremental
// conversation-cache breakpoint (§4.9). Clearing first guarantees we never
// exceed the 4-breakpoint limit as the tool loop appends messages.
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
	if len(messages) == 0 {
		return
	}
	last := messages[len(messages)-1]
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

func historyToAnthropic(h []UnifiedMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range h {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Same-vendor raw replay (§2.3-C): the stored native exchange contains
		// the assistant turn(s) + tool_result turns exactly as Anthropic emitted
		// them — splice them in verbatim for maximal fidelity.
		if m.Role == "assistant" && len(m.Raw) > 2 {
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
				if b.Data != "" {
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

// anthropicThinkingBlock captures a thinking block as it streams in so we can
// replay it verbatim in the next loop turn (§4.3 — extended thinking + tools
// REQUIRES the thinking block AND its signature in the assistant turn or the
// API rejects the request with 400 "invalid_request_error: thinking block …").
type anthropicThinkingBlock struct {
	Text      string
	Signature string
}

// readAnthropicStream consumes the SSE response, forwards text/thinking deltas
// as canonical events, and returns the recovered text + thinking + tool calls.
//
// Returns thinking as a structured slice (each redacted/normal block with its
// signature) so the next tool-loop iteration can replay them in the assistant
// turn.
func readAnthropicStream(body io.Reader, onEvent func(SseEvent)) (string, []anthropicToolCall, string, []anthropicThinkingBlock, []Citation, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, anthropicScannerBufInit), anthropicScannerBufMax)
	stopReason := "end_turn"
	text := strings.Builder{}
	thinking := strings.Builder{}
	thinkingBlocks := []anthropicThinkingBlock{}
	currentThinking := strings.Builder{}
	currentThinkingActive := false
	toolCalls := []anthropicToolCall{}
	citations := []Citation{}
	usage := Usage{}

	var currentTool *anthropicToolCall
	var partialJSON strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimPrefix(line, "data:")
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev["type"] {
		case "content_block_start":
			block, _ := ev["content_block"].(map[string]any)
			if t, _ := block["type"].(string); t == "tool_use" {
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				currentTool = &anthropicToolCall{ID: id, Name: name}
				partialJSON.Reset()
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: id})
			} else if t == "thinking" || t == "redacted_thinking" {
				currentThinking.Reset()
				currentThinkingActive = true
			}
		case "content_block_delta":
			delta, _ := ev["delta"].(map[string]any)
			switch delta["type"] {
			case "text_delta":
				if s, _ := delta["text"].(string); s != "" {
					text.WriteString(s)
					onEvent(SseEvent{Type: "text_delta", Text: s})
				}
			case "thinking_delta":
				if s, _ := delta["thinking"].(string); s != "" {
					thinking.WriteString(s)
					currentThinking.WriteString(s)
					onEvent(SseEvent{Type: "thinking_delta", Text: s})
				}
			case "signature_delta":
				// §4.3: signature_delta carries the cryptographic seal for the
				// thinking block we're currently reading. Without it the next
				// turn's request is rejected as tampered.
				if s, _ := delta["signature"].(string); s != "" {
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
					partialJSON.WriteString(s)
					ev := SseEvent{Type: "tool_input", PartialJson: s}
					if currentTool != nil {
						ev.Name = currentTool.Name
						ev.ID = currentTool.ID
					}
					onEvent(ev)
				}
			}
		case "content_block_stop":
			if currentTool != nil {
				currentTool.Input = json.RawMessage(partialJSON.String())
				if len(currentTool.Input) == 0 {
					currentTool.Input = json.RawMessage("{}")
				}
				toolCalls = append(toolCalls, *currentTool)
				currentTool = nil
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
			if delta, ok := ev["delta"].(map[string]any); ok {
				if sr, _ := delta["stop_reason"].(string); sr != "" {
					stopReason = sr
				}
			}
			if u, ok := ev["usage"].(map[string]any); ok {
				usage.OutputTokens += intOf(u["output_tokens"])
				usage.CacheReadTokens += intOf(u["cache_read_input_tokens"])
				usage.CacheWriteTokens += intOf(u["cache_creation_input_tokens"])
			}
		case "message_start":
			if msg, ok := ev["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["input_tokens"])
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Return whatever was accumulated before the error (e.g. on context cancel)
		// rather than discarding it — partial text must survive a stop signal.
		return text.String(), toolCalls, thinking.String(), thinkingBlocks, citations, usage, err
	}
	return stopReason, toolCalls, text.String(), thinkingBlocks, citations, usage, nil
}

func buildAssistantTurn(text string, thinkingBlocks []anthropicThinkingBlock, calls []anthropicToolCall) map[string]any {
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
