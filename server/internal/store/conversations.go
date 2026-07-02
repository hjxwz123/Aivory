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
// limit controls the page size (default 200, max 500); offset is the row offset.
func ListConversations(ctx context.Context, db *sql.DB, userID, projectID, archivedFilter string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	// Personal listing ONLY: workspace conversations are fully isolated from the
	// personal space (§workspaces) and are listed via ListWorkspaceConversations.
	q := `SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, '') FROM conversations WHERE user_id=? AND COALESCE(inline_source_conv,'')='' AND COALESCE(workspace_id,'')=''`
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
	q += " ORDER BY pinned DESC, updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
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

// ListWorkspaceConversations lists EVERY member's conversations in a workspace
// (§workspaces — shared history), newest first, enriched with each creator's
// display name + avatar so the sidebar can attribute rows. Membership is the
// CALLER's job (the handler validates before calling).
func ListWorkspaceConversations(ctx context.Context, db *sql.DB, workspaceID, projectID, archivedFilter string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT c.id, c.user_id, COALESCE(c.project_id, ''), c.title, c.provider, c.model_id, c.kb_ids, c.rag_mode, c.summary_blocks, COALESCE(c.active_leaf_id, ''), c.provider_state, c.pinned, c.archived, c.starred, c.created_at, c.updated_at, COALESCE(c.inline_source_conv, ''), COALESCE(c.inline_parent_id, ''), COALESCE(c.inline_quote, ''), COALESCE(c.workspace_id, ''), COALESCE(u.name,''), COALESCE(u.settings,'')
	 FROM conversations c LEFT JOIN users u ON u.id = c.user_id
	 WHERE c.workspace_id=? AND COALESCE(c.inline_source_conv,'')=''`
	args := []any{workspaceID}
	if projectID == "_none_" {
		q += " AND c.project_id IS NULL"
	} else if projectID != "" {
		q += " AND c.project_id=?"
		args = append(args, projectID)
	}
	if archivedFilter == "active" {
		q += " AND c.archived=0"
	} else if archivedFilter == "archived" {
		q += " AND c.archived=1"
	}
	q += " ORDER BY c.pinned DESC, c.updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		var pinned, archived, starred int
		var kbIDs, sumBlocks, provState, settings string
		if err := rows.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote, &c.WorkspaceID, &c.CreatorName, &settings); err != nil {
			return nil, err
		}
		c.Pinned = pinned == 1
		c.Archived = archived == 1
		c.Starred = starred == 1
		c.KBIDs = json.RawMessage(orDefault(kbIDs, "[]"))
		c.SummaryBlocks = json.RawMessage(orDefault(sumBlocks, "[]"))
		c.ProviderState = json.RawMessage(orDefault(provState, "{}"))
		c.CreatorAvatar = avatarFromSettings(settings)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListInlineThreads returns the sub-conversations anchored to messages of the
// given source conversation, owned by userID, oldest first. Used to render the
// inline-thread markers on a conversation's messages (§ text-selection threads).
func ListInlineThreads(ctx context.Context, db *sql.DB, sourceConvID, userID string) ([]Conversation, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, '')
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

// GetConversation returns one row checked against userID. Access = the OWNER,
// or — for a workspace conversation — ANY member of that workspace
// (§workspaces: shared visibility). This is THE access primitive: every
// per-user handler gates through it, so the membership clause here is what
// makes the whole conversation surface (read/reply/branch/regenerate/files)
// workspace-aware at once. Deletion stays creator-only via DeleteConversation's
// own user_id scope.
func GetConversation(ctx context.Context, db *sql.DB, id, userID string) (*Conversation, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, '')
		 FROM conversations WHERE id=? AND (user_id=? OR (COALESCE(workspace_id,'')<>'' AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)))`, id, userID, userID)
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
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, '')
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
	if err := s.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote, &c.WorkspaceID); err != nil {
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
	now := time.Now().Unix()
	var projectID any
	if c.ProjectID == "" {
		projectID = nil
	} else {
		projectID = c.ProjectID
	}
	_, err := db.ExecContext(ctx, `INSERT INTO conversations(
		id, user_id, project_id, title, provider, model_id, kb_ids, rag_mode, summary_blocks, active_leaf_id, provider_state, pinned, archived, starred, created_at, updated_at, inline_source_conv, inline_parent_id, inline_quote, workspace_id
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.UserID, projectID, c.Title, c.Provider, c.ModelID,
		string(c.KBIDs), c.RAGMode, string(c.SummaryBlocks), string(c.ProviderState),
		boolInt(c.Pinned), boolInt(c.Archived), boolInt(c.Starred), now, now,
		c.InlineSourceConv, c.InlineParentID, c.InlineQuote, c.WorkspaceID)
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
	args = append(args, id, userID, userID)
	// Same access predicate as GetConversation: the owner, or any member of the
	// conversation's workspace (§workspaces — members switch branches, rename,
	// attach KBs collaboratively). Deletion is NOT here; it stays creator-only.
	q := "UPDATE conversations SET " + strings.Join(parts, ", ") +
		" WHERE id=? AND (user_id=? OR (COALESCE(workspace_id,'')<>'' AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)))"
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
	// Fetch the conversation's messages once, then walk the parent chain from the
	// leaf in memory. (Previously this issued one GetMessage query per node — an
	// N+1 that made a 200-message thread 200 round-trips.) Output is identical:
	// the active path, root → leaf, chronological.
	all, err := ListAllMessages(ctx, db, convID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Message, len(all))
	for _, m := range all {
		byID[m.ID] = m
	}
	// If active_leaf_id dangles (points at a since-deleted message) the walk would
	// otherwise return an empty path and the conversation would render as blank.
	// Fall back to the newest message, mirroring the empty-leaf branch above.
	if _, ok := byID[leafID]; !ok && len(all) > 0 {
		leafID = all[len(all)-1].ID // ListAllMessages is ORDER BY created_at ASC
	}
	current := leafID
	seen := make(map[string]bool, len(all)) // cycle guard against corrupt parent links
	out := []Message{}
	for current != "" && !seen[current] {
		m, ok := byID[current]
		if !ok {
			break
		}
		seen[current] = true
		out = append([]Message{m}, out...)
		current = m.ParentID
	}
	return out, nil
}

// ListAllMessages returns every message of the conversation regardless of
// branch — used by clients that render the full tree (sibling counts/branch
// switching). Sorted by created_at ascending.
func ListAllMessages(ctx context.Context, db *sql.DB, convID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, COALESCE(model_label,''), blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, citations, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms, COALESCE(verify,''), COALESCE(author_id,'') FROM messages WHERE conversation_id=? ORDER BY created_at ASC`, convID)
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
	row := db.QueryRowContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, COALESCE(model_label,''), blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, citations, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms, COALESCE(verify,''), COALESCE(author_id,'') FROM messages WHERE id=?`, id)
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
	var blocks, raw, atts, cites, verify string
	if err := s.Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.Role, &m.Provider, &m.ModelID, &m.ModelLabel, &blocks, &raw, &m.StopReason, &atts, &cites, &m.InputTokens, &m.OutputTokens, &m.CacheReadTokens, &m.CacheWriteTokens, &m.Cost, &m.Currency, &m.Credits, &m.Status, &m.Error, &m.Feedback, &m.CreatedAt, &m.GenMs, &verify, &m.AuthorID); err != nil {
		return m, err
	}
	m.Blocks = json.RawMessage(orDefault(blocks, "[]"))
	if raw != "" {
		m.Raw = json.RawMessage(raw)
	}
	m.Attachments = json.RawMessage(orDefault(atts, "[]"))
	m.Citations = json.RawMessage(orDefault(cites, "[]"))
	// Only set Verify when audited, so `omitempty` keeps it off the wire otherwise.
	if verify != "" {
		m.Verify = json.RawMessage(verify)
	}
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
	// Auto-populate model_label from the models table when the caller hasn't set it.
	// This ensures historical messages display the correct model name even if the model is later deleted.
	if m.ModelLabel == "" && m.ModelID != "" {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(label,'') FROM models WHERE id=?`, m.ModelID).Scan(&m.ModelLabel)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO messages(
		id, conversation_id, parent_id, role, provider, model_id, model_label, blocks, raw, stop_reason, attachments, citations,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, status, error, search_text, author_id, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, parent, m.Role, m.Provider, m.ModelID, m.ModelLabel, string(m.Blocks), raw, m.StopReason,
		string(m.Attachments), string(m.Citations),
		m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.Cost, m.Currency, m.Status, m.Error, searchTextFromBlocks(m.Blocks), m.AuthorID, m.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Always advance the conversation's active leaf to point at this message so
	// the latest reply is what loads on refresh — branches are still navigable
	// via the explicit PATCH active-leaf endpoint.
	_, _ = db.ExecContext(ctx, `UPDATE conversations SET active_leaf_id=?, updated_at=? WHERE id=?`, m.ID, time.Now().Unix(), m.ConversationID)
	return GetMessage(ctx, db, m.ID)
}

