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

func ReorderPrompts(ctx context.Context, db *sql.DB, ids []string) error {
	return reorderAdminRecords(ctx, db, "prompts", ids)
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

func libraryWorkspaceReadPredicate(rowAlias string) string {
	return `EXISTS (
		SELECT 1 FROM workspaces library_workspace
		LEFT JOIN workspace_members library_member
		  ON library_member.workspace_id=library_workspace.id AND library_member.user_id=?
		WHERE library_workspace.id=` + rowAlias + `.workspace_id
		  AND (library_workspace.owner_id=? OR library_member.user_id IS NOT NULL)
	)`
}

// libraryWorkspaceManagePredicateFor returns the write/manage boundary for an
// existing workspace library resource. Creation rights only control insertion:
// revoking one must not lock a non-guest creator out of content they already
// own. Workspace owners and admins may maintain every row in the workspace.
func libraryWorkspaceManagePredicateFor(rowAlias, _ string) string {
	creatorBoundary := isCollaboratorRoleSQL("library_member.role")
	return `EXISTS (
		SELECT 1 FROM workspaces library_workspace
		LEFT JOIN workspace_members library_member
		  ON library_member.workspace_id=library_workspace.id AND library_member.user_id=?
		WHERE library_workspace.id=` + rowAlias + `.workspace_id
		  AND (library_workspace.owner_id=?
		       OR ` + isAdminRoleSQL("library_member.role") + `
		       OR (` + rowAlias + `.user_id=?
		           AND ` + creatorBoundary + `))
	)`
}

// libraryWorkspaceCreateCapabilitySQL returns the authoritative granular
// creation check. The pre-granular aggregate column is migrated into the three
// granular columns at startup and is retained only as a response/input
// compatibility mirror; consulting it here would let a stale aggregate revoke
// one capability while the corresponding granular value is explicitly enabled.
func libraryWorkspaceCreateCapabilitySQL(memberAlias, column string) string {
	return `(` + memberAlias + `.` + column + `=1)`
}

// Legacy callers default to the historical combined skill/prompt capability.
func libraryWorkspaceManagePredicate(rowAlias string) string {
	return libraryWorkspaceManagePredicateFor(rowAlias, "legacy")
}

func libraryWorkspaceCreatePredicateFor(workspaceAlias, capability string) string {
	column := map[string]string{
		"skill":  "can_create_skills",
		"prompt": "can_create_prompts",
		"mcp":    "can_create_mcp",
	}[strings.ToLower(strings.TrimSpace(capability))]
	if column == "" {
		column = "can_create_skills_prompts"
	}
	return `(` + workspaceAlias + `.owner_id=? OR EXISTS (
		SELECT 1 FROM workspace_members library_creator
		 WHERE library_creator.workspace_id=` + workspaceAlias + `.id
		   AND library_creator.user_id=?
		   AND (` + isAdminRoleSQL("library_creator.role") + ` OR ` + libraryWorkspaceCreateCapabilitySQL("library_creator", column) + `)
		   AND ` + isCollaboratorRoleSQL("library_creator.role") + `
	))`
}

func libraryWorkspaceCreatePredicate(workspaceAlias string) string {
	return libraryWorkspaceCreatePredicateFor(workspaceAlias, "legacy")
}

// libraryWorkspaceCapabilityPredicate is a fail-closed workspace-wide switch
// check used by direct store reads and writes. A missing policy row means the
// historical permissive default; an explicit row must opt the capability in.
func libraryWorkspaceCapabilityPredicateForExpr(workspaceExpr, capability string) string {
	column := map[string]string{
		"skill":  "allow_skills",
		"prompt": "allow_prompts",
		"mcp":    "allow_mcp",
	}[strings.ToLower(strings.TrimSpace(capability))]
	if column == "" {
		return "1=1"
	}
	return `(NOT EXISTS (SELECT 1 FROM workspace_policies library_policy0 WHERE library_policy0.workspace_id=` + workspaceExpr + `)
		OR EXISTS (SELECT 1 FROM workspace_policies library_policy1 WHERE library_policy1.workspace_id=` + workspaceExpr + ` AND library_policy1.` + column + `=1))`
}

func libraryWorkspaceCapabilityPredicate(rowAlias, capability string) string {
	return libraryWorkspaceCapabilityPredicateForExpr(rowAlias+`.workspace_id`, capability)
}

func workspaceLibraryCapabilityAllowed(ctx context.Context, db *sql.DB, workspaceID, capability string) (bool, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return true, nil
	}
	policy, err := GetWorkspacePolicy(ctx, db, workspaceID)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(capability)) {
	case "skill":
		return policy.AllowSkills, nil
	case "prompt":
		return policy.AllowPrompts, nil
	case "mcp":
		return policy.AllowMCP, nil
	default:
		return true, nil
	}
}

