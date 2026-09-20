package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

var errDomainSpaceLocked = errors.New("personal space is locked; only your assigned workspace is available")

// Server-authoritative boundary. Never trust a workspace header to authorize an
// object: resolve its actual scope from the database, then let its normal RBAC
// handler check membership/visibility. Unknown routes fail closed for locked users.
func enforceDomainAccess(d Deps, w http.ResponseWriter, r *http.Request, userID string) bool {
	access, err := store.GetDomainAccess(r.Context(), d.DB, userID)
	if err != nil {
		writeError(w, 500, err)
		return true
	}
	if access != nil && access.SubscriptionPurchaseDisabled && r.Method == http.MethodPost {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path == "/api/payments/checkout" || (strings.HasPrefix(path, "/api/payments/orders/") && strings.HasSuffix(path, "/resume")) {
			writeError(w, http.StatusForbidden, errors.New("subscription_purchase_disabled"))
			return true
		}
	}
	if access == nil || !access.Locked {
		return false
	}
	if domainRequestAllowed(d, r, access.WorkspaceID) {
		return false
	}
	writeError(w, http.StatusForbidden, errDomainSpaceLocked)
	return true
}
func domainRequestAllowed(d Deps, r *http.Request, workspaceID string) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	q := r.URL.Query()
	// An explicit foreign scope is always rejected, even on a resource route.
	if scope := q.Get("workspace_id"); scope != "" && scope != workspaceID {
		return false
	}
	switch path {
	case "/api/private-chat":
		// The strict private-chat decoder validates body scope and inherits the
		// locked workspace for legacy clients; never copy image payloads here.
		return r.Method == http.MethodPost
	case "/api/me", "/api/events", "/api/announcement", "/api/me/settings", "/api/me/avatar",
		"/api/me/password", "/api/me/password/set", "/api/me/usage", "/api/me/credits",
		"/api/me/credit-adjustments/claim", "/api/me/upload-policy", "/api/user-feedback",
		"/api/user-groups", "/api/payment-methods", "/api/model-tags", "/api/audio/capabilities",
		"/api/audio/transcriptions", "/api/audio/stream", "/api/me/redeem":
		return true
	case "/api/workspaces":
		return r.Method == http.MethodGet
	}
	if path == "/api/me/domain-data" || strings.HasPrefix(path, "/api/me/domain-data/") {
		return true
	}
	for _, prefix := range []string{"/api/auth/sessions", "/api/me/2fa/", "/api/me/passkeys", "/api/me/identities", "/api/payments/"} {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/workspaces/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/workspaces/"), "/")
		return parts[0] == workspaceID && len(parts) > 1 && parts[1] != "leave"
	}
	matches := func(query string, id string) bool {
		var scope string
		return id != "" && d.DB.QueryRowContext(r.Context(), query, id).Scan(&scope) == nil && scope == workspaceID
	}
	for prefix, table := range map[string]string{"/api/conversations/": "conversations", "/api/projects/": "projects", "/api/kbs/": "knowledge_bases"} {
		if strings.HasPrefix(path, prefix) {
			id := strings.Split(strings.TrimPrefix(path, prefix), "/")[0]
			return matches(`SELECT COALESCE(workspace_id,'') FROM `+table+` WHERE id=?`, id)
		}
	}
	if strings.HasPrefix(path, "/api/files/") {
		return matches(`SELECT COALESCE(c.workspace_id,'') FROM files f JOIN conversations c ON c.id=f.conversation_id WHERE f.id=?`, pathParam(r, "id"))
	}
	if strings.HasPrefix(path, "/api/artifacts/") {
		return matches(`SELECT COALESCE(c.workspace_id,'') FROM artifacts a JOIN messages m ON m.id=a.message_id JOIN conversations c ON c.id=m.conversation_id WHERE a.id=?`, pathParam(r, "id"))
	}
	if strings.HasPrefix(path, "/api/documents/") {
		return matches(`SELECT COALESCE(c.workspace_id,k.workspace_id,'') FROM documents doc LEFT JOIN conversations c ON c.id=doc.conversation_id LEFT JOIN knowledge_bases k ON k.id=doc.kb_id WHERE doc.id=?`, pathParam(r, "id"))
	}
	if path == "/api/files" {
		return matches(`SELECT COALESCE(workspace_id,'') FROM conversations WHERE id=?`, q.Get("conversation_id"))
	}
	// Collection reads must explicitly select the assigned workspace. Body-scoped
	// creates are inspected without consuming the payload needed by the handler.
	scoped := path == "/api/conversations" || path == "/api/projects" || path == "/api/kbs" || path == "/api/search" || path == "/api/models" || path == "/api/image-models" || path == "/api/tools" || path == "/api/image/styles" || path == "/api/library/catalog"
	for _, prefix := range []string{"/api/me/skills", "/api/me/prompts", "/api/me/mcps"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			scoped = true
		}
	}
	if !scoped {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
		// Bulk conversation delete is personal-only in the existing API.
		if path == "/api/conversations" && r.Method != http.MethodGet {
			return false
		}
		return q.Get("workspace_id") == workspaceID
	}
	// MCP test/sync identify their scope in the query rather than the body.
	if (strings.HasSuffix(path, "/test") || strings.HasSuffix(path, "/sync")) && q.Get("workspace_id") == workspaceID {
		return true
	}
	if r.Body == nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, jsonRequestBodySizeCap+1))
	r.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return false
	}
	var payload struct {
		WorkspaceID string `json:"workspace_id"`
	}
	return json.Unmarshal(data, &payload) == nil && payload.WorkspaceID == workspaceID
}
