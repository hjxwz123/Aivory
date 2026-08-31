package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"aivory/server/internal/envcfg"
)

// Env-overridable defaults (§ config-reference). Each falls back to the
// original hardcoded value when its AIVORY_* variable is unset.
var (
	toolResultSummaryTruncationOpenAI = 240
)

// SSE scanner buffer sizing — low-level transport plumbing, not a tunable in
// practice, so hardcoded rather than env-overridable (unlike the knobs above).
const (
	readOpenAIChatStreamBufInit = 64 * 1024
	readOpenAIChatStreamBufMax  = 1024 * 1024
)

// OpenAIProvider supports both the Chat Completions ("chat") and Responses
// API ("responses") formats — the channel's api_format decides at request
// time. When no api_key is set the implementation falls back to the mock
// provider so the orchestrator never errors mid-stream because of missing
// credentials.
type OpenAIProvider struct {
	logger *log.Logger
}

// ID returns "openai".
func (p *OpenAIProvider) ID() string { return "openai" }

// enforceOpenAIOutputTokenCap keeps a strict internal-task budget authoritative
// even when a model's admin extra_params still contains an output-limit alias
// for the other OpenAI API format. Chat Completions accepts max_tokens or the
// newer max_completion_tokens, while Responses uses max_output_tokens. Sending
// conflicting aliases can either be rejected upstream or let a larger alias win.
// Ordinary chat calls retain their configured parameters unchanged.
func enforceOpenAIOutputTokenCap(body map[string]any, req UnifiedChatRequest, responses bool) {
	if !req.StrictMaxOutputTokens || req.MaxOutputTokens <= 0 {
		return
	}
	if responses {
		delete(body, "max_tokens")
		delete(body, "max_completion_tokens")
		body["max_output_tokens"] = req.MaxOutputTokens
		return
	}

	delete(body, "max_output_tokens")
	if _, usesCompletionTokens := body["max_completion_tokens"]; usesCompletionTokens {
		delete(body, "max_tokens")
		body["max_completion_tokens"] = req.MaxOutputTokens
		return
	}
	body["max_tokens"] = req.MaxOutputTokens
}

// Stream runs one model turn against either OpenAI format.
func (p *OpenAIProvider) Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	if req.Model.APIKey == "" && req.Model.Fallback == nil {
		return nil, errors.New("this channel has no API key configured")
	}
	// A Responses-hosted image tool can continue editing one of its own generated
	// artifacts even when the administrator did not mark the mainline text model
	// as generally vision-capable. User uploads are still gated by the API layer.
	if !req.Model.Vision && !officialImageGenerationEnabled(req) {
		req.History = stripImageBlocks(req.History)
	}
	switch req.Model.APIFormat {
	case "responses":
		return p.streamResponses(ctx, req, tools, onEvent)
	default:
		return p.streamChat(ctx, req, tools, onEvent)
	}
}

func officialImageGenerationEnabled(req UnifiedChatRequest) bool {
	if !hostedToolsConfigured(req) {
		return false
	}
	body := MergeOfficialToolRequests(nil, req.OfficialToolRequests)
	return responsesRequestHasToolType(body, "image_generation")
}

// addMissingOpenAIChatReasoning repairs Chat Completions Raw written before
// reasoning fields were preserved. The canonical blocks retain each reasoning
// round for UI rendering, while Raw retains the assistant/tool message sequence.
// Aligning both lets old conversations continue without inventing reasoning for
// cross-provider history, whose Raw is deliberately absent.
func addMissingOpenAIChatReasoning(turns []map[string]any, blocks []UnifiedBlock) {
	assistantCount := 0
	for _, turn := range turns {
		if turn["role"] == "assistant" {
			assistantCount++
		}
	}
	if assistantCount == 0 {
		return
	}

	rounds := openAIChatReasoningRounds(blocks)
	for len(rounds) < assistantCount {
		rounds = append(rounds, "")
	}
	assistantIndex := 0
	for _, turn := range turns {
		if turn["role"] != "assistant" {
			continue
		}
		reasoning := ""
		if assistantIndex < len(rounds) {
			reasoning = rounds[assistantIndex]
		}
		assistantIndex++
		if reasoning == "" || openAIChatTurnHasReasoning(turn) {
			continue
		}
		turn["reasoning_content"] = reasoning
	}
}

func openAIChatTurnHasReasoning(turn map[string]any) bool {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		if value, _ := turn[field].(string); value != "" {
			return true
		}
	}
	return false
}

// openAIChatReasoningRounds mirrors streamChat's canonical block order. A
// tool_call closes one assistant round; the next thinking/text block starts the
// post-tool round. Empty rounds are retained so multiple Raw assistant messages
// stay aligned even if only some of them emitted reasoning.
func openAIChatReasoningRounds(blocks []UnifiedBlock) []string {
	rounds := []string{}
	var current strings.Builder
	afterToolCall := false
	started := false
	for _, block := range blocks {
		switch block.Kind {
		case "thinking":
			if afterToolCall {
				afterToolCall = false
			}
			current.WriteString(block.Text)
			started = true
		case "text":
			if afterToolCall {
				afterToolCall = false
			}
			started = true
		case "tool_call":
			if !afterToolCall {
				rounds = append(rounds, current.String())
				current.Reset()
				afterToolCall = true
			}
			started = true
		}
	}
	if started && !afterToolCall {
		rounds = append(rounds, current.String())
	}
	return rounds
}

