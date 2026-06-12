package llm

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// MockProvider produces editorial-feeling streaming responses entirely
// in-process. It is the only provider that runs in unit tests and on a fresh
// install with no API keys configured. It deliberately exercises the same
// SseEvent shape and tool-call sequence as the real providers so the
// orchestrator + frontend paths are tested end-to-end.
type MockProvider struct {
	logger *log.Logger
}

// ID returns "mock".
func (p *MockProvider) ID() string { return "mock" }

// Stream emits a synthesized response token by token, exercising tool calls
// when the user's prompt mentions phrases that look like they should trigger
// tools.
func (p *MockProvider) Stream(ctx context.Context, req UnifiedChatRequest, tools ToolRunner, onEvent func(SseEvent)) (*UnifiedResult, error) {
	// Short-circuit for TaskLLM-style internal calls (MaxOutputTokens set):
	// produce a single deterministic reply that matches the most common
	// task shape (a one-line title, a JSON router output, a summary).
	if req.MaxOutputTokens > 0 && req.MaxOutputTokens <= 256 {
		text := p.fastTaskReply(req)
		onEvent(SseEvent{Type: "text_delta", Text: text})
		return &UnifiedResult{
			Blocks:     []UnifiedBlock{{Kind: "text", Text: text}},
			StopReason: "end_turn",
			Usage:      Usage{InputTokens: estimateTokens(req.SystemPrompt), OutputTokens: estimateTokens(text)},
		}, nil
	}

	// "Thinking" pause.
	if err := sleep(ctx, 280+time.Duration(rand.Intn(220))*time.Millisecond); err != nil {
		return nil, err
	}

	// Pull last user turn.
	prompt := ""
	for i := len(req.History) - 1; i >= 0; i-- {
		if req.History[i].Role == "user" {
			for _, b := range req.History[i].Blocks {
				if b.Kind == "text" {
					prompt += b.Text + "\n"
				}
			}
			break
		}
	}
	prompt = strings.TrimSpace(prompt)

	citations := []Citation{}
	toolBlocks := []UnifiedBlock{}

	// Mock tool routing — match a few cues.
	low := strings.ToLower(prompt)
	if wantsSearch(low) {
		out, c, err := callTool(ctx, tools, "web_search", map[string]any{"query": deriveQuery(prompt)}, onEvent)
		if err == nil {
			citations = append(citations, c...)
			toolBlocks = append(toolBlocks, UnifiedBlock{Kind: "tool_call", ToolName: "web_search", Summary: truncate(out, 240)})
		}
	}
	if wantsCode(low) {
		out, _, err := callTool(ctx, tools, "python_execute", map[string]any{"code": "print(2 + 2)"}, onEvent)
		if err == nil {
			toolBlocks = append(toolBlocks, UnifiedBlock{Kind: "tool_call", ToolName: "python_execute", Summary: truncate(out, 200)})
		}
	}
	if wantsKB(low) && len(req.RAGSnippets) == 0 {
		out, c, err := callTool(ctx, tools, "search_knowledge_base", map[string]any{"query": deriveQuery(prompt)}, onEvent)
		if err == nil {
			citations = append(citations, c...)
			toolBlocks = append(toolBlocks, UnifiedBlock{Kind: "tool_call", ToolName: "search_knowledge_base", Summary: truncate(out, 200)})
		}
	}

	// Build a long-ish reply.
	reply := buildMockReply(prompt, req, citations)

	// Stream words.
	chunks := splitWords(reply)
	full := strings.Builder{}
	for _, c := range chunks {
		select {
		case <-ctx.Done():
			return finalResult(full.String(), toolBlocks, citations, "interrupted"), ctx.Err()
		default:
		}
		full.WriteString(c)
		onEvent(SseEvent{Type: "text_delta", Text: c})
		if err := sleep(ctx, time.Duration(18+rand.Intn(36))*time.Millisecond); err != nil {
			return nil, err
		}
	}

	// Emit citations (orchestrator-level — providers always have the final say).
	for _, cit := range citations {
		c := cit
		onEvent(SseEvent{Type: "citation", Citation: &c})
	}

	return finalResult(full.String(), toolBlocks, citations, "end_turn"), nil
}

func finalResult(text string, toolBlocks []UnifiedBlock, citations []Citation, stop string) *UnifiedResult {
	blocks := append([]UnifiedBlock{}, toolBlocks...)
	blocks = append(blocks, UnifiedBlock{Kind: "text", Text: text})
	usage := Usage{
		InputTokens:  estimateTokens(text) + 200,
		OutputTokens: estimateTokens(text),
	}
	return &UnifiedResult{
		Blocks:     blocks,
		StopReason: stop,
		Usage:      usage,
		Citations:  citations,
	}
}

func callTool(ctx context.Context, runner ToolRunner, name string, input map[string]any, onEvent func(SseEvent)) (string, []Citation, error) {
	inp, _ := json.Marshal(input)
	onEvent(SseEvent{Type: "tool_start", Name: name, Input: inp})
	if err := sleep(ctx, 500*time.Millisecond); err != nil {
		return "", nil, err
	}
	out, cites, err := runner.Run(ctx, name, inp)
	status := "complete"
	if err != nil {
		status = "error"
	}
	onEvent(SseEvent{Type: "tool_result", Name: name, Summary: truncate(out, 240), Status: status})
	return out, cites, err
}

