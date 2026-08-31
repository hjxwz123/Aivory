package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	// Re-sanitize at the serialization boundary as well as during discovery.
	// This protects rows restored from an older backup or written before metadata
	// redaction existed from exposing a configured header value in the API.
	discoveredTools = redactMCPToolSnapshot(discoveredTools, server.Headers)
	return adminMCPServerResponse{
		ID: server.ID, Name: server.Name, Icon: server.Icon, Description: server.Description,
		URL: mcpResponseURL(server.URL), Headers: headers, Enabled: server.Enabled,
		DiscoveredTools: discoveredTools,
		ProtocolVersion: redactMCPHeaderValues(server.ProtocolVersion, server.Headers),
		LastError:       redactMCPHeaderValues(server.LastError, server.Headers), LastSyncedAt: server.LastSyncedAt,
		CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

// mcpResponseURL keeps legacy/imported endpoint credentials out of HTTP
// responses. Current writes reject userinfo, query parameters, and fragments,
// but an upgraded database may still contain one of those older values. The
// runtime rejects such rows too, so returning only the non-secret endpoint does
// not hide a usable configuration from the editor; saving it migrates the row
// onto the supported headers-based authentication contract.
func mcpResponseURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
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
	metadataChanged := effective.URL != current.URL || !stringMapsEqual(effective.Headers, current.Headers)
	needsFreshDiscovery := metadataChanged || (!current.Enabled && effective.Enabled)
	patch.ResetDiscovery = needsFreshDiscovery
	updated, err := store.UpdateMCPServerIfCurrent(r.Context(), d.DB, *current, patch)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	enabledChanged := current.Enabled != updated.Enabled
	if d.Tools != nil && (needsFreshDiscovery || mcpRuntimeCapabilityChanged(*current, *updated)) {
		d.Tools.InvalidateMCPServer(id)
	}
	if mcpRuntimeCapabilityTightened(*current, *updated) {
		revokeGlobalCapabilitySnapshots(d)
		publishGlobalEvent(d, "account.permissions_updated")
	} else if enabledChanged || mcpRuntimeCapabilityChanged(*current, *updated) || mcpCatalogPresentationChanged(*current, *updated) {
		publishGlobalEvent(d, "account.permissions_updated")
	}
	writeJSON(w, http.StatusOK, adminMCPServerJSON(*updated))
}

func deleteMCPServerAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	current, err := store.GetMCPServer(r.Context(), d.DB, id)
	if err != nil {
		writeMCPServerError(w, err)
		return
	}
	if err := store.DeleteMCPServer(r.Context(), d.DB, id); err != nil {
		writeMCPServerError(w, err)
		return
	}
	if d.Tools != nil {
		d.Tools.InvalidateMCPServer(id)
	}
	if mcpServerHasTools(*current) {
		revokeGlobalCapabilitySnapshots(d)
		publishGlobalEvent(d, "account.permissions_updated")
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
		writeMCPDiscoveryFailure(d, w, r.Context(), *server, server.ProtocolVersion, err)
		return
	}
	discovery, err := client.Discover(r.Context())
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), *server, server.ProtocolVersion, err)
		return
	}
	remoteTools, err := client.ListTools(r.Context())
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), *server, discovery.ProtocolVersion, err)
		return
	}
	// A remote endpoint can echo configured credentials in tool metadata (for
	// example, a description or JSON-Schema default). Never persist that value in
	// a snapshot that is later shown to other users or sent to an LLM.
	remoteTools, err = redactMCPToolSecrets(remoteTools, server.Headers)
	if err != nil {
		writeMCPDiscoveryFailure(d, w, r.Context(), *server, discovery.ProtocolVersion, err)
		return
	}
	var snapshot json.RawMessage
	if persistTools {
		raw, marshalErr := json.Marshal(remoteTools)
		if marshalErr != nil {
			writeMCPDiscoveryFailure(d, w, r.Context(), *server, discovery.ProtocolVersion, marshalErr)
			return
		}
		snapshot = raw
	}
	updated, err := store.UpdateMCPServerSyncStateIfCurrent(
		r.Context(), d.DB, *server, snapshot, discovery.ProtocolVersion, "", 0,
	)
	if err != nil {
		if errors.Is(err, store.ErrMCPDiscoveryStateChanged) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeMCPServerError(w, err)
		return
	}
	if persistTools && d.Tools != nil {
		// A successful sync starts a fresh remote protocol session even when the
		// schema is unchanged; a server restart may have invalidated the cached id.
		d.Tools.InvalidateMCPServer(id)
	}
	if persistTools && server.Enabled && !jsonValuesEqual(server.DiscoveredTools, updated.DiscoveredTools) {
		revokeGlobalCapabilitySnapshots(d)
		publishGlobalEvent(d, "account.permissions_updated")
	}
	writeJSON(w, http.StatusOK, adminMCPServerJSON(*updated))
}

