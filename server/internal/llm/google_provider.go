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

	"aurelia/server/internal/envcfg"
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
	if req.Model.APIKey == "" {
		return nil, errors.New("this channel has no API key configured")
	}
	// §4.13 prompt-mode: drive the text protocol loop.
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

	contents := historyToGemini(req.History)
	var toolsDecl []map[string]any
	if len(req.Tools) > 0 {
		decls := []map[string]any{}
		for _, t := range req.Tools {
			decls = append(decls, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(t.InputSchema),
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

	maxIter := envcfg.Int("AURELIA_LLM_MAX_ITER_4", 20)
	historyLen := len(contents)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	totalUsage := Usage{}

	for i := 0; i < maxIter; i++ {
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]any{{"text": req.SystemPrompt}}},
			"contents":          contents,
		}
		if req.MaxOutputTokens > 0 {
			body["generationConfig"] = map[string]any{"maxOutputTokens": req.MaxOutputTokens}
		}
		if toolsDecl != nil {
			body["tools"] = toolsDecl
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		// §4.10-G stream: streamGenerateContent returns SSE-style JSON-array
		// chunks; we use alt=sse to get one event per line.
		// §B5: API key travels in the x-goog-api-key header, NOT the query string
		// (URLs leak into proxy/access logs, Referer, and error wrappers).
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			streamURL := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", providerBaseURL(baseURL, "https://generativelanguage.googleapis.com"), req.Model.RequestID)
			hr, e := http.NewRequestWithContext(ctx, "POST", streamURL, bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			hr.Header.Set("x-goog-api-key", apiKey)
			return hr, nil
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			respBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("google %d: %s", resp.StatusCode, string(respBytes))
		}

		text, thinkingText, calls, modelParts, u, err := readGeminiStream(resp.Body, onEvent)
		resp.Body.Close()
		if err != nil {
			// Stop button / kill: preserve the partial (§6.2).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if thinkingText != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: thinkingText})
				}
				if text != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
				}
				return &UnifiedResult{Blocks: allBlocks, StopReason: "stopped", Usage: totalUsage}, err
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

		// Append the model turn (text + any functionCall parts) to history.
		contents = append(contents, map[string]any{"role": "model", "parts": modelParts})

		if len(calls) == 0 {
			raw, _ := json.Marshal(contents[historyLen:])
			return &UnifiedResult{
				Blocks:     allBlocks,
				Raw:        raw,
				StopReason: "end_turn",
				Usage:      totalUsage,
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
				out = "Error: " + r.Err.Error()
			}
			// §6.2 tool_result MUST include the upstream tool_use id so the UI
			// can pair the result with the in-flight tool_call card. For Gemini
			// the id is the function name (multiple calls to the same fn rare).
			onEvent(SseEvent{Type: "tool_result", Name: c.Name, ID: c.Name, Summary: truncate(out, envcfg.Int("AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_GEMINI", 240)), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: c.Name, ToolID: c.Name,
				Input: c.Args, Summary: truncate(out, envcfg.Int("AURELIA_LLM_TOOL_RESULT_SUMMARY_TRUNCATION_GEMINI", 240)),
			})
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
	}, nil
}

