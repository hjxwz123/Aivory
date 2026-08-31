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
	ErrMCPServerNameExists = errors.New("MCP server name already exists")
	ErrMCPServerIDExists   = errors.New("MCP server id already exists")
)

const mcpServerColumns = `id, name, icon, description, url, headers, enabled,
	discovered_tools, protocol_version, last_error, last_synced_at, created_at, updated_at`

func scanMCPServer(s scanner) (MCPServer, error) {
	var server MCPServer
	var headersText, discoveredToolsText string
	var enabled int
	if err := s.Scan(
		&server.ID, &server.Name, &server.Icon, &server.Description, &server.URL,
		&headersText, &enabled, &discoveredToolsText, &server.ProtocolVersion,
		&server.LastError, &server.LastSyncedAt, &server.CreatedAt, &server.UpdatedAt,
	); err != nil {
		return server, err
	}
	if err := json.Unmarshal([]byte(headersText), &server.Headers); err != nil || server.Headers == nil {
		if err == nil {
			err = errors.New("headers must be a JSON object")
		}
		return server, fmt.Errorf("decode MCP server %s headers: %w", server.ID, err)
	}
	if !validJSONArray([]byte(discoveredToolsText)) {
		return server, fmt.Errorf("decode MCP server %s discovered tools: expected JSON array", server.ID)
	}
	server.Enabled = enabled == 1
	server.DiscoveredTools = json.RawMessage(discoveredToolsText)
	return server, nil
}

func validJSONArray(raw []byte) bool {
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && values != nil
}