func (p *OpenAIProvider) streamChat(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	// §4.13 prompt-mode: no native function calling — drive the text protocol.
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

	messages := []map[string]any{}
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.History {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Same-vendor raw replay (§2.3-C).
		if m.Role == "assistant" && len(m.Raw) > 2 && !isPromptToolRawEnvelope(m.Raw) && (req.Model.Vision || !nativeRawContainsImage(m.Raw)) {
			var turns []map[string]any
			if err := json.Unmarshal(m.Raw, &turns); err == nil && len(turns) > 0 && turns[0]["role"] != nil {
				// Several OpenAI-compatible reasoning models require the assistant's
				// reasoning payload to be replayed verbatim on every subsequent Chat
				// Completions request. Rows written before reasoning replay support may
				// have the canonical thinking blocks but no wire field in Raw; repair
				// those turns without overwriting native fields that are already intact.
				addMissingOpenAIChatReasoning(turns, m.Blocks)
				messages = append(messages, turns...)
				continue
			}
		}
		text := renderBlocksAsText(m.Blocks)
		// Image attachments → multimodal content array (data URI form). Document
		// attachments are intentionally excluded: PDFs/DOCX/PPTX/etc. always enter
		// the model through the RAG text path, never native provider file blocks.
		imgParts := []map[string]any{}
		for _, b := range m.Blocks {
			// image_url parts are only valid on the user role: OpenAI's content-part
			// enum for system/assistant/tool accepts text only, so an image that rode
			// onto a non-user turn (share/fork history) triggers "unknown variant
			// `image_url`, expected `text`". Drop it here (defense in depth alongside
			// resolveAttachments' role gate).
			if req.Model.Vision && m.Role == "user" && b.Kind == "image" && b.Data != "" {
				imgParts = append(imgParts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + b.MimeType + ";base64," + b.Data},
				})
			}
		}
		if len(imgParts) > 0 {
			content := make([]map[string]any, 0, len(imgParts)+1)
			if text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
			content = append(content, imgParts...)
			messages = append(messages, map[string]any{"role": m.Role, "content": content})
		} else {
			messages = append(messages, map[string]any{"role": m.Role, "content": text})
		}
	}

	maxIter := envcfg.Int("AIVORY_LLM_MAX_ITER_2", 20)
	historyLen := len(messages)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	allCitations := []Citation{}
	usage := Usage{}
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
		requestMessages := messages
		if finalizing {
			requestMessages = append([]map[string]any{}, messages...)
			requestMessages = append(requestMessages, map[string]any{
				"role": "system", "content": toolFinalizationInstruction(finalizationSignal),
			})
		}
		body := map[string]any{
			"model":          req.Model.RequestID,
			"messages":       requestMessages,
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
		}
		if req.MaxOutputTokens > 0 {
			body["max_tokens"] = req.MaxOutputTokens
		}
		nativeToolsEnabled := len(req.Tools) > 0 && !req.ToolModePrompt && !finalizing
		if nativeToolsEnabled {
			openAITools := []map[string]any{}
			for _, t := range req.Tools {
				openAITools = append(openAITools, map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        t.Name,
						"description": t.Description,
						"parameters":  json.RawMessage(t.InputSchema),
					},
				})
			}
			body["tools"] = openAITools
		}
		if req.ToolModePrompt {
			body["stop"] = []string{PromptToolStopSequence()}
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, nativeToolsEnabled)
		if !finalizing {
			body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		}
		enforceOpenAIOutputTokenCap(body, req, false)
		raw, _ := json.Marshal(body)
		var (
			text      string
			reasoning openAIChatReasoning
			calls     []openAIToolCall
			finish    string
			u         Usage
		)
		err := doProviderParsedRequest(ctx, roundModel, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", OpenAIBaseURL(baseURL)+"/chat/completions", bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("authorization", "Bearer "+apiKey)
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		}, func(resp *http.Response, emit func(SseEvent)) error {
			text, reasoning, finish, u = "", openAIChatReasoning{}, "", Usage{}
			calls = nil
			if statusErr := requireProviderSuccess(resp, "openai"); statusErr != nil {
				return statusErr
			}
			var readErr error
			text, reasoning, calls, finish, u, readErr = readOpenAIChatStream(resp.Body, emit)
			// Bind usage before doProviderParsedRequest can retry this hidden task
			// against fallback credentials. The next attempt resets u.
			attachProviderRequestUsage(ctx, u)
			return readErr
		}, onEvent)
		if err != nil {
			partialBlocks := append([]UnifiedBlock{}, allBlocks...)
			if reasoning.Text != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning.Text})
			}
			if text != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "text", Text: text})
			}
			for _, call := range calls {
				partialBlocks = append(partialBlocks, UnifiedBlock{
					Kind: "tool_call", ToolName: call.Name, ToolID: call.ID,
					Input: validPartialToolInput(call.Input),
				})
			}
			partialUsage := usage
			partialUsage.InputTokens += u.InputTokens
			partialUsage.OutputTokens += u.OutputTokens
			if finalizing && !errors.Is(err, context.Canceled) {
				err = toolFinalizationError(finalizationSignal, err)
			}
			// Stop button / kill: preserve what streamed so far (§6.2) instead of
			// blanking the message.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{Blocks: partialBlocks, Raw: raw, StopReason: "stopped", Usage: partialUsage, Citations: allCitations}, err
			}
			visible := providerVisibleOutputFromContext(ctx)
			if len(partialBlocks) > 0 || partialUsage.InputTokens > 0 || partialUsage.OutputTokens > 0 || (visible != nil && visible.Load()) {
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{Blocks: partialBlocks, Raw: raw, StopReason: "error", Usage: partialUsage, Citations: allCitations}, err
			}
			return nil, err
		}
		allText.WriteString(text)
		// Thinking precedes the round's text so the reasoning trace reads
		// think → answer/tool in order.
		if reasoning.Text != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning.Text})
		}
		if text != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		if finalizing {
			assistant := map[string]any{"role": "assistant", "content": text}
			if reasoning.Text != "" {
				assistant[reasoning.WireField()] = reasoning.Text
			}
			if text != "" || reasoning.Text != "" {
				messages = append(messages, assistant)
			}
			if len(calls) > 0 || strings.TrimSpace(text) == "" {
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{
					Blocks: allBlocks, Raw: raw, StopReason: toolFinalizationStopReason(finalizationSignal),
					Usage: usage, Citations: allCitations,
				}, toolFinalizationError(finalizationSignal, errors.New("model did not return a tool-free final answer"))
			}
			raw, _ := json.Marshal(messages[historyLen:])
			if finish == "" {
				finish = "stop"
			}
			return &UnifiedResult{
				Blocks: allBlocks, Raw: raw, StopReason: finish,
				Usage: usage, Citations: allCitations,
			}, nil
		}

		assistant := map[string]any{"role": "assistant", "content": text}
		if reasoning.Text != "" {
			assistant[reasoning.WireField()] = reasoning.Text
		}
		if len(calls) > 0 {
			toolCalls := []map[string]any{}
			for _, c := range calls {
				toolCalls = append(toolCalls, map[string]any{
					"id":   c.ID,
					"type": "function",
					"function": map[string]any{
						"name":      c.Name,
						"arguments": string(c.Input),
					},
				})
			}
			assistant["tool_calls"] = toolCalls
		}
		messages = append(messages, assistant)

		if finish != "tool_calls" || len(calls) == 0 {
			raw, _ := json.Marshal(messages[historyLen:])
			return &UnifiedResult{
				Blocks:     allBlocks,
				Raw:        raw,
				StopReason: finish,
				Usage:      usage,
				Citations:  allCitations,
			}, nil
		}

		specs := make([]toolCallSpec, len(calls))
		for i, tc := range calls {
			specs[i] = toolCallSpec{ID: tc.ID, Name: tc.Name, Input: tc.Input}
		}
		results := runToolsConcurrent(ctx, tools, specs, onEvent)
		batchFinalizationErr := toolFinalizationErrorFromResults(results)
		for i, tc := range calls {
			r := results[i]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = publicToolErrorOutput(r.Err)
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: tc.Name, ID: tc.ID, Summary: truncate(out, toolResultSummaryTruncationOpenAI), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: tc.Name, ToolID: tc.ID,
				Input: tc.Input, Summary: truncate(out, toolResultSummaryTruncationOpenAI),
			})
			allBlocks = append(allBlocks, canonicalToolOutputBlock(tc.Name, tc.ID, out, status))
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"content":      out,
			})
		}
		if batchFinalizationErr != nil {
			finalizationPending = batchFinalizationErr
		} else if i+1 >= maxIter {
			finalizationPending = &ErrToolBudgetExceeded{Kind: "iterations", Limit: maxIter}
		}
	}
	raw, _ := json.Marshal(messages[historyLen:])
	return &UnifiedResult{
		Blocks:     allBlocks,
		Raw:        raw,
		StopReason: "max_iterations",
		Usage:      usage,
		Citations:  allCitations,
	}, nil
}

