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

// Stream runs one model turn against either OpenAI format.
func (p *OpenAIProvider) Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	if req.Model.APIKey == "" {
		return nil, errors.New("this channel has no API key configured")
	}
	switch req.Model.APIFormat {
	case "responses":
		return p.streamResponses(ctx, req, tools, onEvent)
	default:
		return p.streamChat(ctx, req, tools, onEvent)
	}
}

func (p *OpenAIProvider) streamChat(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	// §4.13 prompt-mode: no native function calling — drive the text protocol.
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

	messages := []map[string]any{}
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.History {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		// Same-vendor raw replay (§2.3-C).
		if m.Role == "assistant" && len(m.Raw) > 2 {
			var turns []map[string]any
			if err := json.Unmarshal(m.Raw, &turns); err == nil && len(turns) > 0 && turns[0]["role"] != nil {
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
			if b.Kind == "image" && b.Data != "" {
				imgParts = append(imgParts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + b.MimeType + ";base64," + b.Data},
				})
			}
		}
		if len(imgParts) > 0 {
			content := append([]map[string]any{{"type": "text", "text": text}}, imgParts...)
			messages = append(messages, map[string]any{"role": m.Role, "content": content})
		} else {
			messages = append(messages, map[string]any{"role": m.Role, "content": text})
		}
	}

	const maxIter = 20
	historyLen := len(messages)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	allCitations := []Citation{}
	usage := Usage{}

	for i := 0; i < maxIter; i++ {
		body := map[string]any{
			"model":    req.Model.RequestID,
			"messages": messages,
			"stream":   true,
		}
		if req.MaxOutputTokens > 0 {
			body["max_tokens"] = req.MaxOutputTokens
		}
		if len(req.Tools) > 0 && !req.ToolModePrompt {
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
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.openai.com")+"/v1/chat/completions", bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("authorization", "Bearer "+apiKey)
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("openai %d: %s", resp.StatusCode, string(b))
		}
		text, reasoning, calls, finish, u, err := readOpenAIChatStream(resp.Body, onEvent)
		resp.Body.Close()
		if err != nil {
			// Stop button / kill: preserve what streamed so far (§6.2) instead of
			// blanking the message.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if reasoning != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning})
				}
				if text != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
				}
				raw, _ := json.Marshal(messages[historyLen:])
				return &UnifiedResult{Blocks: allBlocks, Raw: raw, StopReason: "stopped", Usage: usage, Citations: allCitations}, err
			}
			return nil, err
		}
		allText.WriteString(text)
		// Thinking precedes the round's text so the reasoning trace reads
		// think → answer/tool in order.
		if reasoning != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning})
		}
		if text != "" {
			allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
		}
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens

		assistant := map[string]any{"role": "assistant", "content": text}
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
		for i, tc := range calls {
			r := results[i]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = "Error: " + r.Err.Error()
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: tc.Name, ID: tc.ID, Summary: truncate(out, 240), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: tc.Name, ToolID: tc.ID,
				Input: tc.Input, Summary: truncate(out, 240),
			})
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"content":      out,
			})
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
	return func(ctx context.Context, history []UnifiedMessage, system string) (string, Usage, error) {
		messages := []map[string]any{}
		if system != "" {
			messages = append(messages, map[string]any{"role": "system", "content": system})
		}
		for _, m := range history {
			if m.Role != "user" && m.Role != "assistant" {
				continue
			}
			text := strings.Builder{}
			for _, b := range m.Blocks {
				if b.Kind == "text" {
					text.WriteString(b.Text)
					text.WriteString("\n")
				}
			}
			messages = append(messages, map[string]any{"role": m.Role, "content": strings.TrimRight(text.String(), "\n")})
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
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.openai.com")+"/v1/chat/completions", bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("authorization", "Bearer "+apiKey)
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		})
		if err != nil {
			return "", Usage{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return "", Usage{}, fmt.Errorf("openai %d: %s", resp.StatusCode, string(b))
		}
		text, _, _, _, u, err := readOpenAIChatStream(resp.Body, func(SseEvent) {})
		return text, u, err
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
	ID, Name, Summary string
}

