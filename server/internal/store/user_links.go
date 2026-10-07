package store

import (
	"context"
	"database/sql"
)

// UserPublishedLink is a metadata-only projection for the user's settings.
type UserPublishedLink struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Title          string `json:"title,omitempty"`
	CreatedAt      int64  `json:"created_at"`
}

func ListUserConversationShares(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]UserPublishedLink, error) {
	args := []any{userID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.conversation_id, s.title, s.created_at
		FROM conversation_shares s JOIN conversations c ON c.id=s.conversation_id
		WHERE s.user_id=? AND `+workspaceResourceManagerPredicate("c")+`
		ORDER BY s.created_at DESC, s.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []UserPublishedLink{}
	for rows.Next() {
		var item UserPublishedLink
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.Title, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteUserConversationShare binds revocation to the exact published token;
// an old settings row cannot revoke a newer replacement of the same conversation.
func DeleteUserConversationShare(ctx context.Context, db *sql.DB, id, userID string) error {
	args := []any{id, userID}
	args = append(args, workspaceResourceManagerArgs(userID)...)
	result, err := db.ExecContext(ctx, `
		DELETE FROM conversation_shares WHERE id=? AND user_id=? AND EXISTS (
			SELECT 1 FROM conversations c WHERE c.id=conversation_shares.conversation_id
			AND `+workspaceResourceManagerPredicate("c")+`)
	`, args...)
	return userLinkDeleteResult(result, err)
}

func ListUserHTMLPreviewShares(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]UserPublishedLink, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, created_at FROM html_preview_shares WHERE user_id=?
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []UserPublishedLink{}
	for rows.Next() {
		var item UserPublishedLink
		if err := rows.Scan(&item.ID, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func DeleteUserHTMLPreviewShare(ctx context.Context, db *sql.DB, id, userID string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM html_preview_shares WHERE id=? AND user_id=?`, id, userID)
	return userLinkDeleteResult(result, err)
}

func userLinkDeleteResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
