package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"aivory/server/internal/mcp"
	"aivory/server/internal/netsafe"
	"aivory/server/internal/store"
)

// User-level MCP servers (§ resource library). The handlers mirror the admin
// MCP surface: header values are write-only (responses carry the visual mask),
// metadata validation is shared with admin_mcp_handlers.go, and discovery
// failures are redacted so an echoed credential can never reach the client.
// Selection enforcement and the owner exemption live in toolPolicyAllowsID
// (permissions.go); the tool catalog row is emitted by
// listSelectableToolsHandler with the "usermcp:" namespace.

type userMCPServerCreatePayload struct {
	Name        *string            `json:"name"`
	Icon        *string            `json:"icon"`
	Description *string            `json:"description"`
	URL         *string            `json:"url"`
	Headers     *map[string]string `json:"headers"`
	Enabled     *bool              `json:"enabled"`
	WorkspaceID string             `json:"workspace_id"`
}

type userMCPServerResponse struct {
	ID              string            `json:"id"`
	WorkspaceID     string            `json:"workspace_id"`
	CanManage       bool              `json:"can_manage"`
	Name            string            `json:"name"`
	Icon            string            `json:"icon"`
	Description     string            `json:"description"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	Enabled         bool              `json:"enabled"`
	DiscoveredTools json.RawMessage   `json:"discovered_tools"`
	ProtocolVersion string            `json:"protocol_version"`
	LastError       string            `json:"last_error"`
	LastSyncedAt    int64             `json:"last_synced_at"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
}

func userMCPServerJSON(server store.UserMCPServer) userMCPServerResponse {
	headers := make(map[string]string, len(server.Headers))
	for key := range server.Headers {
		headers[key] = mcpHeaderMask
	}
	discoveredTools := server.DiscoveredTools
	if len(discoveredTools) == 0 {
		discoveredTools = json.RawMessage(`[]`)
	}
	discoveredTools = redactMCPToolSnapshot(discoveredTools, server.Headers)
	return userMCPServerResponse{
		ID: server.ID, WorkspaceID: server.WorkspaceID, CanManage: server.CanManage,
		Name: server.Name, Icon: server.Icon, Description: server.Description,
		URL: mcpResponseURL(server.URL), Headers: headers, Enabled: server.Enabled,
		DiscoveredTools: discoveredTools,
		ProtocolVersion: redactMCPHeaderValues(server.ProtocolVersion, server.Headers),
		LastError:       redactMCPHeaderValues(server.LastError, server.Headers), LastSyncedAt: server.LastSyncedAt,
		CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

func userMCPServerListJSON(server store.UserMCPServer, canUse bool) userMCPServerResponse {
	if canUse || server.CanManage {
		return userMCPServerJSON(server)
	}
	// A member who cannot use or manage this workspace resource only needs the
	// metadata required to render the shared library list. Connection details,
	// header names, schemas, and discovery diagnostics describe an executable
	// integration owned by another member and are not list metadata.
	return userMCPServerResponse{
		ID: server.ID, WorkspaceID: server.WorkspaceID, CanManage: false,
		Name: server.Name, Icon: server.Icon, Description: server.Description,
		Headers: map[string]string{}, Enabled: server.Enabled,
		DiscoveredTools: json.RawMessage(`[]`), LastSyncedAt: server.LastSyncedAt,
		CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

// userMCPMetadataProxy reuses the admin metadata validator/normalizer without
// duplicating its limits: user servers share the exact same shape rules.
func userMCPMetadataProxy(server store.UserMCPServer) store.MCPServer {
	return store.MCPServer{
		ID: server.ID, Name: server.Name, Icon: server.Icon, Description: server.Description,
		URL: server.URL, Headers: server.Headers, Enabled: server.Enabled,
	}
}

func validateUserMCPServerMetadata(server store.UserMCPServer) error {
	return validateMCPServerMetadata(userMCPMetadataProxy(server))
}

func newUserMCPClient(d Deps, server store.UserMCPServer) (*mcp.Client, error) {
	httpClient := d.UserMCPHTTPClient
	if httpClient == nil {
		httpClient = netsafe.UserMCPAllowedClient(mcp.DefaultTimeout)
	}
	return mcp.NewClient(mcp.Config{
		URL: server.URL, Headers: server.Headers, HTTPClient: httpClient,
	})
}

// mcpDiscoveredToolsPresent reports whether a stored snapshot contains at
// least one tool the runtime can actually declare. This deliberately mirrors
// the registry's name and object-schema validation so the picker does not show
// a server whose entire snapshot will be discarded at execution time.
func mcpDiscoveredToolsPresent(discoveredTools json.RawMessage) bool {
	var tools []mcp.Tool
	if json.Unmarshal(discoveredTools, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
			continue
		}
		typeJSON, hasType := schema["type"]
		if !hasType {
			return true
		}
		var schemaType string
		if json.Unmarshal(typeJSON, &schemaType) == nil && schemaType == "object" {
			return true
		}
	}
	return false
}

func writeUserMCPServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound)
	case errors.Is(err, store.ErrUserMCPNameExists), errors.Is(err, store.ErrMCPDiscoveryStateChanged):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

// loadManagedUserMCPServer is the shared authorization preamble for existing
// rows. Creation and management are deliberately independent: revoking
// can_create_mcp blocks POST /api/me/mcps, but a workspace administrator or
// the non-guest creator may still maintain an existing row. Remote test/sync
// handlers apply CanUseMCP separately after this ownership check.
func loadManagedUserMCPServer(d Deps, w http.ResponseWriter, r *http.Request) (*store.UserMCPServer, bool) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspaceCapability(d, w, r, workspaceID, libraryCapabilityMCP, false) {
		return nil, false
	}
	server, err := store.GetUserMCPServerScoped(r.Context(), d.DB, pathParam(r, "id"), authUser(r).ID, workspaceID)
	if err != nil {
		writeUserMCPServerError(w, err)
		return nil, false
	}
	if !server.CanManage {
		writeError(w, http.StatusForbidden, errForbidden)
		return nil, false
	}
	return server, true
}

func listMyMCPServersHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspaceCapability(d, w, r, workspaceID, libraryCapabilityMCP, false) {
		return
	}
	canUse := true
	if workspaceID != "" {
		var err error
		canUse, err = workspaceLibraryUseEnabled(d, r, workspaceID, libraryCapabilityMCP)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, errNotFound)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	servers, err := store.ListUserMCPServersScoped(r.Context(), d.DB, authUser(r).ID, workspaceID)
	if err != nil {
		writeUserMCPServerError(w, err)
		return
	}
	response := make([]userMCPServerResponse, 0, len(servers))
	for _, server := range servers {
		response = append(response, userMCPServerListJSON(server, canUse))
	}
	writeJSON(w, http.StatusOK, response)
}

func createMyMCPServerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var body userMCPServerCreatePayload
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	body.WorkspaceID = strings.TrimSpace(body.WorkspaceID)
	if !authorizeLibraryWorkspaceCapability(d, w, r, body.WorkspaceID, libraryCapabilityMCP, true) {
		return
	}
	server := store.UserMCPServer{UserID: authUser(r).ID, WorkspaceID: body.WorkspaceID, Enabled: true}
	if body.Name != nil {
		server.Name = *body.Name
	}
	if body.Icon != nil {
		server.Icon = *body.Icon
	}
	if body.Description != nil {
		server.Description = *body.Description
	}
	if body.URL != nil {
		server.URL = *body.URL
	}
	if body.Enabled != nil {
		server.Enabled = *body.Enabled
	}
	headers := map[string]string{}
	if body.Headers != nil {
		var err error
		headers, err = normalizeMCPHeaders(*body.Headers, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	server.Headers = headers
	proxy := userMCPMetadataProxy(server)
	normalizeMCPServerMetadata(&proxy)
	server.Name, server.Icon, server.Description, server.URL = proxy.Name, proxy.Icon, proxy.Description, proxy.URL
	if err := validateUserMCPServerMetadata(server); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := store.CreateUserMCPServer(r.Context(), d.DB, server)
	if err != nil {
		writeUserMCPServerError(w, err)
		return
	}
	// Creation defaults to enabled, but a row only becomes selectable after a
	// successful sync fills discovered_tools (§ catalog filter). Run one
	// discovery pass inline; a failure is recorded as last_error, not as a
	// rejected request, so the editor can retry via the sync endpoint.
	final := created
	canUse := true
	if server.WorkspaceID != "" {
		// Creation and use are independent member capabilities. A creator who is
		// intentionally barred from using MCP may still save metadata, but this
		// endpoint must not make a remote request on their behalf.
		canUse, _ = workspaceLibraryUseEnabled(d, r, server.WorkspaceID, libraryCapabilityMCP)
	}
	if canUse && created.Enabled {
		if synced, _ := runUserMCPDiscovery(d, r.Context(), *created, authUser(r).ID, true); synced != nil {
			final = synced
		}
	}
	writeJSON(w, http.StatusCreated, userMCPServerJSON(*final))
}

func updateMyMCPServerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	current, ok := loadManagedUserMCPServer(d, w, r)
	if !ok {
		return
	}
	var payload mcpServerPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	effective := *current
	patch := store.UserMCPServerPatch{Enabled: payload.Enabled}
	if payload.Name != nil {
		value := strings.TrimSpace(*payload.Name)
		patch.Name, effective.Name = &value, value
	}
	if payload.Icon != nil {
		value := strings.TrimSpace(*payload.Icon)
		patch.Icon, effective.Icon = &value, value
	}
	if payload.Description != nil {
		value := strings.TrimSpace(*payload.Description)
		patch.Description, effective.Description = &value, value
	}
	if payload.URL != nil {
		value := strings.TrimSpace(*payload.URL)
		patch.URL, effective.URL = &value, value
	}
	if payload.Enabled != nil {
		effective.Enabled = *payload.Enabled
	}
	if payload.Headers != nil {
		headers, err := normalizeMCPHeaders(*payload.Headers, current.Headers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		patch.Headers, effective.Headers = &headers, headers
	}
	if err := validateUserMCPServerMetadata(effective); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metadataChanged := effective.URL != current.URL || !stringMapsEqual(effective.Headers, current.Headers)
	// Re-enabling a server is also a discovery boundary. A server may have been
	// disabled while its endpoint or remote tool set changed, so never expose a
	// stale snapshot merely because URL and headers stayed unchanged.
	needsDiscovery := metadataChanged || (!current.Enabled && effective.Enabled)
	patch.ResetDiscovery = needsDiscovery
	updated, err := store.UpdateUserMCPServerIfCurrent(
		r.Context(), d.DB, *current, authUser(r).ID, patch,
	)
	if err != nil {
		writeUserMCPServerError(w, err)
		return
	}
	invalidated := false
	if needsDiscovery && d.Tools != nil {
		// The row is already fail-closed (new metadata + empty snapshot) at this
		// point. Drop old bindings before the potentially slow discovery request so
		// they cannot become usable again in the small interval after a successful
		// snapshot write and before this handler returns.
		d.Tools.InvalidateMCPServer(current.ID)
		invalidated = true
	}
	if needsDiscovery {
		canUse := true
		if updated.WorkspaceID != "" {
			canUse, _ = workspaceLibraryUseEnabled(d, r, updated.WorkspaceID, libraryCapabilityMCP)
		}
		if canUse && updated.Enabled {
			if synced, _ := runUserMCPDiscovery(d, r.Context(), *updated, authUser(r).ID, true); synced != nil {
				updated = synced
			}
		}
	}
	if d.Tools != nil && !invalidated && userMCPRuntimeStateChanged(*current, *updated) {
		d.Tools.InvalidateMCPServer(current.ID)
	}
	writeJSON(w, http.StatusOK, userMCPServerJSON(*updated))
}

func deleteMyMCPServerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	current, ok := loadManagedUserMCPServer(d, w, r)
	if !ok {
		return
	}
	if err := store.DeleteUserMCPServer(r.Context(), d.DB, current.ID, authUser(r).ID, current.WorkspaceID); err != nil {
		writeUserMCPServerError(w, err)
		return
	}
	if d.Tools != nil {
		d.Tools.InvalidateMCPServer(current.ID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type userMCPTestTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type userMCPTestResponse struct {
	OK    bool              `json:"ok"`
	Tools []userMCPTestTool `json:"tools"`
	Error string            `json:"error,omitempty"`
}

// testMyMCPServerHandler connects to the saved endpoint without persisting
// anything: no snapshot, no sync state, no last_error. The failure message is
// sanitized so an echoed credential cannot leak through the response body.
func testMyMCPServerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	server, ok := loadManagedUserMCPServer(d, w, r)
	if !ok {
		return
	}
	if !authorizeLibraryWorkspaceUse(d, w, r, server.WorkspaceID, libraryCapabilityMCP) {
		return
	}
	if !server.Enabled {
		writeError(w, http.StatusConflict, errors.New("MCP service is disabled"))
		return
	}
	client, err := newUserMCPClient(d, *server)
	if err == nil {
		if _, err = client.Discover(r.Context()); err == nil {
			var remoteTools []mcp.Tool
			remoteTools, err = client.ListTools(r.Context())
			if err == nil {
				remoteTools, err = redactMCPToolSecrets(remoteTools, server.Headers)
			}
			if err == nil {
				tools := make([]userMCPTestTool, 0, len(remoteTools))
				for _, tool := range remoteTools {
					// Keep the protocol name intact for runtime routing, but redact it
					// at this display-only boundary in case a hostile endpoint echoes a
					// configured credential in the name field.
					tools = append(tools, userMCPTestTool{
						Name:        redactMCPHeaderValues(tool.Name, server.Headers),
						Description: tool.Description,
					})
				}
				writeJSON(w, http.StatusOK, userMCPTestResponse{OK: true, Tools: tools})
				return
			}
		}
	}
	message := sanitizeMCPDiscoveryError(err, server.Headers)
	if len(message) > 4000 {
		message = message[:4000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	writeJSON(w, http.StatusOK, userMCPTestResponse{OK: false, Tools: []userMCPTestTool{}, Error: message})
}

func syncMyMCPServerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	server, ok := loadManagedUserMCPServer(d, w, r)
	if !ok {
		return
	}
	if !authorizeLibraryWorkspaceUse(d, w, r, server.WorkspaceID, libraryCapabilityMCP) {
		return
	}
	if !server.Enabled {
		writeError(w, http.StatusConflict, errors.New("MCP service is disabled"))
		return
	}
	updated, discoveryErr := runUserMCPDiscovery(d, r.Context(), *server, authUser(r).ID, true)
	if updated == nil {
		writeUserMCPServerError(w, discoveryErr)
		return
	}
	if discoveryErr != nil {
		writeError(w, http.StatusBadGateway, errors.New(updated.LastError))
		return
	}
	if d.Tools != nil {
		// Reset any legacy MCP session after a successful handshake, even when
		// tools/list is unchanged; the remote may have restarted since last use.
		d.Tools.InvalidateMCPServer(server.ID)
	}
	writeJSON(w, http.StatusOK, userMCPServerJSON(*updated))
}

// runUserMCPDiscovery performs the same Discover + ListTools handshake as the
// admin flow. persistTools replaces the snapshot on success; on failure the
// last good snapshot is preserved (nil raw message) and the sanitized message
// becomes last_error. The returned error is already redacted; a nil row with
// a non-nil error means the state write itself failed.
func runUserMCPDiscovery(
	d Deps, ctx context.Context, server store.UserMCPServer, actorID string, persistTools bool,
) (*store.UserMCPServer, error) {
	if !server.Enabled {
		return nil, errors.New("MCP service is disabled")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		// Internal maintenance callers predating the actor parameter may omit it;
		// personal rows still require their owner and workspace rows are best
		// handled by the authenticated HTTP paths above.
		actorID = server.UserID
	}
	client, err := newUserMCPClient(d, server)
	if err != nil {
		return recordUserMCPDiscoveryFailure(d, ctx, server, actorID, server.ProtocolVersion, err)
	}
	discovery, err := client.Discover(ctx)
	if err != nil {
		return recordUserMCPDiscoveryFailure(d, ctx, server, actorID, server.ProtocolVersion, err)
	}
	remoteTools, err := client.ListTools(ctx)
	if err != nil {
		return recordUserMCPDiscoveryFailure(d, ctx, server, actorID, discovery.ProtocolVersion, err)
	}
	// Do not let a remote endpoint echo a configured credential in tool metadata;
	// workspace snapshots are visible to every current member and are also sent
	// to the model during tool selection.
	remoteTools, err = redactMCPToolSecrets(remoteTools, server.Headers)
	if err != nil {
		return recordUserMCPDiscoveryFailure(d, ctx, server, actorID, discovery.ProtocolVersion, err)
	}
	var snapshot json.RawMessage
	if persistTools {
		raw, marshalErr := json.Marshal(remoteTools)
		if marshalErr != nil {
			return recordUserMCPDiscoveryFailure(d, ctx, server, actorID, discovery.ProtocolVersion, marshalErr)
		}
		snapshot = raw
	}
	updated, err := store.UpdateUserMCPServerSyncStateIfCurrent(
		ctx, d.DB, server, actorID, snapshot, discovery.ProtocolVersion, "", 0,
	)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func recordUserMCPDiscoveryFailure(
	d Deps, ctx context.Context, server store.UserMCPServer, actorID string, protocolVersion string, discoveryErr error,
) (*store.UserMCPServer, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		actorID = server.UserID
	}
	message := sanitizeMCPDiscoveryError(discoveryErr, server.Headers)
	if len(message) > 4000 {
		message = message[:4000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	updated, err := store.UpdateUserMCPServerSyncStateIfCurrent(
		ctx, d.DB, server, actorID, nil, protocolVersion, message, 0,
	)
	if err != nil {
		return nil, err
	}
	if d.Tools != nil {
		// A failed handshake can retire the negotiated protocol/session even when
		// the last successful tools snapshot is preserved. Do not let the runtime
		// reuse that client on the next call.
		d.Tools.InvalidateMCPServer(server.ID)
	}
	return updated, errors.New(message)
}

func userMCPRuntimeStateChanged(before, after store.UserMCPServer) bool {
	return before.Enabled != after.Enabled || before.URL != after.URL ||
		!stringMapsEqual(before.Headers, after.Headers) ||
		!jsonValuesEqual(before.DiscoveredTools, after.DiscoveredTools)
}
