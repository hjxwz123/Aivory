package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"aivory/server/internal/mcp"
	"aivory/server/internal/store"
)

const (
	mcpHeaderMask          = "••••••"
	mcpNameMaxBytes        = 200
	mcpIconMaxBytes        = 512
	mcpDescriptionMaxBytes = 8 << 10
	mcpURLMaxBytes         = 4 << 10
	mcpHeaderNameMaxBytes  = 256
	mcpHeaderValueMaxBytes = 16 << 10
	mcpHeadersMaxBytes     = 64 << 10
	mcpHeadersMaxCount     = 128
)

type mcpServerPayload struct {
	Name        *string            `json:"name"`
	Icon        *string            `json:"icon"`
	Description *string            `json:"description"`
	URL         *string            `json:"url"`
	Headers     *map[string]string `json:"headers"`
	Enabled     *bool              `json:"enabled"`
}

type adminMCPServerResponse struct {
	ID              string            `json:"id"`
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

func adminMCPServerJSON(server store.MCPServer) adminMCPServerResponse {
	headers := make(map[string]string, len(server.Headers))
	for key := range server.Headers {
		headers[key] = mcpHeaderMask
	}
	discoveredTools := server.DiscoveredTools
	if len(discoveredTools) == 0 {
		discoveredTools = json.RawMessage(`[]`)
	}
	return adminMCPServerResponse{
		ID: server.ID, Name: server.Name, Icon: server.Icon, Description: server.Description,
		URL: server.URL, Headers: headers, Enabled: server.Enabled,
		DiscoveredTools: discoveredTools, ProtocolVersion: server.ProtocolVersion,
		LastError: server.LastError, LastSyncedAt: server.LastSyncedAt,
		CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

func listMCPServersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	servers, err := store.ListMCPServers(r.Context(), d.DB, false)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	response := make([]adminMCPServerResponse, 0, len(servers))
	for _, server := range servers {
		response = append(response, adminMCPServerJSON(server))
	}
	writeJSON(w, http.StatusOK, response)
}

func createMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var payload mcpServerPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	server := store.MCPServer{}
	if payload.Name != nil {
		server.Name = *payload.Name
	}
	if payload.Icon != nil {
		server.Icon = *payload.Icon
	}
	if payload.Description != nil {
		server.Description = *payload.Description
	}
	if payload.URL != nil {
		server.URL = *payload.URL
	}
	if payload.Enabled != nil {
		server.Enabled = *payload.Enabled
	}
	headers := map[string]string{}
	if payload.Headers != nil {
		var err error
		headers, err = normalizeMCPHeaders(*payload.Headers, nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	server.Headers = headers
	normalizeMCPServerMetadata(&server)
	if err := validateMCPServerMetadata(server); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	created, err := store.CreateMCPServer(r.Context(), d.DB, server)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminMCPServerJSON(*created))
}

func updateMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	current, err := store.GetMCPServer(r.Context(), d.DB, id)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	var payload mcpServerPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	effective := *current
	patch := store.MCPServerPatch{Enabled: payload.Enabled}
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
	if err := validateMCPServerMetadata(effective); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := store.UpdateMCPServer(r.Context(), d.DB, id, patch)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminMCPServerJSON(*updated))
}

func deleteMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeleteMCPServer(r.Context(), d.DB, pathParam(r, "id")); err != nil {
		writeMCPServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func testMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	runMCPDiscoveryAdmin(d, w, r, false)
}

func syncMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	runMCPDiscoveryAdmin(d, w, r, true)
}

func runMCPDiscoveryAdmin(d Deps, w http.ResponseWriter, r *http.Request, persistTools bool) {
	id := pathParam(r, "id")
	server, err := store.GetMCPServer(r.Context(), d.DB, id)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	client, err := mcp.NewClient(mcp.Config{URL: server.URL, Headers: server.Headers})
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), id, server.ProtocolVersion, server.Headers, err)
		return
	}
	discovery, err := client.Discover(r.Context())
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), id, server.ProtocolVersion, server.Headers, err)
		return
	}
	remoteTools, err := client.ListTools(r.Context())
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), id, discovery.ProtocolVersion, server.Headers, err)
		return
	}
	var snapshot json.RawMessage
	if persistTools {
		raw, marshalErr := json.Marshal(remoteTools)
		if marshalErr != nil {
			writeMCPDiscoveryFailure(d, w, r.Context(), id, discovery.ProtocolVersion, server.Headers, marshalErr)
			return
		}
		snapshot = raw
	}
	updated, err := store.UpdateMCPServerSyncState(
		r.Context(), d.DB, id, snapshot, discovery.ProtocolVersion, "", 0,
	)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminMCPServerJSON(*updated))
}

