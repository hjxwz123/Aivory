package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrSkillNameExists is returned when a skill create/update would make skill
// names ambiguous for use_skill(name).
var ErrSkillNameExists = errors.New("skill name already exists")

// ListSkills returns every skill.
func ListSkills(ctx context.Context, db *sql.DB, onlyEnabled bool) ([]Skill, error) {
	q := `SELECT id, name, description, icon, instructions, assets, enabled, sort_order, updated_at FROM skills`
	if onlyEnabled {
		q += " WHERE enabled=1"
	}
	q += " ORDER BY sort_order, name"
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var s Skill
		var en int
		var assets string
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Enabled = en == 1
		s.Assets = json.RawMessage(assets)
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSkill returns one row.
func GetSkill(ctx context.Context, db *sql.DB, id string) (*Skill, error) {
	var s Skill
	var en int
	var assets string
	err := db.QueryRowContext(ctx,
		`SELECT id, name, description, icon, instructions, assets, enabled, sort_order, updated_at FROM skills WHERE id=?`, id,
	).Scan(&s.ID, &s.Name, &s.Description, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = en == 1
	s.Assets = json.RawMessage(assets)
	return &s, nil
}

// GetSkillByName returns a skill by its case-insensitive, trimmed name.
func GetSkillByName(ctx context.Context, db *sql.DB, name string) (*Skill, error) {
	var s Skill
	var en int
	var assets string
	err := db.QueryRowContext(ctx,
		`SELECT id, name, description, icon, instructions, assets, enabled, sort_order, updated_at FROM skills WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`,
		name,
	).Scan(&s.ID, &s.Name, &s.Description, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = en == 1
	s.Assets = json.RawMessage(assets)
	return &s, nil
}

// CreateSkill inserts a row.
func CreateSkill(ctx context.Context, db *sql.DB, s Skill) (*Skill, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if s.ID == "" {
		s.ID = genID("sk")
	}
	if len(s.Assets) == 0 {
		s.Assets = json.RawMessage("[]")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO skills(id, name, description, icon, instructions, assets, enabled, sort_order, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Description, s.Icon, s.Instructions, string(s.Assets), boolInt(s.Enabled), s.SortOrder, time.Now().Unix())
	if err != nil {
		if isSkillNameUniqueErr(err) {
			return nil, ErrSkillNameExists
		}
		return nil, err
	}
	return GetSkill(ctx, db, s.ID)
}

// UpdateSkill writes selective fields (using full struct).
func UpdateSkill(ctx context.Context, db *sql.DB, id string, s Skill) (*Skill, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if len(s.Assets) == 0 {
		s.Assets = json.RawMessage("[]")
	}
	_, err := db.ExecContext(ctx, `UPDATE skills SET name=?, description=?, icon=?, instructions=?, assets=?, enabled=?, sort_order=?, updated_at=? WHERE id=?`,
		s.Name, s.Description, s.Icon, s.Instructions, string(s.Assets), boolInt(s.Enabled), s.SortOrder, time.Now().Unix(), id)
	if err != nil {
		if isSkillNameUniqueErr(err) {
			return nil, ErrSkillNameExists
		}
		return nil, err
	}
	return GetSkill(ctx, db, id)
}

func isSkillNameUniqueErr(err error) bool {
	return isUniqueIndexErr(err, "idx_skills_name_unique", "skills.name")
}

// DeleteSkill removes the row.
func DeleteSkill(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM skills WHERE id=?", id)
	return err
}

// SkillAsset describes one downloadable file bundled with a skill (§4.17 — the
// `use_skill` flow stages these into /workspace/skills/<name>/).
type SkillAsset struct {
	SkillID     string `json:"skill_id"`
	Filename    string `json:"filename"`
	StoragePath string `json:"-"`
	MimeType    string `json:"mime_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// ListSkillAssets reads the skill's `assets` JSON column. We persist
// [{filename, storage_path, mime_type, size_bytes}, …] so the sandbox can stage
// them by path.
func ListSkillAssets(ctx context.Context, db *sql.DB, skillID string) ([]SkillAsset, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT assets FROM skills WHERE id=?`, skillID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var rows []struct {
		Filename    string `json:"filename"`
		StoragePath string `json:"storage_path"`
		MimeType    string `json:"mime_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, nil
	}
	out := make([]SkillAsset, 0, len(rows))
	for _, r := range rows {
		if r.Filename == "" || r.StoragePath == "" {
			continue
		}
		out = append(out, SkillAsset{
			SkillID: skillID, Filename: r.Filename, StoragePath: r.StoragePath,
			MimeType: r.MimeType, SizeBytes: r.SizeBytes,
		})
	}
	return out, nil
}

// ListProjects returns the user's projects.
// CountProjectsByUser returns how many projects a user owns (§ user-group caps).
func CountProjectsByUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

func ListProjects(ctx context.Context, db *sql.DB, userID string) ([]Project, error) {
	// Personal listing only — workspace projects are isolated (§workspaces) and
	// listed via ListWorkspaceProjects.
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,'')
		 FROM projects WHERE user_id=? AND COALESCE(workspace_id,'')='' ORDER BY pinned DESC, updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListWorkspaceProjects lists a workspace's shared projects (§workspaces).
// Membership is the handler's job.
func ListWorkspaceProjects(ctx context.Context, db *sql.DB, workspaceID string) ([]Project, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,'')
		 FROM projects WHERE workspace_id=? ORDER BY pinned DESC, updated_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject reads one row and checks ownership: the owner, or — for a
// workspace project — any member of that workspace (§workspaces).
func GetProject(ctx context.Context, db *sql.DB, id, userID string) (*Project, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,'')
		 FROM projects WHERE id=? AND (user_id=? OR (COALESCE(workspace_id,'')<>'' AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)))`, id, userID, userID)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProjectByName returns a user's project by case-insensitive, trimmed name.
func GetProjectByName(ctx context.Context, db *sql.DB, userID, name string) (*Project, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,'')
		 FROM projects WHERE user_id=? AND lower(trim(name))=lower(trim(?)) LIMIT 1`,
		userID, name)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanProject(s scanner) (Project, error) {
	var p Project
	var pinned, autoAdd int
	var kbID sql.NullString
	if err := s.Scan(&p.ID, &p.UserID, &p.Name, &p.Description, &p.Instructions, &p.Accent, &p.Emoji, &pinned, &kbID, &autoAdd, &p.CreatedAt, &p.UpdatedAt, &p.WorkspaceID); err != nil {
		return p, err
	}
	p.Pinned = pinned == 1
	p.AutoAddUploads = autoAdd == 1
	p.KBID = kbID.String
	return p, nil
}

// CreateProject inserts a row and (implicitly) the caller is expected to also
// create the project knowledge base in the same transaction at the handler
// level. Pass kbID="" to leave it null.
func CreateProject(ctx context.Context, db *sql.DB, p Project) (*Project, error) {
	if p.ID == "" {
		p.ID = genID("pr")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	p.Instructions = strings.TrimSpace(p.Instructions)
	if p.Accent == "" {
		p.Accent = "violet"
	}
	now := time.Now().Unix()
	var kbID any
	if p.KBID == "" {
		kbID = nil
	} else {
		kbID = p.KBID
	}
	_, err := db.ExecContext(ctx, `INSERT INTO projects(
		id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, workspace_id
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.Name, p.Description, p.Instructions, p.Accent, p.Emoji,
		boolInt(p.Pinned), kbID, boolInt(p.AutoAddUploads), now, now, p.WorkspaceID)
	if err != nil {
		if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
			return nil, ErrProjectNameExists
		}
		return nil, err
	}
	return GetProject(ctx, db, p.ID, p.UserID)
}

// UpdateProject writes selective fields. Use the patch shape.
type ProjectPatch struct {
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	Instructions   *string `json:"instructions"`
	Accent         *string `json:"accent"`
	Emoji          *string `json:"emoji"`
	Pinned         *bool   `json:"pinned"`
	AutoAddUploads *bool   `json:"auto_add_uploads"`
}

func UpdateProject(ctx context.Context, db *sql.DB, id, userID string, patch ProjectPatch) (*Project, error) {
	parts := []string{}
	args := []any{}
	if patch.Name != nil {
		parts = append(parts, "name=?")
		args = append(args, strings.TrimSpace(*patch.Name))
	}
	if patch.Description != nil {
		parts = append(parts, "description=?")
		args = append(args, strings.TrimSpace(*patch.Description))
	}
	if patch.Instructions != nil {
		parts = append(parts, "instructions=?")
		args = append(args, strings.TrimSpace(*patch.Instructions))
	}
	if patch.Accent != nil {
		parts = append(parts, "accent=?")
		args = append(args, *patch.Accent)
	}
	if patch.Emoji != nil {
		parts = append(parts, "emoji=?")
		args = append(args, *patch.Emoji)
	}
	if patch.Pinned != nil {
		parts = append(parts, "pinned=?")
		args = append(args, boolInt(*patch.Pinned))
	}
	if patch.AutoAddUploads != nil {
		parts = append(parts, "auto_add_uploads=?")
		args = append(args, boolInt(*patch.AutoAddUploads))
	}
	if len(parts) == 0 {
		return GetProject(ctx, db, id, userID)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	args = append(args, id, userID, userID)
	// Owner or workspace member (§workspaces — members edit shared projects).
	q := "UPDATE projects SET " + strings.Join(parts, ", ") +
		" WHERE id=? AND (user_id=? OR (COALESCE(workspace_id,'')<>'' AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)))"
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
			return nil, ErrProjectNameExists
		}
		return nil, err
	}
	return GetProject(ctx, db, id, userID)
}

// DeleteProject removes a row and unsets conversations.project_id via FK.
// Owner or workspace member (§workspaces).
func DeleteProject(ctx context.Context, db *sql.DB, id, userID string) error {
	res, err := db.ExecContext(ctx, "DELETE FROM projects WHERE id=? AND (user_id=? OR (COALESCE(workspace_id,'')<>'' AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id=?)))", id, userID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProjectKB attaches a knowledge base id to a project.
func SetProjectKB(ctx context.Context, db *sql.DB, projectID, kbID string) error {
	var kb any
	if kbID == "" {
		kb = nil
	} else {
		kb = kbID
	}
	_, err := db.ExecContext(ctx, "UPDATE projects SET kb_id=?, updated_at=? WHERE id=?", kb, time.Now().Unix(), projectID)
	return err
}
