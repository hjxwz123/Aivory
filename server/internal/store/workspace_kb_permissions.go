package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ListWorkspaceKnowledgeBaseMemberPermissions returns the per-library layer for
// one standalone workspace knowledge base. Only workspace admins may manage this list.
func ListWorkspaceKnowledgeBaseMemberPermissions(
	ctx context.Context,
	db *sql.DB,
	kbID, managerID string,
) ([]WorkspaceKnowledgeBaseMemberPermission, error) {
	if err := requireWorkspaceKnowledgeBaseManager(ctx, db, kbID, managerID); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, workspaceKnowledgeBaseMemberPermissionsQuery()+`
		ORDER BY CASE WHEN w.owner_id=m.user_id THEN 0 WHEN k.user_id=m.user_id THEN 1 ELSE 2 END,
		         LOWER(COALESCE(u.name,'')), LOWER(COALESCE(u.email,'')), m.user_id`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []WorkspaceKnowledgeBaseMemberPermission{}
	for rows.Next() {
		item, err := scanWorkspaceKnowledgeBaseMemberPermission(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateWorkspaceKnowledgeBaseMemberPermission changes only the library-level
// layer. Workspace-member total permissions remain independent upper bounds
// for ordinary members. Only workspace admins may manage member permissions.
func UpdateWorkspaceKnowledgeBaseMemberPermission(
	ctx context.Context,
	db *sql.DB,
	kbID, managerID, memberID string,
	canAddFiles, canDeleteContent bool,
) (*WorkspaceKnowledgeBaseMemberPermission, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(workspace_id,'') FROM knowledge_bases WHERE id=?`, kbID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if workspaceID == "" {
		return nil, ErrNotFound
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var allowed int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		JOIN workspaces w ON w.id=k.workspace_id
		WHERE k.id=? AND `+standaloneKnowledgeBasePredicate("k")+`
		  AND `+workspaceDirectoryManagerPredicate("k"),
		append([]any{kbID}, workspaceDirectoryManagerArgs(managerID)...)...).Scan(&allowed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Only workspace owners/admins bypass content overlays. Library creators
	// remain subject to the member ceiling.
	res, err := tx.ExecContext(ctx, `INSERT INTO workspace_kb_member_permissions(
		kb_id,user_id,can_add_files,can_delete_content,updated_at
	)
	SELECT k.id,m.user_id,?,?,?
	  FROM knowledge_bases k
	  JOIN workspaces w ON w.id=k.workspace_id
	  JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
	 WHERE k.id=? AND m.user_id<>w.owner_id
	   AND COALESCE(m.role,'') NOT IN ('admin','owner')
	ON CONFLICT(kb_id,user_id) DO UPDATE SET
	  can_add_files=excluded.can_add_files,
	  can_delete_content=excluded.can_delete_content,
	  updated_at=excluded.updated_at`,
		boolInt(canAddFiles), boolInt(canDeleteContent), time.Now().Unix(), memberID, kbID)
	if err != nil {
		return nil, err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}

	item, err := scanWorkspaceKnowledgeBaseMemberPermission(tx.QueryRowContext(ctx,
		workspaceKnowledgeBaseMemberPermissionsQuery()+` AND m.user_id=?`, kbID, memberID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

// requireWorkspaceKnowledgeBaseManager protects the member directory even when
// an ordinary member owns the knowledge base.
func requireWorkspaceKnowledgeBaseManager(ctx context.Context, db *sql.DB, kbID, managerID string) error {
	var allowed int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		JOIN workspaces w ON w.id=k.workspace_id
		WHERE k.id=? AND `+standaloneKnowledgeBasePredicate("k")+`
		  AND `+workspaceDirectoryManagerPredicate("k"),
		append([]any{kbID}, workspaceDirectoryManagerArgs(managerID)...)...).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func workspaceKnowledgeBaseMemberPermissionsQuery() string {
	return `SELECT k.id,m.user_id,
		CASE WHEN w.owner_id=m.user_id THEN 'admin' ELSE ` + normalizeWorkspaceRoleSQL("m.role") + ` END,
		CASE WHEN w.owner_id=m.user_id THEN 1 ELSE 0 END,
		COALESCE(u.name,''),COALESCE(u.email,''),COALESCE(u.settings,''),
		CASE WHEN w.owner_id=m.user_id OR ` + isAdminRoleSQL("m.role") + ` THEN 1 ELSE COALESCE(p.can_add_files,1) END,
		CASE WHEN w.owner_id=m.user_id OR ` + isAdminRoleSQL("m.role") + ` THEN 1 ELSE COALESCE(p.can_delete_content,1) END,
		CASE WHEN w.owner_id=m.user_id OR ` + isAdminRoleSQL("m.role") + ` THEN 1 WHEN m.role='guest' THEN 0 ELSE m.can_add_kb_files END,
		CASE WHEN w.owner_id=m.user_id OR ` + isAdminRoleSQL("m.role") + ` THEN 1 WHEN m.role='guest' THEN 0 ELSE m.can_delete_kb_content END,
		CASE WHEN w.owner_id=m.user_id OR ` + isAdminRoleSQL("m.role") + ` THEN 1 ELSE 0 END
	FROM knowledge_bases k
	JOIN workspaces w ON w.id=k.workspace_id
	JOIN workspace_members m ON m.workspace_id=w.id
	LEFT JOIN users u ON u.id=m.user_id
	LEFT JOIN workspace_kb_member_permissions p ON p.kb_id=k.id AND p.user_id=m.user_id
	WHERE k.id=? AND ` + standaloneKnowledgeBasePredicate("k")
}

func scanWorkspaceKnowledgeBaseMemberPermission(s scanner) (WorkspaceKnowledgeBaseMemberPermission, error) {
	var item WorkspaceKnowledgeBaseMemberPermission
	var settings string
	err := s.Scan(
		&item.KBID, &item.UserID, &item.Role, &item.IsOwner, &item.Name, &item.Email, &settings,
		&item.CanAddFiles, &item.CanDeleteContent,
		&item.TotalCanAddKBFiles, &item.TotalCanDeleteKBContent, &item.Locked,
	)
	item.AvatarURL = avatarFromSettings(settings)
	return item, err
}

// Directory visibility is narrower than resource ownership: creating a library
// must never grant an ordinary member access to other workspace identities.
func workspaceDirectoryManagerPredicate(alias string) string {
	return `EXISTS (SELECT 1 FROM workspaces directory_workspace WHERE directory_workspace.id=` + alias + `.workspace_id
 AND (directory_workspace.owner_id=? OR EXISTS (SELECT 1 FROM workspace_members directory_member
 WHERE directory_member.workspace_id=directory_workspace.id AND directory_member.user_id=?
 AND ` + isAdminRoleSQL("directory_member.role") + `)))`
}
func workspaceDirectoryManagerArgs(userID string) []any { return []any{userID, userID} }
