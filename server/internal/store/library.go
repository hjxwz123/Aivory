package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrPromptNameExists          = errors.New("prompt name already exists")
	ErrUserSkillNameExists       = errors.New("user skill name already exists")
	ErrUserPromptNameExists      = errors.New("user prompt name already exists")
	ErrCatalogItemAlreadyAdded   = errors.New("catalog item already added")
	ErrInvalidUserSkillSelection = errors.New("invalid user skill selection")
)

const (
	MaxSelectedUserSkills                = 5
	MaxSelectedUserSkillInstructionBytes = 64 << 10
)

// ListPrompts returns administrator prompt templates. User-facing callers must
// project these rows to display metadata rather than returning Content.
func ListPrompts(ctx context.Context, db *sql.DB, onlyEnabled bool) ([]Prompt, error) {
	query := `SELECT id, name, description, icon, content, enabled, sort_order, updated_at FROM prompts`
	if onlyEnabled {
		query += ` WHERE enabled=1`
	}
	query += ` ORDER BY sort_order, name`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Prompt{}
	for rows.Next() {
		var prompt Prompt
		var enabled int
		if err := rows.Scan(&prompt.ID, &prompt.Name, &prompt.Description, &prompt.Icon, &prompt.Content, &enabled, &prompt.SortOrder, &prompt.UpdatedAt); err != nil {
			return nil, err
		}
		prompt.Enabled = enabled == 1
		out = append(out, prompt)
	}
	return out, rows.Err()
}

func GetPrompt(ctx context.Context, db *sql.DB, id string) (*Prompt, error) {
	var prompt Prompt
	var enabled int
	err := db.QueryRowContext(ctx,
		`SELECT id, name, description, icon, content, enabled, sort_order, updated_at FROM prompts WHERE id=?`, id,
	).Scan(&prompt.ID, &prompt.Name, &prompt.Description, &prompt.Icon, &prompt.Content, &enabled, &prompt.SortOrder, &prompt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	prompt.Enabled = enabled == 1
	return &prompt, nil
}

func CreatePrompt(ctx context.Context, db *sql.DB, prompt Prompt) (*Prompt, error) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Icon = strings.TrimSpace(prompt.Icon)
	prompt.Content = strings.TrimSpace(prompt.Content)
	if prompt.ID == "" {
		prompt.ID = genID("pt")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO prompts(id, name, description, icon, content, enabled, sort_order, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, prompt.ID, prompt.Name, prompt.Description, prompt.Icon,
		prompt.Content, boolInt(prompt.Enabled), prompt.SortOrder, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_prompts_name_unique", "prompts.name") {
			return nil, ErrPromptNameExists
		}
		return nil, err
	}
	return GetPrompt(ctx, db, prompt.ID)
}

