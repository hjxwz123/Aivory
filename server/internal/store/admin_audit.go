package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AuditContext struct {
	ActorID, ActorName, ActorRole          string
	RequestID, Source, ClientIP, UserAgent string
	Method, Route                          string
}

type auditContextKey struct{}

func WithAuditContext(ctx context.Context, audit *AuditContext) context.Context {
	return context.WithValue(ctx, auditContextKey{}, audit)
}

func AuditContextFrom(ctx context.Context) *AuditContext {
	audit, _ := ctx.Value(auditContextKey{}).(*AuditContext)
	return audit
}

type AuditChange struct {
	Before   any  `json:"before,omitempty"`
	After    any  `json:"after,omitempty"`
	Redacted bool `json:"redacted,omitempty"`
}

type AdminAuditLog struct {
	WorkspaceAuditLog
	ActorRole    string                 `json:"actor_role"`
	TargetName   string                 `json:"target_name"`
	Result       string                 `json:"result"`
	Severity     string                 `json:"severity"`
	Source       string                 `json:"source"`
	ClientIP     string                 `json:"client_ip"`
	UserAgent    string                 `json:"user_agent"`
	RequestID    string                 `json:"request_id"`
	OccurredAtMS int64                  `json:"occurred_at_ms"`
	DurationMS   int64                  `json:"duration_ms"`
	HTTPStatus   int                    `json:"http_status"`
	Method       string                 `json:"method"`
	Route        string                 `json:"route"`
	Reason       string                 `json:"reason"`
	Changes      map[string]AuditChange `json:"changes"`
}

// AppendAdminAudit stores bounded, structured evidence. Actor snapshots and
// identifiers deliberately have no foreign keys so deletion cannot erase it.
func AppendAdminAudit(ctx context.Context, db *sql.DB, event AdminAuditLog) error {
	if event.Type == "" || event.Action == "" {
		return fmt.Errorf("admin audit: type and action are required")
	}
	if event.ID == "" {
		event.ID = genID("adm_aud")
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = time.Now().Unix()
	}
	if event.OccurredAtMS == 0 {
		event.OccurredAtMS = event.CreatedAt * 1000
	}
	if event.Result == "" {
		event.Result = "success"
	}
	if event.Severity == "" {
		event.Severity = "info"
	}
	if len(event.Metadata) == 0 {
		event.Metadata = json.RawMessage(`{}`)
	}
	changes, err := json.Marshal(event.Changes)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO admin_audit_logs(
		id,actor_user_id,actor_name,actor_role,event_type,action,target_type,target_id,target_name,
		result,severity,source,client_ip,user_agent,request_id,occurred_at_ms,duration_ms,http_status,method,route,reason,changes,metadata,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.ID, event.ActorUserID, event.ActorName, event.ActorRole, event.Type, event.Action, event.TargetType, event.TargetID, event.TargetName,
		event.Result, event.Severity, event.Source, event.ClientIP, event.UserAgent, event.RequestID, event.OccurredAtMS, event.DurationMS,
		event.HTTPStatus, event.Method, event.Route, event.Reason, string(changes), string(event.Metadata), event.CreatedAt)
	return err
}

// RecordAdminAudit writes a successful administrator operation. Callers pass
// only route-derived identifiers and deliberately small metadata; request
// bodies, credentials, tokens and provider secrets must never be included.
func RecordAdminAudit(ctx context.Context, db *sql.DB, actorID, eventType, action, targetType, targetID string, metadata map[string]any) error {
	actorID = strings.TrimSpace(actorID)
	raw := []byte("{}")
	if metadata != nil {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		raw = encoded
	}
	event := AdminAuditLog{WorkspaceAuditLog: WorkspaceAuditLog{
		ActorUserID: actorID, Type: eventType, Action: action, TargetType: targetType, TargetID: targetID, Metadata: raw,
	}}
	if origin := AuditContextFrom(ctx); origin != nil {
		event.ActorUserID, event.ActorName, event.ActorRole = origin.ActorID, origin.ActorName, origin.ActorRole
		event.RequestID, event.Source, event.ClientIP, event.UserAgent = origin.RequestID, origin.Source, origin.ClientIP, origin.UserAgent
	} else {
		_ = db.QueryRowContext(ctx, `SELECT name,role FROM users WHERE id=?`, actorID).Scan(&event.ActorName, &event.ActorRole)
	}
	return AppendAdminAudit(ctx, db, event)
}
