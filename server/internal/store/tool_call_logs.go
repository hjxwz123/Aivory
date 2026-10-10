package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"aivory/server/internal/tooldiagnostics"
)

type ToolCallLog struct {
	ID              string                    `json:"id"`
	CallID          string                    `json:"call_id"`
	ToolName        string                    `json:"tool_name"`
	ToolKind        string                    `json:"tool_kind"`
	ServerID        string                    `json:"server_id"`
	ServerName      string                    `json:"server_name"`
	RemoteName      string                    `json:"remote_name"`
	UserID          string                    `json:"user_id"`
	UserName        string                    `json:"user_name"`
	ConversationID  string                    `json:"conversation_id"`
	MessageID       string                    `json:"message_id"`
	WorkspaceID     string                    `json:"workspace_id"`
	ModelID         string                    `json:"model_id"`
	ModelLabel      string                    `json:"model_label"`
	Summary         string                    `json:"summary"`
	Status          string                    `json:"status"`
	DurationMS      int64                     `json:"duration_ms"`
	CreatedAtMS     int64                     `json:"created_at_ms"`
	Error           string                    `json:"error,omitempty"`
	Input           string                    `json:"input,omitempty"`
	Output          string                    `json:"output,omitempty"`
	InputTruncated  bool                      `json:"input_truncated,omitempty"`
	OutputTruncated bool                      `json:"output_truncated,omitempty"`
	BodiesRecorded  bool                      `json:"bodies_recorded"`
	Requests        []tooldiagnostics.Request `json:"requests,omitempty"`
	Issues          []tooldiagnostics.Issue   `json:"issues,omitempty"`
}

type ToolCallLogFilter struct {
	Search, Kind, Status, UserID, ToolName string
	From, Until                            int64
}

func InsertToolCallLog(ctx context.Context, db *sql.DB, log ToolCallLog) error {
	requests, err := json.Marshal(log.Requests)
	if err != nil {
		return err
	}
	issues, err := json.Marshal(log.Issues)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO tool_call_logs
		(id,call_id,tool_name,tool_kind,server_id,server_name,remote_name,user_id,conversation_id,message_id,
		workspace_id,model_id,summary,status,duration_ms,created_at_ms,error,input,output,input_truncated,output_truncated,bodies_recorded,requests,issues)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		log.ID, log.CallID, log.ToolName, log.ToolKind, log.ServerID, log.ServerName, log.RemoteName, log.UserID,
		log.ConversationID, log.MessageID, log.WorkspaceID, log.ModelID, log.Summary, log.Status, log.DurationMS,
		log.CreatedAtMS, log.Error, log.Input, log.Output, boolInt(log.InputTruncated), boolInt(log.OutputTruncated), boolInt(log.BodiesRecorded), string(requests), string(issues))
	return err
}

func toolCallLogPredicate(filter ToolCallLogFilter) (string, []any) {
	clauses, args := []string{"1=1"}, []any{}
	if search := strings.TrimSpace(filter.Search); search != "" {
		parts := []string{}
		for _, field := range []string{"l.id", "l.call_id", "l.tool_name", "l.remote_name", "l.server_name", "l.summary", "l.user_id", "l.conversation_id", "l.model_id", "COALESCE(u.name,'')", "COALESCE(m.label,'')"} {
			parts = append(parts, "LOWER("+field+") LIKE ?")
			args = append(args, "%"+strings.ToLower(search)+"%")
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	for _, f := range []struct{ key, value string }{{"tool_kind", filter.Kind}, {"status", filter.Status}, {"user_id", filter.UserID}, {"tool_name", filter.ToolName}} {
		if f.value != "" {
			clauses = append(clauses, "l."+f.key+"=?")
			args = append(args, f.value)
		}
	}
	if filter.From > 0 {
		clauses = append(clauses, "l.created_at_ms>=?")
		args = append(args, filter.From)
	}
	if filter.Until > 0 {
		clauses = append(clauses, "l.created_at_ms<=?")
		args = append(args, filter.Until)
	}
	return strings.Join(clauses, " AND "), args
}

const toolLogJoins = ` FROM tool_call_logs l LEFT JOIN users u ON u.id=l.user_id LEFT JOIN models m ON m.id=l.model_id `
const toolLogSummaryColumns = `l.id,l.call_id,l.tool_name,l.tool_kind,l.server_id,l.server_name,l.remote_name,
	l.user_id,COALESCE(u.name,''),l.conversation_id,l.message_id,l.workspace_id,l.model_id,COALESCE(m.label,''),
	l.summary,l.status,l.duration_ms,l.created_at_ms,l.bodies_recorded`

func toolLogScanTargets(log *ToolCallLog) []any {
	return []any{&log.ID, &log.CallID, &log.ToolName, &log.ToolKind, &log.ServerID, &log.ServerName, &log.RemoteName,
		&log.UserID, &log.UserName, &log.ConversationID, &log.MessageID, &log.WorkspaceID, &log.ModelID, &log.ModelLabel,
		&log.Summary, &log.Status, &log.DurationMS, &log.CreatedAtMS, &log.BodiesRecorded}
}

func ListToolCallLogs(ctx context.Context, db *sql.DB, filter ToolCallLogFilter, limit, offset int) ([]ToolCallLog, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args := toolCallLogPredicate(filter)
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)`+toolLogJoins+`WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+toolLogSummaryColumns+toolLogJoins+`WHERE `+where+` ORDER BY l.created_at_ms DESC,l.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]ToolCallLog, 0, limit)
	for rows.Next() {
		var log ToolCallLog
		if err := rows.Scan(toolLogScanTargets(&log)...); err != nil {
			return nil, 0, err
		}
		out = append(out, log)
	}
	return out, total, rows.Err()
}

func GetToolCallLog(ctx context.Context, db *sql.DB, id string) (*ToolCallLog, error) {
	var log ToolCallLog
	var requests, issues string
	targets := append(toolLogScanTargets(&log), &log.Error, &log.Input, &log.Output, &log.InputTruncated, &log.OutputTruncated, &requests, &issues)
	err := db.QueryRowContext(ctx, `SELECT `+toolLogSummaryColumns+`,l.error,l.input,l.output,l.input_truncated,l.output_truncated,l.requests,l.issues`+toolLogJoins+`WHERE l.id=?`, id).Scan(targets...)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(requests), &log.Requests); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(issues), &log.Issues); err != nil {
		return nil, err
	}
	return &log, nil
}

func DeleteToolCallLog(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM tool_call_logs WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func DeleteFilteredToolCallLogs(ctx context.Context, db *sql.DB, filter ToolCallLogFilter) (int64, error) {
	where, args := toolCallLogPredicate(filter)
	result, err := db.ExecContext(ctx, `DELETE FROM tool_call_logs WHERE id IN (SELECT l.id`+toolLogJoins+`WHERE `+where+`)`, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
