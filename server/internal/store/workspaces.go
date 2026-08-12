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

// workspaceResourceAccessPredicate is the authoritative access boundary for a
// row that carries user_id + workspace_id. Personal rows belong to their user;
// workspace rows belong to the workspace, so their original creator must still
// be the canonical workspace owner or a current member. The owner check keeps
// legacy databases safe when their redundant owner membership row is missing.
//
// alias is a trusted SQL identifier supplied by store code (for example "c").
// Callers must append workspaceResourceAccessArgs(userID) in predicate order.
func workspaceResourceAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces resource_workspace ` +
		`WHERE resource_workspace.id=` + prefix + `workspace_id AND (` +
		`resource_workspace.owner_id=? OR EXISTS (` +
		`SELECT 1 FROM workspace_members resource_member ` +
		`WHERE resource_member.workspace_id=resource_workspace.id AND resource_member.user_id=?` +
		`)` +
		`)` +
		`)` +
		`))`
}

func workspaceResourceAccessArgs(userID string) []any {
	return []any{userID, userID, userID}
}

// conversationResourceAccessPredicate adds per-conversation visibility to the
// standard personal/workspace boundary without adding SQL parameters. Public
// workspace rows are visible to every current member; private rows only match
// when the current owner/member is also conversations.user_id. Keeping the same
// three-argument shape lets document/file/message subqueries share the existing
// authorization plumbing.
func conversationResourceAccessPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces conversation_workspace ` +
		`WHERE conversation_workspace.id=` + prefix + `workspace_id AND (` +
		`(conversation_workspace.owner_id=? AND (` + prefix + `is_public=1 OR ` + prefix + `user_id=conversation_workspace.owner_id)) OR EXISTS (` +
		`SELECT 1 FROM workspace_members conversation_member ` +
		`WHERE conversation_member.workspace_id=conversation_workspace.id AND conversation_member.user_id=? ` +
		`AND (` + prefix + `is_public=1 OR ` + prefix + `user_id=conversation_member.user_id)` +
		`)` +
		`)` +
		`)` +
		`))`
}

// workspaceResourceManagerPredicate is the stricter share-management boundary:
// a personal resource's creator; or, in a workspace, its canonical owner or the
// resource creator while that creator is still a current member. Other ordinary
// members may collaborate on content but may not publish or revoke its share.
func workspaceResourceManagerPredicate(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `((COALESCE(` + prefix + `workspace_id,'')='' AND ` + prefix + `user_id=?) OR (` +
		`COALESCE(` + prefix + `workspace_id,'')<>'' AND EXISTS (` +
		`SELECT 1 FROM workspaces resource_workspace ` +
		`WHERE resource_workspace.id=` + prefix + `workspace_id AND (` +
		`resource_workspace.owner_id=? OR (` + prefix + `user_id=? AND EXISTS (` +
		`SELECT 1 FROM workspace_members resource_manager_member ` +
		`WHERE resource_manager_member.workspace_id=resource_workspace.id AND resource_manager_member.user_id=?` +
		`)` +
		`)` +
		`)` +
		`)` +
		`))`
}

func workspaceResourceManagerArgs(userID string) []any {
	return []any{userID, userID, userID, userID}
}

// workspaceAcceptsResourceCreationPredicate blocks new shared resources once
// either the canonical owner or the creating user has entered account deletion.
// alias is a trusted workspaces-table alias; the single placeholder is the
// creating user's id.
func workspaceAcceptsResourceCreationPredicate(alias string) string {
	return `EXISTS (
		SELECT 1 FROM users creation_owner
		 WHERE creation_owner.id=` + alias + `.owner_id AND creation_owner.status='active'
	) AND EXISTS (
		SELECT 1 FROM users creation_user
		 WHERE creation_user.id=? AND creation_user.status='active'
	)`
}

func beginWorkspaceMutationTx(ctx context.Context, db *sql.DB, workspaceID string) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
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
	w, err := GetWorkspace(ctx, db, id)
	if err != nil {
		return nil, err
	}
	// The creator is, by definition, the owner and sole member. Set the enriched
	// fields GetWorkspace can't (it reads columns only) so the create response is
	// complete — the client's Members dialog gates the invite link on role=owner,
	// and without this it stays hidden until a page reload re-fetches the list.
	w.Role = "owner"
	w.MemberCount = 1
	return w, nil
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

