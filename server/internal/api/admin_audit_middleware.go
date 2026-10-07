package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"

	"aivory/server/internal/store"
	"github.com/google/uuid"
)

type auditRequestKey struct{}
type auditRequestState struct {
	origin      store.AuditContext
	event       store.AdminAuditLog
	before      *store.AuditSnapshot
	resource    string
	pending     bool
	errorReason string
	metadata    map[string]any
}

// Audited routes never buffer bodies. Chat, SSE and model calls bypass this
// collector entirely, so audit persistence cannot delay generated tokens.
type adminAuditResponseWriter struct {
	http.ResponseWriter
	status int
	state  *auditRequestState
}

func (w *adminAuditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *adminAuditResponseWriter) recordError(err error) {
	for _, known := range []struct {
		err    error
		reason string
	}{
		{errInvalidCredentials, "invalid_credentials"}, {errAuthRequired, "authentication_required"},
		{errSessionExpired, "session_expired"}, {errAccountBlocked, "account_suspended"},
		{errAdminOnly, "admin_required"}, {errCaptcha, "captcha_failed"},
		{errInvalidOrExpiredCode, "verification_failed"}, {errTwofaInvalidCode, "verification_failed"},
		{errTwofaCodeUsed, "verification_replayed"}, {errTwofaInvalidSession, "session_expired"}, {errTwofaSessionExpired, "session_expired"},
	} {
		if errors.Is(err, known.err) {
			w.state.errorReason = known.reason
			break
		}
	}
	recordResponseError(w.ResponseWriter, err)
}
func (w *adminAuditResponseWriter) recordRoute(route string) {
	recordResponseRoute(w.ResponseWriter, route)
}
func (w *adminAuditResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *adminAuditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (w *adminAuditResponseWriter) finalStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func auditState(r *http.Request) *auditRequestState {
	s, _ := r.Context().Value(auditRequestKey{}).(*auditRequestState)
	return s
}

func auditRoute(r *http.Request) string {
	if pattern := routePattern(r); pattern != "" {
		return pattern
	}
	return r.URL.Path
}

func shouldRecordAdminAudit(r *http.Request) bool {
	if !strings.HasPrefix(auditRoute(r), "/api/admin/") {
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return r.Method == http.MethodGet && (strings.HasSuffix(auditRoute(r), "/export") || strings.HasSuffix(auditRoute(r), "/download"))
	}
	return true
}

func securityAuditAction(r *http.Request) string {
	switch auditRoute(r) {
	case "/api/auth/login":
		return "auth.login"
	case "/api/auth/login/2fa":
		return "auth.login_2fa"
	case "/api/auth/passkey/verify":
		return "auth.login_passkey"
	case "/api/auth/oauth/:id/callback", "/api/auth/oauth/handoff":
		return "auth.login_oauth"
	case "/api/auth/logout":
		return "auth.logout"
	case "/api/auth/reset-password":
		return "auth.password_reset"
	case "/api/me/password":
		return "auth.password_change"
	case "/api/me/password/set":
		return "auth.password_set"
	case "/api/me/2fa/enable":
		return "auth.twofa_enable"
	case "/api/me/2fa/disable":
		return "auth.twofa_disable"
	case "/api/me/passkeys/finish":
		return "auth.passkey_create"
	case "/api/me/passkeys/:id":
		if r.Method == http.MethodDelete {
			return "auth.passkey_delete"
		}
	case "/api/me/identities":
		if r.Method == http.MethodDelete {
			return "auth.identity_unlink"
		}
	case "/api/auth/sessions/revoke-others", "/api/auth/sessions/:jti/revoke":
		return "auth.sessions_revoke"
	case "/api/me":
		if r.Method == http.MethodDelete {
			return "auth.account_delete"
		}
	}
	return ""
}

func adminAuditClassification(r *http.Request) (eventType, action, targetType, targetID string) {
	parts := splitPath(auditRoute(r))
	resource := "admin"
	if len(parts) >= 3 {
		resource = parts[2]
	}
	switch resource {
	case "channels":
		eventType = "channels"
	case "models", "model-tags":
		eventType = "models"
	case "users", "user-groups":
		eventType = "users"
	case "payment-channels", "payment-methods", "payment-orders", "credit-packages", "redeem-codes", "redeem-batches":
		eventType = "billing"
	case "settings", "onboarding":
		eventType = "settings"
	case "workspaces":
		eventType = "workspace"
	case "domains":
		eventType = "access"
	case "mcp", "oauth-providers":
		eventType = "integrations"
	case "skills", "prompts", "image-styles", "files", "html-previews", "aippt":
		eventType = "content"
	case "usage", "audit-logs":
		eventType = "logs"
	case "backup", "config", "system-update", "vectors":
		eventType = "system"
	default:
		eventType = "other"
	}
	targetType = strings.ReplaceAll(strings.TrimSuffix(resource, "s"), "-", "_")
	for _, key := range []string{"id", "uid", "domain", "name"} {
		if value := pathParam(r, key); value != "" {
			targetID = value
			break
		}
	}
	operation := "update"
	switch r.Method {
	case http.MethodGet:
		operation = "read"
	case http.MethodPost:
		operation = "create"
	case http.MethodDelete:
		operation = "delete"
	}
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if last != resource && !strings.HasPrefix(last, ":") {
			operation = strings.ReplaceAll(last, "-", "_")
		}
	}
	action = "admin." + eventType + "." + operation
	return
}