// officialToolSpec maps a configured official-tool name to its Responses API
// tool spec. Unknown names return nil (skipped). §2.3-B.
func officialToolSpec(name string) map[string]any {
	switch name {
	case "web_search":
		// search_context_size mirrors the documented default ("medium"); the
		// other documented knobs (external_web_access, search_content_types)
		// keep their API defaults, which match the reference, and are omitted so
		// older OpenAI-compatible endpoints don't 400 on unknown fields. Pair
		// this with include=["web_search_call.action.sources"] (set on the body)
		// so the cited sources come back.
		return map[string]any{"type": "web_search", "search_context_size": "medium"}
	case "code_interpreter":
		return map[string]any{"type": "code_interpreter", "container": map[string]any{"type": "auto"}}
	case "image_generation":
		return map[string]any{"type": "image_generation"}
	}
	return nil
}

// hostedToolName maps a Responses hosted-tool output item type (e.g.
// "web_search_call") to the system tool name the frontend already has an icon
// and label for, so hosted rounds render identically to self-built ones.
func hostedToolName(itemType string) string {
	switch itemType {
	case "web_search_call":
		return "web_search"
	case "code_interpreter_call":
		return "python_execute"
	case "image_generation_call":
		return "image_generate"
	case "file_search_call":
		return "search_knowledge_base"
	}
	return strings.TrimSuffix(itemType, "_call")
}

