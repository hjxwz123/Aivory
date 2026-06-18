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
	base := strings.TrimRight(req.Model.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}

	// §4.13 prompt-mode: drive the text protocol loop.
	if req.ToolModePrompt {
		_, blocks, usage, cites, err := RunPromptToolLoop(
			ctx, req.SystemPrompt, req.History, req.Tools,
			p.promptRunOnce(base, req), tools, onEvent,
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
		toolsDecl = []map[string]any{{"function_declarations": decls}}
	}

	const maxIter = 20
	historyLen := len(contents)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	totalUsage := Usage{}

	for i := 0; i < maxIter; i++ {
		body := map[string]any{
			"system_instruction": map[string]any{"parts": []map[string]any{{"text": req.SystemPrompt}}},
			"contents":           contents,
		}
		if req.MaxOutputTokens > 0 {
			body["generationConfig"] = map[string]any{"maxOutputTokens": req.MaxOutputTokens}
		}
		if toolsDecl != nil {
			body["tools"] = toolsDecl
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		// Request thought summaries by default on thinking-capable models so the
		// chain-of-thought streams (official format:
		// generationConfig.thinkingConfig.includeThoughts). param_controls can
		// override the budget or disable it. Older models reject thinkingConfig, so
		// it's gated to the families that support it.
		if geminiSupportsThinking(req.Model.RequestID) {
			gc, ok := body["generationConfig"].(map[string]any)
			if !ok {
				gc = map[string]any{}
				body["generationConfig"] = gc
			}
			if _, has := gc["thinkingConfig"]; !has {
				gc["thinkingConfig"] = map[string]any{"includeThoughts": true}
			}
		}
		raw, _ := json.Marshal(body)
		// §4.10-G stream: streamGenerateContent returns SSE-style JSON-array
		// chunks; we use alt=sse to get one event per line.
		// §B5: API key travels in the x-goog-api-key header, NOT the query string
		// (URLs leak into proxy/access logs, Referer, and error wrappers).
		streamURL := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", base, req.Model.RequestID)
		httpReq, _ := http.NewRequestWithContext(ctx, "POST", streamURL, bytes.NewReader(raw))
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("accept", "text/event-stream")
		httpReq.Header.Set("x-goog-api-key", req.Model.APIKey)
		resp, err := providerHTTPClient.Do(httpReq)
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
			onEvent(SseEvent{Type: "tool_result", Name: c.Name, ID: c.Name, Summary: truncate(out, 240), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: c.Name, ToolID: c.Name,
				Input: c.Args, Summary: truncate(out, 240),
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
				contents = append(contents, turns...)
				continue
			}
		}
		parts := []map[string]any{}
		for _, b := range m.Blocks {
			if b.Kind == "image" && b.Data != "" {
				parts = append(parts, map[string]any{
					"inline_data": map[string]any{"mime_type": b.MimeType, "data": b.Data},
				})
			}
			// PDF document blocks: Gemini accepts them as inline_data with
			// mime_type=application/pdf (§4.10-G doc capability).
			if b.Kind == "document" && b.Data != "" {
				parts = append(parts, map[string]any{
					"inline_data": map[string]any{"mime_type": b.MimeType, "data": b.Data},
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

// geminiSupportsThinking reports whether the model's request id is a
// thinking-capable Gemini (2.5 series and newer). Older models reject
// generationConfig.thinkingConfig, so we only default thought summaries on for
// the families that accept it.
func geminiSupportsThinking(requestID string) bool {
	id := strings.ToLower(requestID)
	for _, p := range []string{"2.5", "2-5", "gemini-3", "gemini-4"} {
		if strings.Contains(id, p) {
			return true
		}
	}
	return false
}

// geminiFunctionCallPart rebuilds a model `parts[]` entry for a functionCall so
// it can be replayed as history. Critically it carries the part-level
// `thoughtSignature` (a sibling of `functionCall`, NOT a field inside it) that
// Gemini emits when thinking is enabled. That signature MUST be echoed back on
// the functionCall part in the next request or the upstream rejects the tool
// turn with 400 "Function call is missing a thought_signature in functionCall
// parts." We copy it under whatever key the upstream used (REST camelCase or
// proto snake_case) to stay robust across gateways.
func geminiFunctionCallPart(part, fc map[string]any) map[string]any {
	out := map[string]any{"functionCall": fc}
	for _, k := range []string{"thoughtSignature", "thought_signature"} {
		if v, ok := part[k]; ok {
			out[k] = v
		}
	}
	return out
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
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	text := strings.Builder{}
	thinking := strings.Builder{}
	calls := []geminiCall{}
	modelParts := []map[string]any{}
	usage := Usage{}
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
				isThought, _ := prm["thought"].(bool)
				if t, _ := prm["text"].(string); t != "" {
					if isThought {
						thinking.WriteString(t)
						onEvent(SseEvent{Type: "thinking_delta", Text: t})
						modelParts = append(modelParts, map[string]any{"text": t, "thought": true})
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
					modelParts = append(modelParts, geminiFunctionCallPart(prm, fc))
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
	cs, _ := parsed["candidates"].([]any)
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		content, _ := cm["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, pr := range parts {
			prm, _ := pr.(map[string]any)
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
				modelParts = append(modelParts, geminiFunctionCallPart(prm, fc))
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
func (p *GoogleProvider) promptRunOnce(base string, req UnifiedChatRequest) PromptToolRunner {
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
			"system_instruction": map[string]any{"parts": []map[string]any{{"text": system}}},
			"contents":           contents,
			"generationConfig":   gc,
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", base, req.Model.RequestID)
		httpReq, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("x-goog-api-key", req.Model.APIKey) // §B5: key in header, not URL
		resp, err := providerHTTPClient.Do(httpReq)
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