// ListMCPServers returns configured MCP endpoints, including their real request
// headers for trusted server-side consumers. HTTP handlers must mask Headers
// before serializing a response.
func ListMCPServers(ctx context.Context, db *sql.DB, onlyEnabled bool) ([]MCPServer, error) {
	query := `SELECT ` + mcpServerColumns + ` FROM mcp_servers`
	if onlyEnabled {
		query += ` WHERE enabled=1`
	}
	query += ` ORDER BY lower(trim(name)), id`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := []MCPServer{}
	for rows.Next() {
		server, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

// GetMCPServer returns one endpoint with its real headers. It is an internal
// credential-bearing model and must not be serialized directly.
func GetMCPServer(ctx context.Context, db *sql.DB, id string) (*MCPServer, error) {
	server, err := scanMCPServer(db.QueryRowContext(ctx,
		`SELECT `+mcpServerColumns+` FROM mcp_servers WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// GetMCPServerByName resolves the case-insensitive administrator display name.
func GetMCPServerByName(ctx context.Context, db *sql.DB, name string) (*MCPServer, error) {
	server, err := scanMCPServer(db.QueryRowContext(ctx,
		`SELECT `+mcpServerColumns+` FROM mcp_servers WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// CreateMCPServer persists a Streamable HTTP endpoint. New records are normally
// created disabled by the API until discovery succeeds, but Enabled remains an
// explicit store field for imports and administrative tooling.
func CreateMCPServer(ctx context.Context, db *sql.DB, server MCPServer) (*MCPServer, error) {
	server.Name = strings.TrimSpace(server.Name)
	server.Icon = strings.TrimSpace(server.Icon)
	server.Description = strings.TrimSpace(server.Description)
	server.URL = strings.TrimSpace(server.URL)
	server.ProtocolVersion = strings.TrimSpace(server.ProtocolVersion)
	if server.ID == "" {
		server.ID = genID("mcp")
	}
	headers, err := json.Marshal(nonNilMCPHeaders(server.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode MCP headers: %w", err)
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
	_, err = db.ExecContext(ctx, `INSERT INTO mcp_servers(
		id, name, icon, description, url, headers, enabled, discovered_tools,
		protocol_version, last_error, last_synced_at, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		server.ID, server.Name, server.Icon, server.Description, server.URL, string(headers),
		boolInt(server.Enabled), string(discoveredTools), server.ProtocolVersion,
		server.LastError, server.LastSyncedAt, server.CreatedAt, server.UpdatedAt)
	if err != nil {
		switch {
		case isMCPServerNameUniqueErr(err):
			return nil, ErrMCPServerNameExists
		case isUniqueIndexErr(err, "mcp_servers.id", "mcp_servers_pkey"):
			return nil, ErrMCPServerIDExists
		default:
			return nil, err
		}
	}
	return GetMCPServer(ctx, db, server.ID)
}

// MCPServerPatch contains fields that administrators may change. Discovery
// state is updated separately so a normal metadata edit cannot forge the
// remote tools snapshot or connection status.
type MCPServerPatch struct {
	Name        *string
	Icon        *string
	Description *string
	URL         *string
	Headers     *map[string]string
	Enabled     *bool
	// ResetDiscovery atomically retires the snapshot negotiated for the previous
	// endpoint/credentials. API callers set it when trust-bearing metadata changes
	// or a disabled service is re-enabled, so runtime readers can never observe a
	// new endpoint paired with stale tool definitions between two SQL statements.
	ResetDiscovery bool
}

func UpdateMCPServer(ctx context.Context, db *sql.DB, id string, patch MCPServerPatch) (*MCPServer, error) {
	return updateMCPServer(ctx, db, id, nil, patch)
}

// UpdateMCPServerIfCurrent applies a metadata patch only while the endpoint,
// credentials, enabled state, and discovery generation still match expected.
// Handlers use this with ResetDiscovery so the decision to retire a snapshot is
// based on the same row version the SQL statement updates.
func UpdateMCPServerIfCurrent(
	ctx context.Context, db *sql.DB, expected MCPServer, patch MCPServerPatch,
) (*MCPServer, error) {
	if strings.TrimSpace(expected.ID) == "" {
		return nil, ErrNotFound
	}
	return updateMCPServer(ctx, db, expected.ID, &expected, patch)
}

func updateMCPServer(
	ctx context.Context, db *sql.DB, id string, expected *MCPServer, patch MCPServerPatch,
) (*MCPServer, error) {
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
			return nil, fmt.Errorf("encode MCP headers: %w", err)
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
		return GetMCPServer(ctx, db, id)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	where := "id=?"
	args = append(args, id)
	if expected != nil {
		headers, err := json.Marshal(nonNilMCPHeaders(expected.Headers))
		if err != nil {
			return nil, fmt.Errorf("encode expected MCP headers: %w", err)
		}
		where += ` AND url=? AND headers=? AND enabled=? AND discovered_tools=? AND protocol_version=? AND last_synced_at=?`
		args = append(args, expected.URL, string(headers), boolInt(expected.Enabled),
			string(expected.DiscoveredTools), expected.ProtocolVersion, expected.LastSyncedAt)
	}
	result, err := db.ExecContext(ctx,
		`UPDATE mcp_servers SET `+strings.Join(parts, ", ")+` WHERE `+where, args...)
	if err != nil {
		if isMCPServerNameUniqueErr(err) {
			return nil, ErrMCPServerNameExists
		}
		return nil, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		if expected != nil {
			return nil, ErrMCPDiscoveryStateChanged
		}
		return nil, ErrNotFound
	}
	return GetMCPServer(ctx, db, id)
}

// UpdateMCPServerSyncState records a discovery attempt. A nil discoveredTools
// value preserves the last successful snapshot, which lets callers record a
// transient connection error without removing working tool definitions.
func UpdateMCPServerSyncState(
	ctx context.Context,
	db *sql.DB,
	id string,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*MCPServer, error) {
	return updateMCPServerSyncState(
		ctx, db, id, nil, discoveredTools, protocolVersion, lastError, lastSyncedAt,
	)
}

// UpdateMCPServerSyncStateIfCurrent is the discovery compare-and-swap path.
// The remote handshake may take seconds, so every trust-bearing field and the
// previous discovery generation are matched in the UPDATE itself. A metadata
// edit, disable, deletion, or newer sync therefore makes this result stale and
// prevents it from replacing the authoritative row.
func UpdateMCPServerSyncStateIfCurrent(
	ctx context.Context,
	db *sql.DB,
	expected MCPServer,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*MCPServer, error) {
	if strings.TrimSpace(expected.ID) == "" {
		return nil, ErrNotFound
	}
	if lastSyncedAt <= expected.LastSyncedAt {
		lastSyncedAt = time.Now().Unix()
		if lastSyncedAt <= expected.LastSyncedAt {
			lastSyncedAt = expected.LastSyncedAt + 1
		}
	}
	return updateMCPServerSyncState(
		ctx, db, expected.ID, &expected, discoveredTools, protocolVersion, lastError, lastSyncedAt,
	)
}

func updateMCPServerSyncState(
	ctx context.Context,
	db *sql.DB,
	id string,
	expected *MCPServer,
	discoveredTools json.RawMessage,
	protocolVersion string,
	lastError string,
	lastSyncedAt int64,
) (*MCPServer, error) {
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
	where := "id=?"
	args = append(args, id)
	if expected != nil {
		headers, err := json.Marshal(nonNilMCPHeaders(expected.Headers))
		if err != nil {
			return nil, fmt.Errorf("encode expected MCP headers: %w", err)
		}
		// Name/icon/description are included as well as endpoint credentials. They
		// do not change the transport, but a concurrent editor should still receive
		// the response for the row they saved, not an older discovery request.
		where += ` AND name=? AND icon=? AND description=? AND url=? AND headers=? AND enabled=? AND last_synced_at=?`
		args = append(args,
			expected.Name, expected.Icon, expected.Description, expected.URL, string(headers),
			boolInt(expected.Enabled), expected.LastSyncedAt,
		)
	}
	result, err := db.ExecContext(ctx,
		`UPDATE mcp_servers SET `+strings.Join(parts, ", ")+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		if expected != nil {
			return nil, ErrMCPDiscoveryStateChanged
		}
		return nil, ErrNotFound
	}
	return GetMCPServer(ctx, db, id)
}

func DeleteMCPServer(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func normalizedMCPDiscoveredTools(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`[]`), nil
	}
	if !validJSONArray(raw) {
		return nil, errors.New("MCP discovered tools must be a JSON array")
	}
	var compact json.RawMessage
	if err := json.Unmarshal(raw, &compact); err != nil {
		return nil, errors.New("MCP discovered tools must be a JSON array")
	}
	return compact, nil
}

func nonNilMCPHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return map[string]string{}
	}
	return headers
}

func isMCPServerNameUniqueErr(err error) bool {
	return isUniqueIndexErr(err, "idx_mcp_servers_name_unique", "mcp_servers.name")
}
