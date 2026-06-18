package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// GetConvProviderStateKey reads one key from a conversation's provider_state
// JSON blob (used to look up the persistent sandbox session id — §4.5).
func GetConvProviderStateKey(ctx context.Context, db *sql.DB, convID, key string) (string, error) {
	var ps string
	if err := db.QueryRowContext(ctx, `SELECT provider_state FROM conversations WHERE id=?`, convID).Scan(&ps); err != nil {
		return "", err
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(orDefault(ps, "{}")), &m)
	if v, ok := m[key].(string); ok {
		return v, nil
	}
	return "", nil
}

// SetConvProviderStateKey merges one key into a conversation's provider_state.
func SetConvProviderStateKey(ctx context.Context, db *sql.DB, convID, key, value string) error {
	var ps string
	if err := db.QueryRowContext(ctx, `SELECT provider_state FROM conversations WHERE id=?`, convID).Scan(&ps); err != nil {
		return err
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(orDefault(ps, "{}")), &m)
	m[key] = value
	b, _ := json.Marshal(m)
	_, err := db.ExecContext(ctx, `UPDATE conversations SET provider_state=?, updated_at=? WHERE id=?`, string(b), time.Now().Unix(), convID)
	return err
}

// ListConversations returns conversations for a user, optionally filtered by
// project. archivedFilter "any" returns all; "active" hides archived.
func ListConversations(ctx context.Context, db *sql.DB, userID, projectID, archivedFilter string) ([]Conversation, error) {
	q := `SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, '') FROM conversations WHERE user_id=? AND COALESCE(inline_source_conv,'')=''`
	args := []any{userID}
	if projectID == "_none_" {
		q += " AND project_id IS NULL"
	} else if projectID != "" {
		q += " AND project_id=?"
		args = append(args, projectID)
	}
	if archivedFilter == "active" {
		q += " AND archived=0"
	} else if archivedFilter == "archived" {
		q += " AND archived=1"
	}
	q += " ORDER BY pinned DESC, updated_at DESC"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListInlineThreads returns the sub-conversations anchored to messages of the
// given source conversation, owned by userID, oldest first. Used to render the
// inline-thread markers on a conversation's messages (§ text-selection threads).
func ListInlineThreads(ctx context.Context, db *sql.DB, sourceConvID, userID string) ([]Conversation, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, '')
		 FROM conversations WHERE inline_source_conv=? AND user_id=? ORDER BY created_at ASC`, sourceConvID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConversation returns one row checked against userID.
func GetConversation(ctx context.Context, db *sql.DB, id, userID string) (*Conversation, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, '')
		 FROM conversations WHERE id=? AND user_id=?`, id, userID)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetConversationByID looks up a conversation WITHOUT an ownership check —
// reserved for admin endpoints (§8.1 user support / abuse triage). All
// per-user surfaces must go through GetConversation.
func GetConversationByID(ctx context.Context, db *sql.DB, id string) (*Conversation, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, '')
		 FROM conversations WHERE id=?`, id)
	c, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func scanConversation(s scanner) (Conversation, error) {
	var c Conversation
	var pinned, archived, starred int
	var kbIDs, sumBlocks, provState string
	if err := s.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote); err != nil {
		return c, err
	}
	c.Pinned = pinned == 1
	c.Archived = archived == 1
	c.Starred = starred == 1
	c.KBIDs = json.RawMessage(orDefault(kbIDs, "[]"))
	c.SummaryBlocks = json.RawMessage(orDefault(sumBlocks, "[]"))
	c.ProviderState = json.RawMessage(orDefault(provState, "{}"))
	return c, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// CreateConversation inserts a new row.
func CreateConversation(ctx context.Context, db *sql.DB, c Conversation) (*Conversation, error) {
	if c.ID == "" {
		c.ID = genID("conv")
	}
	if len(c.KBIDs) == 0 {
		c.KBIDs = json.RawMessage("[]")
	}
	if len(c.SummaryBlocks) == 0 {
		c.SummaryBlocks = json.RawMessage("[]")
	}
	if len(c.ProviderState) == 0 {
		c.ProviderState = json.RawMessage("{}")
	}
	if c.RAGMode == "" {
		c.RAGMode = "auto"
	}
	if c.Title == "" {
		c.Title = "新对话"
	}
	now := time.Now().Unix()
	var projectID any
	if c.ProjectID == "" {
		projectID = nil
	} else {
		projectID = c.ProjectID
	}
	_, err := db.ExecContext(ctx, `INSERT INTO conversations(
		id, user_id, project_id, title, provider, model_id, kb_ids, rag_mode, summary_blocks, active_leaf_id, provider_state, pinned, archived, starred, created_at, updated_at, inline_source_conv, inline_parent_id, inline_quote
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.UserID, projectID, c.Title, c.Provider, c.ModelID,
		string(c.KBIDs), c.RAGMode, string(c.SummaryBlocks), string(c.ProviderState),
		boolInt(c.Pinned), boolInt(c.Archived), boolInt(c.Starred), now, now,
		c.InlineSourceConv, c.InlineParentID, c.InlineQuote)
	if err != nil {
		return nil, err
	}
	return GetConversation(ctx, db, c.ID, c.UserID)
}