func wantsSearch(s string) bool {
	return regexp.MustCompile(`(news|latest|today|search|google|baidu|价格|新闻|最新|查|搜|多少|股价)`).MatchString(s)
}
func wantsCode(s string) bool {
	return regexp.MustCompile(`(calc|compute|python|sum|mean|graph|plot|代码|计算|绘图|matplotlib)`).MatchString(s)
}
func wantsKB(s string) bool {
	return regexp.MustCompile(`(document|文档|file|文件|knowledge|资料|根据)`).MatchString(s)
}

func deriveQuery(p string) string {
	p = strings.TrimSpace(p)
	if len(p) > 80 {
		p = p[:80]
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func splitWords(s string) []string {
	re := regexp.MustCompile(`(\s+|\S+)`)
	return re.FindAllString(s, -1)
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// estimateTokens is a CJK-aware token estimator. ASCII text is well-approximated
// by `words * 4/3`, but Chinese / Japanese / Korean ideographs roughly count
// as ONE token each (BPE rarely splits them), so a pure word-count undercounts
// CJK text by ~3×. We sum (ASCII words * 4/3) + (count of CJK ideographs).
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	ascii := []rune{}
	cjk := 0
	for _, r := range s {
		// Common CJK Unified Ideograph ranges + CJK punctuation.
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
			r >= 0x3400 && r <= 0x4DBF,  // Extension A
			r >= 0xF900 && r <= 0xFAFF,  // Compatibility Ideographs
			r >= 0x3040 && r <= 0x309F,  // Hiragana
			r >= 0x30A0 && r <= 0x30FF,  // Katakana
			r >= 0xAC00 && r <= 0xD7AF,  // Hangul
			r >= 0xFF00 && r <= 0xFFEF:  // Halfwidth/Fullwidth
			cjk++
		default:
			ascii = append(ascii, r)
		}
	}
	w := len(strings.Fields(string(ascii)))
	tot := cjk + w*4/3
	if tot == 0 {
		return 0
	}
	return tot + 1
}

func buildMockReply(prompt string, req UnifiedChatRequest, cites []Citation) string {
	var b strings.Builder
	if req.ProjectName != "" {
		b.WriteString("_Working in **")
		b.WriteString(req.ProjectName)
		b.WriteString("**._\n\n")
	}
	if prompt == "" {
		b.WriteString("How can I help today?")
		return b.String()
	}
	b.WriteString("Here's a thoughtful response to *")
	b.WriteString(truncate(prompt, 120))
	b.WriteString("*.\n\n")
	b.WriteString("- I treated this as a real request and considered the relevant tools.\n")
	b.WriteString("- The mock streaming engine is exercising the same SSE path the production model uses, including ")
	b.WriteString("`tool_call`, `citation`, `text_delta`, and `done` events.\n")
	if len(req.ProjectFiles) > 0 {
		b.WriteString("- I reviewed the project library you provided (")
		names := []string{}
		for _, f := range req.ProjectFiles {
			names = append(names, "**"+f.Name+"**")
			if len(names) >= 3 {
				break
			}
		}
		b.WriteString(strings.Join(names, ", "))
		b.WriteString(") to ground the answer.\n")
	}
	if len(req.RAGSnippets) > 0 {
		b.WriteString("- I retrieved ")
		b.WriteString(formatInt(int64(len(req.RAGSnippets))))
		b.WriteString(" relevant snippets from your knowledge base and folded them into the reply.\n")
	}
	if len(cites) > 0 {
		b.WriteString("\n**Sources**\n")
		for i, c := range cites {
			b.WriteString("[")
			b.WriteString(formatInt(int64(i + 1)))
			b.WriteString("] [")
			b.WriteString(c.Title)
			b.WriteString("](")
			b.WriteString(c.URL)
			b.WriteString(")\n")
		}
	}
	b.WriteString("\nReplace the configured channel with a real model and the same machinery will stream the live answer.")
	return b.String()
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// fastTaskReply returns a deterministic short response for TaskLLM internal
// calls. We sniff the system prompt for the task kind and produce a sane
// default — this lets the mock provider stand in for the task model during
// development so titles / RAG router / summaries all work end-to-end.
func (p *MockProvider) fastTaskReply(req UnifiedChatRequest) string {
	sys := strings.ToLower(req.SystemPrompt)
	userText := ""
	for _, m := range req.History {
		if m.Role == "user" {
			for _, b := range m.Blocks {
				if b.Kind == "text" {
					userText += b.Text + " "
				}
			}
		}
	}
	userText = strings.TrimSpace(userText)
	switch {
	case strings.Contains(sys, "conversation title"):
		// Title — short clip of the user's first message.
		if userText == "" {
			return "New conversation"
		}
		words := strings.Fields(userText)
		if len(words) > 6 {
			words = words[:6]
		}
		return strings.Join(words, " ")
	case strings.Contains(sys, "strategy"):
		// Router — default to retrieve with the user text as query.
		clip := userText
		if len(clip) > 80 {
			clip = clip[:80]
		}
		return `{"strategy":"retrieve","queries":["` + strings.ReplaceAll(clip, `"`, `'`) + `"]}`
	case strings.Contains(sys, "compress"):
		// Compaction — return a short summary.
		if len(userText) > 240 {
			userText = userText[:240] + "…"
		}
		return "Earlier rounds: " + userText
	case strings.Contains(sys, "extract durable"):
		// Memory extraction — return [] (be conservative).
		return "[]"
	default:
		// Generic — echo a tight version.
		if len(userText) > 200 {
			return userText[:200]
		}
		return userText
	}
}