// promptRunOnce returns a PromptToolRunner performing ONE Chat Completions
// call (no native tools, stop on </tool_call>) for §4.13 prompt-mode.
func (p *OpenAIProvider) promptRunOnce(req UnifiedChatRequest) PromptToolRunner {
	return func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		roundModel := req.Model
		if isToolBudgetFinalization(ctx) {
			roundModel.Fallback = nil
		}
		messages := []map[string]any{}
		if system != "" {
			messages = append(messages, map[string]any{"role": "system", "content": system})
		}
		for _, m := range history {
			if m.Role != "user" && m.Role != "assistant" {
				continue
			}
			text := strings.Builder{}
			imageParts := []map[string]any{}
			for _, b := range m.Blocks {
				if b.Kind == "text" {
					text.WriteString(b.Text)
					text.WriteString("\n")
				}
				if req.Model.Vision && m.Role == "user" && b.Kind == "image" && b.Data != "" {
					imageParts = append(imageParts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:" + b.MimeType + ";base64," + b.Data},
					})
				}
			}
			messageText := strings.TrimRight(text.String(), "\n")
			if len(imageParts) > 0 {
				content := make([]map[string]any, 0, len(imageParts)+1)
				if messageText != "" {
					content = append(content, map[string]any{"type": "text", "text": messageText})
				}
				content = append(content, imageParts...)
				messages = append(messages, map[string]any{"role": m.Role, "content": content})
				continue
			}
			messages = append(messages, map[string]any{"role": m.Role, "content": messageText})
		}
		body := map[string]any{
			"model":    req.Model.RequestID,
			"messages": messages,
			"stream":   true,
			"stop":     []string{PromptToolStopSequence()},
		}
		if req.MaxOutputTokens > 0 {
			body["max_tokens"] = req.MaxOutputTokens
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, false)
		if !isToolBudgetFinalization(ctx) {
			body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		}
		enforceOpenAIOutputTokenCap(body, req, false)
		raw, _ := json.Marshal(body)
		var (
			text string
			u    Usage
		)
		err := doProviderParsedRequest(ctx, roundModel, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", OpenAIBaseURL(baseURL)+"/chat/completions", bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("authorization", "Bearer "+apiKey)
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		}, func(resp *http.Response, emit func(SseEvent)) error {
			text, u = "", Usage{}
			if statusErr := requireProviderSuccess(resp, "openai"); statusErr != nil {
				return statusErr
			}
			var readErr error
			text, _, _, _, u, readErr = readOpenAIChatStream(resp.Body, emit)
			return readErr
		}, func(SseEvent) {})
		return PromptToolRound{Text: text, Usage: u}, err
	}
}

// promptResponsesRunOnce performs one Responses-format round for a model whose
// local Functions use the text protocol. Hosted tools remain in the upstream
// request and execute provider-side; local Function declarations are withheld so
// only RunPromptToolLoop can dispatch them through the application registry.
func (p *OpenAIProvider) promptResponsesRunOnce(req UnifiedChatRequest) PromptToolRunner {
	return func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		round := req
		round.SystemPrompt = system
		round.History = history
		round.Tools = nil
		round.ToolModePrompt = false
		if isToolBudgetFinalization(ctx) {
			round.OfficialToolNames = nil
			round.OfficialToolRequests = nil
		}
		result, err := p.streamResponses(
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
}

type openAIToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// hostedToolCall records an OpenAI-hosted tool round (web_search etc.) the model
// ran server-side, so we can persist it as a tool_call block (§2.3-B).
type hostedToolCall struct {
	ID, Name, Summary, Status string
	// ImageBase64 is populated for OpenAI-hosted image_generation calls. It is
	// deliberately kept out of outputItems/Raw and decoded only when building the
	// UnifiedResult, preventing a generated image from being duplicated in SQL.
	ImageBase64       string
	ImagePartialIndex int
}

// responseLineScanner has the small Scanner API used below but no token-size
// ceiling. Responses image_generation completion events contain the final image
// as one base64 JSON field and routinely exceed bufio.Scanner's 1 MiB limit.
type responseLineScanner struct {
	reader *bufio.Reader
	text   string
	err    error
}

func newResponseLineScanner(r io.Reader) *responseLineScanner {
	return &responseLineScanner{reader: bufio.NewReaderSize(r, 64*1024)}
}

func (s *responseLineScanner) Scan() bool {
	if s.err != nil {
		return false
	}
	line, err := s.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
		return false
	}
	if len(line) == 0 && errors.Is(err, io.EOF) {
		return false
	}
	s.text = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if errors.Is(err, io.EOF) {
		s.err = io.EOF
	}
	return true
}

func (s *responseLineScanner) Text() string { return s.text }

func (s *responseLineScanner) Err() error {
	if errors.Is(s.err, io.EOF) {
		return nil
	}
	return s.err
}

// hostedToolName removes the Responses output-item suffix while preserving the
// provider's own tool namespace. In particular, code_interpreter and
// image_generation must not be renamed to Aivory's local python_execute and
// image_generate Functions; those are separate tools with separate policy.
func hostedToolName(itemType string) string {
	return strings.TrimSuffix(itemType, "_call")
}

func appendResponsesInclude(body map[string]any, values ...string) {
	if body == nil {
		return
	}
	seen := map[string]bool{}
	include := []string{}
	switch cur := body["include"].(type) {
	case []string:
		for _, s := range cur {
			if s != "" && !seen[s] {
				seen[s] = true
				include = append(include, s)
			}
		}
	case []any:
		for _, v := range cur {
			if s, _ := v.(string); s != "" && !seen[s] {
				seen[s] = true
				include = append(include, s)
			}
		}
	}
	for _, s := range values {
		if s != "" && !seen[s] {
			seen[s] = true
			include = append(include, s)
		}
	}
	if len(include) > 0 {
		body["include"] = include
	}
}

func responsesRequestHasTools(body map[string]any) bool {
	value, ok := body["tools"]
	if !ok {
		return false
	}
	if tools, isArray := jsonArrayItems(value); isArray {
		return len(tools) > 0
	}
	return value != nil
}

func responsesRequestHasToolType(body map[string]any, toolType string) bool {
	tools, ok := jsonArrayItems(body["tools"])
	if !ok {
		return false
	}
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if ok && tool["type"] == toolType {
			return true
		}
	}
	return false
}

func responseOutputHasFunctionCalls(items []map[string]any) bool {
	for _, item := range items {
		if t, _ := item["type"].(string); t == "function_call" {
			return true
		}
	}
	return false
}

// responsesEasyInputMessage is the channel-neutral representation for prior
// user/assistant text. String content selects the EasyInputMessage schema
// without pretending rebuilt history is a complete ResponseOutputMessage or
// relying on the ambiguous assistant content-array discriminator used by
// Responses-compatible gateways.
func responsesEasyInputMessage(role, text string) map[string]any {
	return map[string]any{
		"role":    role,
		"content": text,
	}
}

// prepareResponsesReplayItems preserves complete response.output messages for
// stateless tool continuation. Legacy assistant messages that cannot form a
// valid ResponseOutputMessage degrade to portable string-content history.
// Terminal calls from compatible gateways get a missing status repaired without
// mutating the captured provider response.
func prepareResponsesReplayItems(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return items
	}
	prepared := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if responsesOutputMessageReplayable(item) {
			prepared = append(prepared, responsesCompletedReplayItem(item))
			continue
		}
		if easy, ok := responsesAssistantMessageAsEasyInput(item); ok {
			prepared = append(prepared, easy)
			continue
		}
		prepared = append(prepared, responsesCompletedReplayItem(item))
	}
	return prepared
}

func responsesCompletedReplayItem(item map[string]any) map[string]any {
	itemType, _ := item["type"].(string)
	if !strings.HasSuffix(itemType, "_call") {
		return item
	}
	if status, _ := item["status"].(string); strings.TrimSpace(status) != "" {
		return item
	}
	clone := make(map[string]any, len(item)+1)
	for key, value := range item {
		clone[key] = value
	}
	clone["status"] = "completed"
	return clone
}

