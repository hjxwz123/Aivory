// Package llm — memory worker (§4.16).
//
// Runs entirely off the request path. After a conversation goes idle (we
// trigger it from the orchestrator at the end of every assistant turn), the
// queue picks up the conversation, asks the task model to extract memory
// candidates, then Tier-0 adjudicates by slot (direct replacement only),
// and writes the results.
//
// Tier 0 is conservative on purpose:
//   - If a new memory has a `slot` that matches an existing ACTIVE memory's
//     slot, the old one becomes STALE and the new one is created ACTIVE.
//   - If `slot` is empty, the new memory is appended (no conflict logic).
//   - Tier 1 (semantic propagation across affected_domains) is left as a
//     future LLM-best-effort step; the data model already supports it.
//
// Failures are swallowed — memory is opportunistic; the user's reply must
// never be delayed because the memory worker crashed.
package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

// MemoryWorker runs the §4.16 capture pipeline asynchronously.
type MemoryWorker struct {
	db     *sql.DB
	task   *TaskLLM
	logger *log.Logger
}

// NewMemoryWorker constructs a MemoryWorker.
func NewMemoryWorker(db *sql.DB, task *TaskLLM, logger *log.Logger) *MemoryWorker {
	return &MemoryWorker{db: db, task: task, logger: logger}
}

// candidate is the shape we ask the task model to produce.
type memoryCandidate struct {
	MemoryText string   `json:"memory_text"`
	Slot       string   `json:"slot"`
	Value      string   `json:"value"`
	Type       string   `json:"memory_type"`
	Confidence float64  `json:"confidence"`
	// Status hint from the extractor: ACTIVE (a stable current fact) or
	// QUERY_DEPENDENT (currency depends on the question) per the STALE model.
	Status string `json:"status"`
	// AffectedDomains lists related slots this fact may invalidate (Tier-1).
	AffectedDomains []string `json:"affected_domains"`
}

// existingMem is the slim view used during write-time adjudication.
type existingMem struct {
	ID     string
	Value  string
	Status string
}

// Process scans the conversation's recent messages and creates / updates the
// user's memories. Triggered from the orchestrator post-message, runs in a
// queue worker context with a short deadline.
func (w *MemoryWorker) Process(ctx context.Context, convID string) error {
	if w == nil || w.db == nil {
		return errors.New("memory worker not initialised")
	}
	// Read the conversation + the last 30 messages on the active path.
	row := w.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID)
	var userID, workspaceID string
	if err := row.Scan(&userID, &workspaceID); err != nil {
		return err
	}
	// §workspaces defence-in-depth: NEVER extract memories from shared
	// conversations — other members' words must not become the creator's
	// "user facts", and the creator must not be billed for members' turns.
	// (The orchestrator already gates the enqueue; this survives future callers.)
	if workspaceID != "" {
		return nil
	}
	// Memory must be enabled both globally AND for this user (Personalization
	// toggle) — otherwise extract nothing.
	if !store.MemoryEnabledForUser(ctx, w.db, userID) {
		return nil
	}

	rows, err := w.db.QueryContext(ctx, `SELECT id, role, blocks FROM messages WHERE conversation_id=? ORDER BY created_at DESC LIMIT 30`, convID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var prompt strings.Builder
	prompt.WriteString("Extract durable facts about the user from this conversation. " +
		"Skip transient/contextual info (current task, opinions about content, etc). " +
		"Return JSON array (max 5 items) of:\n" +
		`{"memory_text":"<short sentence>","slot":"<noun key>","value":"<concrete value>","memory_type":"location|preference|identity|schedule|habit|goal|constraint","confidence":0..1,"status":"ACTIVE|QUERY_DEPENDENT","affected_domains":["<slot>"]}` + "\n" +
		"Use a STABLE, canonical slot key per KIND of fact (e.g. always \"language\" for a language preference) — never invent synonymous keys, and never emit two items that mean the same thing.\n" +
		"Use status=QUERY_DEPENDENT when the fact's currency depends on context (plans, temporary states); otherwise ACTIVE.\n\n")
	prompt.WriteString("--- conversation ---\n")
	turns := 0
	msgIDs := []string{}
	for rows.Next() {
		var id, role, blocks string
		if err := rows.Scan(&id, &role, &blocks); err != nil {
			continue
		}
		// §B7 memory-poisoning guard: extract durable facts ONLY from the user's
		// own messages. Assistant/tool text can carry instructions injected via a
		// fetched document or web page, which must never be promoted into the
		// long-term memory that is later injected into every system prompt.
		if role != "user" {
			continue
		}
		msgIDs = append(msgIDs, id)
		var blks []UnifiedBlock
		_ = json.Unmarshal([]byte(blocks), &blks)
		for _, b := range blks {
			if b.Kind != "text" {
				continue
			}
			fmt.Fprintf(&prompt, "[%s] %s\n", role, strings.TrimSpace(b.Text))
		}
		turns++
		if turns >= 20 {
			break
		}
	}
	if turns == 0 {
		return nil
	}

	var candidates []memoryCandidate
	if err := w.task.RunJSON(ctx, TaskMemoryExtract, prompt.String(), &candidates, RunOpts{
		UserID:          userID,
		ConversationID:  convID,
		MaxOutputTokens: 1024,
	}); err != nil {
		// Fall back to nothing — memory is opportunistic.
		return nil
	}
	if len(candidates) == 0 {
		return nil
	}

	// Write-time adjudication (§4.16). Runs off the request path, so it can
	// afford a Tier-1 LLM adjudication call when a slot conflicts.
	for _, c := range candidates {
		w.adjudicateAndWrite(ctx, userID, convID, msgIDs, c)
	}
	return nil
}

