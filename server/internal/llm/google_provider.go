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

// GoogleProvider speaks the generateContent / streamGenerateContent endpoints
// at https://generativelanguage.googleapis.com/v1beta. Falls back to the mock
// provider when no key is configured.
type GoogleProvider struct {
	logger *log.Logger
}

// ID returns "google".
func (p *GoogleProvider) ID() string { return "google" }

// Stream runs one Gemini-style turn (currently using the non-streaming
// generateContent endpoint and emitting text in one event — simpler and
// compatible with Vertex AI, OpenAI-compatible gateways, and the official
// API. Tool calls are surfaced through the unified events.)
func (p *GoogleProvider) Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	if req.Model.APIKey == "" && req.Model.Fallback == nil {
		return nil, errors.New("this channel has no API key configured")
	}
	if !req.Model.Vision {
		req.History = stripImageBlocks(req.History)
	}
	// §4.13 prompt-mode: drive the text protocol loop.
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

	contents := historyToGemini(req.History, req.Model.Vision)
	var toolsDecl []map[string]any
	if len(req.Tools) > 0 {
		decls := []map[string]any{}
		for _, t := range req.Tools {
			decls = append(decls, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  normalizeGeminiFunctionSchema(t.InputSchema),
			})
		}
		// Canonical camelCase, NOT proto snake_case: Google itself accepts both,
		// but relay gateways (one-api/new-api 中转) re-parse the body into structs
		// tagged camelCase-only — "function_declarations" gets dropped there and an
		// empty tools[0] reaches Google, which 400s with "tool_type: required
		// one_of 'tool_type' must have one initialized field". Same rule for every
		// other key we emit (systemInstruction, inlineData, mimeType).
		toolsDecl = []map[string]any{{"functionDeclarations": decls}}
	}

	maxIter := envcfg.Int("AIVORY_LLM_MAX_ITER_4", 20)
	historyLen := len(contents)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	allCitations := []Citation{}
	totalUsage := Usage{}

	for i := 0; i < maxIter; i++ {
		// Gemini defaults maxOutputTokens to 8192 when the field is omitted,
		// silently truncating well below what current models actually support
		// (up to 64K+) — always send it explicitly (mirrors the Anthropic fix).
		maxTok := envcfg.Int("AIVORY_LLM_GEMINI_MAX_TOK", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]any{{"text": req.SystemPrompt}}},
			"contents":          contents,
			"generationConfig":  map[string]any{"maxOutputTokens": maxTok},
		}
		if toolsDecl != nil {
			body["tools"] = toolsDecl
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, toolsDecl != nil)
		body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		stripGoogleEndpointParams(body)
		raw, _ := json.Marshal(body)
		// §4.10-G stream: streamGenerateContent returns SSE-style JSON-array
		// chunks; we use alt=sse to get one event per line.
		// §B5: API key travels in the x-goog-api-key header, NOT the query string
		// (URLs leak into proxy/access logs, Referer, and error wrappers).
		var (
			text         string
			thinkingText string
			calls        []geminiCall
			modelParts   []map[string]any
			citations    []Citation
			u            Usage
		)
		err := doProviderParsedRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			streamURL := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", providerBaseURL(baseURL, "https://generativelanguage.googleapis.com"), req.Model.RequestID)
			hr, e := http.NewRequestWithContext(ctx, "POST", streamURL, bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			hr.Header.Set("x-goog-api-key", apiKey)
			return hr, nil
		}, func(resp *http.Response, emit func(SseEvent)) error {
			text, thinkingText, u = "", "", Usage{}
			calls, modelParts, citations = nil, nil, nil
			if statusErr := requireProviderSuccess(resp, "google"); statusErr != nil {
				return statusErr
			}
			var readErr error
			text, thinkingText, calls, modelParts, citations, u, readErr = readGeminiStream(resp.Body, emit)
			// Preserve usage for this exact channel attempt before a transparent
			// fallback resets the shared response variables.
			attachProviderRequestUsage(ctx, u)
			return readErr
		}, onEvent)
		if err != nil {
			partialBlocks := append([]UnifiedBlock{}, allBlocks...)
			if thinkingText != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
			}
			if text != "" {
				partialBlocks = append(partialBlocks, UnifiedBlock{Kind: "text", Text: text})
			}
			for _, call := range calls {
				partialBlocks = append(partialBlocks, UnifiedBlock{
					Kind: "tool_call", ToolName: call.Name, ToolID: call.Name, Input: call.Args,
				})
			}
			partialUsage := totalUsage
			partialUsage.InputTokens += u.InputTokens
			partialUsage.OutputTokens += u.OutputTokens
			partialUsage.CacheReadTokens += u.CacheReadTokens
			partialUsage.CacheWriteTokens += u.CacheWriteTokens
			partialCitations := mergeCitationsByURL(append([]Citation{}, allCitations...), citations)

			partialContents := append([]map[string]any{}, contents...)
			partialModelParts := make([]map[string]any, 0, len(modelParts))
			for _, part := range modelParts {
				if _, isToolCall := part["functionCall"]; !isToolCall {
					partialModelParts = append(partialModelParts, part)
				}
			}
			if len(partialModelParts) > 0 {
				// Do not persist a dangling functionCall in native replay. Its
				// canonical tool_call block remains visible after a refresh.
				partialContents = append(partialContents, map[string]any{"role": "model", "parts": partialModelParts})
			}
			partialRaw, _ := json.Marshal(partialContents[historyLen:])
			completedToolResult := len(extractCompactionRawToolOutputs(partialRaw)) > 0
			// Stop button / kill: preserve the partial (§6.2).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return &UnifiedResult{Blocks: partialBlocks, Raw: partialRaw, StopReason: "stopped", Usage: partialUsage, Citations: partialCitations}, err
			}
			visible := providerVisibleOutputFromContext(ctx)
			if len(partialBlocks) > 0 || completedToolResult || len(partialCitations) > len(allCitations) || usageHasValue(partialUsage) || (visible != nil && visible.Load()) {
				return &UnifiedResult{Blocks: partialBlocks, Raw: partialRaw, StopReason: "error", Usage: partialUsage, Citations: partialCitations}, err
			}
			return nil, err
		}
		if text != "" {
			allText.WriteString(text)
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		if thinkingText != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
		}
		totalUsage.InputTokens += u.InputTokens
		totalUsage.OutputTokens += u.OutputTokens
		allCitations = mergeCitationsByURL(allCitations, citations)

		// Append the model turn (text + any functionCall parts) to history.
		contents = append(contents, map[string]any{"role": "model", "parts": modelParts})

		if len(calls) == 0 {
			raw, _ := json.Marshal(contents[historyLen:])
			return &UnifiedResult{
				Blocks:     allBlocks,
				Raw:        raw,
				StopReason: "end_turn",
				Usage:      totalUsage,
				Citations:  allCitations,
			}, nil
		}

		// Execute the requested tools concurrently, then feed functionResponses.
		specs := make([]toolCallSpec, len(calls))
		for j, c := range calls {
			specs[j] = toolCallSpec{ID: c.Name, Name: c.Name, Input: c.Args}
		}
		results := runToolsConcurrent(ctx, tools, specs, onEvent)
		respParts := []map[string]any{}
		for j, c := range calls {
			r := results[j]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = publicToolErrorOutput(r.Err)
			}
			// §6.2 tool_result MUST include the upstream tool_use id so the UI
			// can pair the result with the in-flight tool_call card. For Gemini
			// the id is the function name (multiple calls to the same fn rare).
			onEvent(SseEvent{Type: "tool_result", Name: c.Name, ID: c.Name, Summary: truncate(out, 240), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: c.Name, ToolID: c.Name,
				Input: c.Args, Summary: truncate(out, 240),
			})
			allBlocks = append(allBlocks, canonicalToolOutputBlock(c.Name, c.Name, out, status))
			respParts = append(respParts, map[string]any{
				"functionResponse": map[string]any{
					"name":     c.Name,
					"response": map[string]any{"content": out},
				},
			})
		}
		contents = append(contents, map[string]any{"role": "user", "parts": respParts})
	}
	raw, _ := json.Marshal(contents[historyLen:])
	return &UnifiedResult{
		Blocks:     allBlocks,
		Raw:        raw,
		StopReason: "max_iterations",
		Usage:      totalUsage,
		Citations:  allCitations,
	}, nil
}

