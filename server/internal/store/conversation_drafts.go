package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Older composers already create blank, message-less conversations for upload
// ownership. Recognize that shape without requiring a draft marker or matching
// a localized UI fallback such as "Untitled". Explicitly saved resources are
// excluded so deleting all messages never makes their retained files disposable.
const unusedConversationDraftSQL = `
	c.title='' AND c.pinned=0 AND c.starred=0 AND c.archived=0
	AND COALESCE(c.inline_source_conv,'')=''
	AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id=c.id)
	AND NOT EXISTS (SELECT 1 FROM conversations child WHERE child.inline_source_conv=c.id)
	AND NOT EXISTS (SELECT 1 FROM conversation_shares s WHERE s.conversation_id=c.id)
	AND NOT EXISTS (SELECT 1 FROM files f WHERE f.conversation_id=c.id AND (f.draft=0 OR f.user_id<>c.user_id))`

// The same definition controls visibility, creation limits and explicit discard, so a
// deliberately retained empty conversation remains visible and manageable.
func conversationHistoryPredicate(alias string) string {
	return "NOT (" + strings.ReplaceAll(unusedConversationDraftSQL, "c.", alias+".") + ")"
}

const MaxConversationDrafts = 1

var ErrConversationDraftLimit = errors.New("conversation_draft_limit: only one unsent draft is allowed per account; send or delete the existing draft first")

// ReserveConversationDraft reuses the owner's upload reservation in the same
// workspace/project/chat-or-draw scope, discarding an unused draft in another
// scope first. The creation transaction serializes reservations for this owner,
// so several tabs, devices or API replicas cannot create duplicate scopes
// or exceed the per-account cap. Ordinary first sends use CreateConversation
// instead, so concurrent questions never get merged into one conversation.
func ReserveConversationDraft(ctx context.Context, db *sql.DB, c Conversation) (*Conversation, error) {
	return createConversation(ctx, db, c, true)
}

const conversationDraftScopePredicate = `
	c.user_id=? AND COALESCE(c.workspace_id,'')=? AND COALESCE(c.project_id,'')=?
	AND (c.draft_scope=? OR (
		c.draft_scope=''
		AND EXISTS (SELECT 1 FROM files legacy_file WHERE legacy_file.conversation_id=c.id AND legacy_file.draft=1)
		AND CASE WHEN EXISTS (SELECT 1 FROM models legacy_model WHERE legacy_model.id=c.model_id AND legacy_model.kind='image')
		    THEN 'draw' ELSE 'chat' END=?
	)) AND ` + unusedConversationDraftSQL

// Legacy empty first-send rows use their model kind to determine the scope;
// marked upload reservations keep a stable scope even after a model is removed.
const conversationDraftSameScopeSQL = `
	COALESCE(c.workspace_id,'')=? AND COALESCE(c.project_id,'')=?
	AND CASE WHEN c.draft_scope<>'' THEN c.draft_scope
	    WHEN EXISTS (SELECT 1 FROM models scope_model WHERE scope_model.id=c.model_id AND scope_model.kind='image')
	    THEN 'draw' ELSE 'chat' END=?`

type draftQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findConversationDraftID(ctx context.Context, db draftQueryer, userID, workspaceID, projectID, scope string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT c.id FROM conversations c WHERE `+conversationDraftScopePredicate+
		` ORDER BY c.updated_at DESC, c.id DESC LIMIT 1`, userID, workspaceID, projectID, scope, scope).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// FindConversationDraft restores a reservation even after the browser's local
// storage is lost. It is read-only and never creates a row merely by opening
// the home/project page. Only the creator's drafts can be returned.
func FindConversationDraft(ctx context.Context, db *sql.DB, userID, workspaceID, projectID, scope string) (*Conversation, error) {
	id, err := findConversationDraftID(ctx, db, userID, workspaceID, projectID, scope)
	if err != nil || id == "" {
		return nil, err
	}
	c, err := GetConversation(ctx, db, id, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil // a concurrent explicit discard removed the reservation
	}
	return c, err
}

func lockConversationDraftCreation(ctx context.Context, tx *sql.Tx, userID string) error {
	if usePostgres {
		// A dedicated transaction lock avoids the user-row -> conversation-row
		// order conflicting with a first-message commit's quota charge. It also
		// serializes replicas without adding a Redis connection or table.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "aivory:conversation-draft:"+userID); err != nil {
			return err
		}
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id=? AND status<>'deleting'`, userID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=? AND status<>'deleting'`, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// SwitchConversationDraftScope only removes existing unused drafts in other
// scopes. Opening an empty scope never reserves a conversation. Deletion and
// recovery share the account's reservation lock; first sends retain their own
// conversation lock and can turn a draft into a protected saved conversation.
func SwitchConversationDraftScope(ctx context.Context, db *sql.DB, userID, workspaceID, projectID, scope string) (*Conversation, *ConversationDeletionState, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockConversationDraftCreation(ctx, tx, userID); err != nil {
		return nil, nil, err
	}
	deleted, err := discardOtherConversationDraftScopes(ctx, tx, userID, workspaceID, projectID, scope)
	if err != nil {
		return nil, nil, err
	}
	id, err := findConversationDraftID(ctx, tx, userID, workspaceID, projectID, scope)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if id == "" {
		return nil, deleted, nil
	}
	draft, err := GetConversation(ctx, db, id, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, deleted, nil
	}
	return draft, deleted, err
}

func discardOtherConversationDraftScopes(ctx context.Context, tx *sql.Tx, userID, workspaceID, projectID, scope string) (*ConversationDeletionState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.id FROM conversations c WHERE c.user_id=?
		AND `+unusedConversationDraftSQL+` AND NOT (`+conversationDraftSameScopeSQL+`) ORDER BY c.id`,
		userID, workspaceID, projectID, scope)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	deleted := &ConversationDeletionState{ConversationIDs: []string{}}
	for _, id := range ids {
		state, err := deleteConversationDraftTx(ctx, tx, id, userID)
		if errors.Is(err, ErrNotFound) {
			continue // sent, saved or explicitly deleted since listing
		}
		if err != nil {
			return nil, err
		}
		deleted.ConversationIDs = append(deleted.ConversationIDs, state.ConversationIDs...)
		deleted.StoragePaths = append(deleted.StoragePaths, state.StoragePaths...)
		deleted.SandboxDiscards = append(deleted.SandboxDiscards, state.SandboxDiscards...)
	}
	return deleted, nil
}

func checkConversationDraftLimit(ctx context.Context, tx *sql.Tx, userID string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT c.id FROM conversations c WHERE c.user_id=? AND `+unusedConversationDraftSQL+`
		LIMIT ?
	) draft_candidates`, userID, MaxConversationDrafts).Scan(&count); err != nil {
		return err
	}
	if count >= MaxConversationDrafts {
		return ErrConversationDraftLimit
	}
	return nil
}

// DeleteConversationDraftWithState serializes against CreateMessageForUser's
// conversation lock, then rechecks the draft condition inside that transaction.
// Only an explicit abandonment can delete a draft; there is no expiry sweep.
func DeleteConversationDraftWithState(ctx context.Context, db *sql.DB, id, userID string) (*ConversationDeletionState, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	state, err := deleteConversationDraftTx(ctx, tx, id, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

func deleteConversationDraftTx(ctx context.Context, tx *sql.Tx, id, userID string) (*ConversationDeletionState, error) {
	result, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrNotFound
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations c WHERE c.id=? AND c.user_id=? AND `+unusedConversationDraftSQL, id, userID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	paths, err := storagePathsForConversationIDs(ctx, tx, []string{id})
	if err != nil {
		return nil, err
	}
	var providerState string
	if err := tx.QueryRowContext(ctx, `SELECT provider_state FROM conversations WHERE id=?`, id).Scan(&providerState); err != nil {
		return nil, err
	}
	var sandboxState struct {
		ID string `json:"sandbox_id"`
	}
	_ = json.Unmarshal([]byte(providerState), &sandboxState)
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pending_storage_cleanup(path,user_id,created_at) VALUES(?,?,?) ON CONFLICT(path) DO NOTHING`, path, userID, time.Now().Unix()); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE conversation_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=? AND user_id=?`, id, userID); err != nil {
		return nil, err
	}
	state := &ConversationDeletionState{ConversationIDs: []string{id}, StoragePaths: paths}
	if sandboxState.ID != "" {
		state.SandboxDiscards = []ConversationSandboxDiscard{{ConversationID: id, SessionID: sandboxState.ID}}
	}
	return state, nil
}