// ImportMessageInput is one node of an imported conversation tree (§ conversation
// import). ClientID / ParentClientID are the SOURCE platform's ids; they are
// remapped to fresh server ids on insert so parent links survive the migration.
// Callers MUST order msgs parent-before-child.
type ImportMessageInput struct {
	ClientID       string
	ParentClientID string
	Role           string // "user" | "assistant"
	Content        string // plain text (already stripped of images/details/etc.)
}

// ImportConversation creates a conversation and its message TREE from an external
// export. Each message's ClientID is remapped to a fresh server id and parent
// links are rewired through that map; the conversation's active leaf is set to
// the remapped activeLeafClientID. created_at is assigned sequentially so sibling
// order (SiblingsOf orders by created_at) matches the input order. Reuses
// CreateConversation/CreateMessage so blocks/search_text stay consistent with
// natively-created turns. Returns the new conversation id.
func ImportConversation(ctx context.Context, db *sql.DB, c Conversation, msgs []ImportMessageInput, activeLeafClientID string) (string, error) {
	conv, err := CreateConversation(ctx, db, c)
	if err != nil {
		return "", err
	}
	base := time.Now().Unix()
	idMap := make(map[string]string, len(msgs))
	for i, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		parent := ""
		if m.ParentClientID != "" {
			parent = idMap[m.ParentClientID] // unknown parent → treated as root
		}
		// An empty imported reply (an aborted / never-finished turn in the source
		// export) must not trip the frontend's "no response — retry" banner; mark
		// it stopped (a deliberate, non-error empty turn) rather than complete.
		status := "complete"
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" {
			status = "stopped"
		}
		blocks, _ := json.Marshal([]map[string]string{{"kind": "text", "text": m.Content}})
		created, cerr := CreateMessage(ctx, db, Message{
			ConversationID: conv.ID,
			ParentID:       parent,
			Role:           m.Role,
			Blocks:         json.RawMessage(blocks),
			Status:         status,
			CreatedAt:      base + int64(i),
		})
		if cerr != nil {
			return "", cerr
		}
		idMap[m.ClientID] = created.ID
	}
	// CreateMessage left active_leaf pointing at the last-inserted message; pin it
	// to the export's active leaf so the imported path loads as it was left.
	if leaf := idMap[activeLeafClientID]; leaf != "" {
		_, _ = UpdateConversation(ctx, db, conv.ID, c.UserID, ConversationPatch{ActiveLeafID: &leaf})
	}
	return conv.ID, nil
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
	Credits float64
	Status  string
	Error   string
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
		`UPDATE messages SET blocks=?, raw=?, citations=?, stop_reason=?, input_tokens=?, output_tokens=?, cache_read_tokens=?, cache_write_tokens=?, cost=?, credits=?, status=?, error=?, gen_ms=?, search_text=? WHERE id=?`,
		string(p.Blocks), raw, string(p.Citations), p.StopReason,
		p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CacheWriteTokens, p.Cost, p.Credits, p.Status, p.Error, p.GenMs, searchTextFromBlocks(p.Blocks), id)
	return err
}