func auditText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

func setAuditActor(r *http.Request, user *store.User) {
	if s := auditState(r); s != nil && user != nil {
		s.origin.ActorID = user.ID
		s.origin.ActorName = auditText(user.Name, 160)
		s.origin.ActorRole = user.Role
	}
}

func setAuditLoginTarget(r *http.Request, email string) {
	if s := auditState(r); s != nil {
		sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
		s.metadata["login_identifier_hash"] = hex.EncodeToString(sum[:])
	}
}

func prepareAdminAudit(d Deps, r *http.Request) {
	s := auditState(r)
	if s == nil || r.Method == http.MethodGet {
		return
	}
	parts := splitPath(auditRoute(r))
	if len(parts) < 3 {
		return
	}
	s.resource = parts[2]
	if strings.HasSuffix(auditRoute(r), "/reorder") {
		s.resource += "-order"
	}
	if parts[2] == "models" && strings.HasSuffix(auditRoute(r), "/quotas") {
		s.resource = "model-quotas"
	}
	var err error
	s.before, err = store.LoadAuditSnapshot(r.Context(), d.DB, s.resource, s.event.TargetID)
	if err != nil {
		s.metadata["changes_unavailable"] = true
		slog.Error("audit snapshot failed", "request_id", s.origin.RequestID, "err", err)
	}
}