func responsesOutputMessageReplayable(item map[string]any) bool {
	itemType, _ := item["type"].(string)
	role, _ := item["role"].(string)
	if itemType != "message" || role != "assistant" {
		return false
	}
	if id, _ := item["id"].(string); strings.TrimSpace(id) == "" {
		return false
	}
	if status, _ := item["status"].(string); strings.TrimSpace(status) == "" {
		return false
	}
	content, ok := jsonArrayItems(item["content"])
	if !ok || len(content) == 0 {
		return false
	}
	for _, rawPart := range content {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			return false
		}
		partType, _ := part["type"].(string)
		if partType != "output_text" && partType != "refusal" {
			return false
		}
	}
	return true
}

func responsesAssistantMessageAsEasyInput(item map[string]any) (map[string]any, bool) {
	role, _ := item["role"].(string)
	if role != "assistant" {
		return nil, false
	}
	itemType, _ := item["type"].(string)
	if itemType != "" && itemType != "message" {
		return nil, false
	}
	var text strings.Builder
	if content, ok := jsonArrayItems(item["content"]); ok {
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			if part == nil {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "output_text", "input_text":
				partText, _ := part["text"].(string)
				text.WriteString(partText)
			case "refusal":
				partText, _ := part["refusal"].(string)
				text.WriteString(partText)
			}
		}
	} else if value, isText := item["content"].(string); isText {
		text.WriteString(value)
	}
	if text.Len() == 0 {
		return nil, false
	}
	easy := responsesEasyInputMessage(role, text.String())
	if phase, _ := item["phase"].(string); phase != "" {
		easy["phase"] = phase
	}
	return easy, true
}

type openAIResponseCallBuf struct {
	ID, Name string
	Args     strings.Builder
	Started  bool
}

type openAIChatReasoning struct {
	Text  string
	Field string
}

func (r openAIChatReasoning) WireField() string {
	if r.Field == "reasoning" {
		return "reasoning"
	}
	return "reasoning_content"
}

func readOpenAIChatStream(body io.Reader, onEvent func(SseEvent)) (string, openAIChatReasoning, []openAIToolCall, string, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, readOpenAIChatStreamBufInit), readOpenAIChatStreamBufMax)
	text := strings.Builder{}
	reasoning := strings.Builder{}
	reasoningField := ""
	snapshotReasoning := func() openAIChatReasoning {
		return openAIChatReasoning{Text: reasoning.String(), Field: reasoningField}
	}
	appendReasoning := func(field, value string) {
		if value == "" {
			return
		}
		if reasoningField == "" {
			reasoningField = field
		}
		reasoning.WriteString(value)
		onEvent(SseEvent{Type: "thinking_delta", Text: value})
	}
	usage := Usage{}
	finish := "end_turn"
	sawEvent := false
	terminal := false
	// Tool calls are accumulated by index — OpenAI streams partial args.
	toolByIdx := map[int]*openAIToolCall{}
	toolStarted := map[int]bool{}
	snapshotCalls := func() []openAIToolCall {
		indexes := make([]int, 0, len(toolByIdx))
		for idx := range toolByIdx {
			indexes = append(indexes, idx)
		}
		sort.Ints(indexes)
		calls := make([]openAIToolCall, 0, len(indexes))
		for _, idx := range indexes {
			call := *toolByIdx[idx]
			if len(call.Input) == 0 {
				call.Input = json.RawMessage("{}")
			}
			calls = append(calls, call)
		}
		return calls
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			terminal = true
			break
		}
		// Some Chat-compatible gateways send the usage-only chunk after the
		// finish_reason frame. Keep that accounting, but never let trailing proxy
		// noise turn an already completed response into a retry.
		if terminal {
			var trailer map[string]any
			if json.Unmarshal([]byte(payload), &trailer) == nil {
				if u, ok := trailer["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["prompt_tokens"])
					usage.OutputTokens = intOf(u["completion_tokens"])
				}
			}
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return text.String(), snapshotReasoning(), snapshotCalls(), finish, usage,
				fmt.Errorf("openai chat stream invalid JSON: %w", err)
		}
		if streamErr := providerEventError("openai chat", ev); streamErr != nil {
			return text.String(), snapshotReasoning(), snapshotCalls(), finish, usage, streamErr
		}
		choices, _ := ev["choices"].([]any)
		if len(choices) > 0 {
			sawEvent = true
		}
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			// Reasoning models on the Chat Completions wire (OpenAI o-series via
			// compatible gateways, DeepSeek-R1, etc.) stream chain-of-thought as
			// `reasoning_content` or `reasoning` deltas — surface them as thinking.
			if s, _ := delta["reasoning_content"].(string); s != "" {
				appendReasoning("reasoning_content", s)
			}
			if s, _ := delta["reasoning"].(string); s != "" {
				appendReasoning("reasoning", s)
			}
			if s, _ := delta["content"].(string); s != "" {
				text.WriteString(s)
				onEvent(SseEvent{Type: "text_delta", Text: s})
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, raw := range tcs {
					tc, _ := raw.(map[string]any)
					idx := intOf(tc["index"])
					cur, isExisting := toolByIdx[idx]
					if !isExisting {
						cur = &openAIToolCall{}
						toolByIdx[idx] = cur
					}
					if id, _ := tc["id"].(string); id != "" {
						cur.ID = id
					}
					if fn, _ := tc["function"].(map[string]any); fn != nil {
						if n, _ := fn["name"].(string); n != "" {
							if !toolStarted[idx] {
								// First slice that names the tool — emit tool_start.
								onEvent(SseEvent{Type: "tool_start", Name: n, ID: cur.ID})
								toolStarted[idx] = true
							}
							cur.Name = n
						}
						if a, _ := fn["arguments"].(string); a != "" {
							cur.Input = append(cur.Input, []byte(a)...)
							// Surface partial JSON to the frontend so the
							// search term / code / etc renders as it arrives.
							onEvent(SseEvent{Type: "tool_input", ID: cur.ID, Name: cur.Name, PartialJson: a})
						}
					}
				}
			}
			if fr, _ := ch["finish_reason"].(string); fr != "" {
				finish = fr
				terminal = true
			}
		}
		if u, ok := ev["usage"].(map[string]any); ok {
			sawEvent = true
			usage.InputTokens = intOf(u["prompt_tokens"])
			usage.OutputTokens = intOf(u["completion_tokens"])
		}
	}
	calls := snapshotCalls()
	if err := scanner.Err(); err != nil && !terminal {
		return text.String(), snapshotReasoning(), calls, finish, usage, err
	}
	if !sawEvent {
		return text.String(), snapshotReasoning(), calls, finish, usage, invalidProviderStream("openai chat", "empty response")
	}
	if !terminal {
		return text.String(), snapshotReasoning(), calls, finish, usage, invalidProviderStream("openai chat", "response ended before a terminal event")
	}
	return text.String(), snapshotReasoning(), calls, finish, usage, nil
}

