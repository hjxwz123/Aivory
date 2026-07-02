// Package llm — TaskLLM is the unified entry point for internal LLM calls
// described in design.md §2.3-F. It centralises "small + fast" model invocations
// (title generation, RAG query routing, long-context compression summaries,
// memory triage, cross-vendor history downgrade) so they all share one
// configuration: settings.task_model_id.
//
// Why a separate helper:
//   - One knob to swap the small model (Haiku / Flash-class) without touching
//     callers.
//   - Built-in `purpose` taxonomy so usage_logs can split costs per task type
//     (per design.md §8.3 — task model calls still cost money and must be
//     traced).
//   - Structured-output convention (JSON-only response) so callers can decode
//     with confidence; we add a strict system prompt around the user prompt.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"aurelia/server/internal/store"
)

// TaskKind enumerates the internal task purposes. Used both for routing
// (lookup of task_model_id today, future per-task models tomorrow) and for
// the `purpose` column of usage_logs.
type TaskKind string

const (
	// TaskTitle generates a short conversation title after the first turn.
	TaskTitle TaskKind = "task.title"
	// TaskRouter classifies query intent + rewrites retrieval queries (RAG).
	TaskRouter TaskKind = "task.router"
	// TaskCompact summarises overflow messages into a compact text block.
	TaskCompact TaskKind = "task.compact"
	// TaskMemoryExtract pulls candidate memory facts out of a finished
	// conversation; runs entirely off the request path.
	TaskMemoryExtract TaskKind = "task.memory_extract"
	// TaskMemoryAdjudicate decides whether new memories supersede old ones.
	TaskMemoryAdjudicate TaskKind = "task.memory_adjudicate"
	// TaskDowngrade builds a cross-vendor history downgrade summary.
	TaskDowngrade TaskKind = "task.downgrade"
	// TaskResearchPlan decomposes a Deep Research question into sub-questions +
	// initial search queries.
	TaskResearchPlan TaskKind = "task.research_plan"
	// TaskResearchVerify assesses research coverage and proposes follow-up
	// queries for the next round.
	TaskResearchVerify TaskKind = "task.research_verify"
	// TaskModeration screens a single user prompt for policy violations using a
	// dedicated moderation model (§ moderation).
	TaskModeration TaskKind = "task.moderation"
)

// TaskLLM dispatches small internal model calls to the configured task model.
type TaskLLM struct {
	db     *sql.DB
	reg    *Registry
	logger *log.Logger
}

// NewTaskLLM constructs a TaskLLM helper.
func NewTaskLLM(db *sql.DB, reg *Registry, logger *log.Logger) *TaskLLM {
	return &TaskLLM{db: db, reg: reg, logger: logger}
}

// RunOpts controls a TaskLLM invocation.
type RunOpts struct {
	// SystemPrompt overrides the helper's default JSON-strict system prompt.
	// Empty means "use the default for this TaskKind".
	SystemPrompt string
	// JSONOutput forces the prompt to ask for JSON-only output.
	JSONOutput bool
	// UserID, ConversationID, MessageID — for the usage_logs row (cost tracking).
	UserID         string
	ConversationID string
	MessageID      string
	// WorkspaceID attributes side-task spend to a workspace (§workspaces).
	WorkspaceID string
	// MaxOutputTokens is a soft cap surfaced into the upstream request as
	// max_tokens.
	MaxOutputTokens int
	// ModelID, when set, overrides the resolved task model — used to run a
	// specific model (e.g. the dedicated moderation model) for this call.
	ModelID string
}

