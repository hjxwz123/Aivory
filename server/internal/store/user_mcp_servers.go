package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUserMCPNameExists = errors.New("user MCP server name already exists")
)

// userMCPServerSelectColumns lists every persisted column with the scoped-read
// alias; each SELECT appends the computed can_manage flag after it.
const userMCPServerSelectColumns = `um.id, um.user_id, COALESCE(um.workspace_id,''), um.name, um.icon, um.description, um.url,
	um.headers, um.enabled, um.discovered_tools, um.protocol_version, um.last_error, um.last_synced_at, um.created_at, um.updated_at`

// scanUserMCPServer reads userMCPServerColumns plus the trailing computed
// can_manage flag emitted by every scoped SELECT.
func scanUserMCPServer(s scanner) (UserMCPServer, error) {
	var server UserMCPServer
	var headersText, discoveredToolsText string
	var enabled, canManage int
	if err := s.Scan(
		&server.ID, &server.UserID, &server.WorkspaceID, &server.Name, &server.Icon, &server.Description, &server.URL,
		&headersText, &enabled, &discoveredToolsText, &server.ProtocolVersion,
		&server.LastError, &server.LastSyncedAt, &server.CreatedAt, &server.UpdatedAt, &canManage,
	); err != nil {
		return server, err
	}
	if err := json.Unmarshal([]byte(headersText), &server.Headers); err != nil || server.Headers == nil {
		if err == nil {
			err = errors.New("headers must be a JSON object")
		}
		return server, fmt.Errorf("decode user MCP server %s headers: %w", server.ID, err)
	}
	if !validJSONArray([]byte(discoveredToolsText)) {
		return server, fmt.Errorf("decode user MCP server %s discovered tools: expected JSON array", server.ID)
	}
	server.Enabled = enabled == 1
	server.CanManage = canManage == 1
	server.DiscoveredTools = json.RawMessage(discoveredToolsText)
	return server, nil
}

func isUserMCPNameUniqueErr(err error) bool {
	return isUniqueIndexErr(err,
		"idx_user_mcp_servers_user_name_unique",
		"idx_user_mcp_servers_workspace_name_unique",
		"user_mcp_servers.user_id, user_mcp_servers.name",
	)
}

// ListUserMCPServersScoped returns the personal library for workspaceID="" and
// the shared workspace library otherwise. Workspace rows are visible to every
// current member; CanManage is true only for admins or the current non-guest
// creator (mirrors ListUserSkillsScoped).
func ListUserMCPServersScoped(ctx context.Context, db *sql.DB, userID, workspaceID string) ([]UserMCPServer, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT ` + userMCPServerSelectColumns + `, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_mcp_servers um WHERE um.user_id=? AND COALESCE(um.workspace_id,'')=''`
		args = append(args, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("um", "mcp") + ` THEN 1 ELSE 0 END
			FROM user_mcp_servers um WHERE um.workspace_id=? AND ` + libraryWorkspaceReadPredicate("um") + ` AND ` + libraryWorkspaceCapabilityPredicate("um", "mcp")
		args = append(args, userID, userID, userID, workspaceID, userID, userID)
	}
	query += ` ORDER BY um.updated_at DESC, um.name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserMCPServer{}
	for rows.Next() {
		server, err := scanUserMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, server)
	}
	return out, rows.Err()
}