// streamResponses drives the OpenAI Responses API (`POST /v1/responses`),
// which has a distinct request/response shape from Chat Completions: messages
// become `input` items, tool calls are `function_call` output items, and tool
// results are fed back as `function_call_output` input items (§2.3-E).
//
// §4.10-E compliance:
//   - We use the streaming Responses endpoint so text/reasoning deltas reach
//     the user in real-time (the non-streaming form blocks the whole turn).
//   - `store: false` is REQUIRED by the design: we manage our own conversation
//     state, OpenAI must NOT persist it server-side. Without this flag,
//     reasoning items leak across sessions and billing surprises follow.
//   - `arguments` is the JSON-STRING form expected by the wire protocol; we
//     pass `json.RawMessage(c.Input)` so it's emitted as a string literal,
//     not double-encoded to `"\"{\\\"x\\\":1}\""` (which the upstream rejects).
//   - reasoning summary deltas (`response.output_text.delta` for type=summary)
//     are emitted as `thinking_delta` events so the UI's collapsed-thinking
//     pane updates live.
func (p *OpenAIProvider) streamResponses(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	if req.ToolModePrompt {
		_, blocks, usage, cites, images, raw, err := RunPromptToolLoopWithRaw(
			ctx, req.SystemPrompt, req.History, req.Tools,
			p.promptResponsesRunOnce(req), tools, onEvent,
		)
		if err != nil {
			return promptToolErrorResult(ctx, blocks, raw, usage, cites, images, err)
		}
		return &UnifiedResult{Blocks: blocks, Raw: raw, StopReason: "end_turn", Usage: usage, Citations: cites, GeneratedImages: images}, nil
	}

	// Build the input list from history. Hosted image output is persisted as an
	// artifact, then attached to the following user turn as input_image. This keeps
	// multi-turn editing stateless (`store:false`) without replaying the original
	// multi-megabyte image_generation_call.result from message Raw.
	input := []map[string]any{}
	pendingGeneratedImages := []map[string]any{}
	hostedImageContext := officialImageGenerationEnabled(req)
	for _, m := range req.History {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Same-vendor raw replay (§2.3-C) for Responses-format tool turns. Raw
		// from Chat Completions has role/tool_calls; Responses raw has typed
		// output/input items (`message`, `reasoning`, `function_call`,
		// `function_call_output`). Accept only the latter so switching an OpenAI
		// model between chat/responses formats cannot poison the request body.
		if m.Role == "assistant" && len(m.Raw) > 2 && !isPromptToolRawEnvelope(m.Raw) && !nativeRawContainsImage(m.Raw) {
			var items []map[string]any
			if err := json.Unmarshal(m.Raw, &items); err == nil && len(items) > 0 && items[0]["type"] != nil {
				input = append(input, prepareResponsesReplayItems(items)...)
				continue
			}
		}
		messageText := renderBlocksAsText(m.Blocks)
		if m.Role == "assistant" {
			if messageText != "" {
				input = append(input, responsesEasyInputMessage("assistant", messageText))
			}
		} else {
			parts := []map[string]any{}
			if messageText != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": messageText})
			}
			if len(pendingGeneratedImages) > 0 {
				parts = append(parts, pendingGeneratedImages...)
				pendingGeneratedImages = nil
			}
			// Multimodal: pass image blocks through. Document attachments are
			// intentionally excluded: PDFs/DOCX/PPTX/etc. always enter the model
			// through the RAG text path, never native provider file blocks.
			for _, b := range m.Blocks {
				if req.Model.Vision && b.Kind == "image" && b.Data != "" {
					parts = append(parts, map[string]any{
						"type":      "input_image",
						"image_url": "data:" + b.MimeType + ";base64," + b.Data,
					})
				}
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{
					"role":    "user",
					"content": parts,
				})
			}
		}
		if hostedImageContext && m.Role == "assistant" {
			for _, b := range m.Blocks {
				if b.Kind == "artifact" && b.Data != "" && strings.HasPrefix(strings.ToLower(b.MimeType), "image/") {
					pendingGeneratedImages = append(pendingGeneratedImages, map[string]any{
						"type":      "input_image",
						"image_url": "data:" + b.MimeType + ";base64," + b.Data,
					})
				}
			}
		}
	}

	var respTools []map[string]any
	for _, t := range req.Tools {
		respTools = append(respTools, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  json.RawMessage(t.InputSchema),
		})
	}

	maxIter := envcfg.Int("AIVORY_LLM_MAX_ITER_3", 20)
	historyLen := len(input)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	allCitations := []Citation{}
	allGeneratedImages := []GeneratedImage{}
	usage := Usage{}
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
		body := map[string]any{
			"model": req.Model.RequestID,
			"input": input,
			// §4.10-E hard rule: do NOT let OpenAI persist conversation state.
			"store":  false,
			"stream": true,
		}
		instructions := req.SystemPrompt
		if finalizing {
			instructions = strings.TrimSpace(instructions + "\n\n" + toolFinalizationInstruction(finalizationSignal))
		}
		if instructions != "" {
			body["instructions"] = instructions
		}
		if req.MaxOutputTokens > 0 {
			body["max_output_tokens"] = req.MaxOutputTokens
		}
		nativeToolsEnabled := len(respTools) > 0 && !finalizing
		if nativeToolsEnabled {
			body["tools"] = respTools
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, nativeToolsEnabled)
		if !finalizing {
			body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		}
		enforceOpenAIOutputTokenCap(body, req, true)
		// Ask the API to return the sources the hosted web_search consulted, so
		// we can surface them as citations. For stateless Responses tool loops
		// (`store:false`), also carry encrypted reasoning items forward; otherwise
		// reasoning models can lose their hidden chain between a function_call and
		// the matching function_call_output.
		includes := []string{}
		if responsesRequestHasToolType(body, "web_search") {
			includes = append(includes, "web_search_call.action.sources")
		}
		if responsesRequestHasTools(body) {
			includes = append(includes, "reasoning.encrypted_content")
		}
		appendResponsesInclude(body, includes...)
		raw, _ := json.Marshal(body)
		var (
			text        string
			reasoning   string
			calls       []openAIToolCall
			hosted      []hostedToolCall
			citations   []Citation
			u           Usage
			outputItems []map[string]any
			generated   []GeneratedImage
		)
		// A relay can accept a Responses request and then close the HTTP body
		// before sending even one usable SSE event (commonly surfaced by Go as
		// unexpected EOF). Replaying is safe only while THIS provider round has
		// emitted nothing: no user-visible text/thinking and no tool event can be
		// duplicated. This matters for post-tool continuation rounds, where the
		// turn-scoped visible flag is already committed by the preceding tool call
		// and therefore intentionally disables the normal channel fallback path.
		roundEmitted := false
		emitRound := func(ev SseEvent) {
			roundEmitted = true
			onEvent(ev)
		}
		runRequest := func() error {
			return doProviderParsedRequest(ctx, roundModel, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
				hr, e := http.NewRequestWithContext(ctx, "POST", OpenAIBaseURL(baseURL)+"/responses", bytes.NewReader(raw))
				if e != nil {
					return nil, e
				}
				hr.Header.Set("authorization", "Bearer "+apiKey)
				hr.Header.Set("content-type", "application/json")
				hr.Header.Set("accept", "text/event-stream")
				return hr, nil
			}, func(resp *http.Response, emit func(SseEvent)) error {
				text, reasoning, u = "", "", Usage{}
				calls, hosted, citations, outputItems, generated = nil, nil, nil, nil, nil
				if statusErr := requireProviderSuccess(resp, "openai responses"); statusErr != nil {
					return statusErr
				}
				var readErr error
				text, reasoning, calls, hosted, citations, u, outputItems, readErr = readOpenAIResponsesStream(resp.Body, emit)
				// Record this exact attempt before a transparent channel fallback or
				// the one-shot unexpected-EOF retry overwrites u.
				attachProviderRequestUsage(ctx, u)
				if readErr != nil {
					return readErr
				}
				var imageErr error
				hosted, generated, imageErr = decodeHostedGeneratedImages(hosted)
				return imageErr
			}, emitRound)
		}
		err := runRequest()
		if !finalizing && errors.Is(err, io.ErrUnexpectedEOF) && !roundEmitted && ctx.Err() == nil {
			if p.logger != nil {
				p.logger.Printf("openai responses: upstream stream ended with unexpected EOF before any event; retrying once (model=%s)", req.Model.RequestID)
			}
			err = runRequest()
		}
		allGeneratedImages = append(allGeneratedImages, generated...)
		if err != nil {
			partialBlocks := append([]UnifiedBlock{}, allBlocks...)
			if reasoning != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning})
			}
			for _, h := range hosted {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "tool_call", ToolName: h.Name, ToolID: h.ID, Summary: h.Summary})
			}
			for _, call := range calls {
				partialBlocks = append(partialBlocks, UnifiedBlock{
					Kind: "tool_call", ToolName: call.Name, ToolID: call.ID,
					Input: validPartialToolInput(call.Input),
				})
			}
			if text != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "text", Text: text})
			}
			partialCitations := append(append([]Citation{}, allCitations...), citations...)
			partialUsage := usage
			partialUsage.InputTokens += u.InputTokens
			partialUsage.OutputTokens += u.OutputTokens
			if finalizing && !errors.Is(err, context.Canceled) {
				err = toolFinalizationError(finalizationSignal, err)
			}
			partialInput := append([]map[string]any{}, input...)
			completedToolResult := false
			// A hosted Responses tool can finish before a later relay/provider error
			// terminates the round. Keep only completed items that contain a
			// recognized tool result. This preserves their full compaction evidence
			// without replaying a dangling local function_call or arbitrary partial
			// provider state on the next chat turn.
			for _, item := range outputItems {
				itemRaw, marshalErr := json.Marshal(item)
				if marshalErr != nil || len(extractCompactionRawToolOutputs(itemRaw)) == 0 {
					continue
				}
				completedToolResult = true
				partialInput = append(partialInput, item)
			}
			if text != "" {
				// Keep the visible partial answer in replay history, but never retain
				// a current-round function/hosted call without its required output.
				partialInput = append(partialInput, responsesEasyInputMessage("assistant", text))
			}
			partialRaw, _ := json.Marshal(partialInput[historyLen:])
			// Stop button / kill: preserve the partial (§6.2).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return &UnifiedResult{Blocks: partialBlocks, Raw: partialRaw, StopReason: "stopped", Usage: partialUsage, Citations: partialCitations, GeneratedImages: allGeneratedImages}, err
			}
			visible := providerVisibleOutputFromContext(ctx)
			if len(partialBlocks) > 0 || completedToolResult || len(partialCitations) > len(allCitations) || partialUsage.InputTokens > 0 || partialUsage.OutputTokens > 0 || (visible != nil && visible.Load()) {
				return &UnifiedResult{Blocks: partialBlocks, Raw: partialRaw, StopReason: "error", Usage: partialUsage, Citations: partialCitations, GeneratedImages: allGeneratedImages}, err
			}
			return nil, err
		}
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		allCitations = append(allCitations, citations...)
		// Persist the reasoning summary as a thinking block so it survives reload
		// (it was only streamed live before).
		if reasoning != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning})
		}
		if finalizing {
			if text != "" {
				allText.WriteString(text)
				allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
			}
			if len(calls) > 0 || len(hosted) > 0 || strings.TrimSpace(text) == "" {
				if text != "" {
					input = append(input, responsesEasyInputMessage("assistant", text))
				}
				raw, _ := json.Marshal(input[historyLen:])
				return &UnifiedResult{
					Blocks: allBlocks, Raw: raw, StopReason: toolFinalizationStopReason(finalizationSignal),
					Usage: usage, Citations: allCitations, GeneratedImages: allGeneratedImages,
				}, toolFinalizationError(finalizationSignal, errors.New("model did not return a tool-free final answer"))
			}
			if len(outputItems) > 0 {
				input = append(input, outputItems...)
			} else {
				input = append(input, responsesEasyInputMessage("assistant", text))
			}
			raw, _ := json.Marshal(input[historyLen:])
			return &UnifiedResult{
				Blocks: allBlocks, Raw: raw, StopReason: "end_turn",
				Usage: usage, Citations: allCitations, GeneratedImages: allGeneratedImages,
			}, nil
		}
		// Persist OpenAI-hosted tool rounds as tool_call blocks so reloads show
		// the same steps the user saw live (§2.3-B).
		for _, h := range hosted {
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: h.Name, ToolID: h.ID, Summary: h.Summary,
			})
		}
		if text != "" {
			allText.WriteString(text)
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		if len(outputItems) > 0 {
			input = append(input, outputItems...)
		} else if text != "" {
			// Compatibility fallback for OpenAI-compatible gateways that stream
			// deltas but omit response.completed.output.
			input = append(input, responsesEasyInputMessage("assistant", text))
		}

		if len(calls) == 0 {
			raw, _ := json.Marshal(input[historyLen:])
			return &UnifiedResult{
				Blocks:          allBlocks,
				Raw:             raw,
				StopReason:      "end_turn",
				Usage:           usage,
				Citations:       allCitations,
				GeneratedImages: allGeneratedImages,
			}, nil
		}

		// Insert the function_call items the model emitted (echo them back
		// alongside their outputs — required by the Responses protocol). Official
		// OpenAI responses include those items in response.output; keep this manual
		// path only for compatible gateways that omit the completed output list.
		if len(outputItems) == 0 || !responseOutputHasFunctionCalls(outputItems) {
			for _, c := range calls {
				input = append(input, map[string]any{
					"type":    "function_call",
					"call_id": c.ID,
					"name":    c.Name,
					"status":  "completed",
					// Responses requires `arguments` to be a JSON STRING. Passing
					// json.RawMessage serialises it as an OBJECT and the API rejects
					// it with "expected a string, got an object" on input[N].arguments.
					"arguments": string(c.Input),
				})
			}
		}

		// Execute tools concurrently, then feed function_call_output items.
		specs := make([]toolCallSpec, len(calls))
		for j, c := range calls {
			specs[j] = toolCallSpec{ID: c.ID, Name: c.Name, Input: c.Input}
		}
		results := runToolsConcurrent(ctx, tools, specs, onEvent)
		batchFinalizationErr := toolFinalizationErrorFromResults(results)
		for j, c := range calls {
			r := results[j]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = publicToolErrorOutput(r.Err)
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: c.Name, ID: c.ID, Summary: truncate(out, toolResultSummaryTruncationOpenAI), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: c.Name, ToolID: c.ID,
				Input: c.Input, Summary: truncate(out, toolResultSummaryTruncationOpenAI),
			})
			allBlocks = append(allBlocks, canonicalToolOutputBlock(c.Name, c.ID, out, status))
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": c.ID,
				"output":  out,
			})
		}
		if batchFinalizationErr != nil {
			finalizationPending = batchFinalizationErr
		} else if i+1 >= maxIter {
			finalizationPending = &ErrToolBudgetExceeded{Kind: "iterations", Limit: maxIter}
		}
	}
	raw, _ := json.Marshal(input[historyLen:])
	return &UnifiedResult{
		Blocks:          allBlocks,
		Raw:             raw,
		StopReason:      "max_iterations",
		Usage:           usage,
		Citations:       allCitations,
		GeneratedImages: allGeneratedImages,
	}, nil
}

