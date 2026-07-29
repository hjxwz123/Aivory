package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// GetModelTag returns one tag by id.
func GetModelTag(ctx context.Context, db *sql.DB, id string) (*ModelTag, error) {
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

type ModelTagPatch struct {
	Name      *string `json:"name"`
	SortOrder *int    `json:"sort_order"`
}

// UpdateModelTag only writes fields present in patch.
func UpdateModelTag(ctx context.Context, db *sql.DB, id string, patch ModelTagPatch) (*ModelTag, error) {
	parts := []string{}
	args := []any{}
	if patch.Name != nil {
		parts = append(parts, "name=?")
		args = append(args, strings.TrimSpace(*patch.Name))
	}
	if patch.SortOrder != nil {
		parts = append(parts, "sort_order=?")
		args = append(args, *patch.SortOrder)
	}
	if len(parts) == 0 {
		return GetModelTag(ctx, db, id)
	}
	args = append(args, id)
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("UPDATE model_tags SET %s WHERE id=?", strings.Join(parts, ", ")),
		args...); err != nil {
		if isUniqueIndexErr(err, "idx_model_tags_name_unique", "model_tags.name") {
			return nil, ErrModelTagNameExists
		}
		return nil, err
	}
	return GetModelTag(ctx, db, id)
}

// ReorderModelTags assigns sort_order = position for each id in one
// transaction, matching the model and channel reorder operations.
func ReorderModelTags(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE model_tags SET sort_order=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteModelTag removes a tag definition. Stale ids that may remain inside
// models.tags are harmless — the picker only renders chips for tags that still
// exist, and re-saving a model drops unknown ids.
func DeleteModelTag(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM model_tags WHERE id=?`, id)
	return err
}
