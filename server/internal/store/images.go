package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// §4.20 Image Generation — admin-managed styles. A style's hidden_prompt is
// composed into the final image prompt server-side and is NEVER returned to
// non-admin users (handlers strip it via the public DTO). Generated images
// themselves reuse the existing conversation + artifact storage; there is no
// separate image table.

// ImageStyle is an admin-managed look (name + example thumbnail + hidden prompt).
type ImageStyle struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ExampleImageURL string `json:"example_image_url"`
	HiddenPrompt    string `json:"hidden_prompt,omitempty"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sort_order"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// ListImageStyles returns styles ordered for display. onlyEnabled filters to
// active ones (the user-facing picker); admin passes false to see all.
func ListImageStyles(ctx context.Context, db *sql.DB, onlyEnabled bool) ([]ImageStyle, error) {
	q := `SELECT id, name, example_image_url, hidden_prompt, enabled, sort_order, created_at, updated_at FROM image_styles`
	if onlyEnabled {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY sort_order, name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImageStyle{}
	for rows.Next() {
		var s ImageStyle
		var en int // enabled is INTEGER in both dialects — scan via int (repo convention)
		if err := rows.Scan(&s.ID, &s.Name, &s.ExampleImageURL, &s.HiddenPrompt, &en, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Enabled = en == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetImageStyle loads one style (includes hidden_prompt for server-side use).
func GetImageStyle(ctx context.Context, db *sql.DB, id string) (*ImageStyle, error) {
	var s ImageStyle
	var en int
	err := db.QueryRowContext(ctx,
		`SELECT id, name, example_image_url, hidden_prompt, enabled, sort_order, created_at, updated_at FROM image_styles WHERE id=?`, id).
		Scan(&s.ID, &s.Name, &s.ExampleImageURL, &s.HiddenPrompt, &en, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = en == 1
	return &s, nil
}

// GetImageStyleByName returns a style by case-insensitive, trimmed name.
func GetImageStyleByName(ctx context.Context, db *sql.DB, name string) (*ImageStyle, error) {
	var s ImageStyle
	var en int
	err := db.QueryRowContext(ctx,
		`SELECT id, name, example_image_url, hidden_prompt, enabled, sort_order, created_at, updated_at FROM image_styles WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`, name).
		Scan(&s.ID, &s.Name, &s.ExampleImageURL, &s.HiddenPrompt, &en, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = en == 1
	return &s, nil
}

// CreateImageStyle inserts a new style.
func CreateImageStyle(ctx context.Context, db *sql.DB, s ImageStyle) (*ImageStyle, error) {
	now := time.Now().Unix()
	s.ID = genID("imgsty")
	s.CreatedAt = now
	s.UpdatedAt = now
	s.Name = strings.TrimSpace(s.Name)
	s.ExampleImageURL = strings.TrimSpace(s.ExampleImageURL)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO image_styles(id, name, example_image_url, hidden_prompt, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.ExampleImageURL, s.HiddenPrompt, boolToInt(s.Enabled), s.SortOrder, s.CreatedAt, s.UpdatedAt); err != nil {
		if isUniqueIndexErr(err, "idx_image_styles_name_unique", "image_styles.name") {
			return nil, ErrImageStyleNameExists
		}
		return nil, err
	}
	return &s, nil
}

// UpdateImageStyle overwrites the mutable fields of a style.
func UpdateImageStyle(ctx context.Context, db *sql.DB, s ImageStyle) (*ImageStyle, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.ExampleImageURL = strings.TrimSpace(s.ExampleImageURL)
	if _, err := db.ExecContext(ctx,
		`UPDATE image_styles SET name=?, example_image_url=?, hidden_prompt=?, enabled=?, sort_order=?, updated_at=? WHERE id=?`,
		s.Name, s.ExampleImageURL, s.HiddenPrompt, boolToInt(s.Enabled), s.SortOrder, time.Now().Unix(), s.ID); err != nil {
		if isUniqueIndexErr(err, "idx_image_styles_name_unique", "image_styles.name") {
			return nil, ErrImageStyleNameExists
		}
		return nil, err
	}
	return GetImageStyle(ctx, db, s.ID)
}

// DeleteImageStyle removes a style. A dangling style_id elsewhere is harmless.
func DeleteImageStyle(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM image_styles WHERE id=?`, id)
	return err
}

// AdminImageArtifact is one of a user's generated images, for the admin
// drill-down gallery (§8.1). Joins artifacts → messages → conversations so the
// query is scoped to the user and carries the source conversation.
type AdminImageArtifact struct {
	ID                string `json:"id"`
	ConversationID    string `json:"conversation_id"`
	ConversationTitle string `json:"conversation_title"`
	MessageID         string `json:"message_id"`
	Filename          string `json:"filename"`
	MimeType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	CreatedAt         int64  `json:"created_at"`
	// URL is filled by the handler = /api/artifacts/<id> (admin can view any).
	URL string `json:"url,omitempty"`
}

// ListUserImageArtifacts returns a user's image artifacts newest-first, paged.
// Covers EVERY image the user generated — drawing-mode turns and chat tool-call
// image_generate alike — since both persist as image artifacts.
func ListUserImageArtifacts(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]AdminImageArtifact, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT a.id, m.conversation_id, COALESCE(c.title,''), a.message_id, a.filename, a.mime_type, a.size_bytes, a.created_at
		 FROM artifacts a
		 JOIN messages m ON m.id = a.message_id
		 JOIN conversations c ON c.id = m.conversation_id
		 WHERE c.user_id = ? AND a.mime_type LIKE 'image/%'
		 ORDER BY a.created_at DESC, a.id DESC
		 LIMIT ? OFFSET ?`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminImageArtifact{}
	for rows.Next() {
		var a AdminImageArtifact
		if err := rows.Scan(&a.ID, &a.ConversationID, &a.ConversationTitle, &a.MessageID, &a.Filename, &a.MimeType, &a.SizeBytes, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
