package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Workspaces (§workspaces) — fully-isolated collaborative spaces. A workspace
// owns conversations/projects/KBs via their workspace_id column ('' = personal);
// every member sees all of them. Membership is granted ONLY through the invite
// link (a 192-bit capability token, rotatable). The owner is also a member row
// (role='owner') so membership predicates need no special-casing.

// Workspace is one workspace row.
type Workspace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OwnerID     string `json:"owner_id"`
	InviteToken string `json:"invite_token,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	// Enriched (not columns):
	Role        string `json:"role,omitempty"`         // requesting user's role
	MemberCount int    `json:"member_count,omitempty"` // filled by list queries
	OwnerName   string `json:"owner_name,omitempty"`
}

// WorkspaceMember is one member row enriched with user identity for display.
type WorkspaceMember struct {
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	JoinedAt  int64  `json:"joined_at"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// avatarFromSettings extracts settings.avatar_url from the users.settings JSON
// blob (the same field the sidebar reads client-side).
func avatarFromSettings(settings string) string {
	if settings == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(settings), &m) != nil {
		return ""
	}
	url, _ := m["avatar_url"].(string)
	return url
}

// CreateWorkspace inserts the workspace plus the owner's member row in one tx.
// The per-group cap is the HANDLER's job (needs group config); this is pure
// storage.
func CreateWorkspace(ctx context.Context, db *sql.DB, ownerID, name string) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("workspace name required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	id := genID("ws")
	token := "wsi_" + genToken() // §D1-grade capability: join-by-link only
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces(id, name, owner_id, invite_token) VALUES(?, ?, ?, ?)`,
		id, name, ownerID, token); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_members(workspace_id, user_id, role) VALUES(?, ?, 'owner')`,
		id, ownerID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetWorkspace(ctx, db, id)
}

// GetWorkspace returns a workspace by id (no membership check — callers gate).
func GetWorkspace(ctx context.Context, db *sql.DB, id string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT id, name, owner_id, invite_token, created_at FROM workspaces WHERE id=?`, id,
	).Scan(&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// GetWorkspaceForMember returns the workspace only when userID is a member.
// This is the standard access gate for workspace endpoints.
func GetWorkspaceForMember(ctx context.Context, db *sql.DB, id, userID string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at, m.role
		   FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id
		  WHERE w.id=? AND m.user_id=?`, id, userID,
	).Scan(&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt, &w.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// GetWorkspaceByInviteToken resolves an invite link. Uniform ErrNotFound on
// miss (no enumeration oracle).
func GetWorkspaceByInviteToken(ctx context.Context, db *sql.DB, token string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.created_at,
		        (SELECT COUNT(*) FROM workspace_members m WHERE m.workspace_id=w.id),
		        COALESCE(u.name, '')
		   FROM workspaces w LEFT JOIN users u ON u.id = w.owner_id
		  WHERE w.invite_token=?`, token,
	).Scan(&w.ID, &w.Name, &w.OwnerID, &w.CreatedAt, &w.MemberCount, &w.OwnerName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWorkspacesForUser returns every workspace the user belongs to, with the
// user's role and the member count. Invite tokens are included ONLY for the
// owner (members must not be able to read/leak the link... they could share it
// anyway by joining flow, but least-privilege costs nothing).
func ListWorkspacesForUser(ctx context.Context, db *sql.DB, userID string) ([]Workspace, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at, m.role,
		        (SELECT COUNT(*) FROM workspace_members mm WHERE mm.workspace_id=w.id)
		   FROM workspaces w JOIN workspace_members m ON m.workspace_id = w.id
		  WHERE m.user_id=? ORDER BY w.created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.OwnerID, &w.InviteToken, &w.CreatedAt, &w.Role, &w.MemberCount); err != nil {
			return nil, err
		}
		if w.Role != "owner" {
			w.InviteToken = ""
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CountOwnedWorkspaces backs the per-group creation cap.
func CountOwnedWorkspaces(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_id=?`, userID).Scan(&n)
	return n, err
}

