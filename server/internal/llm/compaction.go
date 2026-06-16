// Package llm — long-context compaction (§4.7).
//
// Strategy (single tier in this build):
//   - Settings.keep_recent_rounds = N : keep last N rounds verbatim
//   - Older rounds get rolled into ONE summary text block per compaction.
//   - Compaction only changes WHAT WE SEND TO THE MODEL — the messages
//     table always holds the full history.
//   - The summary block is recorded on the conversation (summary_blocks JSON
//     column) so subsequent turns reuse the same text (prefix cache friendly).
//
// We deliberately don't (yet) implement the tiered "compact the summaries"
// fallback or per-node anchoring — those are valuable but P2 and require
// real task model calls to be tuned. The interface below is shaped so the
// upgrade is a drop-in: every summary block carries `level` and
// `anchor_message_id`.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

// SummaryBlock is one rolled-up segment of older conversation history.
type SummaryBlock struct {
	Level           int    `json:"level"`
	AnchorMessageID string `json:"anchor_message_id"`
	FromMessageID   string `json:"from_message_id"`
	Text            string `json:"text"`
	Tokens          int    `json:"tokens"`
}

// LoadSummaryBlocks decodes the conversation's stored summary_blocks JSON.
func LoadSummaryBlocks(raw json.RawMessage) []SummaryBlock {
	if len(raw) == 0 {
		return nil
	}
	var out []SummaryBlock
	_ = json.Unmarshal(raw, &out)
	return out
}

// ApplySummaryBlocks renders the (already path-filtered) summary blocks into a
// text fragment. The result is empty when there are no summaries.
func ApplySummaryBlocks(blocks []SummaryBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Earlier conversation (summarised)\n")
	for i, s := range blocks {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, strings.TrimSpace(s.Text))
	}
	return b.String()
}

// filterBlocksForPath keeps only summaries anchored to a message on the current
// active path (§4.15) — so a summary written on one branch never bleeds into a
// sibling branch. Blocks with no anchor (legacy) are kept for safety.
func filterBlocksForPath(blocks []SummaryBlock, history []store.Message) []SummaryBlock {
	pathIDs := map[string]bool{}
	for _, m := range history {
		pathIDs[m.ID] = true
	}
	out := []SummaryBlock{}
	for _, b := range blocks {
		if b.AnchorMessageID == "" || pathIDs[b.AnchorMessageID] {
			out = append(out, b)
		}
	}
	return out
}

// estimateHistoryTokens approximates the token footprint of the kept history.
func estimateHistoryTokens(msgs []store.Message) int {
	total := 0
	for _, m := range msgs {
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		for _, b := range blocks {
			total += estimateTokens(b.Text) + estimateTokens(b.Summary)
		}
		// Per-message structural overhead (role markers, block framing): a few
		// tokens each so deep, short-message histories aren't undercounted.
		total += 4
	}
	return total
}

// contextTokens reports the best available measure of how big the prompt
// actually is — used to decide token-triggered compaction (§4.7).
//
// Preferred: the provider's OWN count from the most recent assistant turn —
// input_tokens + cache_read_tokens. That total is exactly what was sent last
// turn (system prompt + tool defs + RAG + full kept history), so it reflects
// real context-window pressure with zero estimation error and no extra API
// call. cache_read_tokens MUST be included: with prompt caching most of the
// context is billed as cached, so input_tokens alone undercounts heavily.
//
// Fallback (first turn, or a freshly-imported history with no recorded usage):
// the CJK-aware heuristic over the kept history. Returns exact=false so callers
// know it's only an estimate of the history portion.
func contextTokens(history []store.Message) (tokens int, exact bool) {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role == "assistant" && m.InputTokens > 0 {
			return m.InputTokens + m.CacheReadTokens, true
		}
	}
	return estimateHistoryTokens(history), false
}