func historyToGemini(h []UnifiedMessage) []map[string]any {
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
		if m.Role == "assistant" && len(m.Raw) > 2 {
			var turns []map[string]any
			if err := json.Unmarshal(m.Raw, &turns); err == nil && len(turns) > 0 && turns[0]["parts"] != nil {
				// Gemini 3 hard-rejects (400 "missing thought_signature in
				// functionCall parts") any replayed functionCall part that lacks its
				// thoughtSignature. Raw persisted before signature capture landed —
				// or stripped by a relay — carries bare calls; rather than poison
				// the whole request, fall through to the lossy-but-valid block→text
				// path below (the same downgrade used for cross-vendor history).
				if geminiRawCallsAllSigned(turns) {
					contents = append(contents, turns...)
					continue
				}
			}
		}
		parts := []map[string]any{}
		for _, b := range m.Blocks {
			if b.Kind == "image" && b.Data != "" {
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
//   - usageMetadata (only present in the final chunk)
//
// and emit text_delta / thinking_delta as they arrive so the UI updates live.
// Returns: (visible text, thinking text, function calls, raw model parts, usage).
func readGeminiStream(body io.Reader, onEvent func(SseEvent)) (string, string, []geminiCall, []map[string]any, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, envcfg.Int("AURELIA_LLM_READ_GEMINI_STREAM_INIT", 64*1024)), envcfg.Int("AURELIA_LLM_READ_GEMINI_STREAM_MAX", 1024*1024))
	text := strings.Builder{}
	thinking := strings.Builder{}
	calls := []geminiCall{}
	modelParts := []map[string]any{}
	usage := Usage{}
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
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			continue
		}
		cs, _ := parsed["candidates"].([]any)
		for _, c := range cs {
			cm, _ := c.(map[string]any)
			content, _ := cm["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, pr := range parts {
				prm, _ := pr.(map[string]any)
				if sig := geminiPartSig(prm); sig != "" {
					lastSig = sig
				}
				isThought, _ := prm["thought"].(bool)
				if t, _ := prm["text"].(string); t != "" {
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
			}
		}
		if u, ok := parsed["usageMetadata"].(map[string]any); ok {
			usage.InputTokens = intOf(u["promptTokenCount"])
			usage.OutputTokens = intOf(u["candidatesTokenCount"])
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), thinking.String(), calls, modelParts, usage, err
	}
	if len(modelParts) == 0 {
		modelParts = append(modelParts, map[string]any{"text": ""})
	}
	return text.String(), thinking.String(), calls, modelParts, usage, nil
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
			if sig := geminiPartSig(prm); sig != "" {
				candSig = sig
			}
			if t, _ := prm["text"].(string); t != "" {
				text += t
				modelParts = append(modelParts, map[string]any{"text": t})
			}
			if fc, ok := prm["functionCall"].(map[string]any); ok {
				name, _ := fc["name"].(string)
				args, _ := json.Marshal(fc["args"])
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				calls = append(calls, geminiCall{Name: name, Args: args})
				modelParts = append(modelParts, geminiFunctionCallPart(prm, fc, candSig))
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
	return func(ctx context.Context, history []UnifiedMessage, system string) (string, Usage, error) {
		contents := []map[string]any{}
		for _, m := range history {
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			parts := []map[string]any{}
			for _, b := range m.Blocks {
				if b.Kind == "text" {
					parts = append(parts, map[string]any{"text": b.Text})
				}
			}
			contents = append(contents, map[string]any{"role": role, "parts": parts})
		}
		gc := map[string]any{"stopSequences": []string{PromptToolStopSequence()}}
		if req.MaxOutputTokens > 0 {
			gc["maxOutputTokens"] = req.MaxOutputTokens
		}
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]any{{"text": system}}},
			"contents":          contents,
			"generationConfig":  gc,
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", providerBaseURL(baseURL, "https://generativelanguage.googleapis.com"), req.Model.RequestID)
			hr, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("x-goog-api-key", apiKey) // §B5: key in header, not URL
			return hr, nil
		})
		if err != nil {
			return "", Usage{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return "", Usage{}, fmt.Errorf("google %d: %s", resp.StatusCode, string(b))
		}
		respBytes, _ := io.ReadAll(resp.Body)
		var parsed map[string]any
		if err := json.Unmarshal(respBytes, &parsed); err != nil {
			return "", Usage{}, err
		}
		text := ""
		if cs, ok := parsed["candidates"].([]any); ok {
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
		usage := Usage{}
		if u, ok := parsed["usageMetadata"].(map[string]any); ok {
			usage.InputTokens = intOf(u["promptTokenCount"])
			usage.OutputTokens = intOf(u["candidatesTokenCount"])
		}
		return text, usage, nil
	}
}