func ListUserSkills(ctx context.Context, db *sql.DB, userID string) ([]UserSkill, error) {
	return ListUserSkillsScoped(ctx, db, userID, "")
}

// ListUserSkillsScoped returns the personal library for workspaceID="" and the
// shared workspace library otherwise. Workspace rows are visible to every
// current member; CanManage is true only for admins or the current non-guest
// creator.
func ListUserSkillsScoped(ctx context.Context, db *sql.DB, userID, workspaceID string) ([]UserSkill, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT us.id, us.user_id, COALESCE(us.workspace_id,''), us.name, us.description,
		COALESCE(NULLIF(TRIM(s.display_description),''), s.description, ''), us.icon, us.instructions,
		COALESCE(us.source_skill_id,''), us.created_at, us.updated_at, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
			WHERE us.user_id=? AND COALESCE(us.workspace_id,'')=''`
		args = append(args, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("us", "skill") + ` THEN 1 ELSE 0 END
			FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
			WHERE us.workspace_id=? AND ` + libraryWorkspaceReadPredicate("us") + ` AND ` + libraryWorkspaceCapabilityPredicate("us", "skill")
		args = append(args, userID, userID, userID, workspaceID, userID, userID)
	}
	query += ` ORDER BY us.updated_at DESC, us.name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserSkill{}
	for rows.Next() {
		var skill UserSkill
		if err := rows.Scan(&skill.ID, &skill.UserID, &skill.WorkspaceID, &skill.Name, &skill.Description, &skill.DisplayDescription, &skill.Icon, &skill.Instructions,
			&skill.SourceSkillID, &skill.CreatedAt, &skill.UpdatedAt, &skill.CanManage); err != nil {
			return nil, err
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

func GetUserSkill(ctx context.Context, db *sql.DB, id, userID string) (*UserSkill, error) {
	return GetUserSkillScoped(ctx, db, id, userID, "")
}

func GetUserSkillScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string) (*UserSkill, error) {
	var skill UserSkill
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT us.id, us.user_id, COALESCE(us.workspace_id,''), us.name, us.description,
		COALESCE(NULLIF(TRIM(s.display_description),''), s.description, ''), us.icon, us.instructions,
		COALESCE(us.source_skill_id,''), us.created_at, us.updated_at, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
			WHERE us.id=? AND us.user_id=? AND COALESCE(us.workspace_id,'')=''`
		args = append(args, id, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("us", "skill") + ` THEN 1 ELSE 0 END
			FROM user_skills us LEFT JOIN skills s ON s.id=us.source_skill_id
			WHERE us.id=? AND us.workspace_id=? AND ` + libraryWorkspaceReadPredicate("us") + ` AND ` + libraryWorkspaceCapabilityPredicate("us", "skill")
		args = append(args, userID, userID, userID, id, workspaceID, userID, userID)
	}
	err := db.QueryRowContext(ctx, query, args...).Scan(
		&skill.ID, &skill.UserID, &skill.WorkspaceID, &skill.Name, &skill.Description, &skill.DisplayDescription, &skill.Icon, &skill.Instructions,
		&skill.SourceSkillID, &skill.CreatedAt, &skill.UpdatedAt, &skill.CanManage,
	)
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
	var result sql.Result
	var err error
	if strings.TrimSpace(skill.WorkspaceID) == "" {
		result, err = db.ExecContext(ctx, `INSERT INTO user_skills(id, user_id, workspace_id, name, description, icon, instructions, source_skill_id, created_at, updated_at)
			VALUES(?, ?, '', ?, ?, ?, ?, ?, ?, ?)`, skill.ID, skill.UserID, skill.Name, skill.Description,
			skill.Icon, skill.Instructions, nullableText(skill.SourceSkillID), now, now)
	} else {
		skill.WorkspaceID = strings.TrimSpace(skill.WorkspaceID)
		result, err = db.ExecContext(ctx, `INSERT INTO user_skills(id, user_id, workspace_id, name, description, icon, instructions, source_skill_id, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM workspaces library_workspace
				 WHERE library_workspace.id=? AND `+libraryWorkspaceCreatePredicateFor("library_workspace", "skill")+`
				   AND `+libraryWorkspaceCapabilityPredicateForExpr("library_workspace.id", "skill"),
			skill.ID, skill.UserID, skill.WorkspaceID, skill.Name, skill.Description, skill.Icon, skill.Instructions,
			nullableText(skill.SourceSkillID), now, now, skill.WorkspaceID, skill.UserID, skill.UserID)
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_skills_user_name_unique", "idx_user_skills_workspace_name_unique", "user_skills.user_id, user_skills.name") {
			return nil, ErrUserSkillNameExists
		}
		if isUniqueIndexErr(err, "idx_user_skills_source_unique", "idx_user_skills_workspace_source_unique", "user_skills.user_id, user_skills.source_skill_id") {
			return nil, ErrCatalogItemAlreadyAdded
		}
		return nil, err
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	return GetUserSkillScoped(ctx, db, skill.ID, skill.UserID, skill.WorkspaceID)
}

func UpdateUserSkill(ctx context.Context, db *sql.DB, id, userID string, skill UserSkill) (*UserSkill, error) {
	return UpdateUserSkillScoped(ctx, db, id, userID, "", skill)
}

func UpdateUserSkillScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string, skill UserSkill) (*UserSkill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Icon = strings.TrimSpace(skill.Icon)
	skill.Instructions = strings.TrimSpace(skill.Instructions)
	workspaceID = strings.TrimSpace(workspaceID)
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `UPDATE user_skills SET name=?, description=?, icon=?, instructions=?, updated_at=? WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`,
			skill.Name, skill.Description, skill.Icon, skill.Instructions, time.Now().Unix(), id, userID)
	} else {
		result, err = db.ExecContext(ctx, `UPDATE user_skills SET name=?, description=?, icon=?, instructions=?, updated_at=?
				WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_skills", "skill")+` AND `+libraryWorkspaceCapabilityPredicate("user_skills", "skill"),
			skill.Name, skill.Description, skill.Icon, skill.Instructions, time.Now().Unix(), id, workspaceID,
			userID, userID, userID)
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_skills_user_name_unique", "idx_user_skills_workspace_name_unique", "user_skills.user_id, user_skills.name") {
			return nil, ErrUserSkillNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetUserSkillScoped(ctx, db, id, userID, workspaceID)
}

func DeleteUserSkill(ctx context.Context, db *sql.DB, id, userID string) error {
	return DeleteUserSkillScoped(ctx, db, id, userID, "")
}

func DeleteUserSkillScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `DELETE FROM user_skills WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`, id, userID)
	} else {
		result, err = db.ExecContext(ctx, `DELETE FROM user_skills
				WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_skills", "skill")+` AND `+libraryWorkspaceCapabilityPredicate("user_skills", "skill"),
			id, workspaceID, userID, userID, userID)
	}
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func ListUserPrompts(ctx context.Context, db *sql.DB, userID string) ([]UserPrompt, error) {
	return ListUserPromptsScoped(ctx, db, userID, "")
}

func ListUserPromptsScoped(ctx context.Context, db *sql.DB, userID, workspaceID string) ([]UserPrompt, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT up.id, up.user_id, COALESCE(up.workspace_id,''), up.name, up.description, up.content,
		COALESCE(up.source_prompt_id,''), up.created_at, up.updated_at, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_prompts up WHERE up.user_id=? AND COALESCE(up.workspace_id,'')=''`
		args = append(args, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("up", "prompt") + ` THEN 1 ELSE 0 END
			FROM user_prompts up WHERE up.workspace_id=? AND ` + libraryWorkspaceReadPredicate("up") + ` AND ` + libraryWorkspaceCapabilityPredicate("up", "prompt")
		args = append(args, userID, userID, userID, workspaceID, userID, userID)
	}
	query += ` ORDER BY up.updated_at DESC, up.name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserPrompt{}
	for rows.Next() {
		var prompt UserPrompt
		if err := rows.Scan(&prompt.ID, &prompt.UserID, &prompt.WorkspaceID, &prompt.Name, &prompt.Description, &prompt.Content,
			&prompt.SourcePromptID, &prompt.CreatedAt, &prompt.UpdatedAt, &prompt.CanManage); err != nil {
			return nil, err
		}
		out = append(out, prompt)
	}
	return out, rows.Err()
}

func GetUserPrompt(ctx context.Context, db *sql.DB, id, userID string) (*UserPrompt, error) {
	return GetUserPromptScoped(ctx, db, id, userID, "")
}

func GetUserPromptScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string) (*UserPrompt, error) {
	var prompt UserPrompt
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT up.id, up.user_id, COALESCE(up.workspace_id,''), up.name, up.description, up.content,
		COALESCE(up.source_prompt_id,''), up.created_at, up.updated_at, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_prompts up WHERE up.id=? AND up.user_id=? AND COALESCE(up.workspace_id,'')=''`
		args = append(args, id, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("up", "prompt") + ` THEN 1 ELSE 0 END
			FROM user_prompts up WHERE up.id=? AND up.workspace_id=? AND ` + libraryWorkspaceReadPredicate("up") + ` AND ` + libraryWorkspaceCapabilityPredicate("up", "prompt")
		args = append(args, userID, userID, userID, id, workspaceID, userID, userID)
	}
	err := db.QueryRowContext(ctx, query, args...).Scan(
		&prompt.ID, &prompt.UserID, &prompt.WorkspaceID, &prompt.Name, &prompt.Description, &prompt.Content,
		&prompt.SourcePromptID, &prompt.CreatedAt, &prompt.UpdatedAt, &prompt.CanManage,
	)
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
	var result sql.Result
	var err error
	if strings.TrimSpace(prompt.WorkspaceID) == "" {
		result, err = db.ExecContext(ctx, `INSERT INTO user_prompts(id, user_id, workspace_id, name, description, content, source_prompt_id, created_at, updated_at)
			VALUES(?, ?, '', ?, ?, ?, ?, ?, ?)`, prompt.ID, prompt.UserID, prompt.Name, prompt.Description,
			prompt.Content, nullableText(prompt.SourcePromptID), now, now)
	} else {
		prompt.WorkspaceID = strings.TrimSpace(prompt.WorkspaceID)
		result, err = db.ExecContext(ctx, `INSERT INTO user_prompts(id, user_id, workspace_id, name, description, content, source_prompt_id, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ? FROM workspaces library_workspace
			 WHERE library_workspace.id=? AND `+libraryWorkspaceCreatePredicateFor("library_workspace", "prompt")+`
			   AND `+libraryWorkspaceCapabilityPredicateForExpr("library_workspace.id", "prompt"),
			prompt.ID, prompt.UserID, prompt.WorkspaceID, prompt.Name, prompt.Description, prompt.Content,
			nullableText(prompt.SourcePromptID), now, now, prompt.WorkspaceID, prompt.UserID, prompt.UserID)
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_prompts_user_name_unique", "idx_user_prompts_workspace_name_unique", "user_prompts.user_id, user_prompts.name") {
			return nil, ErrUserPromptNameExists
		}
		if isUniqueIndexErr(err, "idx_user_prompts_source_unique", "idx_user_prompts_workspace_source_unique", "user_prompts.user_id, user_prompts.source_prompt_id") {
			return nil, ErrCatalogItemAlreadyAdded
		}
		return nil, err
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	return GetUserPromptScoped(ctx, db, prompt.ID, prompt.UserID, prompt.WorkspaceID)
}

func UpdateUserPrompt(ctx context.Context, db *sql.DB, id, userID string, prompt UserPrompt) (*UserPrompt, error) {
	return UpdateUserPromptScoped(ctx, db, id, userID, "", prompt)
}

func UpdateUserPromptScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string, prompt UserPrompt) (*UserPrompt, error) {
	prompt.Name = strings.TrimSpace(prompt.Name)
	prompt.Description = strings.TrimSpace(prompt.Description)
	prompt.Content = strings.TrimSpace(prompt.Content)
	workspaceID = strings.TrimSpace(workspaceID)
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `UPDATE user_prompts SET name=?, description=?, content=?, updated_at=? WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`,
			prompt.Name, prompt.Description, prompt.Content, time.Now().Unix(), id, userID)
	} else {
		result, err = db.ExecContext(ctx, `UPDATE user_prompts SET name=?, description=?, content=?, updated_at=?
			WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_prompts", "prompt")+` AND `+libraryWorkspaceCapabilityPredicate("user_prompts", "prompt"),
			prompt.Name, prompt.Description, prompt.Content, time.Now().Unix(), id, workspaceID,
			userID, userID, userID)
	}
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_prompts_user_name_unique", "idx_user_prompts_workspace_name_unique", "user_prompts.user_id, user_prompts.name") {
			return nil, ErrUserPromptNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetUserPromptScoped(ctx, db, id, userID, workspaceID)
}

func DeleteUserPrompt(ctx context.Context, db *sql.DB, id, userID string) error {
	return DeleteUserPromptScoped(ctx, db, id, userID, "")
}

func DeleteUserPromptScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `DELETE FROM user_prompts WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`, id, userID)
	} else {
		result, err = db.ExecContext(ctx, `DELETE FROM user_prompts
			WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_prompts", "prompt")+` AND `+libraryWorkspaceCapabilityPredicate("user_prompts", "prompt"),
			id, workspaceID, userID, userID, userID)
	}
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
	return UserLibrarySourceSetsScoped(ctx, db, userID, "")
}

