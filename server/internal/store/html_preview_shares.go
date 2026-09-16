package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HTMLPreviewShare is a public capability link owned by the user who created
// it. The HTML is intentionally stored verbatim; isolation is enforced by the
// public HTTP response's CSP sandbox rather than by rewriting user markup.
type HTMLPreviewShare struct {
	ID        string
	UserID    string
	HTML      string
	CreatedAt int64
}

func CreateHTMLPreviewShare(ctx context.Context, db *sql.DB, userID, html string) (*HTMLPreviewShare, error) {
	allowed, err := conversationSharingAllowedForUser(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	share := &HTMLPreviewShare{
		ID: "hp_" + genToken(), UserID: userID, HTML: html, CreatedAt: time.Now().Unix(),
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO html_preview_shares(id,user_id,html,created_at) VALUES(?,?,?,?)`,
		share.ID, share.UserID, share.HTML, share.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return share, nil
}

func GetHTMLPreviewShare(ctx context.Context, db *sql.DB, token string) (*HTMLPreviewShare, error) {
	var share HTMLPreviewShare
	err := db.QueryRowContext(ctx,
		`SELECT id,user_id,html,created_at FROM html_preview_shares WHERE id=?`, token,
	).Scan(&share.ID, &share.UserID, &share.HTML, &share.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allowed, err := conversationSharingAllowedForUser(ctx, db, share.UserID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	return &share, nil
}
