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

	"aivory/server/internal/envcfg"
)

const promptStopToken = "</tool_call>"

const promptToolRawEnvelopeType = "aivory_prompt_tool_outputs_v1"

var (
	promptMaxIter                     = envcfg.Int("AIVORY_LLM_PROMPT_MAX_ITER", 10)
	promptMaxRetry                    = envcfg.Int("AIVORY_LLM_PROMPT_MAX_RETRY", 2)
	promptModeToolResultSummaryLength = 240
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
// when supported, send the upstream request, and return the assistant text plus
// any provider-hosted results. Hosted blocks/citations/images are carried through
// to the final UnifiedResult but are never dispatched through the local registry.
type PromptToolRound struct {
	Text   string
	Blocks []UnifiedBlock
	// Raw is provider-native output from a nested hosted-tool round. Prompt
	// protocol output itself is provider-neutral, but a provider may execute an
	// administrator-selected hosted tool while the local loop is active. Keep the
	// raw value long enough to extract its complete result into the internal
	// compaction envelope; adapters never replay the envelope upstream.
	Raw                  json.RawMessage
	Usage                Usage
	Citations            []Citation
	GeneratedImages      []GeneratedImage
	UsageAlreadyAttached bool
}

// promptToolRawEnvelope is an application-internal persistence shape. It keeps
// complete prompt-protocol tool results available to context compaction without
// pretending to be replayable OpenAI, Anthropic, or Gemini history. Provider
// adapters explicitly reject this object and fall back to canonical blocks.
type promptToolRawEnvelope struct {
	Type    string                `json:"type"`
	Outputs []promptToolRawOutput `json:"outputs"`
}

type promptToolRawOutput struct {
	Name   string `json:"name,omitempty"`
	ID     string `json:"id,omitempty"`
	Output string `json:"output"`
	Status string `json:"status,omitempty"`
}

func parsePromptToolRawEnvelope(raw json.RawMessage) (promptToolRawEnvelope, bool) {
	var envelope promptToolRawEnvelope
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil ||
		envelope.Type != promptToolRawEnvelopeType || len(envelope.Outputs) == 0 {
		return promptToolRawEnvelope{}, false
	}
	return envelope, true
}

func isPromptToolRawEnvelope(raw json.RawMessage) bool {
	_, ok := parsePromptToolRawEnvelope(raw)
	return ok
}

func marshalPromptToolRawEnvelope(outputs []promptToolRawOutput) json.RawMessage {
	if len(outputs) == 0 {
		return nil
	}
	raw, err := json.Marshal(promptToolRawEnvelope{Type: promptToolRawEnvelopeType, Outputs: outputs})
	if err != nil {
		return nil
	}
	return json.RawMessage(raw)
}

func filterPromptToolRawEnvelope(raw json.RawMessage, allowed func(string) bool) json.RawMessage {
	envelope, ok := parsePromptToolRawEnvelope(raw)
	if !ok || allowed == nil {
		return nil
	}
	filtered := make([]promptToolRawOutput, 0, len(envelope.Outputs))
	for _, output := range envelope.Outputs {
		if allowed(strings.TrimSpace(output.Name)) {
			filtered = append(filtered, output)
		}
	}
	return marshalPromptToolRawEnvelope(filtered)
}

type PromptToolRunner func(ctx context.Context, history []UnifiedMessage, system string) (PromptToolRound, error)

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
) (string, []UnifiedBlock, Usage, []Citation, []GeneratedImage, error) {
	text, blocks, usage, citations, images, _, err := RunPromptToolLoopWithRaw(
		ctx, system, history, tools, runner, toolRunner, onEvent,
	)
	return text, blocks, usage, citations, images, err
}

