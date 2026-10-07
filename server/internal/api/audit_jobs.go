package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/store"
)

func auditJobOrigin(r *http.Request) *store.AuditContext {
	if origin := store.AuditContextFrom(r.Context()); origin != nil {
		copy := *origin
		return &copy
	}
	return nil
}

func recordAuditJobResult(d Deps, origin *store.AuditContext, action, jobID, result, reason string, started time.Time, metadata map[string]any) {
	if origin == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["job_id"] = jobID
	raw, _ := json.Marshal(metadata)
	eventType := "system"
	parts := strings.Split(action, ".")
	if len(parts) > 2 && parts[0] == "admin" {
		eventType = parts[1]
	}
	if strings.HasPrefix(action, "auth.") {
		eventType = "authentication"
	}
	e := store.AdminAuditLog{WorkspaceAuditLog: store.WorkspaceAuditLog{Type: eventType, ActorUserID: origin.ActorID, ActorName: origin.ActorName, Action: action, TargetType: "job", TargetID: jobID, Metadata: raw}, ActorRole: origin.ActorRole, Result: result, Source: "background_job", RequestID: origin.RequestID, ClientIP: origin.ClientIP, UserAgent: origin.UserAgent, Method: origin.Method, Route: origin.Route, OccurredAtMS: time.Now().UnixMilli(), DurationMS: time.Since(started).Milliseconds(), Reason: reason, Severity: "info"}
	if result == "failure" {
		e.Severity = "error"
	}
	if err := store.AppendAdminAudit(ctx, d.DB, e); err != nil {
		slog.Error("audit job result failed", "request_id", origin.RequestID, "job_id", jobID, "err", err)
	}
}

func observeAuditResponse(w http.ResponseWriter, status int, body any) {
	if observer, ok := w.(interface{ observeAuditResponse(int, any) }); ok {
		observer.observeAuditResponse(status, body)
	}
}