// Run issues a single non-streaming task model call and returns the raw text
// response. The call is logged to usage_logs with the kind as `purpose`.
//
// Errors when no task_model_id is configured — the caller should be
// resilient (e.g. compaction worker may skip a round and re-attempt).
func (t *TaskLLM) Run(ctx context.Context, kind TaskKind, prompt string, opts RunOpts) (string, error) {
	if t == nil || t.db == nil {
		return "", errors.New("task llm not initialised")
	}
	modelID := opts.ModelID
	if modelID == "" {
		var rerr error
		modelID, rerr = resolveTaskModelID(t.db)
		if rerr != nil {
			return "", rerr
		}
	}
	model, err := store.GetModel(ctx, t.db, modelID)
	if err != nil {
		return "", fmt.Errorf("load task model %q: %w", modelID, err)
	}
	if !model.Enabled {
		return "", fmt.Errorf("task model %q is disabled", modelID)
	}
	channel, err := store.GetChannel(ctx, t.db, model.ChannelID)
	if err != nil {
		return "", err
	}
	provider, err := t.reg.Get(channel.Type)
	if err != nil {
		return "", err
	}

	system := opts.SystemPrompt
	if system == "" {
		system = defaultSystem(kind, opts.JSONOutput)
	}
	maxTok := opts.MaxOutputTokens
	if maxTok <= 0 {
		maxTok = 512
	}
	req := UnifiedChatRequest{
		UserID:         opts.UserID,
		ConversationID: opts.ConversationID,
		MessageID:      opts.MessageID,
		SystemPrompt:   system,
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: prompt}}},
		},
		Model: ModelInfo{
			ID:        model.ID,
			RequestID: model.RequestID,
			Provider:  channel.Type,
			Vision:    model.Vision,
			BaseURL:   channel.BaseURL,
			APIKey:    channel.APIKey,
			APIFormat: channel.APIFormat,
		},
		// Task calls never use tools.
		Tools:           nil,
		MaxOutputTokens: maxTok,
		Stream:          false,
	}
	// We capture deltas but only really care about the final result.
	captured := strings.Builder{}
	result, err := provider.Stream(ctx, req, &noopToolRunner{}, func(ev SseEvent) {
		if ev.Type == "text_delta" {
			captured.WriteString(ev.Text)
		}
	})
	if err != nil {
		return "", err
	}
	// Some providers emit deltas, others not; pick the longer.
	final := captured.String()
	if result != nil && len(result.Blocks) > 0 {
		blockText := ""
		for _, b := range result.Blocks {
			if b.Kind == "text" {
				blockText += b.Text
			}
		}
		if len(blockText) > len(final) {
			final = blockText
		}
	}
	final = strings.TrimSpace(final)

	// Record usage so we can split task cost on the report.
	if result != nil {
		cost := computeCost(*model, result.Usage)
		_ = store.LogUsage(ctx, t.db, store.UsageLog{
			WorkspaceID:      opts.WorkspaceID,
		UserID:           opts.UserID,
			ConversationID:   opts.ConversationID,
			MessageID:        opts.MessageID,
			ModelID:          model.ID,
			Purpose:          string(kind),
			InputTokens:      result.Usage.InputTokens,
			OutputTokens:     result.Usage.OutputTokens,
			CacheReadTokens:  result.Usage.CacheReadTokens,
			CacheWriteTokens: result.Usage.CacheWriteTokens,
			Cost:             cost,
			Currency:         model.Currency,
		})
	}
	return final, nil
}

// RunJSON is a thin wrapper that asks for JSON-only output and decodes it.
func (t *TaskLLM) RunJSON(ctx context.Context, kind TaskKind, prompt string, out any, opts RunOpts) error {
	opts.JSONOutput = true
	text, err := t.Run(ctx, kind, prompt, opts)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(extractJSON(text))
	if body == "" {
		return errors.New("task llm returned empty output")
	}
	return json.Unmarshal([]byte(body), out)
}

// RunJSONString satisfies rag.TaskRouter — the package can't import llm.TaskKind.
func (t *TaskLLM) RunJSONString(ctx context.Context, kindStr, prompt string, out any, opts RunOpts) error {
	return t.RunJSON(ctx, TaskKind(kindStr), prompt, out, opts)
}

