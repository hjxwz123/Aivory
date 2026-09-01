// Compaction source rendering and single-request summary-model execution.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aivory/server/internal/store"
)

const compactionSummaryInstruction = "Compress the conversation source below into one standalone continuation summary. " +
	"Use no more than %d tokens; be concise without collapsing the source into a generic paragraph. " +
	"Preserve concrete facts, requirements, user preferences, decisions and their rationale, names/IDs/paths, dates, numbers, code and configuration details, tool inputs and outcomes, errors, unresolved questions, and pending next steps. " +
	"Record superseded facts as superseded rather than presenting them as current. Keep uncertainty and disagreements explicit. " +
	"Use these headings in this order: Objective and success criteria; User constraints and corrections; Completed work and decisions; Artifacts and identifiers; Evidence and tool outcomes; Failures and exact errors; Active work; Next steps and open questions. " +
	"Under each heading use compact bullets and write 'None' only when the section is genuinely empty. Do not invent information, repeat points, or include pleasantries. Reply with only the structured summary text.\n\n--- SOURCE (DATA) ---\n"

func compactionTaskInputTokens(customPrompt, instruction string, outputLimit int, extraParams json.RawMessage) int {
	prefix := ""
	if customPrompt != "" {
		prefix = customPrompt + "\n\n"
	}
	userPrompt := prefix + fmt.Sprintf(instruction, outputLimit)
	return estimateRequestTokens(UnifiedChatRequest{
		SystemPrompt: defaultSystem(TaskCompact, false),
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: userPrompt}}}},
		ExtraParams:  extraParams,
	})
}

func compactionPayloadBudget(requestMaxTokens, outputCap int, customPrompt, instruction string, extraParams json.RawMessage) int {
	base := compactionTaskInputTokens(customPrompt, instruction, outputCap, extraParams)
	budget := requestMaxTokens - outputCap - base - compactionRequestSafetyTokens
	if budget < 1 {
		return 0
	}
	return budget
}

func effectiveCompactionOutputCap(requestMaxTokens, configuredCap int) int {
	if requestMaxTokens <= 0 || configuredCap <= 0 {
		return 0
	}
	// summary_max_tokens is the provider output ceiling. Keep enough of the total
	// request budget for source text when the configured ceiling is too large.
	cap := requestMaxTokens / compactionOutputBudgetDivisor
	if cap <= 0 {
		return 0
	}
	return min(configuredCap, cap)
}

func compactionTaskExtraParams(ctx context.Context, task *TaskLLM, conversationModelID string) (json.RawMessage, error) {
	if task == nil || task.db == nil {
		return nil, nil
	}
	return resolvedCompactionExtraParams(ctx, task.db, conversationModelID)
}

// compactionBudgetExtraParams returns the largest request-parameter payload in
// the candidate chain. The selected source must fit whichever candidate serves
// the one summary request.
func compactionBudgetExtraParams(candidates []json.RawMessage) json.RawMessage {
	var selected json.RawMessage
	maxTokens := -1
	for _, params := range candidates {
		if len(params) == 0 {
			if maxTokens < 0 {
				maxTokens = 0
			}
			continue
		}
		score := estimateRequestTokens(UnifiedChatRequest{ExtraParams: params})
		if score > maxTokens {
			selected = append(json.RawMessage(nil), params...)
			maxTokens = score
		}
	}
	return selected
}

func resolvedCompactionExtraParams(ctx context.Context, db *sql.DB, conversationModelID string) (json.RawMessage, error) {
	if db == nil {
		return nil, nil
	}
	modelIDs, err := resolveCompactionModelCandidates(ctx, db, conversationModelID)
	if err != nil {
		return nil, err
	}
	params := make([]json.RawMessage, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		model, modelErr := store.GetModel(ctx, db, modelID)
		if modelErr == nil {
			params = append(params, model.ExtraParams)
		}
	}
	return compactionBudgetExtraParams(params), nil
}

func buildCompactionPrompt(customPrompt, instruction, source string, outputLimit int) string {
	var prompt strings.Builder
	if customPrompt != "" {
		prompt.WriteString(customPrompt)
		prompt.WriteString("\n\n")
	}
	fmt.Fprintf(&prompt, instruction, outputLimit)
	prompt.WriteString(source)
	return prompt.String()
}

