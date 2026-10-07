package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type AdminAuditFilter struct {
	Search, Type, Result, Actor, Target, Action string
	// Inclusive UTC boundaries in milliseconds.
	From, Until int64
}

// ListAdminAuditLogs returns workspace and administrator audit events in one
// newest-first stream. Workspace records are assigned the "workspace" type;
// administrator records use the route-derived event_type.
func ListAdminAuditLogs(ctx context.Context, db *sql.DB, search, eventType string, limit, offset int) ([]AdminAuditLog, int, error) {
	return ListFilteredAdminAuditLogs(ctx, db, AdminAuditFilter{Search: search, Type: eventType}, limit, offset)
}

func adminAuditFilterQuery(filter AdminAuditFilter) (string, string, []any) {
	clauses, args := []string{"1=1"}, []any{}
	if search := strings.TrimSpace(filter.Search); search != "" {
		fields := []string{"id", "workspace_id", "workspace_name", "actor_user_id", "actor_name", "action", "event_type", "target_type", "target_id", "target_name", "request_id"}
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			parts = append(parts, "LOWER(a."+field+") LIKE ?")
			args = append(args, "%"+strings.ToLower(search)+"%")
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	for _, entry := range []struct{ column, value string }{{"event_type", filter.Type}, {"result", filter.Result}, {"actor_user_id", filter.Actor}, {"target_id", filter.Target}, {"action", filter.Action}} {
		if value := strings.TrimSpace(entry.value); value != "" {
			clauses = append(clauses, "a."+entry.column+"=?")
			args = append(args, value)
		}
	}
	if filter.From > 0 {
		clauses = append(clauses, "a.occurred_at_ms>=?")
		args = append(args, filter.From)
	}
	if filter.Until > 0 {
		clauses = append(clauses, "a.occurred_at_ms<=?")
		args = append(args, filter.Until)
	}
	where := strings.Join(clauses, " AND ")
	common := `a.id,a.actor_user_id,COALESCE(NULLIF(a.actor_name,''),u.name,'') AS actor_name,a.actor_role,a.action,
		a.target_type,a.target_id,a.target_name,a.metadata,a.changes,a.created_at,a.result,a.severity,a.source,
		a.client_ip,a.user_agent,a.request_id,CASE WHEN a.occurred_at_ms>0 THEN a.occurred_at_ms ELSE a.created_at*1000 END AS occurred_at_ms,
		a.duration_ms,a.http_status,a.method,a.route,a.reason`
	allLogs := `WITH all_logs AS (
		SELECT ` + common + `,a.workspace_id,COALESCE(w.name,'') AS workspace_name,'workspace' AS event_type,'workspace_audit_logs' AS audit_table
		FROM workspace_audit_logs a LEFT JOIN workspaces w ON w.id=a.workspace_id LEFT JOIN users u ON u.id=a.actor_user_id
		UNION ALL SELECT ` + common + `,'' AS workspace_id,'' AS workspace_name,a.event_type,'admin_audit_logs' AS audit_table
		FROM admin_audit_logs a LEFT JOIN users u ON u.id=a.actor_user_id)`
	return allLogs, where, args
}

func ListFilteredAdminAuditLogs(ctx context.Context, db *sql.DB, filter AdminAuditFilter, limit, offset int) ([]AdminAuditLog, int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	allLogs, where, args := adminAuditFilterQuery(filter)

	var total int
	if err := db.QueryRowContext(ctx, allLogs+` SELECT COUNT(*) FROM all_logs a WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := db.QueryContext(ctx, allLogs+` SELECT a.id, a.workspace_id, a.workspace_name,
		a.actor_user_id, a.actor_name, a.action, a.event_type, a.target_type, a.target_id,
		a.metadata, a.created_at,a.actor_role,a.target_name,a.result,a.severity,a.source,a.client_ip,a.user_agent,
		a.request_id,a.occurred_at_ms,a.duration_ms,a.http_status,a.method,a.route,a.reason,a.changes
		FROM all_logs a WHERE `+where+`
		ORDER BY a.occurred_at_ms DESC, a.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]AdminAuditLog, 0, limit)
	for rows.Next() {
		var log AdminAuditLog
		var metadata, changes string
		if err := rows.Scan(&log.ID, &log.WorkspaceID, &log.WorkspaceName,
			&log.ActorUserID, &log.ActorName, &log.Action, &log.Type,
			&log.TargetType, &log.TargetID, &metadata, &log.CreatedAt, &log.ActorRole, &log.TargetName, &log.Result, &log.Severity,
			&log.Source, &log.ClientIP, &log.UserAgent, &log.RequestID, &log.OccurredAtMS, &log.DurationMS, &log.HTTPStatus,
			&log.Method, &log.Route, &log.Reason, &changes); err != nil {
			return nil, 0, err
		}
		log.Metadata = json.RawMessage(metadata)
		if err := json.Unmarshal([]byte(changes), &log.Changes); err != nil {
			return nil, 0, err
		}
		out = append(out, log)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func DeleteAdminAuditLog(ctx context.Context, db *sql.DB, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrNotFound
	}
	deleted, err := deleteAdminAuditLogs(ctx, db, AdminAuditFilter{}, id)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return nil
}

func DeleteFilteredAdminAuditLogs(ctx context.Context, db *sql.DB, filter AdminAuditFilter) (int64, error) {
	return deleteAdminAuditLogs(ctx, db, filter, "")
}

// Both audit sources share the list's filter predicate and are removed in one
// transaction. A failure cannot leave only one source partially cleared.
func deleteAdminAuditLogs(ctx context.Context, db *sql.DB, filter AdminAuditFilter, id string) (int64, error) {
	allLogs, where, filterArgs := adminAuditFilterQuery(filter)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var deleted int64
	for _, table := range []string{"workspace_audit_logs", "admin_audit_logs"} {
		query, args := `DELETE FROM `+table+` WHERE id=?`, []any{id}
		if id == "" {
			query = allLogs + ` DELETE FROM ` + table + ` WHERE id IN (SELECT a.id FROM all_logs a WHERE ` + where + ` AND a.audit_table=?)`
			args = append(append([]any{}, filterArgs...), table)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// ListAdminWorkspaceAuditLogs is kept as a compatibility wrapper for callers
// and tests that predate the combined administrator audit stream.
func ListAdminWorkspaceAuditLogs(ctx context.Context, db *sql.DB, search string, limit, offset int) ([]AdminAuditLog, int, error) {
	return ListAdminAuditLogs(ctx, db, search, "workspace", limit, offset)
}