// IsWorkspaceMember reports membership + role ('' when not a member).
func IsWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (string, error) {
	var role string
	err := db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspaceID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// ListWorkspaceMembers returns members joined with display identity.
func ListWorkspaceMembers(ctx context.Context, db *sql.DB, workspaceID string) ([]WorkspaceMember, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT m.user_id, m.role, m.joined_at, COALESCE(u.name,''), COALESCE(u.email,''), COALESCE(u.settings,'')
		   FROM workspace_members m LEFT JOIN users u ON u.id = m.user_id
		  WHERE m.workspace_id=? ORDER BY m.joined_at ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceMember{}
	for rows.Next() {
		var m WorkspaceMember
		var settings string
		if err := rows.Scan(&m.UserID, &m.Role, &m.JoinedAt, &m.Name, &m.Email, &settings); err != nil {
			return nil, err
		}
		m.AvatarURL = avatarFromSettings(settings)
		out = append(out, m)
	}
	return out, rows.Err()
}

// JoinWorkspace adds userID as a member (idempotent — re-joining is a no-op).
func JoinWorkspace(ctx context.Context, db *sql.DB, workspaceID, userID string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspace_members(workspace_id, user_id, role) VALUES(?, ?, 'member')
		 ON CONFLICT(workspace_id, user_id) DO NOTHING`, workspaceID, userID)
	return err
}

// LeaveWorkspace removes a member. The owner cannot leave — they must delete
// the workspace instead (there is no ownership transfer).
func LeaveWorkspace(ctx context.Context, db *sql.DB, workspaceID, userID string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=? AND role<>'owner'`,
		workspaceID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveWorkspaceMember is the owner's kick. The owner row itself is protected.
func RemoveWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, memberID string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=? AND role<>'owner'`,
		workspaceID, memberID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateWorkspaceInvite mints a fresh invite token (invalidating the old link).
func RotateWorkspaceInvite(ctx context.Context, db *sql.DB, workspaceID string) (string, error) {
	token := "wsi_" + genToken()
	if _, err := db.ExecContext(ctx,
		`UPDATE workspaces SET invite_token=? WHERE id=?`, token, workspaceID); err != nil {
		return "", err
	}
	return token, nil
}

// DeleteWorkspaceRow removes the workspace row itself; member rows cascade via
// FK. Content teardown (conversations/projects/KBs — which needs vector-store
// cleanup) is orchestrated by the HANDLER through the existing per-entity
// deleters, then this finishes the job.
func DeleteWorkspaceRow(ctx context.Context, db *sql.DB, workspaceID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, workspaceID)
	return err
}

// WorkspaceContentIDs lists the conversation/project/KB ids belonging to a
// workspace — the handler's teardown worklist for DeleteWorkspace.
func WorkspaceContentIDs(ctx context.Context, db *sql.DB, workspaceID string) (convIDs, projectIDs, kbIDs []string, err error) {
	collect := func(q string) ([]string, error) {
		rows, err := db.QueryContext(ctx, q, workspaceID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	if convIDs, err = collect(`SELECT id FROM conversations WHERE workspace_id=?`); err != nil {
		return
	}
	if projectIDs, err = collect(`SELECT id FROM projects WHERE workspace_id=?`); err != nil {
		return
	}
	kbIDs, err = collect(`SELECT id FROM knowledge_bases WHERE workspace_id=?`)
	return
}

// ListAllWorkspaces is the admin listing (owner identity + member count).
func ListAllWorkspaces(ctx context.Context, db *sql.DB, limit, offset int) ([]Workspace, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.created_at, COALESCE(u.name,''),
		        (SELECT COUNT(*) FROM workspace_members m WHERE m.workspace_id=w.id)
		   FROM workspaces w LEFT JOIN users u ON u.id = w.owner_id
		  ORDER BY w.created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.OwnerID, &w.CreatedAt, &w.OwnerName, &w.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
// UserIdentity is a display-only projection of a user (name + avatar) used to
// label message authors and sidebar rows (§workspaces).
type UserIdentity struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// UserIdentities resolves a set of user ids to display identities in one query.
func UserIdentities(ctx context.Context, db *sql.DB, ids []string) (map[string]UserIdentity, error) {
	out := map[string]UserIdentity{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(settings,'') FROM users WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, settings string
		if err := rows.Scan(&id, &name, &settings); err != nil {
			return nil, err
		}
		out[id] = UserIdentity{Name: name, AvatarURL: avatarFromSettings(settings)}
	}
	return out, rows.Err()
}