func runCompactionTask(
	ctx context.Context,
	task *TaskLLM,
	conv *store.Conversation,
	payerID, conversationModelID, prompt string,
	outputCap, requestMaxTokens int,
) (resultText string, returnErr error) {
	taskStarted := time.Now()
	task.logCompactionStage(ctx, conv.ID, "task_call", "started", time.Time{},
		fmt.Sprintf(" input_tokens=%d max_output_tokens=%d request_max_tokens=%d",
			estimateTokens(prompt), outputCap, requestMaxTokens))
	defer func() {
		status := "completed"
		if returnErr != nil {
			status = "failed"
		}
		task.logCompactionStage(context.WithoutCancel(ctx), conv.ID, "task_call", status, taskStarted,
			fmt.Sprintf(" output_tokens=%d error_kind=%q", estimateTokens(resultText), compactionErrorKind(returnErr)))
	}()
	maxInputTokens := requestMaxTokens - outputCap
	if maxInputTokens <= 0 {
		return "", ErrCompactionFailed
	}
	text, err := task.Run(ctx, TaskCompact, prompt, RunOpts{
		UserID:                    payerID,
		WorkspaceID:               conv.WorkspaceID,
		ConversationID:            conv.ID,
		MaxOutputTokens:           outputCap,
		EmptyRetryMaxOutputTokens: -1,
		MaxInputTokens:            maxInputTokens,
		FallbackModelID:           conversationModelID,
	})
	if terminalErr := terminalCompactionTaskError(ctx, err); terminalErr != nil {
		return "", terminalErr
	}
	if err != nil {
		return "", err
	}
	text = clipToTokens(strings.TrimSpace(text), outputCap)
	if text == "" {
		return "", ErrCompactionFailed
	}
	return text, nil
}

// summarizeCompactionText makes exactly one summary-model request. Callers must
// select a source that fits the configured request budget before calling it.
func summarizeCompactionText(
	ctx context.Context,
	task *TaskLLM,
	conv *store.Conversation,
	fullSource, payerID, conversationModelID, customPrompt, instruction string,
	configuredOutputCap, requestMaxTokens int,
) (string, error) {
	fullSource = strings.TrimSpace(fullSource)
	if task == nil || fullSource == "" {
		return "", ErrCompactionFailed
	}
	outputCap := effectiveCompactionOutputCap(requestMaxTokens, configuredOutputCap)
	if outputCap < 1 {
		return "", ErrCompactionFailed
	}
	extraParams, err := compactionTaskExtraParams(ctx, task, conversationModelID)
	if err != nil {
		return "", err
	}
	sourceTokens := estimateTokens(fullSource)
	planStarted := time.Now()
	task.logCompactionStage(ctx, conv.ID, "summary_plan", "started", time.Time{},
		fmt.Sprintf(" source_tokens=%d max_output_tokens=%d request_max_tokens=%d",
			sourceTokens, outputCap, requestMaxTokens))
	payloadBudget := compactionPayloadBudget(
		requestMaxTokens, outputCap, customPrompt, instruction, extraParams,
	)
	if payloadBudget <= 0 || sourceTokens > payloadBudget {
		task.logCompactionStage(ctx, conv.ID, "summary_plan", "failed", planStarted,
			fmt.Sprintf(" strategy=direct payload_budget=%d error_kind=%q", payloadBudget, compactionErrorKind(ErrCompactionFailed)))
		return "", ErrCompactionFailed
	}
	task.logCompactionStage(ctx, conv.ID, "summary_plan", "completed", planStarted,
		fmt.Sprintf(" strategy=direct payload_budget=%d selected_source_tokens=%d", payloadBudget, sourceTokens))
	prompt := buildCompactionPrompt(customPrompt, instruction, fullSource, outputCap)
	return runCompactionTask(ctx, task, conv, payerID, conversationModelID, prompt, outputCap, requestMaxTokens)
}

func renderContinuationSummarySource(prior []SummaryBlock, newer []store.Message) (string, error) {
	var source strings.Builder
	if len(prior) > 0 {
		source.WriteString("[PRIOR CONTINUATION STATE]\n")
		for _, block := range prior {
			source.WriteString(strings.TrimSpace(block.Text))
			source.WriteString("\n\n")
		}
	}
	source.WriteString("[NEW CONVERSATION EVENTS]\n")
	if err := appendCompactionSourceChecked(&source, newer); err != nil {
		return "", err
	}
	return source.String(), nil
}

type compactionPrefixSelection struct {
	messages      []store.Message
	source        string
	sourceTokens  int
	outputCap     int
	payloadBudget int
	newRounds     int
}

