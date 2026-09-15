package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// GetWorkspaceAnnouncement returns the stored JSON configuration for a current
// workspace member. Missing configuration is represented by an empty object.
func GetWorkspaceAnnouncement(ctx context.Context, db *sql.DB, workspaceID, userID string) (string, error) {
	if _, err := GetWorkspaceForMember(ctx, db, workspaceID, userID); err != nil {
		return "", err
	}
	var config string
	err := db.QueryRowContext(ctx,
		`SELECT config FROM workspace_announcements WHERE workspace_id=?`, workspaceID).Scan(&config)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(config) == "" {
		return "{}", nil
	}
	return config, nil
}

// UpdateWorkspaceAnnouncement writes a workspace announcement while holding
// the same workspace row lock used by membership mutations. The authorization
// check therefore cannot race a concurrent admin demotion or removal.
func UpdateWorkspaceAnnouncement(ctx context.Context, db *sql.DB, workspaceID, actorID, config string) (int64, error) {
	return UpdateWorkspaceAnnouncementAt(ctx, db, workspaceID, actorID, config, time.Now().Unix())
}

// UpdateWorkspaceAnnouncementAt is the timestamp-coordinated variant used by
// the HTTP handler so the version in the stored JSON and the row metadata are
// identical even when a save crosses a wall-clock second boundary.
func UpdateWorkspaceAnnouncementAt(ctx context.Context, db *sql.DB, workspaceID, actorID, config string, updatedAt int64) (int64, error) {
	if strings.TrimSpace(config) == "" {
		config = "{}"
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().Unix()
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	var ownerID, role string
	err = tx.QueryRowContext(ctx,
		`SELECT w.owner_id, COALESCE(m.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		actorID, workspaceID, actorID, actorID).Scan(&ownerID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if ownerID != actorID && role != WorkspaceRoleAdmin && role != WorkspaceRoleOwnerLegacy {
		return 0, ErrForbidden
	}

	now := updatedAt
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_announcements(workspace_id, config, updated_by, updated_at)
		 VALUES(?,?,?,?)
		 ON CONFLICT(workspace_id) DO UPDATE SET config=excluded.config, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		workspaceID, config, actorID, now); err != nil {
		return 0, err
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditAnnouncementUpdated,
		"workspace_announcement", workspaceID, map[string]any{"updated_at": now}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return now, nil
}