// UpdateMessageContent overwrites a message's blocks in place — used by the
// user "save edit" action that edits a question WITHOUT branching. The caller
// must verify ownership (conversation belongs to the user) first.
func UpdateMessageContent(ctx context.Context, db *sql.DB, id string, blocks json.RawMessage) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET blocks=?, search_text=? WHERE id=?`, string(blocks), searchTextFromBlocks(blocks), id)
	return err
}

// SetMessageFeedback stores a like/dislike on an assistant message.
// Valid values: "like", "dislike", "" (clear).
func SetMessageFeedback(ctx context.Context, db *sql.DB, id, feedback string) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET feedback=? WHERE id=?`, feedback, id)
	return err
}

// SetMessageVerify stores the secondary-auditor result (Verify mode, §verify) on
// an assistant message AFTER the turn has finalized. The value is the verify
// report JSON; '' clears it.
func SetMessageVerify(ctx context.Context, db *sql.DB, id string, verify json.RawMessage) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET verify=? WHERE id=?`, string(verify), id)
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

// siblingKey is the lookup key used by BatchSiblingsOf to group messages that
// share a parent slot: (conversationID, parentID-or-"", role).
type siblingKey struct {
	ConversationID string
	ParentID       string // empty means root (parent_id IS NULL)
	Role           string
}

// SiblingGroup holds the ordered sibling ids for one branch slot.
type SiblingGroup struct {
	IDs []string
}

// BatchSiblingsOf resolves sibling lists for every message in msgs with a single
// SQL query per conversation instead of one query per message. The returned map
// is keyed by message id; every message in msgs has an entry.
func BatchSiblingsOf(ctx context.Context, db *sql.DB, msgs []Message) (map[string][]string, error) {
	result := make(map[string][]string, len(msgs))
	if len(msgs) == 0 {
		return result, nil
	}

	// Group messages by (conversationID, parentID, role) — sibling scope.
	type groupEntry struct {
		msgIDs []string // which input message IDs belong to this group
		key    siblingKey
	}
	byKey := map[siblingKey]*groupEntry{}
	for _, m := range msgs {
		k := siblingKey{ConversationID: m.ConversationID, ParentID: m.ParentID, Role: m.Role}
		if e, ok := byKey[k]; ok {
			e.msgIDs = append(e.msgIDs, m.ID)
		} else {
			byKey[k] = &groupEntry{key: k, msgIDs: []string{m.ID}}
		}
	}

	// For each unique scope, fetch ordered sibling ids (one query per scope, but
	// there are at most as many scopes as distinct (parent, role) pairs — typically
	// far fewer than the number of messages).
	siblingsByScope := map[siblingKey][]string{}
	for k := range byKey {
		var (
			rows *sql.Rows
			err  error
		)
		if k.ParentID == "" {
			rows, err = db.QueryContext(ctx,
				`SELECT id FROM messages WHERE conversation_id=? AND parent_id IS NULL AND role=? ORDER BY created_at ASC`,
				k.ConversationID, k.Role)
		} else {
			rows, err = db.QueryContext(ctx,
				`SELECT id FROM messages WHERE conversation_id=? AND parent_id=? AND role=? ORDER BY created_at ASC`,
				k.ConversationID, k.ParentID, k.Role)
		}
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		siblingsByScope[k] = ids
	}

	// Attach each input message's sibling list from the pre-fetched scope.
	for _, m := range msgs {
		k := siblingKey{ConversationID: m.ConversationID, ParentID: m.ParentID, Role: m.Role}
		result[m.ID] = siblingsByScope[k]
	}
	return result, nil
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
	var owner, workspaceID string
	if err := db.QueryRowContext(ctx, `SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID).Scan(&owner, &workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	// §workspaces: the creator may always delete. A non-creator member may only
	// reach this point for a round the caller (deleteMessageHandler) already
	// verified belongs to THEM — this check just admits workspace participants;
	// it does not re-derive per-round authorship.
	if owner != userID {
		if workspaceID == "" {
			return "", ErrNotFound
		}
		if role, err := IsWorkspaceMember(ctx, db, workspaceID, userID); err != nil {
			return "", err
		} else if role == "" {
			return "", ErrNotFound
		}
	}
	m, err := GetMessage(ctx, db, msgID)
	if err != nil {
		return "", err
	}
	if m.ConversationID != convID {
		return "", ErrNotFound
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		deletable     []string
		leafBase      string // subtree root the active leaf is re-derived from
		pruneAnchorTs int64  // earliest created_at among the deleted messages
	)

	// Resolve the parent WITHIN the tx — a GetMessage on `db` here would grab a
	// second pool connection and deadlock against this tx's SQLite write lock
	// (single-writer). One read serves both the branch check and the round walk-up.
	var pParent, pRole string
	var pCreated int64
	pFound := false
	if m.ParentID != "" {
		switch perr := tx.QueryRowContext(ctx, `SELECT COALESCE(parent_id,''), role, created_at FROM messages WHERE id=? AND conversation_id=?`, m.ParentID, convID).Scan(&pParent, &pRole, &pCreated); {
		case perr == nil:
			pFound = true
		case errors.Is(perr, sql.ErrNoRows):
			// parent already gone — treat as a root
		default:
			return "", perr
		}
	}

	// A regenerated answer that has SIBLING answers (the `< n/m >` branch picker
	// under the same question) is deleted as ONE branch: remove only this answer
	// and everything downstream on it, never the shared question or the other
	// branches (§4.15 data-loss fix — deleting one branch must not wipe the round).
	branch := false
	if m.Role != "user" && pFound && pRole == "user" {
		sibs, serr := childIDsTx(ctx, tx, convID, m.ParentID)
		if serr != nil {
			return "", serr
		}
		if len(sibs) > 1 {
			branch = true
			leafBase = m.ParentID // re-point onto a surviving sibling / the question
			pruneAnchorTs = m.CreatedAt
			if deletable, err = subtreeIDsTx(ctx, tx, convID, m.ID); err != nil {
				return "", err
			}
		}
	}

	if !branch {
		// Whole-round delete. Resolve the round's user message U (and its parent P):
		// clicking an answer walks up to its question; clicking the question uses it
		// directly. Remove U + all its answer variants, re-parenting each variant's
		// continuation onto P so the surviving thread stays connected.
		uID, uParent := m.ID, m.ParentID
		uCreatedAt := m.CreatedAt
		roundIsUser := m.Role == "user"
		if !roundIsUser && pFound && pRole == "user" {
			uID, uParent, roundIsUser = m.ParentID, pParent, true
			uCreatedAt = pCreated
		}
		leafBase = uParent
		pruneAnchorTs = uCreatedAt
		deletable = []string{uID}
		if roundIsUser {
			answers, aerr := childIDsTx(ctx, tx, convID, uID)
			if aerr != nil {
				return "", aerr
			}
			for _, aid := range answers {
				if err := reparentChildrenTx(ctx, tx, convID, aid, uParent); err != nil {
					return "", err
				}
				deletable = append(deletable, aid)
			}
		} else {
			// Degenerate (a parentless non-user node): re-parent its own children up.
			if err := reparentChildrenTx(ctx, tx, convID, uID, uParent); err != nil {
				return "", err
			}
		}
	}
	for _, id := range deletable {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id=? AND conversation_id=?`, id, convID); err != nil {
			return "", err
		}
	}

	// §4.7 privacy/correctness: a summary block may have rolled up the round being
	// deleted. Drop every block whose [from..anchor] range boundaries on or spans
	// the deleted round, so deleted content stops being re-injected as a summary
	// (the verbatim message is gone, but its summarised essence would otherwise
	// persist and be fed to the model every turn). Re-summarisation happens lazily
	// off the hot path on the next compacting turn.
	deletedSet := make(map[string]bool, len(deletable))
	for _, id := range deletable {
		deletedSet[id] = true
	}
	if err := pruneSummaryBlocksForDeleteTx(ctx, tx, convID, deletedSet, pruneAnchorTs); err != nil {
		return "", err
	}

	// Re-point the active leaf only if it disappeared with the round.
	var curLeaf string
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(active_leaf_id,'') FROM conversations WHERE id=?`, convID).Scan(&curLeaf)
	newLeaf := curLeaf
	if curLeaf == "" || !messageExistsTx(ctx, tx, convID, curLeaf) {
		newLeaf, err = deepestLeafFromTx(ctx, tx, convID, leafBase)
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

// subtreeIDsTx returns rootID and every descendant id (BFS over parent_id) within
// the tx — the full branch removed when deleting a single answer variant.
func subtreeIDsTx(ctx context.Context, tx *sql.Tx, convID, rootID string) ([]string, error) {
	out := []string{rootID}
	for queue := []string{rootID}; len(queue) > 0; {
		id := queue[0]
		queue = queue[1:]
		kids, err := childIDsTx(ctx, tx, convID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, kids...)
		queue = append(queue, kids...)
	}
	return out, nil
}

// msgCreatedAtTx returns a surviving message's created_at within the tx.
func msgCreatedAtTx(ctx context.Context, tx *sql.Tx, convID, id string) (int64, bool) {
	if id == "" {
		return 0, false
	}
	var ts int64
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM messages WHERE conversation_id=? AND id=?`, convID, id).Scan(&ts); err != nil {
		return 0, false
	}
	return ts, true
}

// pruneSummaryBlocksForDeleteTx drops §4.7 summary blocks whose [from..anchor]
// range boundaries on (anchor/from deleted) or spans (deleted round falls inside
// the range by created_at) a deleted message. Each surviving block is preserved
// VERBATIM via json.RawMessage passthrough so unrelated blocks keep their
// level/text/tokens fields intact. Best-effort: a decode failure leaves the
// column untouched rather than blocking the delete.
func pruneSummaryBlocksForDeleteTx(ctx context.Context, tx *sql.Tx, convID string, deleted map[string]bool, deletedAt int64) error {
	var sbRaw string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(summary_blocks,'[]') FROM conversations WHERE id=?`, convID).Scan(&sbRaw); err != nil {
		return nil
	}
	if sbRaw == "" || sbRaw == "[]" {
		return nil
	}
	var raw []json.RawMessage
	if json.Unmarshal([]byte(sbRaw), &raw) != nil || len(raw) == 0 {
		return nil
	}
	kept := make([]json.RawMessage, 0, len(raw))
	changed := false
	for _, br := range raw {
		var meta struct {
			AnchorMessageID string `json:"anchor_message_id"`
			FromMessageID   string `json:"from_message_id"`
		}
		_ = json.Unmarshal(br, &meta)
		drop := deleted[meta.AnchorMessageID] || deleted[meta.FromMessageID]
		if !drop {
			fromAt, okF := msgCreatedAtTx(ctx, tx, convID, meta.FromMessageID)
			anchorAt, okA := msgCreatedAtTx(ctx, tx, convID, meta.AnchorMessageID)
			if okF && okA && fromAt <= deletedAt && deletedAt <= anchorAt {
				drop = true // deleted round falls inside this block's summarised range
			}
		}
		if drop {
			changed = true
			continue
		}
		kept = append(kept, br)
	}
	if !changed {
		return nil
	}
	newRaw, _ := json.Marshal(kept)
	_, err := tx.ExecContext(ctx, `UPDATE conversations SET summary_blocks=? WHERE id=?`, string(newRaw), convID)
	return err
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
