package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Workspace audit trail (§workspace RBAC phase 5). Every recorded row is
// written INSIDE the mutation's transaction where one exists, so the log can
// never claim an operation that rolled back. metadata is a small JSON object
// and must never contain invite tokens, API keys, full request bodies or
// document content — the masking test enforces this.

// Audit action identifiers.
const (
	AuditWorkspaceCreated          = "workspace.created"
	AuditWorkspaceDeleted          = "workspace.deleted"
	AuditWorkspaceTransferred      = "workspace.transferred"
	AuditMemberJoined              = "member.joined"
	AuditMemberLeft                = "member.left"
	AuditMemberRemoved             = "member.removed"
	AuditMemberRoleUpdated         = "member.role_updated"
	AuditMemberPermissionsUpdated  = "member.permissions_updated"
	AuditInviteCreated             = "invite.created"
	AuditInviteRotated             = "invite.rotated"
	AuditInviteUsed                = "invite.used"
	AuditInviteRevoked             = "invite.revoked"
	AuditResourceVisibilityChanged = "resource.visibility_changed"
	AuditPolicyUpdated             = "policy.updated"
)

// auditExecer is satisfied by both *sql.DB and *sql.Tx, letting mutations log
// inside their transaction while standalone callers use the pool.
type auditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// WorkspaceAuditLog is one audit row enriched with the actor's display name.
type WorkspaceAuditLog struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	ActorUserID string          `json:"actor_user_id"`
	ActorName   string          `json:"actor_name"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   int64           `json:"created_at"`
}

// recordWorkspaceAudit writes one audit row. Failures are returned so callers
// inside transactions roll back with the mutation — an unauditable permission
// change must not commit silently.
func recordWorkspaceAudit(
	ctx context.Context,
	ex auditExecer,
	workspaceID, actorID, action, targetType, targetID string,
	metadata map[string]any,
) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(action) == "" {
		return errors.New("workspace audit: workspace, actor and action are required")
	}
	raw := []byte("{}")
	if metadata != nil {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		raw = encoded
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO workspace_audit_logs(id, workspace_id, actor_user_id, action, target_type, target_id, metadata, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		genID("aud"), workspaceID, actorID, action, targetType, targetID, string(raw), time.Now().Unix())
	return err
}

// ListWorkspaceAuditLogs returns the newest-first audit page. Admins only —
// members and guests are rejected so the trail never leaks to ordinary users.
func ListWorkspaceAuditLogs(
	ctx context.Context,
	db *sql.DB,
	workspaceID, actorID string,
	limit, offset int,
) ([]WorkspaceAuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var workspaceOwnerID string
	var callerRole string
	err := db.QueryRowContext(ctx,
		`SELECT w.owner_id, COALESCE(am.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members am ON am.workspace_id=w.id AND am.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR am.user_id=?)`,
		actorID, workspaceID, actorID, actorID,
	).Scan(&workspaceOwnerID, &callerRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if workspaceOwnerID != actorID && callerRole != "admin" && callerRole != "owner" {
		return nil, ErrForbidden
	}
	rows, err := db.QueryContext(ctx,
		`SELECT a.id, a.workspace_id, a.actor_user_id, COALESCE(u.name,''), a.action, a.target_type, a.target_id, a.metadata, a.created_at
		   FROM workspace_audit_logs a
		   LEFT JOIN users u ON u.id=a.actor_user_id
		  WHERE a.workspace_id=?
		  ORDER BY a.created_at DESC, a.id DESC
		  LIMIT ? OFFSET ?`, workspaceID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceAuditLog{}
	for rows.Next() {
		var log WorkspaceAuditLog
		var metadata string
		if err := rows.Scan(
			&log.ID, &log.WorkspaceID, &log.ActorUserID, &log.ActorName,
			&log.Action, &log.TargetType, &log.TargetID, &metadata, &log.CreatedAt,
		); err != nil {
			return nil, err
		}
		log.Metadata = json.RawMessage(metadata)
		out = append(out, log)
	}
	return out, rows.Err()
}