func decodeHostedGeneratedImages(hosted []hostedToolCall) ([]hostedToolCall, []GeneratedImage, error) {
	images := make([]GeneratedImage, 0, len(hosted))
	var decodeErrors []error
	for i := range hosted {
		if hosted[i].Name != "image_generation" {
			continue
		}
		encoded := hosted[i].ImageBase64
		hosted[i].ImageBase64 = ""
		if encoded == "" {
			if strings.EqualFold(hosted[i].Status, "completed") {
				hosted[i].Summary = "The hosted image result was missing."
				decodeErrors = append(decodeErrors, fmt.Errorf("hosted image result %q was missing", hosted[i].ID))
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			hosted[i].Summary = "The hosted image result could not be decoded."
			decodeErrors = append(decodeErrors, fmt.Errorf("decode hosted image result %q: %w", hosted[i].ID, err))
			continue
		}
		if len(data) == 0 {
			hosted[i].Summary = "The hosted image result was empty."
			decodeErrors = append(decodeErrors, fmt.Errorf("hosted image result %q was empty", hosted[i].ID))
			continue
		}
		mimeType := providerImageMIMEFromBytes(data)
		if mimeType == "" {
			hosted[i].Summary = "The hosted image result had an unsupported format."
			decodeErrors = append(decodeErrors, fmt.Errorf("hosted image result %q had an unsupported format", hosted[i].ID))
			continue
		}
		hosted[i].Summary = mimeType
		images = append(images, GeneratedImage{Data: data, MimeType: mimeType, SourceID: hosted[i].ID})
	}
	return hosted, images, errors.Join(decodeErrors...)
}

// readOpenAIResponsesStream consumes the Responses SSE event stream. The event
// taxonomy is:
//   - response.output_text.delta — visible text delta (forward as text_delta)
//   - response.reasoning_summary_text.delta — reasoning summary delta (forward
//     as thinking_delta so the collapsed pane updates live)
//   - response.output_item.added (type=function_call) — start of a tool call
//   - response.function_call_arguments.delta — partial JSON for tool args
//   - response.completed — final response with usage + finalized items
//
// The function returns the joined visible text, the parsed function-call list,
// usage, and the finalized response.output items needed to continue stateless
// Responses tool loops.
func readOpenAIResponsesStream(body io.Reader, onEvent func(SseEvent)) (string, string, []openAIToolCall, []hostedToolCall, []Citation, Usage, []map[string]any, error) {
	scanner := newResponseLineScanner(body)
	text := strings.Builder{}
	reasoning := strings.Builder{}
	usage := Usage{}
	// Web-search citations: inline url_citation annotations + the sources the
	// hosted web_search_call consulted (via include). Deduped by URL, emitted
	// live, and returned for persistence.
	var citations []Citation
	seenCite := map[string]bool{}
	addCitation := func(url, title, snippet string) {
		url = strings.TrimSpace(url)
		if url == "" || seenCite[url] {
			return
		}
		seenCite[url] = true
		c := Citation{
			ID:      fmt.Sprintf("oac%d", len(citations)+1),
			Index:   len(citations) + 1,
			Title:   strings.TrimSpace(title),
			URL:     url,
			Snippet: strings.TrimSpace(snippet),
			Source:  "web",
		}
		citations = append(citations, c)
		onEvent(SseEvent{Type: "citation", Citation: &c})
	}
	callsByItem := map[string]*openAIResponseCallBuf{} // item_id → buffer
	order := []string{}
	hostedByItem := map[string]*hostedToolCall{} // item_id → hosted tool round
	hostedOrder := []string{}
	outputByItem := map[string]map[string]any{} // item_id → finalized output item
	outputOrder := []string{}
	completedOutput := []map[string]any{}
	ensureHosted := func(itemID, itemType string) *hostedToolCall {
		if itemID == "" || !strings.HasSuffix(itemType, "_call") || itemType == "function_call" {
			return nil
		}
		if existing := hostedByItem[itemID]; existing != nil {
			return existing
		}
		h := &hostedToolCall{ID: itemID, Name: hostedToolName(itemType), ImagePartialIndex: -1}
		hostedByItem[itemID] = h
		hostedOrder = append(hostedOrder, itemID)
		return h
	}
	captureHostedImage := func(item map[string]any) {
		itemID, _ := item["id"].(string)
		itemType, _ := item["type"].(string)
		h := ensureHosted(itemID, itemType)
		if h == nil || itemType != "image_generation_call" {
			return
		}
		if status, _ := item["status"].(string); status != "" {
			h.Status = status
		}
		if result, _ := item["result"].(string); result != "" {
			h.ImageBase64 = result
			h.ImagePartialIndex = int(^uint(0) >> 1)
		}
		// The durable artifact is the source of truth. Persisting this field would
		// duplicate several megabytes in messages.raw and every replay request.
		delete(item, "result")
	}
	sawEvent := false
	terminal := false
responseLoop:
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			terminal = true
			break
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			calls, hosted, outputItems := finalizeOpenAIResponsesStream(callsByItem, order, hostedByItem, hostedOrder, outputByItem, outputOrder, completedOutput)
			return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems,
				fmt.Errorf("openai responses stream invalid JSON: %w", err)
		}
		if streamErr := providerEventError("openai responses", ev); streamErr != nil {
			calls, hosted, outputItems := finalizeOpenAIResponsesStream(callsByItem, order, hostedByItem, hostedOrder, outputByItem, outputOrder, completedOutput)
			return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, streamErr
		}
		typ, _ := ev["type"].(string)
		switch typ {
		case "response.output_text.delta":
			sawEvent = true
			if s, _ := ev["delta"].(string); s != "" {
				text.WriteString(s)
				onEvent(SseEvent{Type: "text_delta", Text: s})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning.delta":
			sawEvent = true
			if s, _ := ev["delta"].(string); s != "" {
				reasoning.WriteString(s)
				onEvent(SseEvent{Type: "thinking_delta", Text: s})
			}
		case "response.output_text.annotation.added":
			sawEvent = true
			// Inline web-search citations the model attached to the answer text.
			if ann, _ := ev["annotation"].(map[string]any); ann != nil {
				if at, _ := ann["type"].(string); at == "url_citation" {
					url, _ := ann["url"].(string)
					title, _ := ann["title"].(string)
					addCitation(url, title, "")
				}
			}
		case "response.output_item.added":
			sawEvent = true
			it, _ := ev["item"].(map[string]any)
			if it == nil {
				continue
			}
			t, _ := it["type"].(string)
			if t == "function_call" {
				itemID, _ := it["id"].(string)
				callID, _ := it["call_id"].(string)
				name, _ := it["name"].(string)
				cb := &openAIResponseCallBuf{ID: callID, Name: name, Started: true}
				callsByItem[itemID] = cb
				order = append(order, itemID)
				outputOrder = append(outputOrder, itemID)
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: callID})
			} else if strings.HasSuffix(t, "_call") {
				// §2.3-B OpenAI-hosted tool round (web_search_call, …). OpenAI
				// runs it server-side; surface a live tool step to the UI.
				itemID, _ := it["id"].(string)
				h := ensureHosted(itemID, t)
				if h == nil {
					continue
				}
				outputOrder = append(outputOrder, itemID)
				onEvent(SseEvent{Type: "tool_start", Name: h.Name, ID: itemID})
			} else if itemID, _ := it["id"].(string); itemID != "" {
				outputOrder = append(outputOrder, itemID)
			}
		case "response.output_item.done":
			sawEvent = true
			it, _ := ev["item"].(map[string]any)
			if it == nil {
				continue
			}
			itemID, _ := it["id"].(string)
			captureHostedImage(it)
			if itemID != "" {
				outputByItem[itemID] = it
				outputOrder = append(outputOrder, itemID)
			}
			if t, _ := it["type"].(string); t == "function_call" {
				cb := callsByItem[itemID]
				if cb == nil {
					cb = &openAIResponseCallBuf{}
					callsByItem[itemID] = cb
					order = append(order, itemID)
				}
				if callID, _ := it["call_id"].(string); callID != "" {
					cb.ID = callID
				}
				if name, _ := it["name"].(string); name != "" {
					cb.Name = name
				}
				if args, _ := it["arguments"].(string); args != "" && cb.Args.Len() == 0 {
					cb.Args.WriteString(args)
				}
			}
			if h := hostedByItem[itemID]; h != nil {
				status := "complete"
				if s, _ := it["status"].(string); s != "" && s != "completed" {
					status = "error"
				}
				// Harvest the sources the web_search consulted (include=
				// web_search_call.action.sources) as citations.
				if action, _ := it["action"].(map[string]any); action != nil {
					if srcs, _ := action["sources"].([]any); srcs != nil {
						for _, s := range srcs {
							sm, _ := s.(map[string]any)
							if sm == nil {
								continue
							}
							url, _ := sm["url"].(string)
							title, _ := sm["title"].(string)
							addCitation(url, title, "")
						}
					}
				}
				onEvent(SseEvent{Type: "tool_result", Name: h.Name, ID: itemID, Status: status})
			}
		case "response.image_generation_call.partial_image":
			sawEvent = true
			itemID, _ := ev["item_id"].(string)
			h := ensureHosted(itemID, "image_generation_call")
			if h == nil {
				continue
			}
			partial, _ := ev["partial_image_b64"].(string)
			if partial == "" {
				partial, _ = ev["partial_image"].(string)
			}
			if partial == "" {
				partial, _ = ev["b64_json"].(string)
			}
			index := intOf(ev["partial_image_index"])
			if partial != "" && index >= h.ImagePartialIndex {
				h.ImageBase64 = partial
				h.ImagePartialIndex = index
			}
		case "response.function_call_arguments.delta":
			sawEvent = true
			itemID, _ := ev["item_id"].(string)
			cb := callsByItem[itemID]
			if cb == nil {
				continue
			}
			if d, _ := ev["delta"].(string); d != "" {
				cb.Args.WriteString(d)
				onEvent(SseEvent{Type: "tool_input", ID: cb.ID, Name: cb.Name, PartialJson: d})
			}
		case "response.function_call_arguments.done":
			sawEvent = true
			itemID, _ := ev["item_id"].(string)
			cb := callsByItem[itemID]
			if cb == nil {
				continue
			}
			if a, _ := ev["arguments"].(string); a != "" && cb.Args.Len() == 0 {
				cb.Args.WriteString(a)
			}
		case "response.completed":
			sawEvent = true
			terminal = true
			r, _ := ev["response"].(map[string]any)
			if r != nil {
				if u, ok := r["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["input_tokens"])
					usage.OutputTokens = intOf(u["output_tokens"])
				}
				if out, ok := r["output"].([]any); ok {
					completedOutput = completedOutput[:0]
					for _, raw := range out {
						if item, _ := raw.(map[string]any); item != nil {
							captureHostedImage(item)
							itemID, _ := item["id"].(string)
							if itemType, _ := item["type"].(string); itemType == "image_generation_call" {
								if h := hostedByItem[itemID]; h != nil && h.Status == "" {
									h.Status = "completed"
								}
							}
							completedOutput = append(completedOutput, item)
						}
					}
				}
			}
			// response.completed is terminal by protocol. Relays occasionally append
			// a bogus response.failed/error while closing; it must not replay a
			// completed (and potentially billable hosted-tool) request.
			break responseLoop
		case "response.failed":
			r, _ := ev["response"].(map[string]any)
			if r != nil {
				if errObj, ok := r["error"].(map[string]any); ok {
					msg, _ := errObj["message"].(string)
					calls, hosted, outputItems := finalizeOpenAIResponsesStream(callsByItem, order, hostedByItem, hostedOrder, outputByItem, outputOrder, completedOutput)
					return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, fmt.Errorf("openai responses error: %s", msg)
				}
			}
			calls, hosted, outputItems := finalizeOpenAIResponsesStream(callsByItem, order, hostedByItem, hostedOrder, outputByItem, outputOrder, completedOutput)
			return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, fmt.Errorf("openai responses failed")
		case "response.incomplete":
			// Incomplete is a valid terminal response (for example a configured
			// max-output limit), not a channel transport/protocol failure.
			sawEvent = true
			terminal = true
			break responseLoop
		}
	}
	calls, hosted, outputItems := finalizeOpenAIResponsesStream(callsByItem, order, hostedByItem, hostedOrder, outputByItem, outputOrder, completedOutput)
	if err := scanner.Err(); err != nil && !terminal {
		return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, err
	}
	if !sawEvent {
		return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, invalidProviderStream("openai responses", "empty response")
	}
	if !terminal {
		return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, invalidProviderStream("openai responses", "response ended before a terminal event")
	}
	return text.String(), reasoning.String(), calls, hosted, citations, usage, outputItems, nil
}