func mergeCitationsByURL(existing []Citation, incoming []Citation) []Citation {
	seen := make(map[string]bool, len(existing)+len(incoming))
	seenIDs := make(map[string]bool, len(existing)+len(incoming))
	for _, citation := range existing {
		if url := strings.TrimSpace(citation.URL); url != "" {
			seen[url] = true
		}
		if citation.ID != "" {
			seenIDs[citation.ID] = true
		}
	}
	for _, citation := range incoming {
		url := strings.TrimSpace(citation.URL)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		if citation.ID == "" || seenIDs[citation.ID] {
			citation.ID = fmt.Sprintf("cite%d", len(existing)+1)
		}
		seenIDs[citation.ID] = true
		citation.Index = len(existing) + 1
		existing = append(existing, citation)
	}
	return existing
}

// stripGoogleEndpointParams keeps endpoint-owned request identity and
// authentication out of the JSON payload. Gemini takes model from the URL and
// credentials from the x-goog-api-key header, so extra_params cannot override
// either by adding body-level aliases.
func stripGoogleEndpointParams(body map[string]any) {
	for _, key := range []string{"model", "key", "api_key", "apiKey", "x-goog-api-key"} {
		delete(body, key)
	}
}

func historyToGemini(h []UnifiedMessage, vision bool) []map[string]any {
	if !vision {
		h = stripImageBlocks(h)
	}
	contents := []map[string]any{}
	for _, m := range h {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		// Same-vendor raw replay (§2.3-C): stored model/user (functionResponse)
		// turns from the original Gemini exchange.
		if m.Role == "assistant" && len(m.Raw) > 2 && !isPromptToolRawEnvelope(m.Raw) {
			var turns []map[string]any
			if err := json.Unmarshal(m.Raw, &turns); err == nil && len(turns) > 0 && turns[0]["parts"] != nil {
				// Some Gemini thinking variants emit thoughtSignature as a
				// metadata-only part. It is useful when attached to a functionCall,
				// but a standalone signature does not initialize Part's data oneof
				// and Google rejects the next request with a parts[N].data 400.
				sanitizedTurns, validTurns := sanitizeGeminiRawTurns(turns)
				if validTurns {
					turns = sanitizedTurns
				}
				// Gemini 3 hard-rejects (400 "missing thought_signature in
				// functionCall parts") any replayed functionCall part that lacks its
				// thoughtSignature. Raw persisted before signature capture landed —
				// or stripped by a relay — carries bare calls; rather than poison
				// the whole request, fall through to the lossy-but-valid block→text
				// path below (the same downgrade used for cross-vendor history).
				if validTurns && geminiRawCallsAllSigned(turns) {
					contents = append(contents, turns...)
					continue
				}
			}
		}
		parts := []map[string]any{}
		for _, b := range m.Blocks {
			// inlineData (image) parts belong on the user role; a model turn takes
			// text/functionCall. Drop images that rode onto a non-user turn (share/fork
			// history) so Gemini doesn't reject the content.
			if vision && m.Role == "user" && b.Kind == "image" && b.Data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{"mimeType": b.MimeType, "data": b.Data},
				})
			}
		}
		if text := renderBlocksAsText(m.Blocks); text != "" {
			parts = append(parts, map[string]any{"text": text})
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": ""})
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	return contents
}