func readOpenAIChatStream(body io.Reader, onEvent func(SseEvent)) (string, string, []openAIToolCall, string, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	text := strings.Builder{}
	reasoning := strings.Builder{}
	usage := Usage{}
	finish := "end_turn"
	// Tool calls are accumulated by index — OpenAI streams partial args.
	toolByIdx := map[int]*openAIToolCall{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		choices, _ := ev["choices"].([]any)
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			// Reasoning models on the Chat Completions wire (OpenAI o-series via
			// compatible gateways, DeepSeek-R1, etc.) stream chain-of-thought as
			// `reasoning_content` or `reasoning` deltas — surface them as thinking.
			if s, _ := delta["reasoning_content"].(string); s != "" {
				reasoning.WriteString(s)
				onEvent(SseEvent{Type: "thinking_delta", Text: s})
			}
			if s, _ := delta["reasoning"].(string); s != "" {
				reasoning.WriteString(s)
				onEvent(SseEvent{Type: "thinking_delta", Text: s})
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
							if !isExisting {
								// First slice that names the tool — emit tool_start.
								onEvent(SseEvent{Type: "tool_start", Name: n, ID: cur.ID})
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
			}
		}
		if u, ok := ev["usage"].(map[string]any); ok {
			usage.InputTokens = intOf(u["prompt_tokens"])
			usage.OutputTokens = intOf(u["completion_tokens"])
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), reasoning.String(), nil, finish, usage, err
	}
	calls := []openAIToolCall{}
	for _, c := range toolByIdx {
		if len(c.Input) == 0 {
			c.Input = []byte("{}")
		}
		calls = append(calls, *c)
	}
	return text.String(), reasoning.String(), calls, finish, usage, nil
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
		return p.streamChat(ctx, req, tools, onEvent)
	}

	// Build the input list from history.
	input := []map[string]any{}
	for _, m := range req.History {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.Builder{}
		for _, b := range m.Blocks {
			if b.Kind == "text" {
				text.WriteString(b.Text)
				text.WriteString("\n")
			}
		}
		ctype := "input_text"
		if m.Role == "assistant" {
			ctype = "output_text"
		}
		parts := []map[string]any{{"type": ctype, "text": strings.TrimRight(text.String(), "\n")}}
		// Multimodal: pass image blocks through. Document attachments are
		// intentionally excluded: PDFs/DOCX/PPTX/etc. always enter the model
		// through the RAG text path, never native provider file blocks.
		for _, b := range m.Blocks {
			if b.Kind == "image" && b.Data != "" {
				parts = append(parts, map[string]any{
					"type":      "input_image",
					"image_url": "data:" + b.MimeType + ";base64," + b.Data,
				})
			}
		}
		input = append(input, map[string]any{
			"role":    m.Role,
			"content": parts,
		})
	}

	var respTools []map[string]any
	wantWebSearch := false
	if len(req.OfficialTools) > 0 {
		// §2.3-B: attach OpenAI-hosted tools; OpenAI executes them server-side.
		// We attach NO function tools — the loop below just streams the answer,
		// and hosted-tool rounds surface as tool_start/tool_result events.
		for _, name := range req.OfficialTools {
			if name == "web_search" {
				wantWebSearch = true
			}
			if spec := officialToolSpec(name); spec != nil {
				respTools = append(respTools, spec)
			}
		}
	} else {
		for _, t := range req.Tools {
			respTools = append(respTools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(t.InputSchema),
			})
		}
	}

	const maxIter = 20
	historyLen := len(input)
	allText := strings.Builder{}
	allBlocks := []UnifiedBlock{}
	allCitations := []Citation{}
	usage := Usage{}

	for i := 0; i < maxIter; i++ {
		body := map[string]any{
			"model": req.Model.RequestID,
			"input": input,
			// §4.10-E hard rule: do NOT let OpenAI persist conversation state.
			"store":  false,
			"stream": true,
		}
		if req.SystemPrompt != "" {
			body["instructions"] = req.SystemPrompt
		}
		if req.MaxOutputTokens > 0 {
			body["max_output_tokens"] = req.MaxOutputTokens
		}
		if len(respTools) > 0 {
			body["tools"] = respTools
		}
		// Ask the API to return the sources the hosted web_search consulted, so
		// we can surface them as citations (only valid when web_search is on).
		if wantWebSearch {
			body["include"] = []string{"web_search_call.action.sources"}
		}
		body = MergeParamControls(body, req.ParamControls, req.ParamOverrides)
		raw, _ := json.Marshal(body)
		resp, err := doProviderRequest(ctx, req.Model, req.FallbackUsed, func(baseURL, apiKey string) (*http.Request, error) {
			hr, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://api.openai.com")+"/v1/responses", bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			hr.Header.Set("authorization", "Bearer "+apiKey)
			hr.Header.Set("content-type", "application/json")
			hr.Header.Set("accept", "text/event-stream")
			return hr, nil
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("openai responses %d: %s", resp.StatusCode, string(b))
		}

		text, reasoning, calls, hosted, citations, u, err := readOpenAIResponsesStream(resp.Body, onEvent)
		resp.Body.Close()
		if err != nil {
			// Stop button / kill: preserve the partial (§6.2).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				allCitations = append(allCitations, citations...)
				if reasoning != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "thinking", Text: reasoning})
				}
				for _, h := range hosted {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "tool_call", ToolName: h.Name, ToolID: h.ID, Summary: h.Summary})
				}
				if text != "" {
					allBlocks = append(allBlocks, UnifiedBlock{Kind: "text", Text: text})
				}
				return &UnifiedResult{Blocks: allBlocks, StopReason: "stopped", Usage: usage, Citations: allCitations}, err
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
			input = append(input, map[string]any{
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": text}},
			})
		}

		if len(calls) == 0 {
			raw, _ := json.Marshal(input[historyLen:])
			return &UnifiedResult{
				Blocks:     allBlocks,
				Raw:        raw,
				StopReason: "end_turn",
				Usage:      usage,
				Citations:  allCitations,
			}, nil
		}

		// Insert the function_call items the model emitted (echo them back
		// alongside their outputs — required by the Responses protocol).
		for _, c := range calls {
			input = append(input, map[string]any{
				"type":    "function_call",
				"call_id": c.ID,
				"name":    c.Name,
				// Responses requires `arguments` to be a JSON STRING. Passing
				// json.RawMessage serialises it as an OBJECT and the API rejects
				// it with "expected a string, got an object" on input[N].arguments.
				"arguments": string(c.Input),
			})
		}

		// Execute tools concurrently, then feed function_call_output items.
		specs := make([]toolCallSpec, len(calls))
		for j, c := range calls {
			specs[j] = toolCallSpec{ID: c.ID, Name: c.Name, Input: c.Input}
		}
		results := runToolsConcurrent(ctx, tools, specs, onEvent)
		for j, c := range calls {
			r := results[j]
			out := r.Output
			status := "complete"
			if r.Err != nil {
				status = "error"
				out = "Error: " + r.Err.Error()
			}
			allCitations = append(allCitations, r.Citations...)
			onEvent(SseEvent{Type: "tool_result", Name: c.Name, ID: c.ID, Summary: truncate(out, 240), Status: status})
			allBlocks = append(allBlocks, UnifiedBlock{
				Kind: "tool_call", ToolName: c.Name, ToolID: c.ID,
				Input: c.Input, Summary: truncate(out, 240),
			})
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": c.ID,
				"output":  out,
			})
		}
	}
	raw, _ := json.Marshal(input[historyLen:])
	return &UnifiedResult{
		Blocks:     allBlocks,
		Raw:        raw,
		StopReason: "max_iterations",
		Usage:      usage,
		Citations:  allCitations,
	}, nil
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
// and the input/output token counts.
func readOpenAIResponsesStream(body io.Reader, onEvent func(SseEvent)) (string, string, []openAIToolCall, []hostedToolCall, []Citation, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
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
	type callBuf struct {
		ID, Name string
		Args     strings.Builder
		Started  bool
	}
	callsByItem := map[string]*callBuf{} // item_id → buffer
	order := []string{}
	hostedByItem := map[string]*hostedToolCall{} // item_id → hosted tool round
	hostedOrder := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		typ, _ := ev["type"].(string)
		switch typ {
		case "response.output_text.delta":
			if s, _ := ev["delta"].(string); s != "" {
				text.WriteString(s)
				onEvent(SseEvent{Type: "text_delta", Text: s})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning.delta":
			if s, _ := ev["delta"].(string); s != "" {
				reasoning.WriteString(s)
				onEvent(SseEvent{Type: "thinking_delta", Text: s})
			}
		case "response.output_text.annotation.added":
			// Inline web-search citations the model attached to the answer text.
			if ann, _ := ev["annotation"].(map[string]any); ann != nil {
				if at, _ := ann["type"].(string); at == "url_citation" {
					url, _ := ann["url"].(string)
					title, _ := ann["title"].(string)
					addCitation(url, title, "")
				}
			}
		case "response.output_item.added":
			it, _ := ev["item"].(map[string]any)
			if it == nil {
				continue
			}
			t, _ := it["type"].(string)
			if t == "function_call" {
				itemID, _ := it["id"].(string)
				callID, _ := it["call_id"].(string)
				name, _ := it["name"].(string)
				cb := &callBuf{ID: callID, Name: name, Started: true}
				callsByItem[itemID] = cb
				order = append(order, itemID)
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: callID})
			} else if strings.HasSuffix(t, "_call") {
				// §2.3-B OpenAI-hosted tool round (web_search_call, …). OpenAI
				// runs it server-side; surface a live tool step to the UI.
				itemID, _ := it["id"].(string)
				name := hostedToolName(t)
				hostedByItem[itemID] = &hostedToolCall{ID: itemID, Name: name}
				hostedOrder = append(hostedOrder, itemID)
				onEvent(SseEvent{Type: "tool_start", Name: name, ID: itemID})
			}
		case "response.output_item.done":
			it, _ := ev["item"].(map[string]any)
			if it == nil {
				continue
			}
			itemID, _ := it["id"].(string)
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
		case "response.function_call_arguments.delta":
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
			itemID, _ := ev["item_id"].(string)
			cb := callsByItem[itemID]
			if cb == nil {
				continue
			}
			if a, _ := ev["arguments"].(string); a != "" && cb.Args.Len() == 0 {
				cb.Args.WriteString(a)
			}
		case "response.completed":
			r, _ := ev["response"].(map[string]any)
			if r != nil {
				if u, ok := r["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["input_tokens"])
					usage.OutputTokens = intOf(u["output_tokens"])
				}
			}
		case "response.failed":
			r, _ := ev["response"].(map[string]any)
			if r != nil {
				if errObj, ok := r["error"].(map[string]any); ok {
					msg, _ := errObj["message"].(string)
					return text.String(), reasoning.String(), nil, nil, citations, usage, fmt.Errorf("openai responses error: %s", msg)
				}
			}
			return text.String(), reasoning.String(), nil, nil, citations, usage, fmt.Errorf("openai responses failed")
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), reasoning.String(), nil, nil, citations, usage, err
	}
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
	return text.String(), reasoning.String(), calls, hosted, citations, usage, nil
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