func UserLibrarySourceSetsScoped(ctx context.Context, db *sql.DB, userID, workspaceID string) (map[string]bool, map[string]bool, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	skillQuery := `SELECT source_skill_id FROM user_skills WHERE source_skill_id IS NOT NULL AND COALESCE(workspace_id,'')=?`
	promptQuery := `SELECT source_prompt_id FROM user_prompts WHERE source_prompt_id IS NOT NULL AND COALESCE(workspace_id,'')=?`
	if workspaceID == "" {
		skillQuery += ` AND user_id=?`
		promptQuery += ` AND user_id=?`
	} else {
		skillQuery += ` AND ` + libraryWorkspaceReadPredicate("user_skills") + ` AND ` + libraryWorkspaceCapabilityPredicate("user_skills", "skill")
		promptQuery += ` AND ` + libraryWorkspaceReadPredicate("user_prompts") + ` AND ` + libraryWorkspaceCapabilityPredicate("user_prompts", "prompt")
	}
	skillArgs := []any{workspaceID}
	promptArgs := []any{workspaceID}
	if workspaceID == "" {
		skillArgs = append(skillArgs, userID)
		promptArgs = append(promptArgs, userID)
	} else {
		skillArgs = append(skillArgs, userID, userID)
		promptArgs = append(promptArgs, userID, userID)
	}
	skills, err := sourceIDSet(ctx, db, skillQuery, skillArgs...)
	if err != nil {
		return nil, nil, err
	}
	prompts, err := sourceIDSet(ctx, db, promptQuery, promptArgs...)
	if err != nil {
		return nil, nil, err
	}
	return skills, prompts, nil
}

