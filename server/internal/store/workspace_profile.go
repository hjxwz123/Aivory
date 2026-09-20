package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

var ErrInvalidWorkspaceProfile = errors.New("invalid workspace icon or description")

type WorkspaceProfile struct {
	IconURL     string `json:"icon_url"`
	Description string `json:"description"`
}

func NormalizeWorkspaceProfile(profile WorkspaceProfile) (WorkspaceProfile, error) {
	profile.IconURL = strings.TrimSpace(profile.IconURL)
	profile.Description = strings.TrimSpace(profile.Description)
	if len(profile.IconURL) > 2048 || utf8.RuneCountInString(profile.Description) > 4000 {
		return profile, ErrInvalidWorkspaceProfile
	}
	if profile.IconURL != "" {
		parsed, err := url.Parse(profile.IconURL)
		localIcon := strings.HasPrefix(profile.IconURL, "/api/icons/") && !strings.ContainsAny(profile.IconURL, "\\\r\n")
		if err != nil || (!localIcon && (parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil)) {
			return profile, ErrInvalidWorkspaceProfile
		}
	}
	return profile, nil
}

// UpdateWorkspaceProfile shares the membership lock with admin demotion/removal.
func UpdateWorkspaceProfile(ctx context.Context, db *sql.DB, workspaceID, actorID string, profile WorkspaceProfile) error {
	profile, err := NormalizeWorkspaceProfile(profile)
	if err != nil {
		return err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	access, err := GetDomainAccess(ctx, tx, actorID)
	if err != nil {
		return err
	}
	if access != nil && access.Locked && access.WorkspaceID != workspaceID {
		return ErrForbidden
	}
	var ownerID, role string
	err = tx.QueryRowContext(ctx, `SELECT w.owner_id, COALESCE(m.role,'') FROM workspaces w
 LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
 WHERE w.id=? AND COALESCE(w.deleting,0)=0 AND (w.owner_id=? OR m.user_id=?)`, actorID, workspaceID, actorID, actorID).Scan(&ownerID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerID != actorID && role != WorkspaceRoleAdmin && role != WorkspaceRoleOwnerLegacy {
		return ErrForbidden
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET icon_url=?,description=? WHERE id=?`, profile.IconURL, profile.Description, workspaceID); err != nil {
		return err
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditWorkspaceProfileUpdated, "workspace", workspaceID, nil); err != nil {
		return err
	}
	return tx.Commit()
}
