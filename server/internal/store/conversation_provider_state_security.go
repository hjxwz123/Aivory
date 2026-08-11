package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// SetConvProviderStateKeyForUser is the user-scoped provider-state mutation.
// Workspace writes serialize with membership revocation and recheck access
// after taking the lock. Administrative maintenance may continue to use the
// unscoped SetConvProviderStateKey primitive explicitly.
func SetConvProviderStateKeyForUser(ctx context.Context, db *sql.DB, convID, messageID, userID, key, value string) error {
	if strings.TrimSpace(convID) == "" || strings.TrimSpace(messageID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(key) == "" {
		return ErrNotFound
	}
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, convID,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var tx *sql.Tx
	var err error
	if workspaceID != "" {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	} else {
		tx, err = db.BeginTx(ctx, nil)
	}
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	lockResult, err := tx.ExecContext(ctx, `UPDATE conversations SET id=id WHERE id=?`, convID)
	if err != nil {
		return err
	}
	if n, rowsErr := lockResult.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}

	accessArgs := []any{messageID, convID, userID}
	accessArgs = append(accessArgs, workspaceResourceAccessArgs(userID)...)
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT c.provider_state
		   FROM messages m JOIN conversations c ON c.id=m.conversation_id
		  WHERE m.id=? AND m.conversation_id=? AND m.role='assistant'
		    AND COALESCE(m.author_id,'')=? AND m.status='streaming'
		    AND `+conversationResourceAccessPredicate("c"), accessArgs...,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConversationAccessRevoked
		}
		return err
	}
	state := map[string]any{}
	_ = json.Unmarshal([]byte(orDefault(raw, "{}")), &state)
	state[key] = value
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	updateArgs := []any{string(encoded), time.Now().Unix(), convID}
	updateArgs = append(updateArgs, workspaceResourceAccessArgs(userID)...)
	updateArgs = append(updateArgs, messageID, userID)
	res, err := tx.ExecContext(ctx,
		`UPDATE conversations SET provider_state=?, updated_at=?
		  WHERE id=? AND `+conversationResourceAccessPredicate("conversations")+`
		    AND EXISTS (
		      SELECT 1 FROM messages provider_state_message
		       WHERE provider_state_message.id=?
		         AND provider_state_message.conversation_id=conversations.id
		         AND provider_state_message.role='assistant'
		         AND COALESCE(provider_state_message.author_id,'')=?
		         AND provider_state_message.status='streaming'
		    )`, updateArgs...)
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