// adjudicateAndWrite resolves one candidate against existing same-slot memories
// and writes the result with the correct STALE-model status + provenance.
func (w *MemoryWorker) adjudicateAndWrite(ctx context.Context, userID, convID string, msgIDs []string, c memoryCandidate) {
	text := strings.TrimSpace(c.MemoryText)
	if text == "" {
		return
	}
	conf := c.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.7
	}
	newStatus := strings.ToUpper(strings.TrimSpace(c.Status))
	if newStatus != "QUERY_DEPENDENT" {
		newStatus = "ACTIVE"
	}
	now := time.Now().Unix()

	// Semantic de-dup (§4.16): the extractor frequently re-files the SAME fact
	// under a new wording/slot (e.g. language / response_language /
	// language_preference all meaning "reply in Chinese"), which the slot-keyed
	// logic below can't catch — and slotless facts skip it entirely. Ask the task
	// model whether this fact is already saved under ANY slot; if so, just refresh
	// the existing memory and stop, so we never store the same meaning twice.
	if dupID := w.findSemanticDuplicate(ctx, userID, convID, c); dupID != "" {
		_, _ = w.db.ExecContext(ctx, `UPDATE memories SET updated_at=? WHERE id=?`, now, dupID)
		return
	}

	// Slotless facts have no conflict logic — just append.
	if c.Slot == "" {
		w.createMemory(ctx, userID, convID, msgIDs, c, conf, newStatus, nil)
		return
	}

	// Load existing live memories on the same slot.
	existing := w.existingForSlot(ctx, userID, c.Slot)
	if len(existing) == 0 {
		w.createMemory(ctx, userID, convID, msgIDs, c, conf, newStatus, nil)
		return
	}

	// Same value already recorded → just refresh and stop (avoid duplicates).
	for _, e := range existing {
		if strings.EqualFold(strings.TrimSpace(e.Value), strings.TrimSpace(c.Value)) && e.Status == "ACTIVE" {
			_, _ = w.db.ExecContext(ctx, `UPDATE memories SET updated_at=? WHERE id=?`, now, e.ID)
			return
		}
	}

	// Conflict: ask the Tier-1 adjudicator how to treat each old memory.
	verdicts := w.adjudicate(ctx, userID, convID, c, existing)

	supersedes := []string{}
	newConflicted := false
	coexist := false
	for _, e := range existing {
		switch verdicts[e.ID] {
		case "stale":
			supersedes = append(supersedes, e.ID)
		case "keep":
			// The old fact is judged still current → the NEW one is uncertain.
			newConflicted = true
		case "no_conflict":
			// Old + new are about different facets of the slot (e.g. two roles
			// at different jobs) — keep both, don't supersede.
			coexist = true
		case "unknown_current":
			w.setStatus(ctx, e.ID, "UNKNOWN_CURRENT", "conflicting newer fact, currency unclear", nil)
		default:
			// §4.16 conservative fallback: when the adjudicator's verdict is
			// missing OR unparseable, do NOT silently mark the old fact STALE
			// (the previous behaviour). Instead flag BOTH as UNKNOWN_CURRENT so
			// the model knows neither can be trusted as a present fact, and
			// the user (or a later, clearer message) gets to disambiguate. This
			// reverses the older "default to supersede" path that quietly
			// deleted correct memories whenever the LLM judged returned blank.
			w.setStatus(ctx, e.ID, "UNKNOWN_CURRENT", "conflicting newer fact, unparseable verdict", nil)
			newStatus = "UNKNOWN_CURRENT"
		}
	}
	if newConflicted && len(supersedes) == 0 {
		newStatus = "UNKNOWN_CURRENT"
	}
	if coexist && len(supersedes) == 0 {
		// Coexistence path: write new at ACTIVE (or extractor-hinted), do NOT
		// touch the old memory. The model will see both.
		// (newStatus already correct.)
	}

	newID := w.createMemory(ctx, userID, convID, msgIDs, c, conf, newStatus, supersedes)
	// Mark superseded olds STALE and back-link superseded_by.
	for _, oldID := range supersedes {
		w.setStatus(ctx, oldID, "STALE", "superseded by new fact", []string{newID})
	}
}

