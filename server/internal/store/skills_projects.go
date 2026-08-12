package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// ErrSkillNameExists is returned when a skill create/update would make skill
// names ambiguous for use_skill(name).
var ErrSkillNameExists = errors.New("skill name already exists")

// ListSkills returns every skill.
func ListSkills(ctx context.Context, db *sql.DB, onlyEnabled bool) ([]Skill, error) {
	q := `SELECT id, name, description, COALESCE(display_description,''), icon, instructions, assets, enabled, sort_order, updated_at FROM skills`
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
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.DisplayDescription, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt); err != nil {
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
		`SELECT id, name, description, COALESCE(display_description,''), icon, instructions, assets, enabled, sort_order, updated_at FROM skills WHERE id=?`, id,
	).Scan(&s.ID, &s.Name, &s.Description, &s.DisplayDescription, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt)
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
		`SELECT id, name, description, COALESCE(display_description,''), icon, instructions, assets, enabled, sort_order, updated_at FROM skills WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`,
		name,
	).Scan(&s.ID, &s.Name, &s.Description, &s.DisplayDescription, &s.Icon, &s.Instructions, &assets, &en, &s.SortOrder, &s.UpdatedAt)
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
	s.DisplayDescription = strings.TrimSpace(s.DisplayDescription)
	s.Icon = strings.TrimSpace(s.Icon)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if s.ID == "" {
		s.ID = genID("sk")
	}
	if len(s.Assets) == 0 {
		s.Assets = json.RawMessage("[]")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO skills(id, name, description, display_description, icon, instructions, assets, enabled, sort_order, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Description, s.DisplayDescription, s.Icon, s.Instructions, string(s.Assets), boolInt(s.Enabled), s.SortOrder, time.Now().Unix())
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
	s.DisplayDescription = strings.TrimSpace(s.DisplayDescription)
	s.Icon = strings.TrimSpace(s.Icon)
	s.Instructions = strings.TrimSpace(s.Instructions)
	if len(s.Assets) == 0 {
		s.Assets = json.RawMessage("[]")
	}
	result, err := db.ExecContext(ctx, `UPDATE skills SET name=?, description=?, display_description=?, icon=?, instructions=?, assets=?, enabled=?, sort_order=?, updated_at=? WHERE id=?`,
		s.Name, s.Description, s.DisplayDescription, s.Icon, s.Instructions, string(s.Assets), boolInt(s.Enabled), s.SortOrder, time.Now().Unix(), id)
	if err != nil {
		if isSkillNameUniqueErr(err) {
			return nil, ErrSkillNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetSkill(ctx, db, id)
}

func ReorderSkills(ctx context.Context, db *sql.DB, ids []string) error {
	return reorderAdminRecords(ctx, db, "skills", ids)
}

func isSkillNameUniqueErr(err error) bool {
	return isUniqueIndexErr(err, "idx_skills_name_unique", "skills.name")
}

// DeleteSkill removes the row.
func DeleteSkill(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, "DELETE FROM skills WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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
// CountProjectsByUser returns how many projects count against a user's group
// cap. Workspace projects created by the user count while the user is still an
// authoritative principal of that workspace; a revoked creator cannot keep
// consuming a cap for shared resources they can no longer access.
func CountProjectsByUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	return countProjectsByUser(ctx, db, userID)
}

type commercialCapQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func countProjectsByUser(ctx context.Context, q commercialCapQueryer, userID string) (int, error) {
	var n int
	args := []any{userID}
	args = append(args, workspaceResourceAccessArgs(userID)...)
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects
		  WHERE user_id=? AND `+workspaceResourceAccessPredicate("projects"), args...).Scan(&n)
	return n, err
}

// lockCommercialCapUserTx serializes all capped project/KB creates for one
// creator, including creates targeting different workspaces. Workspace callers
// must lock their workspace first to preserve the global workspace -> user lock
// order used by membership, storage, and account-deletion mutations.
func lockCommercialCapUserTx(ctx context.Context, tx *sql.Tx, userID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, userID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func validateWorkspaceResourceCreationTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) error {
	var one int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		  FROM workspaces create_workspace
		 WHERE create_workspace.id=?
		   AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
		   AND (
		       create_workspace.owner_id=? OR EXISTS (
		         SELECT 1 FROM workspace_members create_member
		          WHERE create_member.workspace_id=create_workspace.id AND create_member.user_id=?
		       )
		   )`, workspaceID, userID, userID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func ListProjects(ctx context.Context, db *sql.DB, userID string) ([]Project, error) {
	// Personal listing only — workspace projects are isolated (§workspaces) and
	// listed via ListWorkspaceProjects.
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
		        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
		        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
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

// ListWorkspaceProjects lists a workspace's shared projects without a caller
// boundary. It is reserved for administrator/maintenance views. User-facing
// callers must use ListWorkspaceProjectsForUser.
func ListWorkspaceProjects(ctx context.Context, db *sql.DB, workspaceID string) ([]Project, error) {
	return listWorkspaceProjects(ctx, db, workspaceID, "")
}

// ListWorkspaceProjectsForUser lists shared projects only while userID is the
// canonical workspace owner or a current member.
func ListWorkspaceProjectsForUser(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]Project, error) {
	return listWorkspaceProjects(ctx, db, workspaceID, userID)
}

func listWorkspaceProjects(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]Project, error) {
	q := `SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
	             COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
	             COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
		 FROM projects WHERE workspace_id=?`
	args := []any{workspaceID}
	if userID != "" {
		q += ` AND ` + workspaceResourceAccessPredicate("projects")
		args = append(args, workspaceResourceAccessArgs(userID)...)
	}
	q += ` ORDER BY pinned DESC, updated_at DESC`
	rows, err := db.QueryContext(ctx,
		q, args...)
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

// GetProject reads one row through the authoritative personal/workspace scope.
// A workspace project's original creator has no access after membership is
// revoked; the canonical workspace owner remains authoritative.
func GetProject(ctx context.Context, db *sql.DB, id, userID string) (*Project, error) {
	args := []any{id}
	args = append(args, workspaceResourceAccessArgs(userID)...)
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
		        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
		        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
		 FROM projects WHERE id=? AND `+workspaceResourceAccessPredicate("projects"), args...)
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
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
		        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
		        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
		 FROM projects
		 WHERE user_id=? AND COALESCE(workspace_id,'')=''
		   AND lower(trim(name))=lower(trim(?)) LIMIT 1`,
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
	if err := s.Scan(&p.ID, &p.UserID, &p.Name, &p.Description, &p.Instructions, &p.Accent, &p.Emoji, &pinned, &kbID, &autoAdd, &p.CreatedAt, &p.UpdatedAt, &p.WorkspaceID, &p.KBEmbeddingModelID, &p.KBEmbeddingDim); err != nil {
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
	var err error
	if p.WorkspaceID == "" {
		_, err = db.ExecContext(ctx, `INSERT INTO projects(
			id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, workspace_id
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.UserID, p.Name, p.Description, p.Instructions, p.Accent, p.Emoji,
			boolInt(p.Pinned), kbID, boolInt(p.AutoAddUploads), now, now, p.WorkspaceID)
	} else {
		tx, txErr := beginWorkspaceMutationTx(ctx, db, p.WorkspaceID)
		if txErr != nil {
			return nil, txErr
		}
		defer tx.Rollback() //nolint:errcheck
		var result sql.Result
		result, err = tx.ExecContext(ctx, `INSERT INTO projects(
			id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, workspace_id
		) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		    FROM workspaces create_workspace
		   WHERE create_workspace.id=?
		     AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
		     AND (
		         create_workspace.owner_id=? OR EXISTS (
		           SELECT 1 FROM workspace_members create_member
		            WHERE create_member.workspace_id=create_workspace.id AND create_member.user_id=?
		         )
		   )`,
			p.ID, p.UserID, p.Name, p.Description, p.Instructions, p.Accent, p.Emoji,
			boolInt(p.Pinned), kbID, boolInt(p.AutoAddUploads), now, now, p.WorkspaceID,
			p.WorkspaceID, p.UserID, p.UserID, p.UserID)
		if err != nil {
			if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
				return nil, ErrProjectNameExists
			}
			return nil, err
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if affected != 1 {
			return nil, ErrNotFound
		}
		created, scanErr := scanProject(tx.QueryRowContext(ctx,
			`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
			        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
			        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
			   FROM projects WHERE id=?`, p.ID))
		if scanErr != nil {
			return nil, scanErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &created, nil
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
			return nil, ErrProjectNameExists
		}
		return nil, err
	}
	return GetProject(ctx, db, p.ID, p.UserID)
}

// CreateProjectWithLimit atomically re-evaluates the creator's commercial cap
// and inserts the project. The creator-user row is the cross-workspace
// serialization point; a workspace row, when present, is locked first so
// membership revocation and resource creation cannot pass each other.
func CreateProjectWithLimit(ctx context.Context, db *sql.DB, p Project, maxProjects int) (*Project, error) {
	return createProjectWithLimit(ctx, db, p, nil, maxProjects, nil)
}

// CreateProjectWithLibraryAndLimit creates a project and its dedicated KB in
// one transaction. The library never becomes visible without its project, and
// a workspace kick can only linearize before the authorization check or after
// both rows commit.
func CreateProjectWithLibraryAndLimit(ctx context.Context, db *sql.DB, p Project, library KnowledgeBase, maxProjects int) (*Project, error) {
	return createProjectWithLimit(ctx, db, p, &library, maxProjects, nil)
}

func createProjectWithLimit(
	ctx context.Context,
	db *sql.DB,
	p Project,
	library *KnowledgeBase,
	maxProjects int,
	afterLibraryInsert func() error,
) (*Project, error) {
	if p.ID == "" {
		p.ID = genID("pr")
	}
	p.UserID = strings.TrimSpace(p.UserID)
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	p.Instructions = strings.TrimSpace(p.Instructions)
	if p.Accent == "" {
		p.Accent = "violet"
	}

	var projectLibrary *KnowledgeBase
	if library != nil {
		prepared := *library
		if prepared.ID == "" {
			prepared.ID = genID("kb")
		}
		prepared.UserID = strings.TrimSpace(prepared.UserID)
		prepared.WorkspaceID = strings.TrimSpace(prepared.WorkspaceID)
		prepared.ProjectID = strings.TrimSpace(prepared.ProjectID)
		prepared.Name = strings.TrimSpace(prepared.Name)
		prepared.Description = strings.TrimSpace(prepared.Description)
		if prepared.UserID != p.UserID || prepared.WorkspaceID != p.WorkspaceID || prepared.ProjectID != "" {
			return nil, errors.New("project library scope mismatch")
		}
		// knowledge_bases.project_id is the durable ownership marker used by the
		// standalone KB boundary. The reverse projects.kb_id relation remains the
		// runtime attachment, but relying on it alone made legacy project libraries
		// indistinguishable from ordinary KBs in listing and deletion paths.
		prepared.ProjectID = p.ID
		if p.KBID != "" && p.KBID != prepared.ID {
			return nil, errors.New("project library id mismatch")
		}
		p.KBID = prepared.ID
		projectLibrary = &prepared
	}
	now := time.Now().Unix()

	var tx *sql.Tx
	var err error
	if p.WorkspaceID == "" {
		tx, err = db.BeginTx(ctx, nil)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, p.WorkspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	if p.WorkspaceID != "" {
		if err := validateWorkspaceResourceCreationTx(ctx, tx, p.WorkspaceID, p.UserID); err != nil {
			return nil, err
		}
	}
	if err := lockCommercialCapUserTx(ctx, tx, p.UserID); err != nil {
		return nil, err
	}
	if maxProjects > 0 {
		n, err := countProjectsByUser(ctx, tx, p.UserID)
		if err != nil {
			return nil, err
		}
		if n >= maxProjects {
			return nil, ErrProjectLimitExceeded
		}
	}

	if projectLibrary != nil {
		if err := insertDedicatedProjectLibraryTx(ctx, tx, *projectLibrary, now); err != nil {
			return nil, err
		}
		if afterLibraryInsert != nil {
			if err := afterLibraryInsert(); err != nil {
				return nil, err
			}
		}
	}
	if err := insertProjectWithScopeTx(ctx, tx, p, now); err != nil {
		return nil, err
	}
	created, err := scanProject(tx.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
		        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
		        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
		   FROM projects WHERE id=?`, p.ID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &created, nil
}

func insertDedicatedProjectLibraryTx(ctx context.Context, tx *sql.Tx, kb KnowledgeBase, now int64) error {
	var err error
	if kb.WorkspaceID == "" {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, kb.ProjectID, now, kb.WorkspaceID)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx,
			`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id)
			 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
			   FROM workspaces create_workspace
			  WHERE create_workspace.id=?
			    AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
			    AND (
			        create_workspace.owner_id=? OR EXISTS (
			          SELECT 1 FROM workspace_members create_member
			           WHERE create_member.workspace_id=create_workspace.id AND create_member.user_id=?
			        )
			  )`,
			kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, kb.ProjectID, now, kb.WorkspaceID,
			kb.WorkspaceID, kb.UserID, kb.UserID, kb.UserID)
		if err == nil {
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
				return rowsErr
			} else if affected != 1 {
				return ErrNotFound
			}
		}
	}
	if err != nil && isUniqueIndexErr(err, "idx_kbs_user_name_unique", "knowledge_bases.user_id") {
		return ErrKBNameExists
	}
	return err
}

func insertProjectWithScopeTx(ctx context.Context, tx *sql.Tx, p Project, now int64) error {
	var kbID any
	if p.KBID == "" {
		kbID = nil
	} else {
		kbID = p.KBID
	}
	var err error
	if p.WorkspaceID == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO projects(
			id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, workspace_id
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.UserID, p.Name, p.Description, p.Instructions, p.Accent, p.Emoji,
			boolInt(p.Pinned), kbID, boolInt(p.AutoAddUploads), now, now, p.WorkspaceID)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `INSERT INTO projects(
			id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, workspace_id
		) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		    FROM workspaces create_workspace
		   WHERE create_workspace.id=?
		     AND `+workspaceAcceptsResourceCreationPredicate("create_workspace")+`
		     AND (
		         create_workspace.owner_id=? OR EXISTS (
		           SELECT 1 FROM workspace_members create_member
		            WHERE create_member.workspace_id=create_workspace.id AND create_member.user_id=?
		         )
		   )`,
			p.ID, p.UserID, p.Name, p.Description, p.Instructions, p.Accent, p.Emoji,
			boolInt(p.Pinned), kbID, boolInt(p.AutoAddUploads), now, now, p.WorkspaceID,
			p.WorkspaceID, p.UserID, p.UserID, p.UserID)
		if err == nil {
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
				return rowsErr
			} else if affected != 1 {
				return ErrNotFound
			}
		}
	}
	if err != nil && isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
		return ErrProjectNameExists
	}
	return err
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
	args = append(args, id)
	args = append(args, workspaceResourceAccessArgs(userID)...)
	q := "UPDATE projects SET " + strings.Join(parts, ", ") +
		" WHERE id=? AND " + workspaceResourceAccessPredicate("projects")
	workspaceID, err := projectWorkspaceID(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if workspaceID == "" {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
				return nil, ErrProjectNameExists
			}
			return nil, err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != 1 {
			return nil, ErrNotFound
		}
		return GetProject(ctx, db, id, userID)
	}

	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		if isUniqueIndexErr(err, "idx_projects_user_name_unique", "projects.user_id") {
			return nil, ErrProjectNameExists
		}
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	updated, err := scanProject(tx.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, instructions, accent, emoji, pinned, kb_id, auto_add_uploads, created_at, updated_at, COALESCE(workspace_id,''),
		        COALESCE((SELECT embedding_model_id FROM knowledge_bases WHERE id=projects.kb_id),''),
		        COALESCE((SELECT embedding_dim FROM knowledge_bases WHERE id=projects.kb_id),0)
		   FROM projects WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &updated, nil
}