// MaybeCompact is called by the orchestrator before assembling a request. It
// inspects the history depth and, if it exceeds `keep_recent_rounds * 2`
// (one round = user + assistant), takes the overflow rows, calls TaskLLM to
// produce a summary, writes it to the conversation, and returns the rolled
// history + the summary block list.
//
// Failures fall back to the original history — compaction never crashes the
// main turn.
//
// §4.7 stable-prefix invariants this respects:
//   - Each turn-block range is summarised AT MOST ONCE. We track the high-water
//     mark in summary_blocks[last].AnchorMessageID; on the next pass we only
//     summarise messages whose seq comes AFTER that anchor, never re-rolling
//     ranges we already condensed. That makes the prompt-prefix
//     `[system] + [summary blocks 1..N]` stable across turns — a hard
//     requirement for the §4.9 prompt cache to keep working.
//   - The token budget is a fraction of the model's context window (NOT a hard
//     absolute), and the estimator counts CJK characters as full tokens because
//     `len(s)/4` undercounts Chinese text by ~3×.
func MaybeCompact(
	ctx context.Context,
	db *sql.DB,
	task *TaskLLM,
	conv *store.Conversation,
	history []store.Message,
) ([]store.Message, []SummaryBlock, error) {
	// Read settings.
	enabled := true
	if raw, err := store.GetSetting(db, "compaction_enabled"); err == nil {
		_ = json.Unmarshal(raw, &enabled)
	}
	if !enabled {
		return history, LoadSummaryBlocks(conv.SummaryBlocks), nil
	}
	keepRounds := 6
	if raw, err := store.GetSetting(db, "keep_recent_rounds"); err == nil {
		_ = json.Unmarshal(raw, &keepRounds)
	}
	if keepRounds <= 0 {
		keepRounds = 6
	}
	// Total-context token budget: compact once the real prompt (system + tools +
	// RAG + history) crosses this. Default sized to keep prompts cheap/cache-
	// friendly while preserving plenty of recent turns; admin-tunable.
	tokenTrigger := 32000
	if raw, err := store.GetSetting(db, "compaction_token_trigger"); err == nil {
		_ = json.Unmarshal(raw, &tokenTrigger)
	}
	summaryMaxTokens := 1500
	if raw, err := store.GetSetting(db, "summary_max_tokens"); err == nil {
		_ = json.Unmarshal(raw, &summaryMaxTokens)
	}

	existing := LoadSummaryBlocks(conv.SummaryBlocks)

	// Dual trigger (§4.7): compact when EITHER the round budget OR the token
	// budget is exceeded. Token size prefers the provider's real prompt count
	// from the last turn (input + cached prefix), falling back to a heuristic.
	keepMsgs := keepRounds * 2
	ctxTok, _ := contextTokens(history)
	if len(history) <= keepMsgs && ctxTok <= tokenTrigger {
		return history, filterBlocksForPath(existing, history), nil
	}
	// Find the cut: the first index that aligns to a round boundary so we
	// don't split tool_use / tool_result. In our blocks-based history this is
	// safe to do at any user-prefix.
	cut := len(history) - keepMsgs
	if cut < 1 {
		cut = 1 // token-triggered with few messages: still summarise the oldest
	}
	// Make sure we cut at a user message boundary so we never split a
	// tool_use / tool_result pair.
	for cut > 0 && history[cut].Role != "user" {
		cut--
	}
	if cut == 0 {
		return history, filterBlocksForPath(existing, history), nil
	}
	older := history[:cut]
	keep := history[cut:]

	// High-water mark: the index in `older` immediately AFTER the last message
	// already summarised on this path. We only feed the model the NEW range,
	// keeping summary blocks immutable so the cache prefix stays stable
	// (§4.7 "每块只从原文摘一次", §4.9 cache friendliness).
	pathExisting := filterBlocksForPath(existing, history)
	highWater := 0
	if len(pathExisting) > 0 {
		anchor := pathExisting[len(pathExisting)-1].AnchorMessageID
		for i, m := range older {
			if m.ID == anchor {
				highWater = i + 1
				break
			}
		}
	}
	if highWater >= len(older) {
		// Nothing new to summarise — the existing prefix already covers it.
		return keep, pathExisting, nil
	}
	newer := older[highWater:]
	if len(newer) == 0 {
		return keep, pathExisting, nil
	}

	// Build a prompt that asks the task model for a tight summary.
	var prompt strings.Builder
	prompt.WriteString("Compress the conversation rounds below into ONE summary " +
		"that preserves user preferences, decisions, and tool outcomes. " +
		"Keep <300 tokens. Reply with only the summary text.\n\n---\n\n")
	for _, m := range newer {
		role := m.Role
		if role == "" {
			continue
		}
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		fmt.Fprintf(&prompt, "[%s]\n", role)
		for _, b := range blocks {
			switch b.Kind {
			case "text":
				prompt.WriteString(b.Text)
				prompt.WriteString("\n")
			case "tool_call":
				fmt.Fprintf(&prompt, "(tool=%s, summary=%s)\n", b.ToolName, b.Summary)
			}
		}
		prompt.WriteString("\n")
	}

	var text string
	if task != nil {
		text, _ = task.Run(ctx, TaskCompact, prompt.String(), RunOpts{
			UserID:          conv.UserID,
			ConversationID:  conv.ID,
			MaxOutputTokens: 512,
		})
	}
	if strings.TrimSpace(text) == "" {
		// Fall back to a deterministic clip so the system never blocks.
		text = clipOlder(newer, 300)
	}
	block := SummaryBlock{
		Level:           1,
		AnchorMessageID: newer[len(newer)-1].ID,
		FromMessageID:   newer[0].ID,
		Text:            strings.TrimSpace(text),
		Tokens:          estimateTokens(text),
	}
	existing = append(existing, block)

	// Tiered merge (§4.7): when the path's summaries themselves exceed the
	// budget, fold the oldest ones into a coarser level-N block. Only consider
	// blocks on the current path so branches stay independent.
	pathBlocks := filterBlocksForPath(existing, history)
	if summaryTokens(pathBlocks) > summaryMaxTokens {
		merged := mergeOldestBlocks(ctx, task, conv, pathBlocks, summaryMaxTokens)
		// Replace the path blocks in `existing` with the merged set (drop the
		// path's old blocks, keep off-path blocks untouched).
		pathSet := map[string]bool{}
		for _, b := range pathBlocks {
			pathSet[b.AnchorMessageID+"|"+b.FromMessageID] = true
		}
		rebuilt := []SummaryBlock{}
		for _, b := range existing {
			if !pathSet[b.AnchorMessageID+"|"+b.FromMessageID] {
				rebuilt = append(rebuilt, b)
			}
		}
		existing = append(rebuilt, merged...)
	}

	encoded, _ := json.Marshal(existing)
	_, _ = db.ExecContext(ctx,
		"UPDATE conversations SET summary_blocks=?, updated_at=? WHERE id=?",
		string(encoded), time.Now().Unix(), conv.ID)
	return keep, filterBlocksForPath(existing, history), nil
}

