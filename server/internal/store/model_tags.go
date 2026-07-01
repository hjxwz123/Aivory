package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ModelTag is an admin-managed label assignable to models (§ model tags). Each
// model stores the tag ids it carries in models.tags; the picker filters by them.
type ModelTag struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	CreatedAt int64  `json:"created_at"`
}

// ListModelTags returns all tags, ordered for display.
func ListModelTags(ctx context.Context, db *sql.DB) ([]ModelTag, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, sort_order, created_at FROM model_tags ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelTag{}
	for rows.Next() {
		var t ModelTag
		if err := rows.Scan(&t.ID, &t.Name, &t.SortOrder, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetModelTagByName returns a tag by case-insensitive, trimmed name.
func GetModelTagByName(ctx context.Context, db *sql.DB, name string) (*ModelTag, error) {
	var t ModelTag
	err := db.QueryRowContext(ctx, `SELECT id, name, sort_order, created_at FROM model_tags WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`, name).
		Scan(&t.ID, &t.Name, &t.SortOrder, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateModelTag inserts a new tag.
func CreateModelTag(ctx context.Context, db *sql.DB, name string, sortOrder int) (*ModelTag, error) {
	name = strings.TrimSpace(name)
	t := ModelTag{ID: genID("mtag"), Name: name, SortOrder: sortOrder, CreatedAt: time.Now().Unix()}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO model_tags(id, name, sort_order, created_at) VALUES(?, ?, ?, ?)`,
		t.ID, t.Name, t.SortOrder, t.CreatedAt); err != nil {
		if isUniqueIndexErr(err, "idx_model_tags_name_unique", "model_tags.name") {
			return nil, ErrModelTagNameExists
		}
		return nil, err
	}
	return &t, nil
}

// UpdateModelTag renames / reorders a tag.
func UpdateModelTag(ctx context.Context, db *sql.DB, id, name string, sortOrder int) (*ModelTag, error) {
	name = strings.TrimSpace(name)
	if _, err := db.ExecContext(ctx, `UPDATE model_tags SET name=?, sort_order=? WHERE id=?`, name, sortOrder, id); err != nil {
		if isUniqueIndexErr(err, "idx_model_tags_name_unique", "model_tags.name") {
			return nil, ErrModelTagNameExists
		}
		return nil, err
	}
	var t ModelTag
	err := db.QueryRowContext(ctx, `SELECT id, name, sort_order, created_at FROM model_tags WHERE id=?`, id).
		Scan(&t.ID, &t.Name, &t.SortOrder, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteModelTag removes a tag definition. Stale ids that may remain inside
// models.tags are harmless — the picker only renders chips for tags that still
// exist, and re-saving a model drops unknown ids.
func DeleteModelTag(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM model_tags WHERE id=?`, id)
	return err
}