func (w *MemoryWorker) existingForSlot(ctx context.Context, userID, slot string) []existingMem {
	rows, err := w.db.QueryContext(ctx,
		`SELECT id, value, status FROM memories WHERE user_id=? AND slot=? AND status IN ('ACTIVE','UNKNOWN_CURRENT','QUERY_DEPENDENT') ORDER BY updated_at DESC LIMIT 10`,
		userID, slot)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []existingMem{}
	for rows.Next() {
		var e existingMem
		if err := rows.Scan(&e.ID, &e.Value, &e.Status); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// adjudicate asks the task model to decide keep|stale|unknown_current for each
// existing memory given the new fact. Returns an empty map if unavailable.
func (w *MemoryWorker) adjudicate(ctx context.Context, userID, convID string, c memoryCandidate, existing []existingMem) map[string]string {
	if w.task == nil {
		return nil
	}
	var p strings.Builder
	fmt.Fprintf(&p, "New fact (slot=%q): %s = %q\n\n", c.Slot, c.MemoryText, c.Value)
	p.WriteString("Existing memories on the same slot:\n")
	for _, e := range existing {
		fmt.Fprintf(&p, "- id=%s value=%q\n", e.ID, e.Value)
	}
	p.WriteString("\nFor each existing id decide whether the new fact makes it: " +
		"`stale` (superseded by the new fact), `keep` (old still correct, new is wrong/uncertain), " +
		"`no_conflict` (different facet — both can be true simultaneously, e.g. different roles at different jobs), " +
		"or `unknown_current` (can't tell which is current).\n" +
		"Reply as JSON: {\"<id1>\":\"stale|keep|no_conflict|unknown_current\", …}. " +
		"Be conservative: when in doubt, use unknown_current — never `stale`.")
	var verdicts map[string]string
	if err := w.task.RunJSON(ctx, TaskMemoryAdjudicate, p.String(), &verdicts, RunOpts{
		UserID: userID, ConversationID: convID, MaxOutputTokens: 256,
	}); err != nil {
		return nil
	}
	return verdicts
}

// findSemanticDuplicate asks the task model whether the candidate is the SAME
// fact (same meaning) as one already saved — across ALL slots/wordings, not just
// the exact slot. Returns the matching memory id, or "" when the fact is new or a
// genuine change to a different value. This is what stops three differently-worded
// "reply in Chinese" memories from all being stored. The returned id is validated
// against the set we showed the model, so a hallucinated id can't refresh the
// wrong row; the dedicated system prompt avoids the keep/stale adjudication framing.
func (w *MemoryWorker) findSemanticDuplicate(ctx context.Context, userID, convID string, c memoryCandidate) string {
	if w.task == nil {
		return ""
	}
	rows, err := w.db.QueryContext(ctx,
		`SELECT id, memory_text FROM memories WHERE user_id=? AND status IN ('ACTIVE','QUERY_DEPENDENT') ORDER BY updated_at DESC LIMIT 40`,
		userID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	type em struct{ id, text string }
	var mems []em
	for rows.Next() {
		var e em
		if rows.Scan(&e.id, &e.text) == nil {
			mems = append(mems, e)
		}
	}
	if len(mems) == 0 {
		return ""
	}
	// Cheap exact-text short-circuit (re-extracted identical sentence) — saves the
	// LLM round-trip for the obvious case.
	ctext := strings.ToLower(strings.TrimSpace(c.MemoryText))
	for _, m := range mems {
		if strings.ToLower(strings.TrimSpace(m.text)) == ctext {
			return m.id
		}
	}
	var p strings.Builder
	fmt.Fprintf(&p, "New fact: %s\n\nExisting saved memories:\n", strings.TrimSpace(c.MemoryText))
	for _, m := range mems {
		fmt.Fprintf(&p, "- id=%s: %s\n", m.id, m.text)
	}
	p.WriteString("\nIf the new fact conveys the SAME information as one of the existing memories " +
		"(semantically equivalent — same meaning, even if worded differently or filed under a different key), " +
		"reply with that memory's id. If the new fact is genuinely NEW, or it CHANGES an existing fact to a " +
		"DIFFERENT value, reply with an empty id. " +
		`Reply with strict JSON only: {"duplicate_of":"<id or empty>"}.`)
	var out struct {
		DuplicateOf string `json:"duplicate_of"`
	}
	if err := w.task.RunJSON(ctx, TaskMemoryAdjudicate, p.String(), &out, RunOpts{
		UserID:          userID,
		ConversationID:  convID,
		MaxOutputTokens: 64,
		SystemPrompt:    "You are a deduplication checker for a user-memory store. Decide whether a new fact already exists in the saved set. Reply with strict JSON only — no prose.",
	}); err != nil {
		return ""
	}
	id := strings.TrimSpace(out.DuplicateOf)
	for _, m := range mems {
		if m.id == id {
			return id
		}
	}
	return ""
}

func (w *MemoryWorker) createMemory(ctx context.Context, userID, convID string, msgIDs []string, c memoryCandidate, conf float64, status string, supersedes []string) string {
	m, err := store.CreateMemory(ctx, w.db, store.Memory{
		UserID:           userID,
		MemoryText:       strings.TrimSpace(c.MemoryText),
		Slot:             c.Slot,
		Value:            c.Value,
		MemoryType:       c.Type,
		Confidence:       conf,
		Status:           status,
		SourceMessageIDs: msgIDs,
		Supersedes:       supersedes,
		AffectedDomains:  c.AffectedDomains,
		Reason:           "extracted from conversation " + convID,
	})
	if err != nil {
		w.logger.Printf("memory: create: %v", err)
		return ""
	}
	return m.ID
}

func (w *MemoryWorker) setStatus(ctx context.Context, id, status, reason string, supersededBy []string) {
	now := time.Now().Unix()
	if supersededBy != nil {
		by, _ := json.Marshal(supersededBy)
		_, _ = w.db.ExecContext(ctx,
			`UPDATE memories SET status=?, reason=?, superseded_by=?, updated_at=? WHERE id=?`,
			status, reason, string(by), now, id)
		return
	}
	_, _ = w.db.ExecContext(ctx,
		`UPDATE memories SET status=?, reason=?, updated_at=? WHERE id=?`,
		status, reason, now, id)
}

// MemoryEnqueuer is the slim queue surface MemoryWorker uses; satisfied by
// queue.Queue. Kept here (with queue.Job inlined) so this file doesn't pull
// in the queue package and create a cycle.
type MemoryEnqueuer interface {
	Enqueue(name string, job func(ctx context.Context) error)
}

// EnqueueIfReady is what the orchestrator calls after the assistant message is
// finalised. It checks settings.memory_enabled and dispatches to the queue.
func (w *MemoryWorker) EnqueueIfReady(q MemoryEnqueuer, convID string) {
	if w == nil || q == nil {
		return
	}
	q.Enqueue("memory.process", func(ctx context.Context) error {
		return w.Process(ctx, convID)
	})
}