// UserMCPServerIDsForWorkspace returns the complete invalidation worklist for
// workspace teardown. It deliberately bypasses member visibility predicates:
// callers use it only after establishing deletion authority and need every
// cached endpoint, including disabled servers, removed from process memory.
func UserMCPServerIDsForWorkspace(ctx context.Context, db *sql.DB, workspaceID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM user_mcp_servers WHERE workspace_id=? ORDER BY id`,
		strings.TrimSpace(workspaceID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetUserMCPServerScoped returns one row with its real headers, visible only
// inside the requested scope. HTTP handlers must mask Headers before
// serializing, and the runtime must re-check visibility at execution time.
func GetUserMCPServerScoped(ctx context.Context, db *sql.DB, id, userID, workspaceID string) (*UserMCPServer, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	query := `SELECT ` + userMCPServerSelectColumns + `, `
	args := []any{}
	if workspaceID == "" {
		query += `1 FROM user_mcp_servers um WHERE um.id=? AND um.user_id=? AND COALESCE(um.workspace_id,'')=''`
		args = append(args, id, userID)
	} else {
		query += `CASE WHEN ` + libraryWorkspaceManagePredicateFor("um", "mcp") + ` THEN 1 ELSE 0 END
			FROM user_mcp_servers um WHERE um.id=? AND um.workspace_id=? AND ` + libraryWorkspaceReadPredicate("um") + ` AND ` + libraryWorkspaceCapabilityPredicate("um", "mcp")
		args = append(args, userID, userID, userID, id, workspaceID, userID, userID)
	}
	server, err := scanUserMCPServer(db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// CreateUserMCPServer persists a user-owned Streamable HTTP endpoint. Personal
// rows insert directly; workspace rows go through the same
// can_create_mcp gate as user skills/prompts and fail with
// ErrNotFound when the caller may not contribute to that workspace.
func CreateUserMCPServer(ctx context.Context, db *sql.DB, server UserMCPServer) (*UserMCPServer, error) {
	server.Name = strings.TrimSpace(server.Name)
	server.Icon = strings.TrimSpace(server.Icon)
	server.Description = strings.TrimSpace(server.Description)
	server.URL = strings.TrimSpace(server.URL)
	server.ProtocolVersion = strings.TrimSpace(server.ProtocolVersion)
	if server.ID == "" {
		server.ID = genID("umcp")
	}
	headers, err := json.Marshal(nonNilMCPHeaders(server.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode user MCP headers: %w", err)
	}
	discoveredTools, err := normalizedMCPDiscoveredTools(server.DiscoveredTools)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if server.CreatedAt <= 0 {
		server.CreatedAt = now
	}
	if server.UpdatedAt <= 0 {
		server.UpdatedAt = now
	}
	workspaceID := strings.TrimSpace(server.WorkspaceID)
	var result sql.Result
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `INSERT INTO user_mcp_servers(
			id, user_id, workspace_id, name, icon, description, url, headers, enabled,
			discovered_tools, protocol_version, last_error, last_synced_at, created_at, updated_at
		) VALUES(?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			server.ID, server.UserID, server.Name, server.Icon, server.Description, server.URL, string(headers),
			boolInt(server.Enabled), string(discoveredTools), server.ProtocolVersion,
			server.LastError, server.LastSyncedAt, server.CreatedAt, server.UpdatedAt)
	} else {
		server.WorkspaceID = workspaceID
		result, err = db.ExecContext(ctx, `INSERT INTO user_mcp_servers(
			id, user_id, workspace_id, name, icon, description, url, headers, enabled,
			discovered_tools, protocol_version, last_error, last_synced_at, created_at, updated_at
		) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ? FROM workspaces library_workspace
			WHERE library_workspace.id=? AND `+libraryWorkspaceCreatePredicateFor("library_workspace", "mcp")+`
			  AND `+libraryWorkspaceCapabilityPredicateForExpr("library_workspace.id", "mcp"),
			server.ID, server.UserID, workspaceID, server.Name, server.Icon, server.Description, server.URL,
			string(headers), boolInt(server.Enabled), string(discoveredTools), server.ProtocolVersion,
			server.LastError, server.LastSyncedAt, server.CreatedAt, server.UpdatedAt,
			workspaceID, server.UserID, server.UserID)
	}
	if err != nil {
		if isUserMCPNameUniqueErr(err) {
			return nil, ErrUserMCPNameExists
		}
		return nil, err
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, rowsErr
	} else if n != 1 {
		return nil, ErrNotFound
	}
	return GetUserMCPServerScoped(ctx, db, server.ID, server.UserID, server.WorkspaceID)
}

// UserMCPServerPatch contains the fields the resource-library editor may
// change. Discovery state is updated separately so a normal metadata edit
// cannot forge the remote tools snapshot or connection status.
type UserMCPServerPatch struct {
	Name        *string
	Icon        *string
	Description *string
	URL         *string
	Headers     *map[string]string
	Enabled     *bool
	// ResetDiscovery atomically retires the snapshot negotiated for the previous
	// endpoint/credentials. See MCPServerPatch.ResetDiscovery.
	ResetDiscovery bool
}

// UpdateUserMCPServer applies a partial merge inside the caller's scope.
// Personal rows require ownership; workspace rows require the same manage
// authority the library editor UI reflects in CanManage.
func UpdateUserMCPServer(ctx context.Context, db *sql.DB, id, userID, workspaceID string, patch UserMCPServerPatch) (*UserMCPServer, error) {
	return updateUserMCPServer(ctx, db, id, userID, workspaceID, nil, patch)
}