// resolveTaskModelID reads settings.task_model_id, falling back to
// default_model_id if unset.
func resolveTaskModelID(db *sql.DB) (string, error) {
	var id string
	if raw, err := store.GetSetting(db, "task_model_id"); err == nil {
		_ = json.Unmarshal(raw, &id)
	}
	if id == "" {
		if raw, err := store.GetSetting(db, "default_model_id"); err == nil {
			_ = json.Unmarshal(raw, &id)
		}
	}
	if id == "" {
		return "", errors.New("settings.task_model_id (and default_model_id) are unset")
	}
	return id, nil
}

// defaultSystem returns the system prompt used when callers don't supply one.
func defaultSystem(kind TaskKind, jsonOutput bool) string {
	base := "You are an internal helper. Be concise."
	switch kind {
	case TaskTitle:
		// Reply language is appended authoritatively by scheduleTitle (it forces
		// the user's UI language, since a language-biased task model ignores a soft
		// "same language" hint here).
		return base + " Write a short title (≤8 words) capturing the topic of the conversation." +
			" Reply with the title only, no quotes, no period, no explanation."
	case TaskRouter:
		return base + " Classify the user's last message into one of: full_doc, retrieve, none. " +
			"`full_doc`=summarise/explain entire document; `retrieve`=specific question; `none`=unrelated. " +
			"Also propose up to 3 short retrieval queries when strategy=retrieve. " +
			`Reply with strict JSON: {"strategy":"retrieve","queries":["..."]}.`
	case TaskCompact:
		return base + " Compress the prior conversation rounds into a SHORT summary block " +
			"≤300 tokens. Keep user preferences, decisions, tool outcomes, and pending tasks. " +
			"Drop pleasantries. Reply with just the summary text — no preamble."
	case TaskMemoryExtract:
		return base + " Extract durable, user-specific facts from the conversation. " +
			"Skip transient context. Return JSON array: " +
			`[{"memory_text":"...","slot":"city","value":"Tokyo","confidence":0.8}]. ` +
			"Return [] if nothing significant."
	case TaskMemoryAdjudicate:
		return base + " Compare new and existing memories. " +
			"For each old memory, decide: keep|stale|unknown_current. " +
			`Reply with JSON {"old_id":"verdict",...}.`
	case TaskDowngrade:
		return base + " Compress a multi-turn assistant response into a single short " +
			"paragraph that preserves key facts, tool outputs, and decisions. No tool block syntax."
	case TaskResearchPlan:
		return base + " You are planning a thorough research investigation. Break the user's" +
			" topic into 3-6 focused, non-overlapping sub-questions that together fully cover it," +
			" and for each give 1-3 specific web search queries. Write the title and questions in" +
			" the user's language. Reply with strict JSON only: " +
			`{"title":"...","sub_questions":[{"id":"q1","question":"...","search_queries":["...","..."]}]}.`
	case TaskResearchVerify:
		return base + " You are auditing research coverage. A sub-question is covered only when at" +
			" least two independent sources agree. Given the question and gathered findings, decide" +
			" whether coverage is sufficient; if not, list uncovered sub-question ids, weak/unverified" +
			" claims, and up to 4 new search queries to close the gaps. Reply with strict JSON only: " +
			`{"sufficient":false,"uncovered":["q2"],"weak_claims":["..."],"new_queries":["..."]}.`
	}
	if jsonOutput {
		return base + " Reply with strict JSON only."
	}
	return base
}

// extractJSON strips markdown code fences if present and returns the JSON body.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip ```json ... ``` fences.
	if strings.HasPrefix(s, "```") {
		end := strings.LastIndex(s, "```")
		if end > 3 {
			body := s[3:end]
			// Skip language tag on the first line.
			if i := strings.Index(body, "\n"); i >= 0 {
				body = body[i+1:]
			}
			return strings.TrimSpace(body)
		}
	}
	return s
}

// noopToolRunner is used by task calls — task models never invoke tools.
type noopToolRunner struct{}

func (n *noopToolRunner) Run(_ context.Context, name string, _ []byte) (string, []Citation, error) {
	return "", nil, fmt.Errorf("task model attempted to call tool %q (not allowed)", name)
}