// UpdateConversation writes selected fields.
type ConversationPatch struct {
	Title        *string         `json:"title"`
	ProjectID    *string         `json:"project_id"`
	ModelID      *string         `json:"model_id"`
	Provider     *string         `json:"provider"`
	KBIDs        json.RawMessage `json:"kb_ids"`
	RAGMode      *string         `json:"rag_mode"`
	Pinned       *bool           `json:"pinned"`
	Archived     *bool           `json:"archived"`
	Starred      *bool           `json:"starred"`
	ActiveLeafID *string         `json:"active_leaf_id"`
}

func UpdateConversation(ctx context.Context, db *sql.DB, id, userID string, p ConversationPatch) (*Conversation, error) {
	parts := []string{}
	args := []any{}
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t != "" {
			parts = append(parts, "title=?")
			args = append(args, t)
		}
	}
	if p.ProjectID != nil {
		parts = append(parts, "project_id=?")
		if *p.ProjectID == "" {
			args = append(args, nil)
		} else {
			args = append(args, *p.ProjectID)
		}
	}
	if p.ModelID != nil {
		parts = append(parts, "model_id=?")
		args = append(args, *p.ModelID)
	}
	if p.Provider != nil {
		parts = append(parts, "provider=?")
		args = append(args, *p.Provider)
	}
	if p.KBIDs != nil {
		parts = append(parts, "kb_ids=?")
		args = append(args, string(p.KBIDs))
	}
	if p.RAGMode != nil {
		parts = append(parts, "rag_mode=?")
		args = append(args, *p.RAGMode)
	}
	if p.Pinned != nil {
		parts = append(parts, "pinned=?")
		args = append(args, boolInt(*p.Pinned))
	}
	if p.Archived != nil {
		parts = append(parts, "archived=?")
		args = append(args, boolInt(*p.Archived))
	}
	if p.Starred != nil {
		parts = append(parts, "starred=?")
		args = append(args, boolInt(*p.Starred))
	}
	if p.ActiveLeafID != nil {
		parts = append(parts, "active_leaf_id=?")
		if *p.ActiveLeafID == "" {
			args = append(args, nil)
		} else {
			args = append(args, *p.ActiveLeafID)
		}
	}
	if len(parts) == 0 {
		return GetConversation(ctx, db, id, userID)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	args = append(args, id, userID)
	q := "UPDATE conversations SET " + strings.Join(parts, ", ") + " WHERE id=? AND user_id=?"
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return nil, err
	}
	return GetConversation(ctx, db, id, userID)
}

