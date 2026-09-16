package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// AdminHTMLPreviewShare is the metadata-only administrator projection. The
// stored HTML is deliberately excluded so listing links cannot turn the admin
// inventory into a bulk export of users' generated content.
type AdminHTMLPreviewShare struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	UserEmail string `json:"user_email"`
	UserName  string `json:"user_name"`
	CreatedAt int64  `json:"created_at"`
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

func adminHTMLPreviewShareFilter(search string) (string, []any) {
	search = strings.TrimSpace(search)
	if search == "" {
		return "", nil
	}
	like := "%" + strings.ToLower(search) + "%"
	return ` WHERE LOWER(s.id) LIKE ? OR LOWER(COALESCE(u.email,'')) LIKE ? OR LOWER(COALESCE(u.name,'')) LIKE ?`,
		[]any{like, like, like}
}

// ListAdminHTMLPreviewShares returns newest-first link metadata for the global
// administrator inventory. Public availability is still decided at read time
// by GetHTMLPreviewShare, including the owner's current sharing permission.
func ListAdminHTMLPreviewShares(ctx context.Context, db *sql.DB, search string, limit, offset int) ([]AdminHTMLPreviewShare, error) {
	where, args := adminHTMLPreviewShareFilter(search)
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.user_id, COALESCE(u.email,''), COALESCE(u.name,''), s.created_at
		  FROM html_preview_shares s
		  LEFT JOIN users u ON u.id=s.user_id`+where+`
		 ORDER BY s.created_at DESC, s.id DESC
		 LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AdminHTMLPreviewShare{}
	for rows.Next() {
		var item AdminHTMLPreviewShare
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserEmail, &item.UserName, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func CountAdminHTMLPreviewShares(ctx context.Context, db *sql.DB, search string) (int, error) {
	where, args := adminHTMLPreviewShareFilter(search)
	var total int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM html_preview_shares s
		  LEFT JOIN users u ON u.id=s.user_id`+where, args...).Scan(&total)
	return total, err
}

func DeleteAdminHTMLPreviewShare(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM html_preview_shares WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