// ProjectDeletionState is the exact, transactionally-derived external cleanup
// worklist for a project deletion. KnowledgeBaseIDs includes both durable
// project_id markers and the legacy projects.kb_id reverse relationship.
type ProjectDeletionState struct {
	KnowledgeBaseIDs []string
	StoragePaths     []string
}

// DeleteProject preserves the historical store API for callers that do not
// need the external vector/object-storage cleanup worklist.
func DeleteProject(ctx context.Context, db *sql.DB, id, userID string, storageRoots ...string) error {
	_, err := DeleteProjectWithState(ctx, db, id, userID, storageRoots...)
	return err
}

// DeleteProjectWithState removes a project and every knowledge base owned by
// it. The project and KB rows are deleted in one transaction; documents/chunks
// cascade through their FKs and stale conversation KB selections are removed
// explicitly. Personal owner or authoritative workspace principal (§workspaces).
//
// storageRoots are optional for backwards-compatible store callers. Physical
// cleanup is skipped when they are omitted; API handlers should pass the
// configured upload and artifact roots explicitly.
func DeleteProjectWithState(ctx context.Context, db *sql.DB, id, userID string, storageRoots ...string) (*ProjectDeletionState, error) {
	workspaceID, err := projectWorkspaceID(ctx, db, id)
	if err != nil {
		return nil, err
	}

	var tx *sql.Tx
	if workspaceID == "" {
		tx, err = db.BeginTx(ctx, nil)
	} else {
		tx, err = beginWorkspaceMutationTx(ctx, db, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Authorize inside the mutation transaction. Ordinary workspace members can
	// use a shared project, but only the workspace owner or its current creator
	// may destroy it.
	managerArgs := []any{id}
	managerArgs = append(managerArgs, workspaceResourceManagerArgs(userID)...)
	var projectUserID, authoritativeWorkspaceID, legacyKBID string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, COALESCE(workspace_id,''), COALESCE(kb_id,'')
		   FROM projects
		  WHERE id=? AND `+workspaceResourceManagerPredicate("projects"),
		managerArgs...,
	).Scan(&projectUserID, &authoritativeWorkspaceID, &legacyKBID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// New project libraries carry knowledge_bases.project_id. Older rows are
	// identified only by projects.kb_id, so include both forms while constraining
	// ownership and workspace scope to avoid widening a malformed relationship.
	kbRows, err := tx.QueryContext(ctx, `
		SELECT id
		  FROM knowledge_bases
		 WHERE user_id=?
		   AND COALESCE(workspace_id,'')=?
		   AND (project_id=? OR id=?)
		 ORDER BY id`, projectUserID, authoritativeWorkspaceID, id, legacyKBID)
	if err != nil {
		return nil, err
	}
	var kbIDs []string
	for kbRows.Next() {
		var kbID string
		if err := kbRows.Scan(&kbID); err != nil {
			_ = kbRows.Close()
			return nil, err
		}
		kbIDs = append(kbIDs, kbID)
	}
	if err := kbRows.Err(); err != nil {
		_ = kbRows.Close()
		return nil, err
	}
	if err := kbRows.Close(); err != nil {
		return nil, err
	}

	// Collect local paths before the cascading delete. Database cleanup remains
	// strict and atomic; physical storage cleanup is best-effort after commit.
	diskPathSet := make(map[string]struct{})
	if len(kbIDs) > 0 {
		pathRows, err := tx.QueryContext(ctx,
			`SELECT storage_path FROM documents WHERE kb_id IN (`+idPlaceholders(len(kbIDs))+`) AND storage_path<>''`,
			anySlice(kbIDs)...,
		)
		if err != nil {
			return nil, err
		}
		for pathRows.Next() {
			var path string
			if err := pathRows.Scan(&path); err != nil {
				_ = pathRows.Close()
				return nil, err
			}
			if path = strings.TrimSpace(path); path != "" {
				diskPathSet[path] = struct{}{}
			}
		}
		if err := pathRows.Err(); err != nil {
			_ = pathRows.Close()
			return nil, err
		}
		if err := pathRows.Close(); err != nil {
			return nil, err
		}
	}

	deleteArgs := []any{id}
	deleteArgs = append(deleteArgs, workspaceResourceManagerArgs(userID)...)
	res, err := tx.ExecContext(ctx,
		`DELETE FROM projects WHERE id=? AND `+workspaceResourceManagerPredicate("projects"),
		deleteArgs...,
	)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}

	if len(kbIDs) > 0 {
		kbDeleteArgs := anySlice(kbIDs)
		kbDeleteArgs = append(kbDeleteArgs, projectUserID, authoritativeWorkspaceID, id, legacyKBID)
		res, err = tx.ExecContext(ctx,
			`DELETE FROM knowledge_bases
			  WHERE id IN (`+idPlaceholders(len(kbIDs))+`)
			    AND user_id=?
			    AND COALESCE(workspace_id,'')=?
			    AND (project_id=? OR id=?)`,
			kbDeleteArgs...,
		)
		if err != nil {
			return nil, err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != int64(len(kbIDs)) {
			return nil, fmt.Errorf("delete project %s libraries: deleted %d of %d", id, n, len(kbIDs))
		}

		for _, kbID := range kbIDs {
			if IsPostgres() {
				_, err = tx.ExecContext(ctx, `
					UPDATE conversations
					SET kb_ids = COALESCE(
						(SELECT json_agg(value ORDER BY ordinality)
						 FROM json_array_elements_text(kb_ids::json) WITH ORDINALITY
						 WHERE value != $1),
						'[]'::json
					)::text
					WHERE kb_ids LIKE '%' || $1 || '%'
				`, kbID)
			} else {
				_, err = tx.ExecContext(ctx, `
					UPDATE conversations
					SET kb_ids = (
						SELECT COALESCE(json_group_array(value), '[]')
						FROM json_each(kb_ids)
						WHERE value != ?
					)
					WHERE json_type(kb_ids) = 'array' AND kb_ids LIKE '%' || ? || '%'
				`, kbID, kbID)
			}
			if err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	diskPaths := make([]string, 0, len(diskPathSet))
	for path := range diskPathSet {
		diskPaths = append(diskPaths, path)
		referenced, refErr := StoragePathReferenced(context.Background(), db, path)
		if refErr != nil {
			log.Printf("delete project %s: check storage refs for %q: %v", id, path, refErr)
			continue
		}
		if referenced {
			continue
		}
		if removeErr := removeLocalStoragePath(path, storageRoots...); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Printf("delete project %s: remove file %q: %v", id, path, removeErr)
		}
	}
	return &ProjectDeletionState{KnowledgeBaseIDs: kbIDs, StoragePaths: diskPaths}, nil
}

func projectWorkspaceID(ctx context.Context, db *sql.DB, id string) (string, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(workspace_id,'') FROM projects WHERE id=?`, id,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return workspaceID, nil
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