func finalizeOpenAIResponsesStream(
	callsByItem map[string]*openAIResponseCallBuf,
	order []string,
	hostedByItem map[string]*hostedToolCall,
	hostedOrder []string,
	outputByItem map[string]map[string]any,
	outputOrder []string,
	completedOutput []map[string]any,
) ([]openAIToolCall, []hostedToolCall, []map[string]any) {
	calls := []openAIToolCall{}
	for _, itemID := range order {
		cb := callsByItem[itemID]
		if cb == nil {
			continue
		}
		args := strings.TrimSpace(cb.Args.String())
		if args == "" {
			args = "{}"
		}
		calls = append(calls, openAIToolCall{ID: cb.ID, Name: cb.Name, Input: json.RawMessage(args)})
	}
	hosted := []hostedToolCall{}
	for _, itemID := range hostedOrder {
		if h := hostedByItem[itemID]; h != nil {
			hosted = append(hosted, *h)
		}
	}
	outputItems := completedOutput
	if len(outputItems) == 0 {
		seen := map[string]bool{}
		for _, itemID := range outputOrder {
			if itemID == "" || seen[itemID] {
				continue
			}
			seen[itemID] = true
			if item := outputByItem[itemID]; item != nil {
				outputItems = append(outputItems, item)
			}
		}
	}
	return calls, hosted, prepareResponsesReplayItems(outputItems)
}

// parseResponsesOutput is retained for callers that need a non-streaming JSON
// decode of a /v1/responses payload (e.g. tests, batch jobs). The streaming
// path uses readOpenAIResponsesStream instead.
func parseResponsesOutput(b []byte) (string, []openAIToolCall, Usage) {
	var parsed struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			CallID    string          `json:"call_id"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return "", nil, Usage{}
	}
	text := ""
	calls := []openAIToolCall{}
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			args := item.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			calls = append(calls, openAIToolCall{ID: id, Name: item.Name, Input: args})
		}
	}
	return text, calls, Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens}
}