// geminiPartHasData reports whether a decoded Gemini Part initializes one of
// Google's data oneof fields. thought/thoughtSignature are metadata and do not
// count. A standalone signature part is invalid on replay and causes the
// opaque `parts[N].data required oneof` error from the Google API.
func geminiPartHasData(part map[string]any) bool {
	for _, key := range []string{
		"text", "inlineData", "inline_data", "fileData", "file_data",
		"functionCall", "function_call", "functionResponse", "function_response",
		"executableCode", "executable_code", "codeExecutionResult", "code_execution_result",
	} {
		if value, ok := part[key]; ok && value != nil {
			return true
		}
	}
	return false
}

// sanitizeGeminiRawTurns removes metadata-only parts from a decoded native
// history replay. The JSON shape remains []any so it is compatible with the
// raw wire format and geminiRawCallsAllSigned. Malformed turns are rejected and
// the caller falls back to canonical blocks instead of sending partial history.
func sanitizeGeminiRawTurns(turns []map[string]any) ([]map[string]any, bool) {
	sanitized := make([]map[string]any, 0, len(turns))
	for _, turn := range turns {
		rawParts, ok := turn["parts"].([]any)
		if !ok {
			return nil, false
		}
		parts := make([]any, 0, len(rawParts))
		for _, rawPart := range rawParts {
			part, ok := rawPart.(map[string]any)
			if !ok || !geminiPartHasData(part) {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			continue
		}
		copyTurn := make(map[string]any, len(turn)+1)
		for key, value := range turn {
			copyTurn[key] = value
		}
		copyTurn["parts"] = parts
		sanitized = append(sanitized, copyTurn)
	}
	return sanitized, len(sanitized) > 0
}

// geminiCall is one Gemini functionCall request parsed from the stream.
type geminiCall struct {
	Name string
	Args json.RawMessage
}

// geminiSkipSigSentinel is Google's documented placeholder thoughtSignature for
// functionCall parts that have no genuine signature (history transferred from a
// model/store that never produced one, or a relay that stripped it). It tells
// the upstream to skip signature validation for that part. Google warns it
// degrades model performance, so it is a last-resort fallback only — never used
// when a real signature is available. Value is
// base64("skip_thought_signature_validator"), matching the proven LiteLLM/Vertex
// behaviour.
const geminiSkipSigSentinel = "c2tpcF90aG91Z2h0X3NpZ25hdHVyZV92YWxpZGF0b3I="

// geminiFunctionCallPart rebuilds a model `parts[]` entry for a functionCall so
// it can be replayed as history. Critically it carries the part-level
// `thoughtSignature` (a sibling of `functionCall`, NOT a field inside it) that
// Gemini emits when thinking is enabled. That signature MUST be echoed back on
// the functionCall part in the next request or the upstream rejects the tool
// turn with 400 "Function call is missing a thought_signature in functionCall
// parts." We copy it under whatever key the upstream used (REST camelCase or
// proto snake_case) to stay robust across gateways.
func geminiFunctionCallPart(part, fc map[string]any, fallbackSig string) map[string]any {
	out := map[string]any{"functionCall": fc}
	sig := geminiPartSig(part)
	if sig == "" {
		// Gemini 3 sometimes attaches the signature to the preceding thought part
		// (or, in streaming, an earlier chunk) rather than the functionCall part
		// itself. Fall back to the most recent signature seen this turn so the
		// replayed functionCall never goes back bare (→ 400 "missing
		// thought_signature in functionCall parts").
		sig = fallbackSig
	}
	if sig == "" {
		// Last resort: the model produced this call but no signature ever reached
		// us this turn (thinking off, or a relay stripped the field). A bare
		// functionCall hard-400s on Gemini 3, so emit the documented bypass
		// sentinel instead — it keeps the tool loop alive at the cost of lost
		// reasoning context. Not hit on a direct connection to a thinking-capable
		// model, where every functionCall carries a real signature.
		sig = geminiSkipSigSentinel
	}
	out["thoughtSignature"] = sig
	return out
}

// geminiPartSig returns a part's thought signature under either the REST
// camelCase or proto snake_case key. Empty when absent.
func geminiPartSig(part map[string]any) string {
	for _, k := range []string{"thoughtSignature", "thought_signature"} {
		if s, ok := part[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// geminiRawCallsAllSigned reports whether every functionCall part across a set
// of replayed `contents` turns carries a non-empty thoughtSignature. Gemini 3
// hard-rejects any history turn whose functionCall part is bare, so the caller
// uses this to choose between verbatim raw replay and the lossy block→text
// downgrade. Turns with no functionCall parts trivially pass.
func geminiRawCallsAllSigned(turns []map[string]any) bool {
	for _, t := range turns {
		parts, _ := t["parts"].([]any)
		for _, pr := range parts {
			prm, ok := pr.(map[string]any)
			if !ok {
				continue
			}
			if _, hasCall := prm["functionCall"]; hasCall && geminiPartSig(prm) == "" {
				return false
			}
		}
	}
	return true
}

// readGeminiStream consumes the streamGenerateContent SSE response. Each line
// of `data:` carries one GenerateContentResponse fragment. We accumulate
//   - visible text (parts[].text where thought!=true)
//   - thinking text (parts[].thought_summary / parts[].text where thought==true)
//   - functionCall items (parts[].functionCall)
//   - candidate.groundingMetadata web sources from provider-hosted Google Search
//   - usageMetadata (cumulative metadata that may appear on multiple chunks)
//
// and emit text_delta / thinking_delta as they arrive so the UI updates live.
// Returns: (visible text, thinking text, function calls, raw model parts,
// citations, usage).
func readGeminiStream(body io.Reader, onEvent func(SseEvent)) (string, string, []geminiCall, []map[string]any, []Citation, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	text := strings.Builder{}
	thinking := strings.Builder{}
	calls := []geminiCall{}
	modelParts := []map[string]any{}
	citations := []Citation{}
	seenCitations := map[string]bool{}
	addGroundingCitations := func(metadata map[string]any) {
		chunks, _ := metadata["groundingChunks"].([]any)
		for _, chunk := range chunks {
			chunkMap, _ := chunk.(map[string]any)
			web, _ := chunkMap["web"].(map[string]any)
			url, _ := web["uri"].(string)
			url = strings.TrimSpace(url)
			if url == "" || seenCitations[url] {
				continue
			}
			seenCitations[url] = true
			title, _ := web["title"].(string)
			citation := Citation{
				ID: fmt.Sprintf("gmc%d", len(citations)+1), Index: len(citations) + 1,
				Title: strings.TrimSpace(title), URL: url, Source: "web",
			}
			citations = append(citations, citation)
			onEvent(SseEvent{Type: "citation", Citation: &citation})
		}
	}
	usage := Usage{}
	sawEvent := false
	terminal := false
	// Most recent thought signature seen this turn — Gemini 3 may attach it to a
	// thought part (or an earlier streaming chunk) instead of the functionCall
	// part. We carry it forward so every replayed functionCall keeps a signature.
	lastSig := ""
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
		// A completed candidate may be followed by a usage-only frame. Consume that
		// accounting while ignoring unrelated gateway noise after the model has
		// already reached a semantic terminal state.
		if terminal {
			var trailer map[string]any
			if json.Unmarshal([]byte(payload), &trailer) == nil {
				if u, ok := trailer["usageMetadata"].(map[string]any); ok {
					usage.InputTokens = intOf(u["promptTokenCount"])
					usage.OutputTokens = intOf(u["candidatesTokenCount"])
				}
			}
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			return text.String(), thinking.String(), calls, modelParts, citations, usage,
				fmt.Errorf("google stream invalid JSON: %w", err)
		}
		if streamErr := providerEventError("google", parsed); streamErr != nil {
			return text.String(), thinking.String(), calls, modelParts, citations, usage, streamErr
		}
		cs, _ := parsed["candidates"].([]any)
		if len(cs) > 0 {
			sawEvent = true
		}
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			if grounding, _ := cm["groundingMetadata"].(map[string]any); grounding != nil {
				addGroundingCitations(grounding)
			}
			if finishReason, _ := cm["finishReason"].(string); finishReason != "" {
				terminal = true
			}
			content, _ := cm["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, pr := range parts {
				prm, _ := pr.(map[string]any)
				handled := false
				if sig := geminiPartSig(prm); sig != "" {
					lastSig = sig
				}
				isThought, _ := prm["thought"].(bool)
				if t, _ := prm["text"].(string); t != "" {
					handled = true
					if isThought {
						thinking.WriteString(t)
						onEvent(SseEvent{Type: "thinking_delta", Text: t})
						tp := map[string]any{"text": t, "thought": true}
						if sig := geminiPartSig(prm); sig != "" {
							tp["thoughtSignature"] = sig
						}
						modelParts = append(modelParts, tp)
					} else {
						text.WriteString(t)
						onEvent(SseEvent{Type: "text_delta", Text: t})
						modelParts = append(modelParts, map[string]any{"text": t})
					}
				}
				// Gemini also exposes thought_summary in some preview variants.
				if ts, _ := prm["thought_summary"].(string); ts != "" {
					thinking.WriteString(ts)
					onEvent(SseEvent{Type: "thinking_delta", Text: ts})
				}
				if fc, ok := prm["functionCall"].(map[string]any); ok {
					handled = true
					name, _ := fc["name"].(string)
					args, _ := json.Marshal(fc["args"])
					if len(args) == 0 || string(args) == "null" {
						args = json.RawMessage("{}")
					}
					calls = append(calls, geminiCall{Name: name, Args: args})
					modelParts = append(modelParts, geminiFunctionCallPart(prm, fc, lastSig))
					onEvent(SseEvent{Type: "tool_start", Name: name, ID: name})
					onEvent(SseEvent{Type: "tool_input", Name: name, ID: name, PartialJson: string(args)})
				}
				// Provider-hosted Gemini tools use their own part types (for example
				// executableCode/codeExecutionResult), never functionCall. Preserve
				// those parts for same-provider replay without adding them to calls,
				// which is the client Function list executed by Aivory below.
				if !handled && len(prm) > 0 && geminiPartHasData(prm) {
					modelParts = append(modelParts, prm)
				}
			}
		}
		if u, ok := parsed["usageMetadata"].(map[string]any); ok {
			sawEvent = true
			usage.InputTokens = intOf(u["promptTokenCount"])
			usage.OutputTokens = intOf(u["candidatesTokenCount"])
		}
		if feedback, ok := parsed["promptFeedback"].(map[string]any); ok {
			sawEvent = true
			// promptFeedback can carry non-blocking safety ratings. Only an explicit
			// blockReason is a terminal model decision.
			if blockReason, _ := feedback["blockReason"].(string); strings.TrimSpace(blockReason) != "" {
				terminal = true
			}
		}
	}
	if err := scanner.Err(); err != nil && !terminal {
		return text.String(), thinking.String(), calls, modelParts, citations, usage, err
	}
	if !sawEvent {
		return text.String(), thinking.String(), calls, modelParts, citations, usage, invalidProviderStream("google", "empty response")
	}
	if !terminal {
		return text.String(), thinking.String(), calls, modelParts, citations, usage, invalidProviderStream("google", "response ended before a terminal event")
	}
	if len(modelParts) == 0 {
		modelParts = append(modelParts, map[string]any{"text": ""})
	}
	return text.String(), thinking.String(), calls, modelParts, citations, usage, nil
}

// parseGeminiCandidate extracts visible text, functionCall requests, and the
// raw model parts (to replay as history) from a generateContent response.
func parseGeminiCandidate(parsed map[string]any) (string, []geminiCall, []map[string]any) {
	text := ""
	calls := []geminiCall{}
	modelParts := []map[string]any{}
	candSig := ""
	cs, _ := parsed["candidates"].([]any)
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		content, _ := cm["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, pr := range parts {
			prm, _ := pr.(map[string]any)
			handled := false
			if sig := geminiPartSig(prm); sig != "" {
				candSig = sig
			}
			if t, _ := prm["text"].(string); t != "" {
				handled = true
				text += t
				modelParts = append(modelParts, map[string]any{"text": t})
			}
			if fc, ok := prm["functionCall"].(map[string]any); ok {
				handled = true
				name, _ := fc["name"].(string)
				args, _ := json.Marshal(fc["args"])
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				calls = append(calls, geminiCall{Name: name, Args: args})
				modelParts = append(modelParts, geminiFunctionCallPart(prm, fc, candSig))
			}
			if !handled && len(prm) > 0 && geminiPartHasData(prm) {
				modelParts = append(modelParts, prm)
			}
		}
	}
	if len(modelParts) == 0 {
		modelParts = append(modelParts, map[string]any{"text": ""})
	}
	return text, calls, modelParts
}

// promptRunOnce returns a PromptToolRunner performing ONE generateContent call
// (stop sequence on </tool_call>) for §4.13 prompt-mode.
func (p *GoogleProvider) promptRunOnce(req UnifiedChatRequest) PromptToolRunner {
	return func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error) {
		if len(req.OfficialToolRequests) > 0 {
			round := req
			round.SystemPrompt = system
			round.History = history
			round.Tools = nil
			round.ToolModePrompt = false
			round.ExtraParams = withPromptStopSequence(req.ExtraParams, map[string]any{
				"generationConfig": map[string]any{
					"stopSequences": []string{PromptToolStopSequence()},
				},
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

		contents := []map[string]any{}
		for _, m := range history {
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			parts := []map[string]any{}
			for _, b := range m.Blocks {
				if req.Model.Vision && m.Role == "user" && b.Kind == "image" && b.Data != "" {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{"mimeType": b.MimeType, "data": b.Data},
					})
				}
				if b.Kind == "text" {
					parts = append(parts, map[string]any{"text": b.Text})
				}
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}
			contents = append(contents, map[string]any{"role": role, "parts": parts})
		}
		gc := map[string]any{"stopSequences": []string{PromptToolStopSequence()}}
		// Gemini defaults maxOutputTokens to 8192 when omitted — always send it
		// explicitly (mirrors the Anthropic fix and the main Stream() path above).
		maxTok := envcfg.Int("AIVORY_LLM_GEMINI_MAX_TOK_2", 64000)
		if req.MaxOutputTokens > 0 {
			maxTok = req.MaxOutputTokens
		}
		gc["maxOutputTokens"] = maxTok
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]any{{"text": system}}},
			"contents":          contents,
			"generationConfig":  gc,
		}
		body = MergeRequestParams(body, req.ExtraParams, req.ParamControls, req.ParamOverrides)
		body = StripToolFields(body, false)
		body = MergeOfficialToolRequests(body, req.OfficialToolRequests)
		stripGoogleEndpointParams(body)
		raw, _ := json.Marshal(body)
		var (
			text  string
			usage Usage
		)
		err := doProviderParsedRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", providerBaseURL(baseURL, "https://generativelanguage.googleapis.com"), req.Model.RequestID)
			hr, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("x-goog-api-key", apiKey) // §B5: key in header, not URL
			return hr, nil
		}, func(resp *http.Response, _ func(SseEvent)) error {
			text, usage = "", Usage{}
			if statusErr := requireProviderSuccess(resp, "google"); statusErr != nil {
				return statusErr
			}
			respBytes, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return readErr
			}
			var parsed map[string]any
			if parseErr := json.Unmarshal(respBytes, &parsed); parseErr != nil {
				return parseErr
			}
			if streamErr := providerEventError("google", parsed); streamErr != nil {
				return streamErr
			}
			text = ""
			cs, hasCandidates := parsed["candidates"].([]any)
			if hasCandidates {
				for _, c := range cs {
					cm, _ := c.(map[string]any)
					content, _ := cm["content"].(map[string]any)
					parts, _ := content["parts"].([]any)
					for _, pr := range parts {
						prm, _ := pr.(map[string]any)
						if t, _ := prm["text"].(string); t != "" {
							text += t
						}
					}
				}
			}
			usage = Usage{}
			if u, ok := parsed["usageMetadata"].(map[string]any); ok {
				usage.InputTokens = intOf(u["promptTokenCount"])
				usage.OutputTokens = intOf(u["candidatesTokenCount"])
			}
			if (!hasCandidates || len(cs) == 0) && parsed["promptFeedback"] == nil {
				return invalidProviderStream("google", "response contained no candidates")
			}
			return nil
		}, func(SseEvent) {})
		return PromptToolRound{Text: text, Usage: usage}, err
	}
}