// GetWorkspaceForMember returns the workspace only when userID is a member or
// its canonical owner. The owner fallback supports legacy rows missing from
// workspace_members without letting an ordinary former member back in.
// This is the standard access gate for workspace endpoints.
func GetWorkspaceForMember(ctx context.Context, db *sql.DB, id, userID string) (*Workspace, error) {
	var w Workspace
	err := db.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at,
		        CASE WHEN w.owner_id=? THEN 'owner' ELSE COALESCE(m.role,'') END
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, id, userID, userID,
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
		   FROM workspaces w JOIN users u ON u.id = w.owner_id
		  WHERE w.invite_token=? AND u.status='active'`, token,
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
		`SELECT w.id, w.name, w.owner_id, w.invite_token, w.created_at,
		        CASE WHEN w.owner_id=? THEN 'owner' ELSE COALESCE(m.role,'') END,
		        (SELECT COUNT(*) FROM workspace_members mm WHERE mm.workspace_id=w.id)
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.owner_id=? OR m.user_id=? ORDER BY w.created_at ASC`,
		userID, userID, userID, userID)
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

// IsWorkspaceMember reports membership + role ("" when not a member). The
// canonical owner remains authoritative even if a legacy owner membership row
// is missing.
func IsWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, userID string) (string, error) {
	var role string
	err := db.QueryRowContext(ctx,
		`SELECT CASE WHEN w.owner_id=? THEN 'owner' ELSE COALESCE(m.role,'') END
		   FROM workspaces w
		   LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=?
		  WHERE w.id=? AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, workspaceID, userID, userID,
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return err
	}
	if err := joinWorkspaceTx(ctx, tx, workspaceID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// JoinWorkspaceByInviteToken consumes the capability under the same workspace
// lock used by kick/token rotation. Rechecking the token after acquiring that
// lock prevents a request that resolved the old token just before a kick from
// re-adding the removed member afterwards.
func JoinWorkspaceByInviteToken(ctx context.Context, db *sql.DB, token, userID string) (*Workspace, error) {
	var workspaceID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE invite_token=?`, token,
	).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var workspace Workspace
	if err := tx.QueryRowContext(ctx,
		`SELECT w.id, w.name, w.owner_id, w.created_at
		   FROM workspaces w JOIN users owner ON owner.id=w.owner_id
		  WHERE w.id=? AND w.invite_token=? AND owner.status='active'`, workspaceID, token,
	).Scan(&workspace.ID, &workspace.Name, &workspace.OwnerID, &workspace.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := joinWorkspaceTx(ctx, tx, workspaceID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	workspace.Role = "member"
	if workspace.OwnerID == userID {
		workspace.Role = "owner"
	}
	return &workspace, nil
}

func joinWorkspaceTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) error {
	var ownerStatus, joiningStatus string
	if err := tx.QueryRowContext(ctx,
		`SELECT owner.status, joining_user.status
		   FROM workspaces w
		   JOIN users owner ON owner.id=w.owner_id
		   JOIN users joining_user ON joining_user.id=?
		  WHERE w.id=?`, userID, workspaceID,
	).Scan(&ownerStatus, &joiningStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if ownerStatus != "active" || joiningStatus != "active" {
		return ErrNotFound
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_members(workspace_id, user_id, role) VALUES(?, ?, 'member')
		 ON CONFLICT(workspace_id, user_id) DO NOTHING`, workspaceID, userID)
	return err
}

// LeaveWorkspace removes a member. The owner cannot leave — they must delete
// the workspace instead (there is no ownership transfer).
func LeaveWorkspace(ctx context.Context, db *sql.DB, workspaceID, userID string) error {
	_, err := LeaveWorkspaceWithRevokedGenerations(ctx, db, workspaceID, userID)
	return err
}

// LeaveWorkspaceWithRevokedGenerations returns the assistant message ids that
// were terminalized in the membership transaction. The API uses those ids as
// immutable generation epochs: their cache streams are tombstoned before the
// leave response is acknowledged, and a later rejoin cannot revive them.
func LeaveWorkspaceWithRevokedGenerations(ctx context.Context, db *sql.DB, workspaceID, userID string) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_shares
		  WHERE user_id=? AND EXISTS (
		    SELECT 1 FROM conversations c
		     WHERE c.id=conversation_shares.conversation_id AND c.workspace_id=?
		  )`, userID, workspaceID); err != nil {
		return nil, err
	}
	revokedMessageIDs, err := scrubWorkspaceUserStreamingMessagesTx(ctx, tx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM workspace_members
		  WHERE workspace_id=? AND user_id=? AND role<>'owner'
		    AND NOT EXISTS (
		      SELECT 1 FROM workspaces w
		       WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id
		    )`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revokedMessageIDs, nil
}

// RemoveWorkspaceMember is the owner's kick. The owner row itself is protected.
func RemoveWorkspaceMember(ctx context.Context, db *sql.DB, workspaceID, memberID string) error {
	_, err := RemoveWorkspaceMemberWithRevokedGenerations(ctx, db, workspaceID, memberID)
	return err
}

