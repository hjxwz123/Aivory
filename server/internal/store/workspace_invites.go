package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	workspaceInvitePurposeManual    = "manual"
	workspaceInvitePurposeQuickLink = "quick_link"
	quickWorkspaceInviteTTL         = 7 * 24 * time.Hour
)

// Workspace invites (§workspace RBAC phase 3) — capability records replacing
// the single permanent workspace token. Creation authority: admins may invite
// members/guests; only the owner may mint admin invites. Consumption runs
// under the workspace membership lock so one-time invites stay one-time and a
// kick/rotate cannot race a join.

// WorkspaceInvite is one invite row. Token is exposed ONLY through the
// creator-side list/create responses — never in member/guest views or logs.
type WorkspaceInvite struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Token       string `json:"token"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	ExpiresAt   int64  `json:"expires_at"`
	MaxUses     int64  `json:"max_uses"`
	UsedCount   int64  `json:"used_count"`
	CreatedBy   string `json:"created_by"`
	RevokedAt   int64  `json:"revoked_at"`
	CreatedAt   int64  `json:"created_at"`
	// CreatorName is display-only enrichment for the admin list.
	CreatorName string `json:"creator_name,omitempty"`
}

// WorkspaceInvitePreview is the pre-join view of an invite (no token).
type WorkspaceInvitePreview struct {
	WorkspaceID string `json:"id"`
	Name        string `json:"name"`
	OwnerName   string `json:"owner_name"`
	MemberCount int    `json:"member_count"`
	Role        string `json:"role"`
	// EmailBound tells the joiner the invite is locked to a specific address.
	EmailBound bool `json:"email_bound"`
}

func inviteAliveSQL(alias string) string {
	prefix := alias + "."
	return `(` + prefix + `revoked_at=0 AND (` + prefix + `expires_at=0 OR ` + prefix + `expires_at>=?)
		AND (` + prefix + `max_uses=0 OR ` + prefix + `used_count<` + prefix + `max_uses))`
}

// requireWorkspaceInviteAdminTx must run after beginWorkspaceMutationTx has
// locked the workspace row. Keeping authorization inside the same transaction
// closes the handler-check-to-write window when an administrator is demoted or
// removed while an invite request is in flight.
func requireWorkspaceInviteAdminTx(ctx context.Context, tx *sql.Tx, workspaceID, actorID string) (string, error) {
	var ownerID, actorRole string
	err := tx.QueryRowContext(ctx,
		`SELECT w.owner_id, COALESCE(m.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND COALESCE(w.deleting,0)=0`,
		actorID, workspaceID,
	).Scan(&ownerID, &actorRole)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if ownerID != actorID && actorRole != WorkspaceRoleAdmin && actorRole != "owner" {
		return "", ErrForbidden
	}
	return ownerID, nil
}

// CreateWorkspaceInvite mints an invite. Role must be admin/member/guest;
// admin invites are owner-exclusive and re-checked inside the transaction.
func CreateWorkspaceInvite(
	ctx context.Context,
	db *sql.DB,
	workspaceID, actorID, email, role string,
	expiresAt int64,
	maxUses int64,
) (*WorkspaceInvite, error) {
	if !ValidWorkspaceMemberRole(role) {
		return nil, errors.New("invalid role")
	}
	email = strings.TrimSpace(strings.ToLower(email))
	if email != "" && !validInviteEmail(email) {
		return nil, errors.New("invalid email")
	}
	if maxUses < 0 {
		return nil, errors.New("invalid max_uses")
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	workspaceOwnerID, err := requireWorkspaceInviteAdminTx(ctx, tx, workspaceID, actorID)
	if err != nil {
		return nil, err
	}
	if role == WorkspaceRoleAdmin && workspaceOwnerID != actorID {
		return nil, ErrForbidden
	}

	id := genID("wsi_rec")
	token := "wsi_" + genToken()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_invites(id, workspace_id, token, email, role, expires_at, max_uses, used_count, created_by, purpose, revoked_at, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 0, ?)`,
		id, workspaceID, token, email, role, expiresAt, maxUses, actorID, workspaceInvitePurposeManual, now); err != nil {
		return nil, err
	}
	// §15: the audit metadata never carries the invite token.
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditInviteCreated,
		"workspace_invite", id, map[string]any{
			"role": role, "email_bound": email != "", "expires_at": expiresAt, "max_uses": maxUses,
		}); err != nil {
		return nil, err
	}
	invite := &WorkspaceInvite{
		ID: id, WorkspaceID: workspaceID, Token: token, Email: email, Role: role,
		ExpiresAt: expiresAt, MaxUses: maxUses, CreatedBy: actorID, CreatedAt: now,
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return invite, nil
}

func validInviteEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t")
}

// ListWorkspaceInvites returns the workspace's invites for its admins. Dead
// invites (revoked/expired/exhausted) stay listed so managers can audit use.
func ListWorkspaceInvites(ctx context.Context, db *sql.DB, workspaceID, actorID string) ([]WorkspaceInvite, error) {
	var workspaceOwnerID string
	var actorRole string
	err := db.QueryRowContext(ctx,
		`SELECT w.owner_id, COALESCE(am.role,'')
		   FROM workspaces w
		   LEFT JOIN workspace_members am ON am.workspace_id=w.id AND am.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR am.user_id=?)`,
		actorID, workspaceID, actorID, actorID,
	).Scan(&workspaceOwnerID, &actorRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if workspaceOwnerID != actorID && actorRole != "admin" && actorRole != "owner" {
		return nil, ErrForbidden
	}
	rows, err := db.QueryContext(ctx,
		`SELECT i.id, i.workspace_id, i.token, i.email, i.role, i.expires_at, i.max_uses, i.used_count, COALESCE(i.created_by,''), i.revoked_at, i.created_at,
		        COALESCE(u.name,'')
		   FROM workspace_invites i
		   LEFT JOIN users u ON u.id=i.created_by
		  WHERE i.workspace_id=? ORDER BY i.created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceInvite{}
	for rows.Next() {
		var invite WorkspaceInvite
		if err := rows.Scan(
			&invite.ID, &invite.WorkspaceID, &invite.Token, &invite.Email, &invite.Role,
			&invite.ExpiresAt, &invite.MaxUses, &invite.UsedCount, &invite.CreatedBy,
			&invite.RevokedAt, &invite.CreatedAt, &invite.CreatorName,
		); err != nil {
			return nil, err
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}

// RevokeWorkspaceInvite kills an invite. Admins may revoke the invites they
// could create; only the owner may revoke admin invites.
func RevokeWorkspaceInvite(ctx context.Context, db *sql.DB, workspaceID, actorID, inviteID string) error {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	workspaceOwnerID, err := requireWorkspaceInviteAdminTx(ctx, tx, workspaceID, actorID)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE workspace_invites SET revoked_at=?
		  WHERE id=? AND workspace_id=? AND revoked_at=0
		    AND (?>0 OR role<>'admin')`,
		time.Now().Unix(), inviteID, workspaceID, map[bool]int{true: 1, false: 0}[workspaceOwnerID == actorID])
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditInviteRevoked,
		"workspace_invite", inviteID, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// GetWorkspaceInvitePreview resolves a token to a join preview. Uniform
// ErrNotFound on unknown, revoked, expired or exhausted invites so the token
// space cannot be probed.
func GetWorkspaceInvitePreview(ctx context.Context, db *sql.DB, token string) (*WorkspaceInvitePreview, error) {
	var preview WorkspaceInvitePreview
	var email string
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, COALESCE(u.name,''),
		        (SELECT COUNT(*) FROM workspace_members m WHERE m.workspace_id=w.id),
		        i.role, i.email
		   FROM workspace_invites i
		   JOIN workspaces w ON w.id=i.workspace_id
		   JOIN users u ON u.id=w.owner_id
		  WHERE i.token=? AND `+inviteAliveSQL("i")+` AND u.status='active'`,
		token, time.Now().Unix(),
	).Scan(&preview.WorkspaceID, &preview.Name, &preview.OwnerName, &preview.MemberCount, &preview.Role, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	preview.EmailBound = strings.TrimSpace(email) != ""
	return &preview, nil
}

// JoinWorkspaceByInviteRecord consumes an invite under the workspace
// membership lock: re-validates liveness and the exact-email match inside the
// transaction, then inserts the membership row and atomically increments
// used_count — a one-time invite can only ever succeed once, even under
// concurrent joins. Re-joining as an existing member is a no-op that does not
// consume a use.
func JoinWorkspaceByInviteRecord(ctx context.Context, db *sql.DB, token, userID, userEmail string) (*Workspace, string, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT workspace_id FROM workspace_invites WHERE token=?`, token,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().Unix()
	var role, boundEmail string
	err = tx.QueryRowContext(ctx,
		`SELECT i.role, i.email
		   FROM workspace_invites i
		   JOIN workspaces w ON w.id=i.workspace_id
		   JOIN users owner ON owner.id=w.owner_id
		  WHERE i.token=? AND i.workspace_id=? AND `+inviteAliveSQL("i")+` AND owner.status='active'`,
		token, workspaceID, now,
	).Scan(&role, &boundEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if boundEmail = strings.TrimSpace(strings.ToLower(boundEmail)); boundEmail != "" &&
		boundEmail != strings.TrimSpace(strings.ToLower(userEmail)) {
		return nil, "", ErrNotFound
	}

	var workspace Workspace
	if err := tx.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.created_at
		   FROM workspaces w WHERE w.id=?`, workspaceID,
	).Scan(&workspace.ID, &workspace.Name, &workspace.OwnerID, &workspace.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	joined, err := joinWorkspaceWithRoleTx(ctx, tx, workspaceID, userID, role)
	if err != nil {
		return nil, "", err
	}
	if joined {
		// Consume exactly one use; the conditional UPDATE keeps concurrent
		// one-time joins from both succeeding.
		res, cerr := tx.ExecContext(ctx,
			`UPDATE workspace_invites SET used_count=used_count+1
			  WHERE token=? AND `+inviteAliveSQL("workspace_invites"),
			token, now)
		if cerr != nil {
			return nil, "", cerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, "", ErrNotFound
		}
		var inviteID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM workspace_invites WHERE token=?`, token).Scan(&inviteID); err != nil {
			return nil, "", err
		}
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditMemberJoined,
			"workspace_member", userID, map[string]any{"role": role, "source": "invite"}); err != nil {
			return nil, "", err
		}
		if err := recordWorkspaceAudit(ctx, tx, workspaceID, userID, AuditInviteUsed,
			"workspace_invite", inviteID, map[string]any{"role": role}); err != nil {
			return nil, "", err
		}
	}
	var effectiveRole string
	if err := tx.QueryRowContext(ctx,
		`SELECT CASE WHEN w.owner_id=? THEN 'admin' ELSE `+normalizeWorkspaceRoleSQL("m.role")+` END
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, workspaceID, userID, userID,
	).Scan(&effectiveRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	workspace.Role = effectiveRole
	workspace.IsOwner = workspace.OwnerID == userID
	applyWorkspacePermissions(&workspace, fullWorkspaceMemberPermissions())
	return &workspace, effectiveRole, nil
}

// RotateWorkspaceInvite creates a short-lived, single-use ordinary-member
// invite for the legacy quick-link endpoint. It deliberately does not update
// workspaces.invite_token: that permanent token is no longer an accepted
// capability. Authorization is repeated after the workspace row lock, so a
// stale handler-level approval cannot rotate a link after a demotion/removal.
func RotateWorkspaceInvite(ctx context.Context, db *sql.DB, workspaceID, actorID string) (string, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := requireWorkspaceInviteAdminTx(ctx, tx, workspaceID, actorID); err != nil {
		return "", err
	}

	now := time.Now().Unix()
	// Revoking zero rows is valid for the first rotation. Previous quick links
	// alone are revoked; administrator-created invitations are independent.
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_invites SET revoked_at=?
		  WHERE workspace_id=? AND purpose=? AND revoked_at=0`,
		now, workspaceID, workspaceInvitePurposeQuickLink); err != nil {
		return "", err
	}

	id := genID("wsi_rec")
	token := "wsi_" + genToken()
	expiresAt := time.Unix(now, 0).Add(quickWorkspaceInviteTTL).Unix()
	inserted, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_invites(id, workspace_id, token, email, role, expires_at, max_uses, used_count, created_by, purpose, revoked_at, created_at)
		 VALUES(?, ?, ?, '', ?, ?, 1, 0, ?, ?, 0, ?)`,
		id, workspaceID, token, WorkspaceRoleMember, expiresAt, actorID, workspaceInvitePurposeQuickLink, now)
	if err != nil {
		return "", err
	}
	if n, err := inserted.RowsAffected(); err != nil {
		return "", err
	} else if n != 1 {
		return "", ErrNotFound
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditInviteRotated,
		"workspace_invite", id, map[string]any{
			"role": WorkspaceRoleMember, "expires_at": expiresAt, "max_uses": 1,
		}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// TransferWorkspaceOwnership moves the canonical owner (§workspace RBAC §13):
// only the current owner may initiate; the receiver must be a current member
// and becomes admin; the previous owner keeps the admin role. owner_id and the
// member role flip in one transaction under the membership lock, so owner-
// exclusive authority switches atomically.
func TransferWorkspaceOwnership(ctx context.Context, db *sql.DB, workspaceID, actorID, newOwnerID string) (*Workspace, error) {
	if actorID == newOwnerID {
		return nil, ErrForbidden
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	var currentOwnerID string
	if err := tx.QueryRowContext(ctx,
		`SELECT owner_id FROM workspaces WHERE id=? AND COALESCE(deleting,0)=0`, workspaceID,
	).Scan(&currentOwnerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if currentOwnerID != actorID {
		return nil, ErrForbidden
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE workspace_members SET role='admin'
		  WHERE workspace_id=? AND user_id=?`,
		workspaceID, newOwnerID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound // receiver must be a current member
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspaces SET owner_id=? WHERE id=?`, newOwnerID, workspaceID); err != nil {
		return nil, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE workspace_invites SET revoked_at=?
		  WHERE workspace_id=? AND created_by=? AND role=? AND revoked_at=0`,
		time.Now().Unix(), workspaceID, currentOwnerID, WorkspaceRoleAdmin)
	if err != nil {
		return nil, err
	}
	revokedAdminInvites, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if err := recordWorkspaceAudit(ctx, tx, workspaceID, actorID, AuditWorkspaceTransferred,
		"workspace", workspaceID, map[string]any{
			"from": currentOwnerID, "to": newOwnerID, "revoked_admin_invites": revokedAdminInvites,
		}); err != nil {
		return nil, err
	}
	var workspace Workspace
	if err := tx.QueryRowContext(ctx,
		`SELECT id, name, owner_id, created_at FROM workspaces WHERE id=?`, workspaceID,
	).Scan(&workspace.ID, &workspace.Name, &workspace.OwnerID, &workspace.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	workspace.Role = WorkspaceRoleAdmin
	workspace.IsOwner = workspace.OwnerID == actorID
	applyWorkspacePermissions(&workspace, fullWorkspaceMemberPermissions())
	return &workspace, nil
}