// inlineDescendants returns every inline sub-conversation transitively anchored
// to rootID (children, grandchildren, …) via inline_source_conv. A sub-thread
// can itself be a source for deeper threads, so this walks the whole subtree;
// the visited set also guards against any accidental cycle. rootID is NOT
// included in the result.
func inlineDescendants(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	seen := map[string]bool{rootID: true}
	var out []string
	frontier := []string{rootID}
	for len(frontier) > 0 {
		var next []string
		for _, pid := range frontier {
			rows, err := db.QueryContext(ctx, "SELECT id FROM conversations WHERE inline_source_conv=?", pid)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
					next = append(next, id)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
		frontier = next
	}
	return out, nil
}

// DeleteConversation removes a row and every inline sub-conversation anchored to
// it (recursively), so deleting a conversation also discards the sub-threads
// spawned from its text selections (§ text-selection threads). Returns the ids
// of the additionally-deleted sub-conversations so the caller can clean up their
// side state (e.g. RAG vectors).
func DeleteConversation(ctx context.Context, db *sql.DB, id, userID string) ([]string, error) {
	res, err := db.ExecContext(ctx, "DELETE FROM conversations WHERE id=? AND user_id=?", id, userID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	children, err := inlineDescendants(ctx, db, id)
	if err != nil {
		return nil, err
	}
	for _, cid := range children {
		_, _ = db.ExecContext(ctx, "DELETE FROM conversations WHERE id=? AND user_id=?", cid, userID)
	}
	return children, nil
}

// DeleteConversationByID removes a conversation regardless of owner — admin
// authority only (the route is gated by requireAdmin). Messages/chunks cascade
// via FK ON DELETE CASCADE; inline sub-conversations are removed recursively
// (their id list is returned for side-state cleanup).
func DeleteConversationByID(ctx context.Context, db *sql.DB, id string) ([]string, error) {
	res, err := db.ExecContext(ctx, "DELETE FROM conversations WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	children, err := inlineDescendants(ctx, db, id)
	if err != nil {
		return nil, err
	}
	for _, cid := range children {
		_, _ = db.ExecContext(ctx, "DELETE FROM conversations WHERE id=?", cid)
	}
	return children, nil
}

// ListMessages walks the active path (parent_id chain) from the active leaf
// back to the root. If leafID is empty, the newest leaf is used. Returned in
// chronological order (root → leaf).
func ListMessages(ctx context.Context, db *sql.DB, convID, leafID string) ([]Message, error) {
	if leafID == "" {
		err := db.QueryRowContext(ctx, `SELECT COALESCE(active_leaf_id, '') FROM conversations WHERE id=?`, convID).Scan(&leafID)
		if err != nil {
			return nil, err
		}
	}
	if leafID == "" {
		// Fall back to newest message.
		err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? ORDER BY created_at DESC LIMIT 1`, convID).Scan(&leafID)
		if errors.Is(err, sql.ErrNoRows) {
			return []Message{}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	// Walk up.
	current := leafID
	out := []Message{}
	for current != "" {
		m, err := GetMessage(ctx, db, current)
		if err != nil {
			return nil, err
		}
		out = append([]Message{*m}, out...)
		current = m.ParentID
	}
	return out, nil
}

// ListAllMessages returns every message of the conversation regardless of
// branch — used by clients that render the full tree (sibling counts/branch
// switching). Sorted by created_at ascending.
func ListAllMessages(ctx context.Context, db *sql.DB, convID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, citations, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms FROM messages WHERE conversation_id=? ORDER BY created_at ASC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessage returns one row.
func GetMessage(ctx context.Context, db *sql.DB, id string) (*Message, error) {
	row := db.QueryRowContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, citations, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms FROM messages WHERE id=?`, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func scanMessage(s scanner) (Message, error) {
	var m Message
	var blocks, raw, atts, cites string
	if err := s.Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.Role, &m.Provider, &m.ModelID, &blocks, &raw, &m.StopReason, &atts, &cites, &m.InputTokens, &m.OutputTokens, &m.CacheReadTokens, &m.CacheWriteTokens, &m.Cost, &m.Currency, &m.Credits, &m.Status, &m.Error, &m.Feedback, &m.CreatedAt, &m.GenMs); err != nil {
		return m, err
	}
	m.Blocks = json.RawMessage(orDefault(blocks, "[]"))
	if raw != "" {
		m.Raw = json.RawMessage(raw)
	}
	m.Attachments = json.RawMessage(orDefault(atts, "[]"))
	m.Citations = json.RawMessage(orDefault(cites, "[]"))
	return m, nil
}

// CreateMessage inserts a new message (assistant placeholder uses status='streaming').
func CreateMessage(ctx context.Context, db *sql.DB, m Message) (*Message, error) {
	if m.ID == "" {
		m.ID = genID("msg")
	}
	if len(m.Blocks) == 0 {
		m.Blocks = json.RawMessage("[]")
	}
	if len(m.Attachments) == 0 {
		m.Attachments = json.RawMessage("[]")
	}
	if len(m.Citations) == 0 {
		m.Citations = json.RawMessage("[]")
	}
	if m.Currency == "" {
		m.Currency = "USD"
	}
	if m.Status == "" {
		m.Status = "complete"
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().Unix()
	}
	var parent any
	if m.ParentID == "" {
		parent = nil
	} else {
		parent = m.ParentID
	}
	var raw any
	if len(m.Raw) > 0 {
		raw = string(m.Raw)
	} else {
		raw = nil
	}
	_, err := db.ExecContext(ctx, `INSERT INTO messages(
		id, conversation_id, parent_id, role, provider, model_id, blocks, raw, stop_reason, attachments, citations,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, status, error, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, parent, m.Role, m.Provider, m.ModelID, string(m.Blocks), raw, m.StopReason,
		string(m.Attachments), string(m.Citations),
		m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.Cost, m.Currency, m.Status, m.Error, m.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Always advance the conversation's active leaf to point at this message so
	// the latest reply is what loads on refresh — branches are still navigable
	// via the explicit PATCH active-leaf endpoint.
	_, _ = db.ExecContext(ctx, `UPDATE conversations SET active_leaf_id=?, updated_at=? WHERE id=?`, m.ID, time.Now().Unix(), m.ConversationID)
	return GetMessage(ctx, db, m.ID)
}

// UpdateMessage writes finishing state (blocks/raw/citations/usage/status/cost).
type MessageFinishPatch struct {
	Blocks           json.RawMessage
	Raw              json.RawMessage
	Citations        json.RawMessage
	StopReason       string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	Cost             float64
	// Credits charged for this turn (user-facing currency; 0 = free).
	Credits          float64
	Status           string
	Error            string
	// GenMs is the wall-clock generation time for the turn (ms), shown per-reply
	// in the UI.
	GenMs int64
}

func FinishMessage(ctx context.Context, db *sql.DB, id string, p MessageFinishPatch) error {
	var raw any
	if len(p.Raw) > 0 {
		raw = string(p.Raw)
	} else {
		raw = nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE messages SET blocks=?, raw=?, citations=?, stop_reason=?, input_tokens=?, output_tokens=?, cache_read_tokens=?, cache_write_tokens=?, cost=?, credits=?, status=?, error=?, gen_ms=? WHERE id=?`,
		string(p.Blocks), raw, string(p.Citations), p.StopReason,
		p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CacheWriteTokens, p.Cost, p.Credits, p.Status, p.Error, p.GenMs, id)
	return err
}

// UpdateMessageContent overwrites a message's blocks in place — used by the
// user "save edit" action that edits a question WITHOUT branching. The caller
// must verify ownership (conversation belongs to the user) first.
func UpdateMessageContent(ctx context.Context, db *sql.DB, id string, blocks json.RawMessage) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET blocks=? WHERE id=?`, string(blocks), id)
	return err
}

// SetMessageFeedback stores a like/dislike on an assistant message.
// Valid values: "like", "dislike", "" (clear).
func SetMessageFeedback(ctx context.Context, db *sql.DB, id, feedback string) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET feedback=? WHERE id=?`, feedback, id)
	return err
}

// SiblingsOf returns ids of messages sharing the same parent and role (or the
// same nil parent for roots), used by the frontend to render the < n/m >
// branch picker.
func SiblingsOf(ctx context.Context, db *sql.DB, m Message) ([]string, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if m.ParentID == "" {
		rows, err = db.QueryContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id IS NULL AND role=? ORDER BY created_at ASC`, m.ConversationID, m.Role)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id=? AND role=? ORDER BY created_at ASC`, m.ConversationID, m.ParentID, m.Role)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// LatestAssistantInSubtree finds the youngest assistant descendant reachable
// from msgID — used by "switch to this sibling" to advance the active_leaf to
// the bottom of the chosen branch.
func LatestAssistantInSubtree(ctx context.Context, db *sql.DB, convID, msgID string) (string, error) {
	current := msgID
	for {
		var child string
		err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id=? ORDER BY created_at DESC LIMIT 1`, convID, current).Scan(&child)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		current = child
	}
}

// DeleteRound removes one conversational round — a user message together with
// ALL of its assistant replies (every regenerated variant) — identified by ANY
// message id inside the round (the question OR any of its answers resolve to the
// same round). It is branch-safe and non-destructive to the rest of the thread:
//
//   - sibling rounds (other children of the round's parent) are untouched;
//   - everything that came AFTER the round is preserved by re-parenting each
//     continuation onto the round's own parent BEFORE deleting (so the FK
//     ON DELETE CASCADE can't take later messages with it);
//   - the active leaf is only re-pointed when it was itself part of the round.
//
// Returns the conversation's (possibly new) active leaf id.
func DeleteRound(ctx context.Context, db *sql.DB, convID, userID, msgID string) (string, error) {
	var owner string
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM conversations WHERE id=?`, convID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if owner != userID {
		return "", ErrNotFound
	}
	m, err := GetMessage(ctx, db, msgID)
	if err != nil {
		return "", err
	}
	if m.ConversationID != convID {
		return "", ErrNotFound
	}

	// Resolve the round's user message U (and its parent P). Clicking an answer
	// walks up to its question; clicking the question uses it directly.
	uID, uParent := m.ID, m.ParentID
	roundIsUser := m.Role == "user"
	if !roundIsUser && m.ParentID != "" {
		if pu, perr := GetMessage(ctx, db, m.ParentID); perr == nil && pu.Role == "user" {
			uID, uParent, roundIsUser = pu.ID, pu.ParentID, true
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	deletable := []string{uID}
	if roundIsUser {
		// Children of U are the answer variants. Re-parent each variant's
		// continuation onto P, then delete the variants (and U below).
		answers, err := childIDsTx(ctx, tx, convID, uID)
		if err != nil {
			return "", err
		}
		for _, aid := range answers {
			if err := reparentChildrenTx(ctx, tx, convID, aid, uParent); err != nil {
				return "", err
			}
			deletable = append(deletable, aid)
		}
	} else {
		// Degenerate (a parentless non-user node): re-parent its own children up
		// and delete just it.
		if err := reparentChildrenTx(ctx, tx, convID, uID, uParent); err != nil {
			return "", err
		}
	}
	for _, id := range deletable {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id=? AND conversation_id=?`, id, convID); err != nil {
			return "", err
		}
	}

	// Re-point the active leaf only if it disappeared with the round.
	var curLeaf string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, convID).Scan(&curLeaf)
	newLeaf := curLeaf
	if curLeaf == "" || !messageExistsTx(ctx, tx, convID, curLeaf) {
		newLeaf, err = deepestLeafFromTx(ctx, tx, convID, uParent)
		if err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET active_leaf_id=NULLIF(?,''), updated_at=? WHERE id=?`, newLeaf, time.Now().Unix(), convID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newLeaf, nil
}

// childIDsTx lists the direct children of parentID, oldest first.
func childIDsTx(ctx context.Context, tx *sql.Tx, convID, parentID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id=? ORDER BY created_at ASC`, convID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// reparentChildrenTx moves every direct child of fromParent onto toParent
// (empty toParent → NULL, i.e. they become roots).
func reparentChildrenTx(ctx context.Context, tx *sql.Tx, convID, fromParent, toParent string) error {
	if toParent == "" {
		_, err := tx.ExecContext(ctx, `UPDATE messages SET parent_id=NULL WHERE conversation_id=? AND parent_id=?`, convID, fromParent)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE messages SET parent_id=? WHERE conversation_id=? AND parent_id=?`, toParent, convID, fromParent)
	return err
}

func messageExistsTx(ctx context.Context, tx *sql.Tx, convID, id string) bool {
	var got string
	err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE id=? AND conversation_id=?`, id, convID).Scan(&got)
	return err == nil
}

// deepestLeafFromTx walks from a node (or, when fromID is empty, the newest root)
// down its newest-child chain to the leaf — the natural place to re-anchor the
// active path after a deletion. Returns "" when the conversation is now empty.
func deepestLeafFromTx(ctx context.Context, tx *sql.Tx, convID, fromID string) (string, error) {
	current := fromID
	if current == "" {
		var root string
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id IS NULL ORDER BY created_at DESC LIMIT 1`, convID).Scan(&root)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		current = root
	}
	for {
		var child string
		err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id=? ORDER BY created_at DESC LIMIT 1`, convID, current).Scan(&child)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		current = child
	}
}