func auditHTTPRequest(d Deps, w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	admin := shouldRecordAdminAudit(r)
	securityAction := securityAuditAction(r)
	if auditState(r) != nil || (!admin && securityAction == "") {
		next(w, r)
		return
	}
	started := time.Now()
	s := &auditRequestState{origin: store.AuditContext{RequestID: uuid.NewString(), Source: "admin", ClientIP: auditText(clientIP(r), 64), UserAgent: auditText(r.UserAgent(), 512)}, metadata: map[string]any{}}
	s.event.Type, s.event.Action, s.event.TargetType, s.event.TargetID = adminAuditClassification(r)
	s.event.TargetID = auditText(s.event.TargetID, 160)
	if !admin {
		s.origin.Source = "account"
		s.event.Type, s.event.Action, s.event.TargetType, s.event.TargetID = "authentication", securityAction, "user", ""
	}
	s.event.Method = r.Method
	s.event.Route = auditRoute(r)
	s.origin.Method, s.origin.Route = r.Method, s.event.Route
	s.event.OccurredAtMS = started.UnixMilli()
	ctx := context.WithValue(r.Context(), auditRequestKey{}, s)
	r = r.WithContext(store.WithAuditContext(ctx, &s.origin))
	w.Header().Set("X-Request-ID", s.origin.RequestID)
	observed := &adminAuditResponseWriter{ResponseWriter: w, state: s}
	next(observed, r)
	status := observed.finalStatus()
	// Workspace writes already commit their evidence in the transaction.
	if admin && strings.HasPrefix(s.event.Route, "/api/admin/workspaces") && r.Method != http.MethodGet && status >= 200 && status < 300 {
		return
	}
	e := &s.event
	e.HTTPStatus = status
	e.DurationMS = time.Since(started).Milliseconds()
	e.CreatedAt = started.Unix()
	e.ActorUserID, e.ActorName, e.ActorRole = s.origin.ActorID, s.origin.ActorName, s.origin.ActorRole
	e.RequestID, e.Source, e.ClientIP, e.UserAgent = s.origin.RequestID, s.origin.Source, s.origin.ClientIP, s.origin.UserAgent
	e.Result, e.Severity = "success", "info"
	switch {
	case status == 401 || status == 403:
		e.Result, e.Severity, e.Reason = "denied", "warning", "access_denied"
	case status >= 500:
		e.Result, e.Severity, e.Reason = "failure", "error", "internal_error"
	case status == 429:
		e.Result, e.Severity, e.Reason = "denied", "warning", "rate_limited"
	case status >= 400:
		e.Result, e.Severity, e.Reason = "failure", "warning", "invalid_input"
	case status == 202 || s.pending:
		e.Result = "pending"
	}
	if status == 409 {
		e.Reason = "conflict"
	}
	if status >= 400 && s.errorReason != "" {
		e.Reason = s.errorReason
	}
	if location, err := url.Parse(observed.Header().Get("Location")); err == nil && (location.Query().Get("oauth_error") != "" || location.Query().Get("link_error") != "") {
		e.Result, e.Severity, e.Reason = "denied", "warning", "oauth_denied"
	}
	// An unhandled login redirect must never be reported as a completed login.
	if securityAction != "" && strings.Contains(securityAction, "login") && status < 400 && s.origin.ActorID == "" && e.Result == "success" {
		e.Result = "pending"
	}
	if !admin && e.TargetID == "" {
		e.TargetID = s.origin.ActorID
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if s.resource != "" && status >= 200 && status < 300 && status != 202 {
		after, err := store.LoadAuditSnapshot(ctx, d.DB, s.resource, e.TargetID)
		if err == nil && s.metadata["changes_unavailable"] != true {
			e.Changes = store.AuditSnapshotChanges(s.before, after)
			if after != nil {
				e.TargetName = auditText(after.Name, 160)
			} else if s.before != nil {
				e.TargetName = auditText(s.before.Name, 160)
			}
		} else if err != nil {
			s.metadata["changes_unavailable"] = true
			slog.Error("audit snapshot failed", "request_id", e.RequestID, "err", err)
		}
	}
	s.metadata["method"], s.metadata["route"], s.metadata["status"] = e.Method, e.Route, status
	e.Metadata, _ = json.Marshal(s.metadata)
	if err := store.AppendAdminAudit(ctx, d.DB, *e); err != nil {
		slog.Error("audit write failed", "request_id", e.RequestID, "action", e.Action, "err", err)
	}
}

// Extract only allowlisted identifiers from typed responses, never marshal or
// capture entire auth/config/content responses (which can contain secrets).
func auditResponseField(body any, key string) any {
	v := reflect.ValueOf(body)
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
		value := v.MapIndex(reflect.ValueOf(key).Convert(v.Type().Key()))
		if value.IsValid() {
			return value.Interface()
		}
	}
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if strings.Split(field.Tag.Get("json"), ",")[0] == key && v.Field(i).CanInterface() {
				return v.Field(i).Interface()
			}
		}
	}
	return nil
}

func (w *adminAuditResponseWriter) observeAuditResponse(status int, body any) {
	if status < 200 || status >= 300 {
		return
	}
	s := w.state
	if required, ok := auditResponseField(body, "totp_required").(bool); ok && required {
		s.pending = true
	}
	if status == 202 {
		if id, ok := auditResponseField(auditResponseField(body, "running"), "id").(string); ok && id != "" {
			s.metadata["job_id"] = auditText(id, 160)
		}
	}
	if s.event.TargetID != "" || s.event.Method != http.MethodPost || s.origin.Source != "admin" {
		return
	}
	if id, ok := auditResponseField(body, "id").(string); ok {
		s.event.TargetID = auditText(id, 160)
		return
	}
	for _, key := range []string{"user", "model", "channel", "workspace", "package", "method", "order", "provider", "prompt", "skill"} {
		if id, ok := auditResponseField(auditResponseField(body, key), "id").(string); ok {
			s.event.TargetID = auditText(id, 160)
			return
		}
	}
}