func sourceIDSet(ctx context.Context, db *sql.DB, query string, args ...any) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
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
	return ResolveUserSkillSelectionScoped(ctx, db, userID, "", ids, strict)
}

func ResolveUserSkillSelectionScoped(ctx context.Context, db *sql.DB, userID, workspaceID string, ids []string, strict bool) ([]UserSkill, []string, error) {
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

	workspaceID = strings.TrimSpace(workspaceID)
	args := make([]any, 0, len(normalized)+4)
	query := `SELECT us.id, us.user_id, COALESCE(us.workspace_id,''), us.name, us.description, us.icon, us.instructions,
		COALESCE(us.source_skill_id,''), us.created_at, us.updated_at FROM user_skills us WHERE `
	if workspaceID == "" {
		query += `us.user_id=? AND COALESCE(us.workspace_id,'')='' AND `
		args = append(args, userID)
	} else {
		query += `us.workspace_id=? AND ` + libraryWorkspaceReadPredicate("us") + ` AND ` + libraryWorkspaceCapabilityPredicate("us", "skill") + ` AND `
		args = append(args, workspaceID, userID, userID)
	}
	query += `us.id IN (` + idPlaceholders(len(normalized)) + `)`
	for _, id := range normalized {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := make(map[string]UserSkill, len(normalized))
	for rows.Next() {
		var skill UserSkill
		if err := rows.Scan(&skill.ID, &skill.UserID, &skill.WorkspaceID, &skill.Name, &skill.Description, &skill.Icon, &skill.Instructions,
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
