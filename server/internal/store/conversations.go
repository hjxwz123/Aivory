package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Pagination limits for conversation listings.
var (
	listConversationsLimit           = 20
	listConversationsLimit2          = 500
	listWorkspaceConversationsLimit  = 20
	listWorkspaceConversationsLimit2 = 500
)

// NormalizeConversationRAGMode keeps the two server-owned retrieval policies.
// The retired model-driven "tool" mode and any unknown legacy value become
// auto so a stale client or restored database can never suppress document RAG.
func NormalizeConversationRAGMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "inject") {
		return "inject"
	}
	return "auto"
}

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
// limit controls the page size (default 20, max 500); offset is the row offset.
func ListConversations(ctx context.Context, db *sql.DB, userID, projectID, archivedFilter string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = listConversationsLimit
	}
	if limit > listConversationsLimit2 {
		limit = listConversationsLimit2
	}
	// Personal listing ONLY: workspace conversations are fully isolated from the
	// personal space (§workspaces) and are listed via ListWorkspaceConversations.
	q := `SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, fast, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, ''), is_public FROM conversations WHERE user_id=? AND COALESCE(inline_source_conv,'')='' AND COALESCE(workspace_id,'')=''`
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

// ListWorkspaceConversations is the unscoped administrator/maintenance view.
// User-facing callers must use ListWorkspaceConversationsForUser.
func ListWorkspaceConversations(ctx context.Context, db *sql.DB, workspaceID, projectID, archivedFilter string, limit, offset int) ([]Conversation, error) {
	return listWorkspaceConversations(ctx, db, workspaceID, projectID, archivedFilter, "", limit, offset)
}

// ListWorkspaceConversationsForUser lists the caller's private conversations
// plus public shared history while userID remains a current workspace member.
func ListWorkspaceConversationsForUser(ctx context.Context, db *sql.DB, workspaceID, projectID, archivedFilter, userID string, limit, offset int) ([]Conversation, error) {
	return listWorkspaceConversations(ctx, db, workspaceID, projectID, archivedFilter, userID, limit, offset)
}

func listWorkspaceConversations(ctx context.Context, db *sql.DB, workspaceID, projectID, archivedFilter, userID string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = listWorkspaceConversationsLimit
	}
	if limit > listWorkspaceConversationsLimit2 {
		limit = listWorkspaceConversationsLimit2
	}
	q := `SELECT c.id, c.user_id, COALESCE(c.project_id, ''), c.title, c.provider, c.model_id, c.fast, c.kb_ids, c.rag_mode, c.summary_blocks, COALESCE(c.active_leaf_id, ''), c.provider_state, c.pinned, c.archived, c.starred, c.created_at, c.updated_at, COALESCE(c.inline_source_conv, ''), COALESCE(c.inline_parent_id, ''), COALESCE(c.inline_quote, ''), COALESCE(c.workspace_id, ''), c.is_public, COALESCE(u.name,''), COALESCE(u.settings,'')
	 FROM conversations c LEFT JOIN users u ON u.id = c.user_id
	 WHERE c.workspace_id=? AND COALESCE(c.inline_source_conv,'')=''`
	args := []any{workspaceID}
	if userID != "" {
		q += " AND " + conversationResourceAccessPredicate("c")
		args = append(args, workspaceResourceAccessArgs(userID)...)
	}
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
		var pinned, archived, starred, fastI, publicI int
		var kbIDs, sumBlocks, provState, settings string
		if err := rows.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &fastI, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote, &c.WorkspaceID, &publicI, &c.CreatorName, &settings); err != nil {
			return nil, err
		}
		c.Pinned = pinned == 1
		c.Archived = archived == 1
		c.Starred = starred == 1
		c.Fast = fastI == 1
		c.IsPublic = publicI == 1
		c.KBIDs = json.RawMessage(orDefault(kbIDs, "[]"))
		c.RAGMode = NormalizeConversationRAGMode(c.RAGMode)
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
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, fast, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, ''), is_public
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