// RemoveWorkspaceMemberWithRevokedGenerations is the cache-aware owner kick.
// See LeaveWorkspaceWithRevokedGenerations for the returned id contract.
func RemoveWorkspaceMemberWithRevokedGenerations(ctx context.Context, db *sql.DB, workspaceID, memberID string) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return nil, err
	}
	// A kicked member typically still knows the old capability URL. Rotate it in
	// this transaction so revocation cannot be bypassed by immediately rejoining.
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspaces SET invite_token=? WHERE id=?`, "wsi_"+genToken(), workspaceID); err != nil {
		return nil, err
	}
	// Public capability links published by the departing creator stay revoked even
	// if that account later receives a fresh invitation and rejoins.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_shares
		  WHERE user_id=? AND EXISTS (
		    SELECT 1 FROM conversations c
		     WHERE c.id=conversation_shares.conversation_id AND c.workspace_id=?
		  )`, memberID, workspaceID); err != nil {
		return nil, err
	}
	revokedMessageIDs, err := scrubWorkspaceUserStreamingMessagesTx(ctx, tx, workspaceID, memberID)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM workspace_members
		  WHERE workspace_id=? AND user_id=? AND role<>'owner'
		    AND NOT EXISTS (
		      SELECT 1 FROM workspaces w
		       WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id
		    )`,
		workspaceID, memberID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revokedMessageIDs, nil
}

// scrubWorkspaceUserStreamingMessagesTx makes membership revocation durable
// across a later legitimate rejoin. Current membership alone cannot distinguish
// a fresh generation from one started under the previous membership epoch, so
// kick/leave terminalizes every still-streaming placeholder authored by the
// departing principal before removing the membership row.
func scrubWorkspaceUserStreamingMessagesTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) ([]string, error) {
	return scrubWorkspaceStreamingMessagesTx(ctx, tx, workspaceID, userID)
}

// ScrubWorkspaceStreamingMessages terminalizes every active generation before
// workspace teardown and returns the message ids whose cache streams must be
// independently revoked. The workspace lock also serializes this sweep with
// scoped generation persistence on PostgreSQL.
func ScrubWorkspaceStreamingMessages(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	messageIDs, err := scrubWorkspaceStreamingMessagesTx(ctx, tx, workspaceID, "")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return messageIDs, nil
}

func scrubWorkspaceStreamingMessagesTx(ctx context.Context, tx *sql.Tx, workspaceID, userID string) ([]string, error) {
	query := `SELECT messages.id
		  FROM messages JOIN conversations revoked_generation_conversation
		    ON revoked_generation_conversation.id=messages.conversation_id
		 WHERE messages.role='assistant' AND messages.status='streaming'
		   AND revoked_generation_conversation.workspace_id=?`
	args := []any{workspaceID}
	if userID != "" {
		query += ` AND COALESCE(messages.author_id,'')=?`
		args = append(args, userID)
	}
	query += ` ORDER BY messages.id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	messageIDs := []string{}
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(messageIDs) == 0 {
		return messageIDs, nil
	}
	updateArgs := anySlice(messageIDs)
	_, err = tx.ExecContext(ctx,
		`UPDATE messages
		    SET blocks='[]', raw=NULL, citations='[]', stop_reason='stopped',
		        input_tokens=0, output_tokens=0, cache_read_tokens=0, cache_write_tokens=0,
		        cost=0, credits=0, status='stopped', error='', gen_ms=0,
		        verify='', search_text=''
		  WHERE id IN (`+idPlaceholders(len(messageIDs))+`)
		    AND role='assistant' AND status='streaming'`, updateArgs...)
	if err != nil {
		return nil, err
	}
	return messageIDs, nil
}

// lockWorkspaceMembershipTx is the serialization point shared by membership
// revocation and multi-statement workspace mutations. An operation that obtains
// this lock before a kick is allowed to finish; one that obtains it afterwards
// observes the revoked membership and fails closed. This is needed on Postgres,
// where SQLite's database-wide writer lock is not available.
func lockWorkspaceMembershipTx(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	res, err := tx.ExecContext(ctx, `UPDATE workspaces SET id=id WHERE id=?`, workspaceID)
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
	// Project libraries are deleted through DeleteProjectWithState so its exact
	// vector and storage cleanup worklist is preserved. The standalone loop must
	// not try (and fail) to delete them through DeleteKB first.
	kbIDs, err = collect(`SELECT id FROM knowledge_bases WHERE workspace_id=? AND ` + standaloneKnowledgeBasePredicate("knowledge_bases"))
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