func mcpServerHasTools(server store.MCPServer) bool {
	if !server.Enabled {
		return false
	}
	return mcpDiscoveredToolsPresent(server.DiscoveredTools)
}

func mcpRuntimeCapabilityChanged(before, after store.MCPServer) bool {
	beforeAvailable := mcpServerHasTools(before)
	afterAvailable := mcpServerHasTools(after)
	if beforeAvailable != afterAvailable {
		return true
	}
	if !beforeAvailable && !afterAvailable {
		return false
	}
	return before.URL != after.URL || !stringMapsEqual(before.Headers, after.Headers)
}

// A capability addition only needs to refresh connected clients. Cancelling
// generations is reserved for changes that can invalidate a tool snapshot
// already in use: disabling an available server or replacing its endpoint or
// credentials while it remains available.
func mcpRuntimeCapabilityTightened(before, after store.MCPServer) bool {
	beforeAvailable := mcpServerHasTools(before)
	afterAvailable := mcpServerHasTools(after)
	if !beforeAvailable {
		return false
	}
	if !afterAvailable {
		return true
	}
	return before.URL != after.URL || !stringMapsEqual(before.Headers, after.Headers)
}

func mcpCatalogPresentationChanged(before, after store.MCPServer) bool {
	if !mcpServerHasTools(before) && !mcpServerHasTools(after) {
		return false
	}
	return before.Name != after.Name || before.Icon != after.Icon || before.Description != after.Description
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func jsonValuesEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func writeMCPDiscoveryFailure(
	d Deps,
	w http.ResponseWriter,
	ctx context.Context,
	server store.MCPServer,
	protocolVersion string,
	discoveryErr error,
) {
	message := sanitizeMCPDiscoveryError(discoveryErr, server.Headers)
	if len(message) > 4000 {
		message = message[:4000]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	_, err := store.UpdateMCPServerSyncStateIfCurrent(
		ctx, d.DB, server, nil, protocolVersion, message, 0,
	)
	if errors.Is(err, store.ErrMCPDiscoveryStateChanged) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err == nil && d.Tools != nil {
		// The failed handshake may have negotiated a different protocol or
		// invalidated the remote session. Retire any cached client even though the
		// last successful tools snapshot remains available for display/retry.
		d.Tools.InvalidateMCPServer(server.ID)
	}
	writeError(w, http.StatusBadGateway, errors.New(message))
}

func sanitizeMCPDiscoveryError(discoveryErr error, headers map[string]string) string {
	message := "MCP discovery failed"
	if discoveryErr != nil && strings.TrimSpace(discoveryErr.Error()) != "" {
		message = strings.TrimSpace(discoveryErr.Error())
	}
	return redactMCPHeaderValues(message, headers)
}

// redactMCPHeaderValues replaces configured header values longest-first. Header
// names are intentionally retained for diagnostics, while empty and already
// masked values are ignored because they carry no secret.
func redactMCPHeaderValues(value string, headers map[string]string) string {
	if value == "" || len(headers) == 0 {
		return value
	}
	values := make([]string, 0, len(headers))
	seen := map[string]struct{}{}
	for _, secret := range headers {
		if secret == "" || secret == mcpHeaderMask {
			continue
		}
		if _, ok := seen[secret]; ok {
			continue
		}
		seen[secret] = struct{}{}
		values = append(values, secret)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, secret := range values {
		value = strings.ReplaceAll(value, secret, mcpHeaderMask)
	}
	return value
}

var errMCPJSONKeyCollision = errors.New("MCP tool metadata keys conflict after credential redaction")

// redactMCPJSONValues recursively replaces secret-bearing object keys and
// string values while preserving the JSON shape expected by providers.
func redactMCPJSONValues(raw json.RawMessage, headers map[string]string) (json.RawMessage, error) {
	if len(raw) == 0 || len(headers) == 0 {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("invalid MCP tool metadata JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid MCP tool metadata JSON")
	}
	var redact func(any) (any, bool, error)
	redact = func(input any) (any, bool, error) {
		switch typed := input.(type) {
		case string:
			redacted := redactMCPHeaderValues(typed, headers)
			return redacted, redacted != typed, nil
		case []any:
			changed := false
			for index := range typed {
				redactedItem, itemChanged, err := redact(typed[index])
				if err != nil {
					return nil, false, err
				}
				typed[index] = redactedItem
				changed = changed || itemChanged
			}
			return typed, changed, nil
		case map[string]any:
			changed := false
			redactedMap := make(map[string]any, len(typed))
			for key, item := range typed {
				redactedKey := redactMCPHeaderValues(key, headers)
				if _, exists := redactedMap[redactedKey]; exists {
					return nil, false, errMCPJSONKeyCollision
				}
				redactedItem, itemChanged, err := redact(item)
				if err != nil {
					return nil, false, err
				}
				redactedMap[redactedKey] = redactedItem
				changed = changed || redactedKey != key || itemChanged
			}
			return redactedMap, changed, nil
		}
		return input, false, nil
	}
	redacted, changed, err := redact(value)
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return nil, errors.New("encode redacted MCP tool metadata JSON")
	}
	return encoded, nil
}

// redactMCPToolSecrets sanitizes every user-visible/raw metadata field except
// Name. The protocol name is retained as the trusted routing key used by
// tools/call; descriptions, titles, schemas, annotations, and icon metadata
// cannot leak configured header values into snapshots or model prompts.
func redactMCPToolSecrets(tools []mcp.Tool, headers map[string]string) ([]mcp.Tool, error) {
	if len(tools) == 0 || len(headers) == 0 {
		return tools, nil
	}
	redacted := append([]mcp.Tool(nil), tools...)
	for index := range redacted {
		tool := &redacted[index]
		tool.Title = redactMCPHeaderValues(tool.Title, headers)
		tool.Description = redactMCPHeaderValues(tool.Description, headers)
		var err error
		if tool.InputSchema, err = redactMCPJSONValues(tool.InputSchema, headers); err != nil {
			return nil, err
		}
		if tool.OutputSchema, err = redactMCPJSONValues(tool.OutputSchema, headers); err != nil {
			return nil, err
		}
		if tool.Annotations, err = redactMCPJSONValues(tool.Annotations, headers); err != nil {
			return nil, err
		}
		if tool.Meta, err = redactMCPJSONValues(tool.Meta, headers); err != nil {
			return nil, err
		}
		if len(tool.Icons) > 0 {
			tool.Icons = append([]mcp.Icon(nil), tool.Icons...)
		}
		for iconIndex := range tool.Icons {
			tool.Icons[iconIndex].Source = redactMCPHeaderValues(tool.Icons[iconIndex].Source, headers)
			tool.Icons[iconIndex].MimeType = redactMCPHeaderValues(tool.Icons[iconIndex].MimeType, headers)
			tool.Icons[iconIndex].Sizes = append([]string(nil), tool.Icons[iconIndex].Sizes...)
			for sizeIndex := range tool.Icons[iconIndex].Sizes {
				tool.Icons[iconIndex].Sizes[sizeIndex] = redactMCPHeaderValues(tool.Icons[iconIndex].Sizes[sizeIndex], headers)
			}
		}
	}
	return redacted, nil
}

// redactMCPToolSnapshot sanitizes a persisted tools/list array before it crosses
// an API boundary. Discovery already sanitizes new writes, but legacy backups
// and direct database imports may predate that guarantee.
func redactMCPToolSnapshot(raw json.RawMessage, headers map[string]string) json.RawMessage {
	if len(raw) == 0 || len(headers) == 0 {
		return raw
	}
	redacted, err := redactMCPJSONValues(raw, headers)
	if err != nil {
		// A legacy/imported snapshot with ambiguous redacted keys cannot be
		// represented faithfully. Hide the complete snapshot at this display
		// boundary instead of returning either credential-bearing key.
		return json.RawMessage(`[]`)
	}
	return redacted
}

func writeMCPServerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound)
	case errors.Is(err, store.ErrMCPServerNameExists), errors.Is(err, store.ErrMCPServerIDExists),
		errors.Is(err, store.ErrMCPDiscoveryStateChanged):
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
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("MCP URL must not contain query parameters")
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