// migrateWorkspaceInviteCreatorReference upgrades databases created before
// workspace invites had a nullable creator and ON DELETE SET NULL. SQLite
// cannot alter a foreign-key action in place, so its small capability table is
// rebuilt atomically. PostgreSQL replaces only the relevant constraint.
func migrateWorkspaceInviteCreatorReference(db *sql.DB) error {
	if usePostgres {
		if _, err := db.Exec(`ALTER TABLE workspace_invites ALTER COLUMN created_by DROP NOT NULL`); err != nil {
			return err
		}
		_, err := db.Exec(`DO $$
		DECLARE creator_constraint TEXT;
		BEGIN
			SELECT c.conname INTO creator_constraint
			  FROM pg_constraint c
			  JOIN pg_attribute a
			    ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
			 WHERE c.conrelid='workspace_invites'::regclass
			   AND c.contype='f'
			   AND a.attname='created_by'
			 LIMIT 1;
			IF creator_constraint IS NOT NULL THEN
				EXECUTE format('ALTER TABLE workspace_invites DROP CONSTRAINT %I', creator_constraint);
			END IF;
			ALTER TABLE workspace_invites
			  ADD CONSTRAINT workspace_invites_created_by_fkey
			  FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;
		END $$`)
		return err
	}

	rows, err := db.Query(`PRAGMA foreign_key_list(workspace_invites)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		if strings.EqualFold(from, "created_by") && strings.EqualFold(onDelete, "SET NULL") {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`CREATE TABLE workspace_invites_rebuild (
		id           TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		token        TEXT NOT NULL UNIQUE,
		email        TEXT NOT NULL DEFAULT '',
		role         TEXT NOT NULL DEFAULT 'guest',
		expires_at   INTEGER NOT NULL DEFAULT 0,
		max_uses     INTEGER NOT NULL DEFAULT 1,
		used_count   INTEGER NOT NULL DEFAULT 0,
		created_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
		purpose      TEXT NOT NULL DEFAULT 'manual',
		revoked_at   INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO workspace_invites_rebuild(
		id, workspace_id, token, email, role, expires_at, max_uses, used_count,
		created_by, purpose, revoked_at, created_at
	) SELECT id, workspace_id, token, email, role, expires_at, max_uses, used_count,
		created_by, COALESCE(NULLIF(purpose,''), 'manual'), revoked_at, created_at
		FROM workspace_invites`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE workspace_invites`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE workspace_invites_rebuild RENAME TO workspace_invites`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_ws_invites_workspace ON workspace_invites(workspace_id, created_at DESC)`); err != nil {
		return err
	}
	return tx.Commit()
}