// UpdateUserMCPServerIfCurrent is the scoped metadata CAS companion to
// UpdateUserMCPServerSyncStateIfCurrent. It lets API handlers atomically replace
// trust-bearing metadata and retire the old discovery snapshot without basing
// that decision on a row changed by a concurrent editor or sync.
func UpdateUserMCPServerIfCurrent(
	ctx context.Context,
	db *sql.DB,
	expected UserMCPServer,
	actorID string,
	patch UserMCPServerPatch,
) (*UserMCPServer, error) {
	if strings.TrimSpace(expected.ID) == "" {
		return nil, ErrNotFound
	}
	return updateUserMCPServer(
		ctx, db, expected.ID, actorID, expected.WorkspaceID, &expected, patch,
	)
}

func updateUserMCPServer(
	ctx context.Context,
	db *sql.DB,
	id, userID, workspaceID string,
	expected *UserMCPServer,
	patch UserMCPServerPatch,
) (*UserMCPServer, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	parts := []string{}
	args := []any{}
	if patch.Name != nil {
		parts = append(parts, "name=?")
		args = append(args, strings.TrimSpace(*patch.Name))
	}
	if patch.Icon != nil {
		parts = append(parts, "icon=?")
		args = append(args, strings.TrimSpace(*patch.Icon))
	}
	if patch.Description != nil {
		parts = append(parts, "description=?")
		args = append(args, strings.TrimSpace(*patch.Description))
	}
	if patch.URL != nil {
		parts = append(parts, "url=?")
		args = append(args, strings.TrimSpace(*patch.URL))
	}
	if patch.Headers != nil {
		raw, err := json.Marshal(nonNilMCPHeaders(*patch.Headers))
		if err != nil {
			return nil, fmt.Errorf("encode user MCP headers: %w", err)
		}
		parts = append(parts, "headers=?")
		args = append(args, string(raw))
	}
	if patch.Enabled != nil {
		parts = append(parts, "enabled=?")
		args = append(args, boolInt(*patch.Enabled))
	}
	if patch.ResetDiscovery {
		parts = append(parts,
			"discovered_tools=?", "protocol_version=?", "last_error=?", "last_synced_at=?",
		)
		args = append(args, "[]", "", "", int64(0))
	}
	if len(parts) == 0 {
		return GetUserMCPServerScoped(ctx, db, id, userID, workspaceID)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	casWhere := ""
	casArgs := []any{}
	if expected != nil {
		headers, marshalErr := json.Marshal(nonNilMCPHeaders(expected.Headers))
		if marshalErr != nil {
			return nil, fmt.Errorf("encode expected user MCP headers: %w", marshalErr)
		}
		casWhere = ` AND url=? AND headers=? AND enabled=? AND discovered_tools=? AND protocol_version=? AND last_synced_at=?`
		casArgs = append(casArgs,
			expected.URL, string(headers), boolInt(expected.Enabled),
			string(expected.DiscoveredTools), expected.ProtocolVersion, expected.LastSyncedAt,
		)
	}
	var result sql.Result
	var err error
	if workspaceID == "" {
		args = append(args, id, userID)
		args = append(args, casArgs...)
		result, err = db.ExecContext(ctx, `UPDATE user_mcp_servers SET `+strings.Join(parts, ", ")+
			` WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`+casWhere, args...)
	} else {
		args = append(args, id, workspaceID)
		args = append(args, userID, userID, userID)
		args = append(args, casArgs...)
		result, err = db.ExecContext(ctx, `UPDATE user_mcp_servers SET `+strings.Join(parts, ", ")+`
			WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_mcp_servers", "mcp")+` AND `+libraryWorkspaceCapabilityPredicate("user_mcp_servers", "mcp")+casWhere,
			args...)
	}
	if err != nil {
		if isUserMCPNameUniqueErr(err) {
			return nil, ErrUserMCPNameExists
		}
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if expected != nil {
			return nil, ErrMCPDiscoveryStateChanged
		}
		return nil, ErrNotFound
	}
	return GetUserMCPServerScoped(ctx, db, id, userID, workspaceID)
}

// UpdateUserMCPServerSyncState records a discovery attempt for a visible row.
// A nil discoveredTools value preserves the last successful snapshot, matching
// UpdateMCPServerSyncState semantics. Callers must invalidate the runtime MCP
// registry cache after a state change that removes or disables a server.
func UpdateUserMCPServerSyncState(
	ctx context.Context,
	db *sql.DB,
	id, userID, workspaceID string,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*UserMCPServer, error) {
	return updateUserMCPServerSyncState(
		ctx, db, id, userID, workspaceID, nil,
		discoveredTools, protocolVersion, lastError, lastSyncedAt,
	)
}

// UpdateUserMCPServerSyncStateIfCurrent applies a discovery result only while
// the scoped row still matches the endpoint and generation that initiated the
// remote request. It closes the metadata-update/sync race for both personal and
// shared workspace servers without weakening the existing manage predicates.
func UpdateUserMCPServerSyncStateIfCurrent(
	ctx context.Context,
	db *sql.DB,
	expected UserMCPServer,
	actorID string,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*UserMCPServer, error) {
	if strings.TrimSpace(expected.ID) == "" {
		return nil, ErrNotFound
	}
	if lastSyncedAt <= expected.LastSyncedAt {
		lastSyncedAt = time.Now().Unix()
		if lastSyncedAt <= expected.LastSyncedAt {
			lastSyncedAt = expected.LastSyncedAt + 1
		}
	}
	return updateUserMCPServerSyncState(
		ctx, db, expected.ID, actorID, expected.WorkspaceID, &expected,
		discoveredTools, protocolVersion, lastError, lastSyncedAt,
	)
}

func updateUserMCPServerSyncState(
	ctx context.Context,
	db *sql.DB,
	id, userID, workspaceID string,
	expected *UserMCPServer,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*UserMCPServer, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	parts := []string{"protocol_version=?", "last_error=?", "last_synced_at=?", "updated_at=?"}
	if lastSyncedAt <= 0 {
		lastSyncedAt = time.Now().Unix()
	}
	args := []any{strings.TrimSpace(protocolVersion), strings.TrimSpace(lastError), lastSyncedAt, time.Now().Unix()}
	if discoveredTools != nil {
		normalized, err := normalizedMCPDiscoveredTools(discoveredTools)
		if err != nil {
			return nil, err
		}
		parts = append(parts, "discovered_tools=?")
		args = append(args, string(normalized))
	}
	casWhere := ""
	casArgs := []any{}
	if expected != nil {
		headers, marshalErr := json.Marshal(nonNilMCPHeaders(expected.Headers))
		if marshalErr != nil {
			return nil, fmt.Errorf("encode expected user MCP headers: %w", marshalErr)
		}
		casWhere = ` AND name=? AND icon=? AND description=? AND url=? AND headers=? AND enabled=? AND last_synced_at=?`
		casArgs = append(casArgs,
			expected.Name, expected.Icon, expected.Description, expected.URL, string(headers),
			boolInt(expected.Enabled), expected.LastSyncedAt,
		)
	}
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `UPDATE user_mcp_servers SET `+strings.Join(parts, ", ")+
			` WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`+casWhere,
			append(append(args, id, userID), casArgs...)...)
	} else {
		result, err = db.ExecContext(ctx, `UPDATE user_mcp_servers SET `+strings.Join(parts, ", ")+`
			WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_mcp_servers", "mcp")+` AND `+libraryWorkspaceCapabilityPredicate("user_mcp_servers", "mcp")+casWhere,
			append(append(args, id, workspaceID, userID, userID, userID), casArgs...)...)
	}
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		if expected != nil {
			return nil, ErrMCPDiscoveryStateChanged
		}
		return nil, ErrNotFound
	}
	return GetUserMCPServerScoped(ctx, db, id, userID, workspaceID)
}

// DeleteUserMCPServer removes a row from the caller's personal library or,
// with manage authority, from a shared workspace.
func DeleteUserMCPServer(ctx context.Context, db *sql.DB, id, userID, workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	var result sql.Result
	var err error
	if workspaceID == "" {
		result, err = db.ExecContext(ctx, `DELETE FROM user_mcp_servers WHERE id=? AND user_id=? AND COALESCE(workspace_id,'')=''`, id, userID)
	} else {
		result, err = db.ExecContext(ctx, `DELETE FROM user_mcp_servers
			WHERE id=? AND workspace_id=? AND `+libraryWorkspaceManagePredicateFor("user_mcp_servers", "mcp")+` AND `+libraryWorkspaceCapabilityPredicate("user_mcp_servers", "mcp"),
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