// GetConversation returns one row checked against userID. Personal rows and
// private workspace rows are creator-only; public workspace rows are available
// to current members. This is THE access primitive for the whole conversation
// surface (read/reply/branch/regenerate/files). Deletion stays creator-only via
// DeleteConversation's own user_id scope.
func GetConversation(ctx context.Context, db *sql.DB, id, userID string) (*Conversation, error) {
	// LEFT JOIN users to carry creator_name/avatar, matching the list endpoint —
	// otherwise a single-conversation fetch (loadOne on the client) returns a row
	// with an empty creator and, when it replaces the list entry, blanks the
	// sidebar's creator badge until a full reload (§workspaces).
	args := []any{id}
	args = append(args, workspaceResourceAccessArgs(userID)...)
	row := db.QueryRowContext(ctx,
		`SELECT c.id, c.user_id, COALESCE(c.project_id, ''), c.title, c.provider, c.model_id, c.fast, c.kb_ids, c.rag_mode, c.summary_blocks, COALESCE(c.active_leaf_id, ''), c.provider_state, c.pinned, c.archived, c.starred, c.created_at, c.updated_at, COALESCE(c.inline_source_conv, ''), COALESCE(c.inline_parent_id, ''), COALESCE(c.inline_quote, ''), COALESCE(c.workspace_id, ''), c.is_public, COALESCE(u.name,''), COALESCE(u.settings,'')
		 FROM conversations c LEFT JOIN users u ON u.id = c.user_id
		 WHERE c.id=? AND `+conversationResourceAccessPredicate("c"), args...)
	c, err := scanConversationWithCreator(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// scanConversationWithCreator scans the base columns PLUS the joined creator
// name + avatar (from users.name / users.settings). Mirrors the list-query scan.
func scanConversationWithCreator(s scanner) (Conversation, error) {
	var c Conversation
	var pinned, archived, starred, fastI, publicI int
	var kbIDs, sumBlocks, provState, settings string
	if err := s.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &fastI, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote, &c.WorkspaceID, &publicI, &c.CreatorName, &settings); err != nil {
		return c, err
	}
	c.Pinned = pinned == 1
	c.Archived = archived == 1
	c.Starred = starred == 1
	c.Fast = fastI == 1
	c.IsPublic = publicI == 1
	c.KBIDs = json.RawMessage(orDefault(kbIDs, "[]"))
	c.RAGMode = NormalizeConversationRAGMode(c.RAGMode)
	c.SummaryBlocks = json.RawMessage(orDefault(sumBlocks, "[]"))
	c.ProviderState = json.RawMessage(orDefault(provState, "{}"))
	c.CreatorAvatar = avatarFromSettings(settings)
	return c, nil
}

// GetConversationByID looks up a conversation WITHOUT an ownership check —
// reserved for admin endpoints (§8.1 user support / abuse triage). All
// per-user surfaces must go through GetConversation.
func GetConversationByID(ctx context.Context, db *sql.DB, id string) (*Conversation, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, COALESCE(project_id, ''), title, provider, model_id, fast, kb_ids, rag_mode, summary_blocks, COALESCE(active_leaf_id, ''), provider_state, pinned, archived, starred, created_at, updated_at, COALESCE(inline_source_conv, ''), COALESCE(inline_parent_id, ''), COALESCE(inline_quote, ''), COALESCE(workspace_id, ''), is_public
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
	var pinned, archived, starred, fastI, publicI int
	var kbIDs, sumBlocks, provState string
	if err := s.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Provider, &c.ModelID, &fastI, &kbIDs, &c.RAGMode, &sumBlocks, &c.ActiveLeafID, &provState, &pinned, &archived, &starred, &c.CreatedAt, &c.UpdatedAt, &c.InlineSourceConv, &c.InlineParentID, &c.InlineQuote, &c.WorkspaceID, &publicI); err != nil {
		return c, err
	}
	c.Pinned = pinned == 1
	c.Archived = archived == 1
	c.Starred = starred == 1
	c.Fast = fastI == 1
	c.IsPublic = publicI == 1
	c.KBIDs = json.RawMessage(orDefault(kbIDs, "[]"))
	c.RAGMode = NormalizeConversationRAGMode(c.RAGMode)
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
	c.RAGMode = NormalizeConversationRAGMode(c.RAGMode)
	now := time.Now().Unix()
	var projectID any
	if c.ProjectID == "" {
		projectID = nil
	} else {
		projectID = c.ProjectID
	}
	insertColumns := `INSERT INTO conversations(
		id, user_id, project_id, title, provider, model_id, fast, kb_ids, rag_mode, summary_blocks, active_leaf_id, provider_state, pinned, archived, starred, created_at, updated_at, inline_source_conv, inline_parent_id, inline_quote, workspace_id, is_public
	)`
	insertArgs := []any{
		c.ID, c.UserID, projectID, c.Title, c.Provider, c.ModelID, boolInt(c.Fast),
		string(c.KBIDs), c.RAGMode, string(c.SummaryBlocks), string(c.ProviderState),
		boolInt(c.Pinned), boolInt(c.Archived), boolInt(c.Starred), now, now,
		c.InlineSourceConv, c.InlineParentID, c.InlineQuote, c.WorkspaceID, boolInt(c.IsPublic),
	}
	var (
		res sql.Result
		err error
	)
	if c.WorkspaceID == "" {
		res, err = db.ExecContext(ctx, insertColumns+`
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, insertArgs...)
	} else {
		tx, txErr := beginWorkspaceMutationTx(ctx, db, c.WorkspaceID)
		if txErr != nil {
			return nil, txErr
		}
		defer tx.Rollback() //nolint:errcheck
		if !c.IsPublic {
			// Admins are not subject to member capability limits; ordinary
			// members need the private-conversation capability. Guests cannot
			// create conversations at all (guarded below).
			var canPrivate int
			if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_private_conversations,0) END
				FROM workspaces w LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
				WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
				c.UserID, c.UserID, c.WorkspaceID, c.UserID, c.UserID).Scan(&canPrivate); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, ErrNotFound
				}
				return nil, err
			}
			if canPrivate != 1 {
				c.IsPublic = true
				insertArgs[len(insertArgs)-1] = boolInt(true)
			}
		}
		res, err = tx.ExecContext(ctx, insertColumns+`
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM workspaces create_workspace
			  WHERE create_workspace.id=?
			    AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
			    AND (
			        create_workspace.owner_id=? OR EXISTS (
			          SELECT 1 FROM workspace_members create_member
			           WHERE create_member.workspace_id=create_workspace.id AND create_member.user_id=?
			             AND `+isCollaboratorRoleSQL("create_member.role")+`
			        )
			    )`, append(insertArgs, c.WorkspaceID, c.UserID, c.UserID, c.UserID)...)
		if err != nil {
			return nil, err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != 1 {
			return nil, ErrNotFound
		}
		created, scanErr := scanConversationWithCreator(tx.QueryRowContext(ctx,
			`SELECT c.id, c.user_id, COALESCE(c.project_id, ''), c.title, c.provider, c.model_id, c.fast, c.kb_ids, c.rag_mode, c.summary_blocks, COALESCE(c.active_leaf_id, ''), c.provider_state, c.pinned, c.archived, c.starred, c.created_at, c.updated_at, COALESCE(c.inline_source_conv, ''), COALESCE(c.inline_parent_id, ''), COALESCE(c.inline_quote, ''), COALESCE(c.workspace_id, ''), c.is_public, COALESCE(u.name,''), COALESCE(u.settings,'')
			   FROM conversations c LEFT JOIN users u ON u.id=c.user_id WHERE c.id=?`, c.ID))
		if scanErr != nil {
			return nil, scanErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &created, nil
	}
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	return GetConversation(ctx, db, c.ID, c.UserID)
}

// UpdateConversation writes selected fields.
type ConversationPatch struct {
	Title        *string         `json:"title"`
	ProjectID    *string         `json:"project_id"`
	ModelID      *string         `json:"model_id"`
	Provider     *string         `json:"provider"`
	Fast         *bool           `json:"fast"` // §fast-mode: 快速/进阶 picker selection
	KBIDs        json.RawMessage `json:"kb_ids"`
	RAGMode      *string         `json:"rag_mode"`
	Pinned       *bool           `json:"pinned"`
	Archived     *bool           `json:"archived"`
	Starred      *bool           `json:"starred"`
	IsPublic     *bool           `json:"is_public"`
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
	if p.Fast != nil {
		parts = append(parts, "fast=?")
		args = append(args, boolInt(*p.Fast))
	}
	if p.KBIDs != nil {
		parts = append(parts, "kb_ids=?")
		args = append(args, string(p.KBIDs))
	}
	if p.RAGMode != nil {
		parts = append(parts, "rag_mode=?")
		args = append(args, NormalizeConversationRAGMode(*p.RAGMode))
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
	if p.IsPublic != nil {
		parts = append(parts, "is_public=?")
		args = append(args, boolInt(*p.IsPublic))
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
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, id,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Workspace conversations are shared resources and deliberately do not have
	// an archive view. Reject archive toggles here as well as in the UI so an API
	// client cannot make a conversation disappear from every workspace listing.
	if p.Archived != nil && workspaceID != "" {
		return nil, ErrNotFound
	}
	// Visibility is a workspace concept — personal rows keep rejecting it.
	if p.IsPublic != nil && workspaceID == "" {
		return nil, ErrNotFound
	}
	args = append(args, id)
	// §workspace RBAC: metadata fields (title, model, KB binding, visibility,
	// project link) are the creator's or a workspace admin's to change. Purely
	// collaborative state (active branch, pin/archive/star toggles) stays open
	// to every non-guest member of a shared conversation. Personal rows are
	// always creator-only under both predicates.
	metadataTouched := p.Title != nil || p.ProjectID != nil || p.ModelID != nil ||
		p.Provider != nil || p.Fast != nil || p.KBIDs != nil || p.RAGMode != nil || p.IsPublic != nil
	var q string
	if metadataTouched {
		args = append(args, workspaceResourceManagerArgs(userID)...)
		q = "UPDATE conversations SET " + strings.Join(parts, ", ") +
			" WHERE id=? AND " + workspaceResourceManagerPredicate("conversations")
	} else {
		args = append(args, conversationMemberMutationArgs(userID)...)
		q = "UPDATE conversations SET " + strings.Join(parts, ", ") +
			" WHERE id=? AND " + conversationMemberMutationPredicate("conversations")
	}
	if p.IsPublic != nil && !*p.IsPublic && workspaceID != "" {
		// Making a workspace conversation private additionally requires the
		// private-conversation capability for ordinary members (admins bypass
		// it via the capability predicate itself).
		q += ` AND EXISTS (
			SELECT 1 FROM workspaces private_workspace
			 WHERE private_workspace.id=conversations.workspace_id
			   AND ` + workspaceMemberCapabilityPredicate("private_workspace", "can_private_conversations") + `
		)`
		args = append(args, workspaceMemberCapabilityArgs(userID)...)
	}
	if workspaceID == "" {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != 1 {
			return nil, ErrNotFound
		}
		return GetConversation(ctx, db, id, userID)
	}

	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	if p.IsPublic != nil && workspaceID != "" {
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditResourceVisibilityChanged,
			"conversation", id, map[string]any{"is_public": *p.IsPublic}); err != nil {
			return nil, err
		}
	}
	updated, err := scanConversationWithCreator(tx.QueryRowContext(ctx,
		`SELECT c.id, c.user_id, COALESCE(c.project_id, ''), c.title, c.provider, c.model_id, c.fast, c.kb_ids, c.rag_mode, c.summary_blocks, COALESCE(c.active_leaf_id, ''), c.provider_state, c.pinned, c.archived, c.starred, c.created_at, c.updated_at, COALESCE(c.inline_source_conv, ''), COALESCE(c.inline_parent_id, ''), COALESCE(c.inline_quote, ''), COALESCE(c.workspace_id, ''), c.is_public, COALESCE(u.name,''), COALESCE(u.settings,'')
		   FROM conversations c LEFT JOIN users u ON u.id=c.user_id WHERE c.id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &updated, nil
}

// inlineDescendants returns every inline sub-conversation transitively anchored
// to rootID (children, grandchildren, …) via inline_source_conv. A sub-thread
// can itself be a source for deeper threads, so this walks the whole subtree;
// the visited set also guards against any accidental cycle. rootID is NOT
// included in the result.
func inlineDescendants(ctx context.Context, db conversationRowsQueryer, rootID string) ([]string, error) {
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

// conversationIDsOwnedBy filters ids to rows owned by userID while preserving
// the caller's traversal order. Both *sql.DB and *sql.Tx implement the query
// surface, allowing user-scoped deletion to derive its complete worklist inside
// the same transaction that performs the delete.
func conversationIDsOwnedBy(ctx context.Context, q conversationRowsQueryer, ids []string, userID string) ([]string, error) {
	ids = cleanIDs(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	args := anySlice(ids)
	args = append(args, userID)
	args = append(args, workspaceResourceAccessArgs(userID)...)
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM conversations
		  WHERE id IN (`+idPlaceholders(len(ids))+`) AND user_id=?
		    AND `+conversationResourceAccessPredicate("conversations"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owned := make(map[string]struct{}, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owned[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(owned))
	for _, id := range ids {
		if _, ok := owned[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// ConversationTreeIDs returns rootID plus every inline sub-conversation anchored
// to it. Callers use this before deletion to collect side-state rows that SQL
// cascades would otherwise hide (files/artifacts/storage refs).
func ConversationTreeIDs(ctx context.Context, db *sql.DB, rootID string) ([]string, error) {
	children, err := inlineDescendants(ctx, db, rootID)
	if err != nil {
		return nil, err
	}
	return append([]string{rootID}, children...), nil
}

// ConversationDeletionState is the exact, transactionally-derived cleanup
// worklist for a user-scoped conversation deletion. ConversationIDs contains the
// root followed by only those descendants that the transaction actually deletes;
// StoragePaths is restricted to that same set.
type ConversationDeletionState struct {
	ConversationIDs []string
	StoragePaths    []string
}

// DeleteConversationWithState removes a user-owned conversation and every
// user-owned inline descendant anchored to it. Inline threads owned by another
// workspace member are deliberately preserved, including their files and cleanup
// side state. The returned IDs and storage paths are computed in the deletion
// transaction so API cleanup cannot run against a descendant that survived.
func DeleteConversationWithState(ctx context.Context, db *sql.DB, id, userID string) (*ConversationDeletionState, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var ownerID, workspaceID string
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, id,
	).Scan(&ownerID, &workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Membership revocation takes this same workspace lock. The authorization
	// and permission-change transactions take this same lock. The authorization
	// decision below therefore remains true until this transaction commits.
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return nil, err
		}
	}
	lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	managerArgs := []any{id}
	managerArgs = append(managerArgs, workspaceResourceManagerArgs(userID)...)
	managerArgs = append(managerArgs, workspaceMemberCapabilityArgs(userID)...)
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM conversations
		  WHERE id=? AND `+workspaceResourceManagerPredicate("conversations")+`
		    AND `+workspaceConversationDeletionCapabilityPredicate("conversations"), managerArgs...,
	).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	creator := ownerID == userID
	// A workspace admin may remove another member's conversation and its full
	// inline subtree. Resolve this only after the membership lock so a concurrent
	// role downgrade cannot leave a stale elevated subtree decision behind.
	adminManager := false
	if workspaceID != "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM workspaces w
			  WHERE w.id=? AND (w.owner_id=? OR EXISTS (
			    SELECT 1 FROM workspace_members dm
			     WHERE dm.workspace_id=w.id AND dm.user_id=? AND `+isAdminRoleSQL("dm.role")+`
			  )))`, workspaceID, userID, userID,
		).Scan(&adminManager); err != nil {
			return nil, err
		}
	}
	children, err := inlineDescendants(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !adminManager || creator {
		// Deleting your own conversation only takes the descendants you own.
		children, err = conversationIDsOwnedBy(ctx, tx, children, userID)
		if err != nil {
			return nil, err
		}
	}
	ids := append([]string{id}, children...)
	storagePaths, err := storagePathsForConversationIDs(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE conversation_id IN (`+idPlaceholders(len(ids))+`)`, anySlice(ids)...); err != nil {
		return nil, err
	}
	deleteArgs := anySlice(ids)
	deleteArgs = append(deleteArgs, workspaceResourceManagerArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`DELETE FROM conversations
		  WHERE id IN (`+idPlaceholders(len(ids))+`) AND `+workspaceResourceManagerPredicate("conversations"), deleteArgs...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != int64(len(ids)) {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ConversationDeletionState{ConversationIDs: ids, StoragePaths: storagePaths}, nil
}

// DeleteConversation preserves the historical store API for callers that only
// need descendant IDs. The slice now contains only descendants actually deleted
// by the user-scoped transaction.
func DeleteConversation(ctx context.Context, db *sql.DB, id, userID string) ([]string, error) {
	state, err := DeleteConversationWithState(ctx, db, id, userID)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), state.ConversationIDs[1:]...), nil
}

// DeleteConversationByID removes a conversation regardless of owner — admin
// authority only (the route is gated by requireAdmin). Messages/chunks cascade
// via FK ON DELETE CASCADE; inline sub-conversations are removed recursively
// (their id list is returned for side-state cleanup).
func DeleteConversationByID(ctx context.Context, db *sql.DB, id string) ([]string, error) {
	children, err := inlineDescendants(ctx, db, id)
	if err != nil {
		return nil, err
	}
	ids := append([]string{id}, children...)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id=?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE conversation_id IN (`+idPlaceholders(len(ids))+`)`, anySlice(ids)...); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM conversations WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	for _, cid := range children {
		_, _ = tx.ExecContext(ctx, "DELETE FROM conversations WHERE id=?", cid)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return children, nil
}

// ResolveConversationAppendParent validates the stored/preferred leaf used by
// a normal append. If it no longer belongs to the conversation, recover to the
// leaf reached from the newest surviving root by following newest children.
// The repaired result is not persisted here: CreateMessage advances the active
// leaf atomically when the append succeeds.
func ResolveConversationAppendParent(ctx context.Context, db *sql.DB, convID, preferredLeaf string) (parentID string, repaired bool, err error) {
	if preferredLeaf != "" {
		var id string
		err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE id=? AND conversation_id=?`, preferredLeaf, convID).Scan(&id)
		if err == nil {
			return preferredLeaf, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}

	parentID, err = deepestLeafFrom(ctx, db, convID, "")
	if err != nil {
		return "", false, err
	}
	return parentID, parentID != preferredLeaf, nil
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
	rows, err := db.QueryContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, COALESCE(model_label,''), fast, blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, COALESCE(selected_user_skill_ids,'[]'), citations, input_tokens, context_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms, COALESCE(verify,''), COALESCE(author_id,'') FROM messages WHERE conversation_id=? ORDER BY created_at ASC`, convID)
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
	row := db.QueryRowContext(ctx, `SELECT id, conversation_id, COALESCE(parent_id,''), role, provider, model_id, COALESCE(model_label,''), fast, blocks, COALESCE(raw,''), COALESCE(stop_reason,''), attachments, COALESCE(selected_user_skill_ids,'[]'), citations, input_tokens, context_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, credits, status, error, COALESCE(feedback,''), created_at, gen_ms, COALESCE(verify,''), COALESCE(author_id,'') FROM messages WHERE id=?`, id)
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
	var blocks, raw, atts, selectedSkills, cites, verify string
	var fastI int
	if err := s.Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.Role, &m.Provider, &m.ModelID, &m.ModelLabel, &fastI, &blocks, &raw, &m.StopReason, &atts, &selectedSkills, &cites, &m.InputTokens, &m.ContextTokens, &m.OutputTokens, &m.CacheReadTokens, &m.CacheWriteTokens, &m.Cost, &m.Currency, &m.Credits, &m.Status, &m.Error, &m.Feedback, &m.CreatedAt, &m.GenMs, &verify, &m.AuthorID); err != nil {
		return m, err
	}
	m.Fast = fastI == 1
	m.Blocks = json.RawMessage(orDefault(blocks, "[]"))
	if raw != "" {
		m.Raw = json.RawMessage(raw)
	}
	m.Attachments = json.RawMessage(orDefault(atts, "[]"))
	m.FeedbackReasons = []string{}
	m.SelectedUserSkillIDs = json.RawMessage(orDefault(selectedSkills, "[]"))
	m.Citations = json.RawMessage(orDefault(cites, "[]"))
	// Only set Verify when audited, so `omitempty` keeps it off the wire otherwise.
	if verify != "" {
		m.Verify = json.RawMessage(verify)
	}
	return m, nil
}

// CreateMessage is the unscoped ingestion/maintenance primitive. User-triggered
// generation must use CreateMessageForUser.
func CreateMessage(ctx context.Context, db *sql.DB, m Message) (*Message, error) {
	return createMessage(ctx, db, m, "")
}

// CreateMessageForUser serializes message persistence against membership
// revocation and verifies the conversation boundary in the same transaction.
func CreateMessageForUser(ctx context.Context, db *sql.DB, m Message, userID string) (*Message, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, ErrNotFound
	}
	return createMessage(ctx, db, m, userID)
}

func createMessage(ctx context.Context, db *sql.DB, m Message, userID string) (*Message, error) {
	if m.ID == "" {
		m.ID = genID("msg")
	}
	if len(m.Blocks) == 0 {
		m.Blocks = json.RawMessage("[]")
	}
	if len(m.Attachments) == 0 {
		m.Attachments = json.RawMessage("[]")
	}
	if len(m.SelectedUserSkillIDs) == 0 {
		m.SelectedUserSkillIDs = json.RawMessage("[]")
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
	var attachedFileIDs []string
	if m.Role == "user" {
		attachedFileIDs = attachmentFileIDs(m.Attachments)
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	workspaceID := ""
	workspaceBillingUserID := ""
	if userID != "" {
		// Every user-triggered row records the principal that initiated it. User
		// rows expose authorship in shared conversations; assistant rows use the
		// same field internally to bind later finalization to the generation owner.
		if strings.TrimSpace(m.AuthorID) != userID {
			return nil, ErrNotFound
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, m.ConversationID,
		).Scan(&workspaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if workspaceID != "" {
			if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
				return nil, err
			}
			if err := tx.QueryRowContext(ctx,
				`SELECT owner_id FROM workspaces WHERE id=?`, workspaceID,
			).Scan(&workspaceBillingUserID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, ErrNotFound
				}
				return nil, err
			}
		}
		lockResult, err := tx.ExecContext(ctx,
			`UPDATE conversations SET id=id WHERE id=?`, m.ConversationID)
		if err != nil {
			return nil, err
		}
		if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != 1 {
			return nil, ErrNotFound
		}
		accessArgs := []any{m.ConversationID}
		accessArgs = append(accessArgs, conversationMemberMutationArgs(userID)...)
		var allowed int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM conversations c
			  WHERE c.id=? AND `+conversationMemberMutationPredicate("c"), accessArgs...,
		).Scan(&allowed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		// Committing a member's workspace draft transfers its quota charge from
		// the uploader to the canonical owner. Re-evaluate the exact transitioning
		// bytes while both serialization locks are held by this transaction.
		if m.Role == "user" && workspaceID != "" && workspaceBillingUserID != "" &&
			strings.TrimSpace(m.AuthorID) != workspaceBillingUserID && len(attachedFileIDs) > 0 {
			quotaArgs := []any{m.ConversationID}
			quotaArgs = append(quotaArgs, anySlice(attachedFileIDs)...)
			quotaArgs = append(quotaArgs, strings.TrimSpace(m.AuthorID))
			var additional sql.NullInt64
			if err := tx.QueryRowContext(ctx,
				`SELECT COALESCE(SUM(size_bytes),0)
				   FROM files
				  WHERE conversation_id=? AND id IN (`+idPlaceholders(len(attachedFileIDs))+`)
				    AND draft=1 AND user_id=? AND kind<>'image'`, quotaArgs...,
			).Scan(&additional); err != nil {
				return nil, err
			}
			if err := enforceStorageQuotaTx(ctx, tx, workspaceBillingUserID, additional.Int64); err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(
		id, conversation_id, parent_id, role, provider, model_id, model_label, fast, blocks, raw, stop_reason, attachments, selected_user_skill_ids, citations,
		input_tokens, context_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, status, error, search_text, author_id, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, parent, m.Role, m.Provider, m.ModelID, m.ModelLabel, boolInt(m.Fast), string(m.Blocks), raw, m.StopReason,
		string(m.Attachments), string(m.SelectedUserSkillIDs), string(m.Citations),
		m.InputTokens, m.ContextTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.Cost, m.Currency, m.Status, m.Error, searchTextFromBlocks(m.Blocks), m.AuthorID, m.CreatedAt)
	if err != nil {
		return nil, err
	}
	// A composer upload is a durable draft until the user message that carries it
	// is persisted. Commit those file rows in the SAME transaction as the message,
	// so a refresh can never observe a saved question whose attachments still look
	// unsent (or an unsaved question whose files were prematurely committed).
	if m.Role == "user" {
		if len(attachedFileIDs) > 0 {
			args := []any{m.ConversationID}
			args = append(args, anySlice(attachedFileIDs)...)
			// Only the uploader may transition a composer draft to committed.
			// The API resolves attachment visibility before reaching this point,
			// but keep the invariant in the transaction as well so a caller cannot
			// commit another workspace member's draft by supplying its id.
			args = append(args, strings.TrimSpace(m.AuthorID))
			if _, err := tx.ExecContext(ctx,
				`UPDATE files SET draft=0 WHERE conversation_id=? AND id IN (`+idPlaceholders(len(attachedFileIDs))+`) AND draft=1 AND user_id=?`, args...); err != nil {
				return nil, err
			}
		}
	}
	// Always advance the conversation's active leaf to point at this message so
	// the latest reply is what loads on refresh — branches are still navigable
	// via the explicit PATCH active-leaf endpoint.
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET active_leaf_id=?, updated_at=? WHERE id=?`, m.ID, time.Now().Unix(), m.ConversationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetMessage(ctx, db, m.ID)
}

// CreateMessagePath inserts a LINEAR chain of messages in ONE transaction and
// points the conversation's active leaf at the last one. Built for fork (§7.x):
// the old per-message CreateMessage loop paid one transaction + fsync per
// copied message, which made forking a long conversation take seconds to tens
// of seconds on SQLite. Parent links are chained automatically (msgs[0] keeps
// its given ParentID, each subsequent message parents on the previous one);
// ids are generated up front. Defaults mirror CreateMessage, except the
// model_label lookup: callers pass the source row's label through verbatim.
// Returns the leaf (last) message id.
func CreateMessagePath(ctx context.Context, db *sql.DB, msgs []Message) (string, error) {
	if len(msgs) == 0 {
		return "", errors.New("empty message path")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages(
		id, conversation_id, parent_id, role, provider, model_id, model_label, fast, blocks, raw, stop_reason, attachments, selected_user_skill_ids, citations,
		input_tokens, context_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost, currency, status, error, search_text, author_id, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return "", err
	}
	defer stmt.Close() //nolint:errcheck
	now := time.Now().Unix()
	parent := msgs[0].ParentID
	last := ""
	for _, m := range msgs {
		m.ID = genID("msg")
		if len(m.Blocks) == 0 {
			m.Blocks = json.RawMessage("[]")
		}
		if len(m.Attachments) == 0 {
			m.Attachments = json.RawMessage("[]")
		}
		if len(m.SelectedUserSkillIDs) == 0 {
			m.SelectedUserSkillIDs = json.RawMessage("[]")
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
			m.CreatedAt = now
		}
		var parentArg any
		if parent == "" {
			parentArg = nil
		} else {
			parentArg = parent
		}
		var raw any
		if len(m.Raw) > 0 {
			raw = string(m.Raw)
		} else {
			raw = nil
		}
		if _, err := stmt.ExecContext(ctx,
			m.ID, m.ConversationID, parentArg, m.Role, m.Provider, m.ModelID, m.ModelLabel, boolInt(m.Fast), string(m.Blocks), raw, m.StopReason,
			string(m.Attachments), string(m.SelectedUserSkillIDs), string(m.Citations),
			m.InputTokens, m.ContextTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.Cost, m.Currency, m.Status, m.Error,
			searchTextFromBlocks(m.Blocks), m.AuthorID, m.CreatedAt); err != nil {
			return "", err
		}
		parent = m.ID
		last = m.ID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET active_leaf_id=?, updated_at=? WHERE id=?`, last, now, msgs[0].ConversationID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return last, nil
}

func attachmentFileIDs(raw json.RawMessage) []string {
	var atts []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &atts) != nil {
		return nil
	}
	ids := make([]string, 0, len(atts))
	for _, att := range atts {
		ids = append(ids, att.ID)
	}
	return cleanIDs(ids)
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
	ContextTokens    int
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

// ErrConversationAccessRevoked means a generation lost access to its workspace
// before terminal persistence. The streaming placeholder is scrubbed and marked
// stopped before this error is returned.
var ErrConversationAccessRevoked = errors.New("conversation access revoked during generation")

type messageFinishExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func execMessageFinish(ctx context.Context, ex messageFinishExecer, id string, p MessageFinishPatch, extraWhere string, extraArgs ...any) (sql.Result, error) {
	var raw any
	if len(p.Raw) > 0 {
		raw = string(p.Raw)
	} else {
		raw = nil
	}
	args := []any{
		string(p.Blocks), raw, string(p.Citations), p.StopReason,
		p.InputTokens, p.ContextTokens, p.OutputTokens, p.CacheReadTokens, p.CacheWriteTokens,
		p.Cost, p.Credits, p.Status, p.Error, p.GenMs, searchTextFromBlocks(p.Blocks), id,
	}
	args = append(args, extraArgs...)
	return ex.ExecContext(ctx,
		`UPDATE messages SET blocks=?, raw=?, citations=?, stop_reason=?, input_tokens=?, context_tokens=?, output_tokens=?, cache_read_tokens=?, cache_write_tokens=?, cost=?, credits=?, status=?, error=?, gen_ms=?, search_text=? WHERE id=?`+extraWhere,
		args...)
}

func FinishMessage(ctx context.Context, db *sql.DB, id string, p MessageFinishPatch) error {
	_, err := execMessageFinish(ctx, db, id, p, "")
	return err
}

// FinishMessageForUser is the user-generation finalizer. It shares the workspace
// membership lock with kick/leave, verifies both the conversation boundary and
// the assistant placeholder's initiating principal, and only then writes model
// output. If revocation wins the lock, it terminalizes the placeholder with no
// generated content so another member never inherits an answer produced after
// the caller lost access.
func FinishMessageForUser(ctx context.Context, db *sql.DB, id, expectedConvID, userID string, p MessageFinishPatch) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(expectedConvID) == "" || strings.TrimSpace(userID) == "" {
		return ErrNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var workspaceID, authorID, status string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(c.workspace_id,''), COALESCE(m.author_id,''), m.status
		   FROM messages m JOIN conversations c ON c.id=m.conversation_id
		  WHERE m.id=? AND m.conversation_id=? AND m.role='assistant'`, id, expectedConvID,
	).Scan(&workspaceID, &authorID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if authorID != userID {
		return ErrNotFound
	}
	if status != "streaming" {
		return ErrConversationAccessRevoked
	}
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return err
		}
	}
	lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, expectedConvID)
	if err != nil {
		return err
	}
	if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}

	accessArgs := []any{id, expectedConvID, userID}
	accessArgs = append(accessArgs, conversationMemberMutationArgs(userID)...)
	var allowed int
	err = tx.QueryRowContext(ctx,
		`SELECT 1
		   FROM messages m JOIN conversations c ON c.id=m.conversation_id
		  WHERE m.id=? AND m.conversation_id=? AND COALESCE(m.author_id,'')=?
		    AND `+conversationMemberMutationPredicate("c"), accessArgs...,
	).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		// This row was created while access was valid. Revocation is allowed to
		// terminalize only this caller's still-streaming assistant placeholder;
		// content finalized before revocation won the workspace lock is preserved.
		if _, scrubErr := tx.ExecContext(ctx,
			`UPDATE messages
			    SET blocks='[]', raw=NULL, citations='[]', stop_reason='stopped',
			        input_tokens=0, output_tokens=0, cache_read_tokens=0, cache_write_tokens=0,
			        cost=0, credits=0, status='stopped', error='', gen_ms=?, verify='', search_text=''
			  WHERE id=? AND conversation_id=? AND role='assistant'
			    AND COALESCE(author_id,'')=? AND status='streaming'`,
			p.GenMs, id, expectedConvID, userID); scrubErr != nil {
			return scrubErr
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrConversationAccessRevoked
	}
	if err != nil {
		return err
	}

	res, err := execMessageFinish(ctx, tx, id, p,
		` AND conversation_id=? AND role='assistant' AND COALESCE(author_id,'')=? AND status='streaming'`, expectedConvID, userID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrConversationAccessRevoked
	}
	return tx.Commit()
}

// UpdateMessageContent is the unscoped maintenance/test primitive. User-facing
// handlers must call UpdateMessageContentForUser so authorization and the write
// share one transaction.
func UpdateMessageContent(ctx context.Context, db *sql.DB, id string, blocks json.RawMessage) error {
	return updateMessageContent(ctx, db, id, "", "", blocks)
}

// UpdateMessageContentForUser overwrites visible message content only while the
// caller still has access to expectedConvID. User questions may be edited only
// by their author; a legacy empty author belongs to the conversation creator.
// Assistant replies remain collaboratively editable by current workspace
// principals. Membership revocation shares the workspace lock acquired here.
func UpdateMessageContentForUser(ctx context.Context, db *sql.DB, expectedConvID, userID, id string, blocks json.RawMessage) error {
	if strings.TrimSpace(expectedConvID) == "" || strings.TrimSpace(userID) == "" {
		return ErrNotFound
	}
	return updateMessageContent(ctx, db, id, expectedConvID, userID, blocks)
}

func updateMessageContent(ctx context.Context, db *sql.DB, id, expectedConvID, userID string, blocks json.RawMessage) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var convID, role, authorID string
	var createdAt int64
	if expectedConvID == "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT conversation_id, role, created_at, COALESCE(author_id,'') FROM messages WHERE id=?`, id,
		).Scan(&convID, &role, &createdAt, &authorID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	} else {
		var ownerID, workspaceID string
		if err := tx.QueryRowContext(ctx,
			`SELECT m.conversation_id, m.role, m.created_at, COALESCE(m.author_id,''),
			        c.user_id, COALESCE(c.workspace_id,'')
			   FROM messages m JOIN conversations c ON c.id=m.conversation_id
			  WHERE m.id=? AND m.conversation_id=?`, id, expectedConvID,
		).Scan(&convID, &role, &createdAt, &authorID, &ownerID, &workspaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if workspaceID != "" {
			if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
				return err
			}
		}
		lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, convID)
		if err != nil {
			return err
		}
		if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if n != 1 {
			return ErrNotFound
		}
		accessArgs := []any{id, expectedConvID}
		// Message edits are mutations. Guests retain read access to shared
		// conversations but cannot change either their historical prompts or
		// collaboratively editable assistant replies after a role downgrade.
		accessArgs = append(accessArgs, conversationMemberMutationArgs(userID)...)
		if err := tx.QueryRowContext(ctx,
			`SELECT m.conversation_id, m.role, m.created_at, COALESCE(m.author_id,'')
			   FROM messages m JOIN conversations c ON c.id=m.conversation_id
			  WHERE m.id=? AND m.conversation_id=?
			    AND `+conversationMemberMutationPredicate("c"), accessArgs...,
		).Scan(&convID, &role, &createdAt, &authorID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role != "user" && role != "assistant" {
			return ErrNotFound
		}
		if role == "user" && ((authorID != "" && authorID != userID) || (authorID == "" && ownerID != userID)) {
			return ErrNotFound
		}
	}
	// Context compaction locks this same conversation row before validating and
	// appending a summary block. Serialize every edit with that write even for the
	// unscoped maintenance primitive, closing the validate-then-write race where
	// stale text could be reintroduced after pruneSummaryBlocksForDeleteTx ran.
	if expectedConvID == "" {
		lockResult, lockErr := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, convID)
		if lockErr != nil {
			return lockErr
		}
		if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if n != 1 {
			return ErrNotFound
		}
	}
	updateSQL := `UPDATE messages SET blocks=?, raw='', search_text=?, feedback='' WHERE id=?`
	updateArgs := []any{string(blocks), searchTextFromBlocks(blocks), id}
	if expectedConvID != "" {
		updateSQL += ` AND conversation_id=? AND EXISTS (
			SELECT 1 FROM conversations c
			 WHERE c.id=messages.conversation_id AND ` + conversationMemberMutationPredicate("c") + `
		)`
		updateArgs = append(updateArgs, expectedConvID)
		updateArgs = append(updateArgs, conversationMemberMutationArgs(userID)...)
	}
	res, err := tx.ExecContext(ctx, updateSQL, updateArgs...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	// Feedback evaluates the exact question/answer text that was shown. An in-place
	// edit invalidates that evidence: editing an answer clears its own evaluations;
	// editing a question clears every direct assistant answer (including regenerate
	// siblings). Keep the compatibility mirror and normalized rows in sync.
	if role == "assistant" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_feedback WHERE message_id=?`, id); err != nil {
			return err
		}
	} else if role == "user" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_feedback
			WHERE message_id IN (SELECT id FROM messages WHERE parent_id=? AND role='assistant')`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET feedback=''
			WHERE parent_id=? AND role='assistant'`, id); err != nil {
			return err
		}
	}
	// A saved edit changes the historical truth for this message. Any summary
	// block that already rolled it up now contains stale text, so prune it and let
	// the normal compaction path rebuild that range from the edited message.
	if err := pruneSummaryBlocksForDeleteTx(ctx, tx, convID, map[string]bool{id: true}, createdAt); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMessageVerify stores the secondary-auditor result (Verify mode, §verify) on
// an assistant message AFTER the turn has finalized. The value is the verify
// report JSON; ” clears it.
func SetMessageVerify(ctx context.Context, db *sql.DB, id string, verify json.RawMessage) error {
	_, err := db.ExecContext(ctx, `UPDATE messages SET verify=? WHERE id=?`, string(verify), id)
	return err
}

// SetMessageVerifyForUser persists an audit only while the generation initiator
// still has authoritative access to the exact conversation. Kick/leave takes the
// same workspace lock, so a completed audit cannot be attached after revocation.
func SetMessageVerifyForUser(ctx context.Context, db *sql.DB, id, expectedConvID, userID string, verify json.RawMessage) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(expectedConvID) == "" || strings.TrimSpace(userID) == "" {
		return ErrNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var workspaceID, status string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(c.workspace_id,''), m.status
		   FROM messages m JOIN conversations c ON c.id=m.conversation_id
		  WHERE m.id=? AND m.conversation_id=? AND m.role='assistant'
		    AND COALESCE(m.author_id,'')=?`, id, expectedConvID, userID,
	).Scan(&workspaceID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != "streaming" {
		return ErrConversationAccessRevoked
	}
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return err
		}
	}
	args := []any{string(verify), id, expectedConvID, userID}
	args = append(args, conversationMemberMutationArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`UPDATE messages SET verify=?
		  WHERE id=? AND conversation_id=? AND role='assistant' AND COALESCE(author_id,'')=? AND status='streaming'
		    AND EXISTS (
		      SELECT 1 FROM conversations c
		       WHERE c.id=messages.conversation_id AND `+conversationMemberMutationPredicate("c")+`
		    )`, args...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrConversationAccessRevoked
	}
	return tx.Commit()
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
	var start string
	err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE id=? AND conversation_id=?`, msgID, convID).Scan(&start)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}

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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var owner, workspaceID string
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&owner, &workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	// Serialize membership revocation before locking the conversation row. The
	// same ordering is used by conversation deletion and member removal, avoiding
	// a Postgres window where a kick could commit after authorization but before
	// this destructive transaction.
	if workspaceID != "" {
		if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
			return "", err
		}
	}
	lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, convID)
	if err != nil {
		return "", err
	}
	if affected, affectedErr := lockResult.RowsAffected(); affectedErr != nil {
		return "", affectedErr
	} else if affected == 0 {
		return "", ErrNotFound
	}
	accessArgs := []any{convID}
	accessArgs = append(accessArgs, conversationMemberMutationArgs(userID)...)
	accessArgs = append(accessArgs, workspaceMemberCapabilityArgs(userID)...)
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, COALESCE(workspace_id,'') FROM conversations
		  WHERE id=? AND `+conversationMemberMutationPredicate("conversations")+`
		    AND `+workspaceConversationDeletionCapabilityPredicate("conversations"), accessArgs...,
	).Scan(&owner, &workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	creator := owner == userID
	// Workspace admins may delete other members' rounds (§workspace RBAC 9.1);
	// ordinary members remain limited to their own turns.
	roundAdmin := false
	if !creator && workspaceID != "" {
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM workspaces w
			  WHERE w.id=? AND (w.owner_id=? OR EXISTS (
			    SELECT 1 FROM workspace_members dm
			     WHERE dm.workspace_id=w.id AND dm.user_id=? AND `+isAdminRoleSQL("dm.role")+`
			  )))`, workspaceID, userID, userID,
		).Scan(&roundAdmin); err != nil {
			return "", err
		}
	}

	var m Message
	if err := tx.QueryRowContext(ctx,
		`SELECT id, conversation_id, COALESCE(parent_id,''), role, created_at, COALESCE(author_id,'')
		 FROM messages WHERE id=? AND conversation_id=?`, msgID, convID,
	).Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.Role, &m.CreatedAt, &m.AuthorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}

	var (
		deletable     []string
		leafBase      string // subtree root the active leaf is re-derived from
		pruneAnchorTs int64  // earliest created_at among the deleted messages
	)

	// Resolve the parent WITHIN the tx — a GetMessage on `db` here would grab a
	// second pool connection and deadlock against this tx's SQLite write lock
	// (single-writer). One read serves both the branch check and the round walk-up.
	var pParent, pRole, pAuthor string
	var pCreated int64
	pFound := false
	if m.ParentID != "" {
		switch perr := tx.QueryRowContext(ctx,
			`SELECT COALESCE(parent_id,''), role, created_at, COALESCE(author_id,'')
			 FROM messages WHERE id=? AND conversation_id=?`, m.ParentID, convID,
		).Scan(&pParent, &pRole, &pCreated, &pAuthor); {
		case perr == nil:
			pFound = true
		case errors.Is(perr, sql.ErrNoRows):
			// parent already gone — treat as a root
		default:
			return "", perr
		}
	}

	// The HTTP handler performs the same check for a fast 404, but the store is
	// the authority: derive the round's user turn under this transaction's lock.
	// Legacy empty authors belong to the conversation creator and therefore fail
	// closed for ordinary workspace members. Workspace admins may remove any
	// round, matching the conversation permission matrix.
	if !creator && !roundAdmin {
		roundAuthor := ""
		switch {
		case m.Role == "user":
			roundAuthor = m.AuthorID
		case pFound && pRole == "user":
			roundAuthor = pAuthor
		}
		if strings.TrimSpace(roundAuthor) != userID {
			return "", ErrNotFound
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

	// Deleting one regenerated assistant variant removes its complete downstream
	// branch. In a shared conversation that branch may contain continuations
	// authored by other members. A non-creator may remove only a branch whose user
	// turns all belong to them; empty legacy authors fail closed as well.
	if branch && !creator && len(deletable) > 0 {
		args := []any{convID}
		args = append(args, anySlice(deletable)...)
		args = append(args, userID)
		var foreignUserID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM messages
			 WHERE conversation_id=? AND id IN (`+idPlaceholders(len(deletable))+`)
			   AND role='user' AND COALESCE(author_id,'')<>?
			 LIMIT 1`, args...,
		).Scan(&foreignUserID)
		if err == nil {
			return "", ErrNotFound
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
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

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// deepestLeafFromTx walks from a node (or, when fromID is empty, the newest root)
// down its newest-child chain to the leaf — the natural place to re-anchor the
// active path after a deletion. Returns "" when the conversation is now empty.
func deepestLeafFromTx(ctx context.Context, tx *sql.Tx, convID, fromID string) (string, error) {
	return deepestLeafFrom(ctx, tx, convID, fromID)
}

func deepestLeafFrom(ctx context.Context, q rowQueryer, convID, fromID string) (string, error) {
	current := fromID
	if current == "" {
		var root string
		err := q.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id IS NULL ORDER BY created_at DESC LIMIT 1`, convID).Scan(&root)
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
		err := q.QueryRowContext(ctx, `SELECT id FROM messages WHERE conversation_id=? AND parent_id=? ORDER BY created_at DESC LIMIT 1`, convID, current).Scan(&child)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		current = child
	}
}