func UpdatePrompt(ctx context.Context, db *sql.DB, id string, prompt Prompt) (*Prompt, error) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Icon = strings.TrimSpace(prompt.Icon)
	prompt.Content = strings.TrimSpace(prompt.Content)
	result, err := db.ExecContext(ctx, `UPDATE prompts SET name=?, description=?, icon=?, content=?, enabled=?, sort_order=?, updated_at=? WHERE id=?`,
		prompt.Name, prompt.Description, prompt.Icon, prompt.Content, boolInt(prompt.Enabled), prompt.SortOrder, time.Now().Unix(), id)
	if err != nil {
		if isUniqueIndexErr(err, "idx_prompts_name_unique", "prompts.name") {
			return nil, ErrPromptNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetPrompt(ctx, db, id)
}

func DeletePrompt(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM prompts WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func ListUserSkills(ctx context.Context, db *sql.DB, userID string) ([]UserSkill, error) {
	rows, err := db.QueryContext(ctx, `SELECT us.id, us.user_id, us.name, us.description,
		COALESCE(NULLIF(TRIM(s.display_description),''), s.description, ''), us.icon, us.instructions,
		COALESCE(us.source_skill_id,''), us.created_at, us.updated_at
		FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
		WHERE us.user_id=? ORDER BY us.updated_at DESC, us.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserSkill{}
	for rows.Next() {
		var skill UserSkill
		if err := rows.Scan(&skill.ID, &skill.UserID, &skill.Name, &skill.Description, &skill.DisplayDescription, &skill.Icon, &skill.Instructions,
			&skill.SourceSkillID, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

func GetUserSkill(ctx context.Context, db *sql.DB, id, userID string) (*UserSkill, error) {
	var skill UserSkill
	err := db.QueryRowContext(ctx, `SELECT us.id, us.user_id, us.name, us.description,
		COALESCE(NULLIF(TRIM(s.display_description),''), s.description, ''), us.icon, us.instructions,
		COALESCE(us.source_skill_id,''), us.created_at, us.updated_at
		FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
		WHERE us.id=? AND us.user_id=?`, id, userID,
	).Scan(&skill.ID, &skill.UserID, &skill.Name, &skill.Description, &skill.DisplayDescription, &skill.Icon, &skill.Instructions,
		&skill.SourceSkillID, &skill.CreatedAt, &skill.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &skill, nil
}

func CreateUserSkill(ctx context.Context, db *sql.DB, skill UserSkill) (*UserSkill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Icon = strings.TrimSpace(skill.Icon)
	skill.Instructions = strings.TrimSpace(skill.Instructions)
	if skill.ID == "" {
		skill.ID = genID("usk")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO user_skills(id, user_id, name, description, icon, instructions, source_skill_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, skill.ID, skill.UserID, skill.Name, skill.Description,
		skill.Icon, skill.Instructions, nullableText(skill.SourceSkillID), now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_skills_user_name_unique", "user_skills.user_id, user_skills.name") {
			return nil, ErrUserSkillNameExists
		}
		if isUniqueIndexErr(err, "idx_user_skills_source_unique", "user_skills.user_id, user_skills.source_skill_id") {
			return nil, ErrCatalogItemAlreadyAdded
		}
		return nil, err
	}
	return GetUserSkill(ctx, db, skill.ID, skill.UserID)
}

func UpdateUserSkill(ctx context.Context, db *sql.DB, id, userID string, skill UserSkill) (*UserSkill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Icon = strings.TrimSpace(skill.Icon)
	skill.Instructions = strings.TrimSpace(skill.Instructions)
	result, err := db.ExecContext(ctx, `UPDATE user_skills SET name=?, description=?, icon=?, instructions=?, updated_at=? WHERE id=? AND user_id=?`,
		skill.Name, skill.Description, skill.Icon, skill.Instructions, time.Now().Unix(), id, userID)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_skills_user_name_unique", "user_skills.user_id, user_skills.name") {
			return nil, ErrUserSkillNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetUserSkill(ctx, db, id, userID)
}

func DeleteUserSkill(ctx context.Context, db *sql.DB, id, userID string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM user_skills WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func ListUserPrompts(ctx context.Context, db *sql.DB, userID string) ([]UserPrompt, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, name, description, content,
		COALESCE(source_prompt_id,''), created_at, updated_at FROM user_prompts
		WHERE user_id=? ORDER BY updated_at DESC, name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserPrompt{}
	for rows.Next() {
		var prompt UserPrompt
		if err := rows.Scan(&prompt.ID, &prompt.UserID, &prompt.Name, &prompt.Description, &prompt.Content,
			&prompt.SourcePromptID, &prompt.CreatedAt, &prompt.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, prompt)
	}
	return out, rows.Err()
}

func GetUserPrompt(ctx context.Context, db *sql.DB, id, userID string) (*UserPrompt, error) {
	var prompt UserPrompt
	err := db.QueryRowContext(ctx, `SELECT id, user_id, name, description, content,
		COALESCE(source_prompt_id,''), created_at, updated_at FROM user_prompts WHERE id=? AND user_id=?`, id, userID,
	).Scan(&prompt.ID, &prompt.UserID, &prompt.Name, &prompt.Description, &prompt.Content,
		&prompt.SourcePromptID, &prompt.CreatedAt, &prompt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &prompt, nil
}

func CreateUserPrompt(ctx context.Context, db *sql.DB, prompt UserPrompt) (*UserPrompt, error) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Content = strings.TrimSpace(prompt.Content)
	if prompt.ID == "" {
		prompt.ID = genID("upm")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO user_prompts(id, user_id, name, description, content, source_prompt_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, prompt.ID, prompt.UserID, prompt.Name, prompt.Description,
		prompt.Content, nullableText(prompt.SourcePromptID), now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_prompts_user_name_unique", "user_prompts.user_id, user_prompts.name") {
			return nil, ErrUserPromptNameExists
		}
		if isUniqueIndexErr(err, "idx_user_prompts_source_unique", "user_prompts.user_id, user_prompts.source_prompt_id") {
			return nil, ErrCatalogItemAlreadyAdded
		}
		return nil, err
	}
	return GetUserPrompt(ctx, db, prompt.ID, prompt.UserID)
}

func UpdateUserPrompt(ctx context.Context, db *sql.DB, id, userID string, prompt UserPrompt) (*UserPrompt, error) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Content = strings.TrimSpace(prompt.Content)
	result, err := db.ExecContext(ctx, `UPDATE user_prompts SET name=?, description=?, content=?, updated_at=? WHERE id=? AND user_id=?`,
		prompt.Name, prompt.Description, prompt.Content, time.Now().Unix(), id, userID)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_prompts_user_name_unique", "user_prompts.user_id, user_prompts.name") {
			return nil, ErrUserPromptNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetUserPrompt(ctx, db, id, userID)
}

func DeleteUserPrompt(ctx context.Context, db *sql.DB, id, userID string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM user_prompts WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserLibrarySourceSets supports catalog "added" state without exposing any
// private row content. Every query is scoped to the authenticated user id.
func UserLibrarySourceSets(ctx context.Context, db *sql.DB, userID string) (map[string]bool, map[string]bool, error) {
	skills, err := sourceIDSet(ctx, db, `SELECT source_skill_id FROM user_skills WHERE user_id=? AND source_skill_id IS NOT NULL`, userID)
	if err != nil {
		return nil, nil, err
	}
	prompts, err := sourceIDSet(ctx, db, `SELECT source_prompt_id FROM user_prompts WHERE user_id=? AND source_prompt_id IS NOT NULL`, userID)
	if err != nil {
		return nil, nil, err
	}
	return skills, prompts, nil
}

func sourceIDSet(ctx context.Context, db *sql.DB, query, userID string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// ResolveUserSkillSelection normalizes ids, enforces the per-turn count and
// aggregate instruction limits, and resolves rows only inside userID's private
// library. strict=true rejects missing/not-owned ids; strict=false is used when
// regenerating an older turn after one of its selected skills was deleted.
func ResolveUserSkillSelection(ctx context.Context, db *sql.DB, userID string, ids []string, strict bool) ([]UserSkill, []string, error) {
	normalized := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	if len(normalized) > MaxSelectedUserSkills {
		return nil, nil, ErrInvalidUserSkillSelection
	}
	if len(normalized) == 0 {
		return []UserSkill{}, []string{}, nil
	}

	args := make([]any, 0, len(normalized)+1)
	args = append(args, userID)
	for _, id := range normalized {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, name, description, icon, instructions,
		COALESCE(source_skill_id,''), created_at, updated_at FROM user_skills
		WHERE user_id=? AND id IN (`+idPlaceholders(len(normalized))+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := make(map[string]UserSkill, len(normalized))
	for rows.Next() {
		var skill UserSkill
		if err := rows.Scan(&skill.ID, &skill.UserID, &skill.Name, &skill.Description, &skill.Icon, &skill.Instructions,
			&skill.SourceSkillID, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
			return nil, nil, err
		}
		byID[skill.ID] = skill
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	resolved := make([]UserSkill, 0, len(normalized))
	resolvedIDs := make([]string, 0, len(normalized))
	total := 0
	for _, id := range normalized {
		skill, ok := byID[id]
		if !ok {
			if strict {
				return nil, nil, ErrInvalidUserSkillSelection
			}
			continue
		}
		total += len(skill.Instructions)
		if total > MaxSelectedUserSkillInstructionBytes {
			return nil, nil, ErrInvalidUserSkillSelection
		}
		resolved = append(resolved, skill)
		resolvedIDs = append(resolvedIDs, id)
	}
	return resolved, resolvedIDs, nil
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