// RunPromptToolLoopWithRaw is RunPromptToolLoop plus the internal, provider-
// neutral tool-result envelope used by context compaction.
func RunPromptToolLoopWithRaw(
	ctx context.Context,
	system string,
	history []UnifiedMessage,
	tools []ToolDef,
	runner PromptToolRunner,
	toolRunner ToolRunner,
	onEvent func(SseEvent),
) (string, []UnifiedBlock, Usage, []Citation, []GeneratedImage, json.RawMessage, error) {
	preamble := PromptToolPreamble(tools)
	sys := system + preamble
	usage := Usage{}
	citations := []Citation{}
	generatedImages := []GeneratedImage{}
	blocks := []UnifiedBlock{}
	rawOutputs := []promptToolRawOutput{}
	full := strings.Builder{}
	parseRetries := 0

	for i := 0; i < promptMaxIter; i++ {
		round, err := runner(ctx, history, sys)
		// Hosted-tool prompt rounds are delegated to the provider's native path.
		// That path can return complete result payloads even though its canonical
		// blocks intentionally contain only a bounded display summary. Recover the
		// recognized result text before handling an error or a final answer.
		rawOutputs = appendPromptToolRawOutputs(rawOutputs, round.Raw)
		text := round.Text
		u := round.Usage
		// §B5-per-request usage rows: one attach per prompt-protocol round —
		// covers every provider's prompt mode from this single loop, including a
		// failed stream that reported partial usage. Rich hosted-tool runners use
		// the provider's normal loop, which already attached each upstream request.
		if !round.UsageAlreadyAttached {
			attachProviderRequestUsage(ctx, u)
		}
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheReadTokens += u.CacheReadTokens
		usage.CacheWriteTokens += u.CacheWriteTokens
		for _, block := range round.Blocks {
			if block.Kind == "text" {
				continue
			}
			blocks = append(blocks, block)
			switch block.Kind {
			case "thinking":
				if block.Text != "" {
					onEvent(SseEvent{Type: "thinking_delta", Text: block.Text})
				}
			case "tool_call":
				onEvent(SseEvent{Type: "tool_start", ID: block.ToolID, Name: block.ToolName, Input: block.Input})
				if block.Summary != "" {
					onEvent(SseEvent{Type: "tool_result", ID: block.ToolID, Name: block.ToolName, Summary: block.Summary, Status: "complete"})
				}
			}
		}
		citationStart := len(citations)
		citations = mergeCitationsByURL(citations, round.Citations)
		for i := citationStart; i < len(citations); i++ {
			citation := citations[i]
			onEvent(SseEvent{Type: "citation", Citation: &citation})
		}
		generatedImages = append(generatedImages, round.GeneratedImages...)
		if err != nil {
			// Raw provider deltas are hidden in prompt mode until their protocol
			// envelope can be stripped. Surface and persist only the safe prefix
			// before a possible <tool_call> marker.
			visible, _, _ := SplitTextAndCall(text)
			if visible != "" {
				full.WriteString(visible)
				onEvent(SseEvent{Type: "text_delta", Text: visible})
			}
			if full.Len() > 0 {
				blocks = append(blocks, UnifiedBlock{Kind: "text", Text: full.String()})
			}
			return full.String(), blocks, usage, citations, generatedImages, marshalPromptToolRawEnvelope(rawOutputs), err
		}

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
			return full.String(), blocks, usage, citations, generatedImages, marshalPromptToolRawEnvelope(rawOutputs), nil
		}
		parseRetries = 0

		if call == nil {
			// No tool call → conversation complete. Emit visible text only.
			blocks = append(blocks, UnifiedBlock{Kind: "text", Text: full.String()})
			return full.String(), blocks, usage, citations, generatedImages, marshalPromptToolRawEnvelope(rawOutputs), nil
		}

		// Got a tool call — emit events and execute. A stable per-round id pairs
		// tool_start↔tool_result so the frontend trace clears the "running" dot
		// (the result handler drops events with no id). One tool call per round.
		toolID := fmt.Sprintf("pt_%d", i)
		onEvent(SseEvent{Type: "tool_start", ID: toolID, Name: call.Name, Input: call.Arguments})
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
		onEvent(SseEvent{Type: "tool_result", ID: toolID, Name: call.Name, Summary: truncate(output, promptModeToolResultSummaryLength), Status: summaryStatus})

		blocks = append(blocks, UnifiedBlock{
			Kind:     "tool_call",
			ToolName: call.Name,
			ToolID:   toolID,
			Input:    call.Arguments,
			Summary:  truncate(output, promptModeToolResultSummaryLength),
		})
		blocks = append(blocks, canonicalToolOutputBlock(call.Name, toolID, output, summaryStatus))
		rawOutputs = append(rawOutputs, promptToolRawOutput{
			Name: call.Name, ID: toolID, Output: output, Status: summaryStatus,
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
	return final, blocks, usage, citations, generatedImages, marshalPromptToolRawEnvelope(rawOutputs), nil
}

// appendPromptToolRawOutputs converts a nested provider exchange into the same
// provider-neutral envelope used for local prompt-protocol tools. Keep one
// result per stable tool id (or exact name/output when a provider has no id),
// because hosted pause/resume responses may repeat the completed item.
func appendPromptToolRawOutputs(existing []promptToolRawOutput, raw json.RawMessage) []promptToolRawOutput {
	if len(raw) == 0 {
		return existing
	}
	for _, output := range extractCompactionRawToolOutputs(raw) {
		existing = appendPromptToolRawOutput(existing, promptToolRawOutput{
			Name: output.Name, ID: output.ID, Output: output.Text, Status: "complete",
		})
	}
	return existing
}

func appendPromptToolRawOutput(outputs []promptToolRawOutput, candidate promptToolRawOutput) []promptToolRawOutput {
	candidate.Name = strings.TrimSpace(candidate.Name)
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.Output = strings.TrimSpace(candidate.Output)
	if candidate.Output == "" {
		return outputs
	}
	for i, existing := range outputs {
		// Provider pause/resume responses can repeat the same stable call id. Keep
		// one entry and let the later provider snapshot replace an earlier partial
		// result, while retaining metadata omitted by the later snapshot.
		if candidate.ID != "" && strings.TrimSpace(existing.ID) == candidate.ID {
			if candidate.Name == "" {
				candidate.Name = strings.TrimSpace(existing.Name)
			}
			if candidate.Status == "" {
				candidate.Status = strings.TrimSpace(existing.Status)
			}
			outputs[i] = candidate
			return outputs
		}
		// Gemini hosted code execution has no call id. Its native turn may still
		// be repeated verbatim by a continuation, so dedupe an exact result.
		if candidate.ID == "" && strings.TrimSpace(existing.ID) == "" &&
			strings.TrimSpace(existing.Name) == candidate.Name && strings.TrimSpace(existing.Output) == candidate.Output {
			return outputs
		}
	}
	return append(outputs, candidate)
}

func promptRoundText(blocks []UnifiedBlock) string {
	var text strings.Builder
	for _, block := range blocks {
		if block.Kind == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func withPromptStopSequence(extra json.RawMessage, fragment map[string]any) json.RawMessage {
	base := map[string]any{}
	if len(extra) > 0 {
		_ = json.Unmarshal(extra, &base)
	}
	deepMerge(base, fragment)
	raw, _ := json.Marshal(base)
	return json.RawMessage(raw)
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