// selectCompactionPrefix chooses the largest prefix ending at a complete round
// boundary that fits one summary request. Unselected rounds remain verbatim and
// are eligible for a later background pass.
func selectCompactionPrefix(
	prior []SummaryBlock,
	newer []store.Message,
	customPrompt string,
	extraParams json.RawMessage,
	summaryMaxTokens, requestMaxTokens int,
) (compactionPrefixSelection, error) {
	var selected compactionPrefixSelection
	newRounds := 0
	outputCap := effectiveCompactionOutputCap(requestMaxTokens, summaryMaxTokens)
	if outputCap < 1 {
		return compactionPrefixSelection{}, ErrCompactionFailed
	}
	for end := 1; end <= len(newer); end++ {
		if newer[end-1].Role == "user" {
			newRounds++
		}
		if end < len(newer) && newer[end].Role != "user" {
			continue
		}
		candidate := newer[:end]
		source, err := renderContinuationSummarySource(prior, candidate)
		if err != nil {
			return compactionPrefixSelection{}, err
		}
		payloadBudget := compactionPayloadBudget(
			requestMaxTokens, outputCap, customPrompt, compactionSummaryInstruction, extraParams,
		)
		sourceTokens := estimateTokens(source)
		if payloadBudget < 1 || sourceTokens > payloadBudget {
			break
		}
		selected = compactionPrefixSelection{
			messages:      candidate,
			source:        source,
			sourceTokens:  sourceTokens,
			outputCap:     outputCap,
			payloadBudget: payloadBudget,
			newRounds:     newRounds,
		}
	}
	if len(selected.messages) == 0 {
		return compactionPrefixSelection{}, ErrCompactionFailed
	}
	return selected, nil
}

func fallbackContinuationSummary(prior []SummaryBlock, newer []store.Message, outputLimit int) string {
	if outputLimit <= 0 {
		return ""
	}
	parts := make([]string, 0, len(prior))
	for _, block := range prior {
		if text := strings.TrimSpace(block.Text); text != "" {
			parts = append(parts, text)
		}
	}
	priorText := strings.Join(parts, "\n\n")
	if priorText == "" {
		return clipOlder(newer, outputLimit)
	}
	priorBudget := min(estimateTokens(priorText), outputLimit*2/3)
	if priorBudget < 1 {
		priorBudget = 1
	}
	priorText = clipToTokens(priorText, priorBudget)
	newBudget := outputLimit - estimateTokens(priorText)
	if newBudget < 1 {
		return priorText
	}
	return strings.TrimSpace(priorText + "\n\n" + clipOlder(newer, newBudget))
}

func summaryBlocksText(blocks []SummaryBlock) string {
	var source strings.Builder
	for i, block := range blocks {
		fmt.Fprintf(&source, "[continuation summary %d/%d]\n%s\n\n", i+1, len(blocks), strings.TrimSpace(block.Text))
	}
	return source.String()
}

func renderCompactionSource(msgs []store.Message) (string, error) {
	var source strings.Builder
	if err := appendCompactionSourceChecked(&source, msgs); err != nil {
		return "", err
	}
	return source.String(), nil
}

// appendCompactionResearchState keeps the useful bounded state from a research
// panel without replaying its complete JSON into a later summary request.
func appendCompactionResearchState(prompt *strings.Builder, raw string) {
	const (
		maxResearchTasks   = 12
		maxResearchSources = 24
		maxResearchItemTok = 128
	)
	itemTokens := min(compactionMetadataLimit(), maxResearchItemTok)
	type researchTask struct {
		Question string `json:"question"`
		Status   string `json:"status"`
		Round    int    `json:"round"`
	}
	type researchSource struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Domain  string `json:"domain"`
		Status  string `json:"status"`
		Verdict string `json:"verdict"`
	}
	var state struct {
		Title   string           `json:"title"`
		Rounds  int              `json:"rounds"`
		Tasks   []researchTask   `json:"tasks"`
		Sources []researchSource `json:"sources"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil {
		prompt.WriteString("[research state unavailable]\n")
		return
	}
	fmt.Fprintf(prompt, "[research title=%s rounds=%d]\n", quotedCompactionMetadata(state.Title, itemTokens), state.Rounds)
	for i, task := range state.Tasks {
		if i >= maxResearchTasks {
			fmt.Fprintf(prompt, "[research tasks omitted=%d]\n", len(state.Tasks)-i)
			break
		}
		fmt.Fprintf(prompt, "[research_task status=%s round=%d] %s\n",
			quotedCompactionMetadata(task.Status, itemTokens), task.Round,
			clipToTokens(strings.TrimSpace(task.Question), itemTokens))
	}
	for i, source := range state.Sources {
		if i >= maxResearchSources {
			fmt.Fprintf(prompt, "[research sources omitted=%d]\n", len(state.Sources)-i)
			break
		}
		fmt.Fprintf(prompt, "[research_source status=%s title=%s domain=%s url=%s verdict=%s]\n",
			quotedCompactionMetadata(source.Status, itemTokens),
			quotedCompactionMetadata(source.Title, itemTokens),
			quotedCompactionMetadata(source.Domain, itemTokens),
			quotedCompactionMetadata(source.URL, itemTokens),
			quotedCompactionMetadata(source.Verdict, itemTokens))
	}
}
