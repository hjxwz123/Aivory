package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ListWorkspaceKnowledgeBaseMemberPermissions returns the per-library layer for
// one standalone workspace knowledge base. Only the workspace owner or that
// library's current creator may manage this list.
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
// for ordinary members; owners and current library creators always manage the
// library they own.
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
	managerArgs := []any{kbID, managerID, managerID, managerID}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		JOIN workspaces w ON w.id=k.workspace_id
		WHERE k.id=? AND `+standaloneKnowledgeBasePredicate("k")+`
		  AND (w.owner_id=? OR (k.user_id=? AND EXISTS (
		    SELECT 1 FROM workspace_members manager_member
		     WHERE manager_member.workspace_id=w.id AND manager_member.user_id=?
		  )))`, managerArgs...).Scan(&allowed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// The workspace owner and KB creator are fixed principals and always retain
	// effective access while the creator remains a workspace member.
	res, err := tx.ExecContext(ctx, `INSERT INTO workspace_kb_member_permissions(
		kb_id,user_id,can_add_files,can_delete_content,updated_at
	)
	SELECT k.id,m.user_id,?,?,?
	  FROM knowledge_bases k
	  JOIN workspaces w ON w.id=k.workspace_id
	  JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
	 WHERE k.id=? AND m.user_id<>w.owner_id AND m.user_id<>k.user_id
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

func requireWorkspaceKnowledgeBaseManager(ctx context.Context, db *sql.DB, kbID, managerID string) error {
	var allowed int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM knowledge_bases k
		JOIN workspaces w ON w.id=k.workspace_id
		WHERE k.id=? AND `+standaloneKnowledgeBasePredicate("k")+`
		  AND (w.owner_id=? OR (k.user_id=? AND EXISTS (
		    SELECT 1 FROM workspace_members manager_member
		     WHERE manager_member.workspace_id=w.id AND manager_member.user_id=?
		  )))`, kbID, managerID, managerID, managerID).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func workspaceKnowledgeBaseMemberPermissionsQuery() string {
	return `SELECT k.id,m.user_id,
		CASE WHEN w.owner_id=m.user_id THEN 'owner' ELSE m.role END,
		COALESCE(u.name,''),COALESCE(u.email,''),COALESCE(u.settings,''),
		CASE WHEN w.owner_id=m.user_id OR k.user_id=m.user_id THEN 1 ELSE COALESCE(p.can_add_files,1) END,
		CASE WHEN w.owner_id=m.user_id OR k.user_id=m.user_id THEN 1 ELSE COALESCE(p.can_delete_content,1) END,
		CASE WHEN w.owner_id=m.user_id OR k.user_id=m.user_id THEN 1 ELSE m.can_add_kb_files END,
		CASE WHEN w.owner_id=m.user_id OR k.user_id=m.user_id THEN 1 ELSE m.can_delete_kb_content END,
		CASE WHEN w.owner_id=m.user_id OR k.user_id=m.user_id THEN 1 ELSE 0 END
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
		&item.KBID, &item.UserID, &item.Role, &item.Name, &item.Email, &settings,
		&item.CanAddFiles, &item.CanDeleteContent,
		&item.TotalCanAddKBFiles, &item.TotalCanDeleteKBContent, &item.Locked,
	)
	item.AvatarURL = avatarFromSettings(settings)
	return item, err
}