func writeMCPDiscoveryFailure(
	d Deps,
	w http.ResponseWriter,
	ctx context.Context,
	id string,
	protocolVersion string,
	headers map[string]string,
	discoveryErr error,
) {
	message := sanitizeMCPDiscoveryError(discoveryErr, headers)
	if len(message) > 4000 {
		message = message[:4000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	_, _ = store.UpdateMCPServerSyncState(ctx, d.DB, id, nil, protocolVersion, message, 0)
	writeError(w, http.StatusBadGateway, errors.New(message))
}

func sanitizeMCPDiscoveryError(discoveryErr error, headers map[string]string) string {
	message := "MCP discovery failed"
	if discoveryErr != nil && strings.TrimSpace(discoveryErr.Error()) != "" {
		message = strings.TrimSpace(discoveryErr.Error())
	}
	// Replace longer values first so overlapping credentials cannot leave a
	// visible suffix. Header names are intentionally retained for diagnostics.
	values := make([]string, 0, len(headers))
	for _, value := range headers {
		if value != "" && value != mcpHeaderMask {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		message = strings.ReplaceAll(message, value, mcpHeaderMask)
	}
	return message
}

func writeMCPServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound)
	case errors.Is(err, store.ErrMCPServerNameExists), errors.Is(err, store.ErrMCPServerIDExists):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func normalizeMCPServerMetadata(server *store.MCPServer) {
	server.Name = strings.TrimSpace(server.Name)
	server.Icon = strings.TrimSpace(server.Icon)
	server.Description = strings.TrimSpace(server.Description)
	server.URL = strings.TrimSpace(server.URL)
}

func validateMCPServerMetadata(server store.MCPServer) error {
	if server.Name == "" {
		return errors.New("name required")
	}
	if server.Description == "" {
		return errors.New("description required")
	}
	if server.Icon == "" {
		return errors.New("icon required")
	}
	if !utf8.ValidString(server.Name) || len(server.Name) > mcpNameMaxBytes {
		return errors.New("name is too long or invalid")
	}
	if !utf8.ValidString(server.Icon) || len(server.Icon) > mcpIconMaxBytes {
		return errors.New("icon is too long or invalid")
	}
	if !utf8.ValidString(server.Description) || len(server.Description) > mcpDescriptionMaxBytes {
		return errors.New("description is too long or invalid")
	}
	if len(server.URL) == 0 || len(server.URL) > mcpURLMaxBytes {
		return errors.New("valid MCP URL required")
	}
	parsed, err := url.Parse(server.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("MCP URL must use http or https")
	}
	if parsed.User != nil {
		return errors.New("MCP URL must not contain credentials")
	}
	if parsed.Fragment != "" {
		return errors.New("MCP URL must not contain a fragment")
	}
	return nil
}

// normalizeMCPHeaders treats the submitted object as a replacement so removing
// a key from the editor removes it from the server. For keys that remain, an
// empty or masked value keeps the current credential. This gives administrators
// a usable write-only secret editor without ever returning the real value.
func normalizeMCPHeaders(incoming, existing map[string]string) (map[string]string, error) {
	if len(incoming) > mcpHeadersMaxCount {
		return nil, errors.New("too many MCP request headers")
	}
	current := make(map[string]string, len(existing))
	for key, value := range existing {
		canonical, err := normalizeMCPHeaderName(key)
		if err != nil {
			return nil, err
		}
		current[strings.ToLower(canonical)] = value
	}
	normalized := make(map[string]string, len(incoming))
	seen := make(map[string]bool, len(incoming))
	totalBytes := 0
	for key, value := range incoming {
		canonical, err := normalizeMCPHeaderName(key)
		if err != nil {
			return nil, err
		}
		lookup := strings.ToLower(canonical)
		if seen[lookup] {
			return nil, errors.New("duplicate MCP request header name")
		}
		seen[lookup] = true
		if strings.ContainsAny(value, "\r\n\x00") || !utf8.ValidString(value) || len(value) > mcpHeaderValueMaxBytes {
			return nil, errors.New("invalid MCP request header value")
		}
		if strings.TrimSpace(value) == "" || value == mcpHeaderMask {
			saved, ok := current[lookup]
			if !ok {
				return nil, errors.New("masked or empty MCP header has no saved value")
			}
			value = saved
		}
		totalBytes += len(canonical) + len(value)
		if totalBytes > mcpHeadersMaxBytes {
			return nil, errors.New("MCP request headers are too large")
		}
		normalized[canonical] = value
	}
	return normalized, nil
}

func normalizeMCPHeaderName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > mcpHeaderNameMaxBytes {
		return "", errors.New("invalid MCP request header name")
	}
	for _, char := range value {
		if char > 127 || !isHTTPTokenCharacter(byte(char)) {
			return "", errors.New("invalid MCP request header name")
		}
	}
	canonical := http.CanonicalHeaderKey(value)
	if reservedMCPHeader(canonical) {
		return "", errors.New("MCP request header is managed by the client")
	}
	return canonical, nil
}

func reservedMCPHeader(value string) bool {
	switch strings.ToLower(value) {
	case "accept", "content-type", "content-length", "host", "transfer-encoding",
		"mcp-protocol-version", "mcp-session-id", "mcp-method", "mcp-name":
		return true
	default:
		return false
	}
}

func isHTTPTokenCharacter(char byte) bool {
	if char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' {
		return true
	}
	switch char {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
