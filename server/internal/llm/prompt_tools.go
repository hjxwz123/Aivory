// Package llm — prompt-mode tool protocol per design.md §4.13.
//
// When a model's tool_mode = "prompt" it doesn't support native function
// calling. To keep one Registry across all models we expose tools through a
// text protocol:
//
//  1. The orchestrator wraps the system prompt with a "tools available" block
//     and a strict output contract (call `<tool_call>{...}</tool_call>`).
//  2. The provider sets stop_sequences = ["</tool_call>"] so the model is
//     cut off the moment it emits a call (this is the single most important
//     anti-hallucination mechanism — A3 in design.md appendix B).
//  3. The orchestrator parses the streamed text, detects the `<tool_call>`
//     marker, executes the tool via the Registry, and feeds the result back
//     as the next user turn wrapped in <tool_result>...</tool_result>.
//  4. Loop up to 6 iterations (lower than native's 12 because the protocol
//     is less reliable on weaker models).
//  5. JSON parse errors retry up to 2 times with an instructional message.
//
// The result blocks are normalised to UnifiedBlock so the database / frontend
// see the same shape as native mode.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	promptMaxIter   = 6
	promptMaxRetry  = 2
	promptStopToken = "</tool_call>"
)

// PromptToolPreamble builds the text block appended to the system prompt
// when tool_mode=prompt. It documents the protocol and lists each tool.
func PromptToolPreamble(tools []ToolDef) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available tools\n")
	b.WriteString("You can call any of the tools below by emitting EXACTLY this format and then STOPPING:\n\n")
	b.WriteString("<tool_call>{\"name\": \"<tool>\", \"arguments\": <args>}</tool_call>\n\n")
	b.WriteString("Important rules:\n")
	b.WriteString("- After emitting `</tool_call>` STOP. Do not write anything else and do not invent the result.\n")
	b.WriteString("- The orchestrator will execute the tool and reply with a `<tool_result>` block. Continue from there.\n")
	b.WriteString("- If you don't need a tool, just answer the user directly.\n\n")
	b.WriteString("Tools:\n\n")
	for _, t := range tools {
		b.WriteString("### " + t.Name + "\n")
		b.WriteString(t.Description + "\n")
		b.WriteString("Input schema: ")
		b.Write(t.InputSchema)
		b.WriteString("\n\n")
	}
	return b.String()
}

// PromptToolStopSequence is the stop token providers should attach to the
// upstream request when tool_mode=prompt.
func PromptToolStopSequence() string { return promptStopToken }

// PromptToolCall is the parsed payload extracted from a `<tool_call>` block.
type PromptToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ParsePromptToolCall reads the text between `<tool_call>` (exclusive) and
// `</tool_call>` (exclusive). It tolerates the closing tag being absent
// because the provider's stop sequence catches the model right at the tag.
func ParsePromptToolCall(text string) (*PromptToolCall, error) {
	start := strings.Index(text, "<tool_call>")
	if start < 0 {
		return nil, errors.New("no tool call marker")
	}
	body := text[start+len("<tool_call>"):]
	if end := strings.Index(body, "</tool_call>"); end >= 0 {
		body = body[:end]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("empty tool call body")
	}
	var c PromptToolCall
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return nil, fmt.Errorf("tool call JSON parse: %w", err)
	}
	if c.Name == "" {
		return nil, errors.New("tool call missing name")
	}
	if len(c.Arguments) == 0 {
		c.Arguments = json.RawMessage("{}")
	}
	return &c, nil
}

// PromptToolResultText formats the orchestrator's tool result as the next
// user-turn message body.
func PromptToolResultText(name, output string, isError bool) string {
	tag := "tool_result"
	if isError {
		tag = "tool_error"
	}
	return fmt.Sprintf("<%s name=\"%s\">\n%s\n</%s>\nContinue from here.", tag, name, output, tag)
}

// SplitTextAndCall consumes one round of streamed text and returns (visible,
// callMaybe, parseErr). When a `<tool_call>` marker is present the visible
// portion is the text before it; a marker with unparseable JSON returns a
// non-nil parseErr so the loop can ask the model to re-emit (§4.13-5).
//
// Use this to filter what gets forwarded to the SSE consumer so the user
// never sees the tool call markup.
func SplitTextAndCall(text string) (visible string, call *PromptToolCall, parseErr error) {
	idx := strings.Index(text, "<tool_call>")
	if idx < 0 {
		return text, nil, nil
	}
	visible = text[:idx]
	c, err := ParsePromptToolCall(text[idx:])
	if err != nil {
		return visible, nil, err
	}
	return visible, c, nil
}