// summaryTokens sums the token estimate across blocks.
func summaryTokens(blocks []SummaryBlock) int {
	t := 0
	for _, b := range blocks {
		t += b.Tokens
	}
	return t
}

// mergeOldestBlocks folds the oldest half of the path's summary blocks into one
// coarser (level+1) block so the total stays under budget. Never re-summarises
// from summaries-of-summaries beyond one extra level to preserve fidelity.
func mergeOldestBlocks(ctx context.Context, task *TaskLLM, conv *store.Conversation, blocks []SummaryBlock, budget int) []SummaryBlock {
	if len(blocks) < 2 {
		return blocks
	}
	half := len(blocks) / 2
	if half < 1 {
		half = 1
	}
	oldest := blocks[:half]
	rest := blocks[half:]

	var prompt strings.Builder
	prompt.WriteString("Merge these earlier summaries into ONE shorter summary, " +
		"keeping only durable facts, decisions and outcomes. Reply with only the text.\n\n")
	maxLevel := 1
	for _, b := range oldest {
		prompt.WriteString("- ")
		prompt.WriteString(strings.TrimSpace(b.Text))
		prompt.WriteString("\n")
		if b.Level > maxLevel {
			maxLevel = b.Level
		}
	}
	text := ""
	if task != nil {
		text, _ = task.Run(ctx, TaskCompact, prompt.String(), RunOpts{
			UserID:          conv.UserID,
			ConversationID:  conv.ID,
			MaxOutputTokens: budget / 2,
		})
	}
	if strings.TrimSpace(text) == "" {
		// Deterministic fallback: concatenate + clip.
		parts := []string{}
		for _, b := range oldest {
			parts = append(parts, b.Text)
		}
		text = strings.Join(parts, " ")
		if words := strings.Fields(text); len(words) > 250 {
			text = strings.Join(words[:250], " ") + "…"
		}
	}
	coarse := SummaryBlock{
		Level:           maxLevel + 1,
		AnchorMessageID: oldest[len(oldest)-1].AnchorMessageID,
		FromMessageID:   oldest[0].FromMessageID,
		Text:            strings.TrimSpace(text),
		Tokens:          estimateTokens(text),
	}
	return append([]SummaryBlock{coarse}, rest...)
}

// clipOlder collapses old messages into a short text fallback when the task
// model isn't reachable.
func clipOlder(msgs []store.Message, maxWords int) string {
	var b strings.Builder
	words := 0
	for _, m := range msgs {
		var blocks []UnifiedBlock
		_ = json.Unmarshal(m.Blocks, &blocks)
		for _, blk := range blocks {
			if blk.Kind != "text" {
				continue
			}
			fields := strings.Fields(blk.Text)
			for _, w := range fields {
				if words >= maxWords {
					return strings.TrimSpace(b.String()) + "…"
				}
				b.WriteString(w)
				b.WriteRune(' ')
				words++
			}
		}
	}
	return strings.TrimSpace(b.String())
}
