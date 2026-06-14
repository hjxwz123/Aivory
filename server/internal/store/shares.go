package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Conversation shares (§ public read-only sharing). A share freezes a
// cost-stripped snapshot of the active message path at share time and exposes it
// under a public token. Revoking deletes the row, so the link dies and no later
// private messages are ever reachable. At most one live share per conversation
// (enforced by a unique index) — re-sharing replaces the snapshot.

// Share is one public share record.
type Share struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	UserID         string          `json:"user_id"`
	Title          string          `json:"title"`
	Snapshot       json.RawMessage `json:"snapshot"`
	CreatedAt      int64           `json:"created_at"`
}

// CreateShare replaces any existing share for the conversation with a fresh
// snapshot and returns the new record. snapshot is opaque JSON built by the
// caller (the cost-stripped public message projection).
func CreateShare(ctx context.Context, db *sql.DB, userID, convID, title string, snapshot []byte) (*Share, error) {
	if _, err := db.ExecContext(ctx, `DELETE FROM conversation_shares WHERE conversation_id=?`, convID); err != nil {
		return nil, err
	}
	id := "sh_" + genToken() // §D1: 192-bit unguessable token (public capability URL)
	if len(snapshot) == 0 {
		snapshot = []byte("[]")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO conversation_shares(id, conversation_id, user_id, title, snapshot) VALUES(?, ?, ?, ?, ?)`,
		id, convID, userID, title, string(snapshot))
	if err != nil {
		return nil, err
	}
	return GetShareByConversation(ctx, db, convID, userID)
}

// GetShareByConversation returns the live share for a conversation owned by the
// user, or ErrNotFound.
func GetShareByConversation(ctx context.Context, db *sql.DB, convID, userID string) (*Share, error) {
	var s Share
	var snapshot string
	err := db.QueryRowContext(ctx,
		`SELECT id, conversation_id, user_id, title, snapshot, created_at
		   FROM conversation_shares WHERE conversation_id=? AND user_id=?`, convID, userID,
	).Scan(&s.ID, &s.ConversationID, &s.UserID, &s.Title, &snapshot, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Snapshot = json.RawMessage(snapshot)
	return &s, nil
}

// GetShareByToken returns a share by its public id — no user scoping, used by
// the unauthenticated public view.
func GetShareByToken(ctx context.Context, db *sql.DB, token string) (*Share, error) {
	var s Share
	var snapshot string
	err := db.QueryRowContext(ctx,
		`SELECT id, conversation_id, user_id, title, snapshot, created_at
		   FROM conversation_shares WHERE id=?`, token,
	).Scan(&s.ID, &s.ConversationID, &s.UserID, &s.Title, &snapshot, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Snapshot = json.RawMessage(snapshot)
	return &s, nil
}

// DeleteShareByConversation revokes a conversation's share (owner-scoped).
func DeleteShareByConversation(ctx context.Context, db *sql.DB, convID, userID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM conversation_shares WHERE conversation_id=? AND user_id=?`, convID, userID)
	return err
}