// RunPromptToolLoop drives the §4.13 loop on top of a base function that
// runs ONE upstream call and returns the raw text. Provider implementations
// can build a simple `runOnce(messages)` closure and hand it here so the
// loop logic lives in one place.
//
// `runOnce(history, system)` should: configure stop_sequences = []string{"</tool_call>"}
// when not nil, send the upstream request, and return the assistant text +
// usage. The loop calls `runOnce` up to 6 times, executes tools, and feeds
// the results back as user messages.
type PromptToolRunner func(ctx context.Context, history []UnifiedMessage, system string) (text string, usage Usage, err error)

// RunPromptToolLoop executes a complete prompt-mode tool loop. Returns the
// final assistant text after the loop exits (when the model returns plain
// text or the loop budget is exhausted) plus accumulated usage and
// citations.
func RunPromptToolLoop(
	ctx context.Context,
	system string,
	history []UnifiedMessage,
	tools []ToolDef,
	runner PromptToolRunner,
	toolRunner ToolRunner,
	onEvent func(SseEvent),
) (string, []UnifiedBlock, Usage, []Citation, error) {
	preamble := PromptToolPreamble(tools)
	sys := system + preamble
	usage := Usage{}
	citations := []Citation{}
	blocks := []UnifiedBlock{}
	full := strings.Builder{}
	parseRetries := 0

	for i := 0; i < promptMaxIter; i++ {
		text, u, err := runner(ctx, history, sys)
		if err != nil {
			return "", blocks, usage, citations, err
		}
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheReadTokens += u.CacheReadTokens
		usage.CacheWriteTokens += u.CacheWriteTokens

		visible, call, parseErr := SplitTextAndCall(text)
		if visible != "" {
			full.WriteString(visible)
			// Stream the user-visible portion (the tool-call markup is stripped
			// by SplitTextAndCall so the UI never sees the protocol envelope).
			onEvent(SseEvent{Type: "text_delta", Text: visible})
		}

		// §4.13-5 容错: the model emitted a <tool_call> marker with broken JSON.
		// Feed the parse error back via <tool_error> and ask it to re-emit, up
		// to promptMaxRetry times; the retry round doesn't count as progress.
		if parseErr != nil {
			if parseRetries < promptMaxRetry {
				parseRetries++
				history = append(history, UnifiedMessage{
					Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: text}},
				})
				history = append(history, UnifiedMessage{
					Role: "user",
					Blocks: []UnifiedBlock{{Kind: "text", Text: "<tool_error>\nYour <tool_call> JSON failed to parse: " +
						parseErr.Error() + "\nRe-emit the tool call as ONE valid JSON object: " +
						`<tool_call>{"name": "<tool>", "arguments": {...}}</tool_call>` + "\n</tool_error>"}},
				})
				i-- // don't burn an iteration on the malformed round
				continue
			}
			// Retries exhausted — treat the text as the final answer.
			blocks = append(blocks, UnifiedBlock{Kind: "text", Text: full.String()})
			return full.String(), blocks, usage, citations, nil
		}
		parseRetries = 0

		if call == nil {
			// No tool call → conversation complete. Emit visible text only.
			blocks = append(blocks, UnifiedBlock{Kind: "text", Text: full.String()})
			return full.String(), blocks, usage, citations, nil
		}

		// Got a tool call — emit events and execute.
		onEvent(SseEvent{Type: "tool_start", Name: call.Name, Input: call.Arguments})
		var (
			output  string
			cites   []Citation
			runErr  error
			retries int
		)
		for retries = 0; retries <= promptMaxRetry; retries++ {
			output, cites, runErr = toolRunner.Run(ctx, call.Name, call.Arguments)
			if runErr == nil {
				break
			}
		}
		isError := runErr != nil
		summaryStatus := "complete"
		if isError {
			output = "Error: " + runErr.Error()
			summaryStatus = "error"
		}
		citations = append(citations, cites...)
		onEvent(SseEvent{Type: "tool_result", Name: call.Name, Summary: truncate(output, 240), Status: summaryStatus})

		blocks = append(blocks, UnifiedBlock{
			Kind:     "tool_call",
			ToolName: call.Name,
			Input:    call.Arguments,
			Summary:  truncate(output, 240),
		})

		// Append assistant + tool_result rounds to history.
		history = append(history, UnifiedMessage{
			Role:   "assistant",
			Blocks: []UnifiedBlock{{Kind: "text", Text: visible + "<tool_call>" + mustMarshal(call) + "</tool_call>"}},
		})
		history = append(history, UnifiedMessage{
			Role:   "user",
			Blocks: []UnifiedBlock{{Kind: "text", Text: PromptToolResultText(call.Name, output, isError)}},
		})
	}
	// Loop exhausted.
	final := full.String()
	if final == "" {
		final = "I tried several tools but couldn't reach a conclusion within the budget."
	}
	blocks = append(blocks, UnifiedBlock{Kind: "text", Text: final})
	return final, blocks, usage, citations, nil
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
