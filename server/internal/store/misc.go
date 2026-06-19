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

// CreateFile inserts a file metadata row.
func CreateFile(ctx context.Context, db *sql.DB, f File) (*File, error) {
	if f.ID == "" {
		f.ID = genID("f")
	}
	var conv any
	if f.ConversationID != "" {
		conv = f.ConversationID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO files(id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, kind, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.UserID, conv, f.Filename, f.MimeType, f.SizeBytes, f.StoragePath, f.Kind, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return GetFile(ctx, db, f.ID, f.UserID)
}

// ListFilesByConversation returns a conversation's uploaded files (oldest
// first) — used to stage data files into the sandbox /workspace/uploads (§4.5).
func ListFilesByConversation(ctx context.Context, db *sql.DB, convID, userID string) ([]File, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, kind, created_at
		 FROM files WHERE conversation_id=? AND user_id=? ORDER BY created_at ASC`, convID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		var conv sql.NullString
		if err := rows.Scan(&f.ID, &f.UserID, &conv, &f.Filename, &f.MimeType, &f.SizeBytes, &f.StoragePath, &f.Kind, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.ConversationID = conv.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// DetachFileFromConversation clears a file's conversation link (ownership
// checked) so it is no longer staged into the sandbox or counted among the
// conversation's referenced files (§ conversation files drawer). The file row
// itself survives, so any historical message that uploaded it can still be
// downloaded.
func DetachFileFromConversation(ctx context.Context, db *sql.DB, fileID, convID, userID string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE files SET conversation_id=NULL WHERE id=? AND conversation_id=? AND user_id=?`,
		fileID, convID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetFile returns one row with ownership check.
// GetFile returns a file scoped to userID. An empty userID means "any owner" —
// reserved for admin triage (an admin viewing another user's conversation).
func GetFile(ctx context.Context, db *sql.DB, id, userID string) (*File, error) {
	var f File
	var conv sql.NullString
	q := `SELECT id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, kind, created_at FROM files WHERE id=?`
	args := []any{id}
	if userID != "" {
		q += ` AND user_id=?`
		args = append(args, userID)
	}
	err := db.QueryRowContext(ctx, q, args...).
		Scan(&f.ID, &f.UserID, &conv, &f.Filename, &f.MimeType, &f.SizeBytes, &f.StoragePath, &f.Kind, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.ConversationID = conv.String
	return &f, nil
}

// ListMemories returns the user's memories filtered by status (or all).
func ListMemories(ctx context.Context, db *sql.DB, userID, statusFilter string) ([]Memory, error) {
	q := `SELECT id, user_id, memory_text, memory_type, slot, value, status, confidence, source_message_ids, supersedes, superseded_by, affected_domains, reason, COALESCE(valid_from, 0), COALESCE(valid_until, 0), created_at, updated_at FROM memories WHERE user_id=?`
	args := []any{userID}
	if statusFilter != "" {
		q += " AND status=?"
		args = append(args, statusFilter)
	}
	q += " ORDER BY updated_at DESC"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanMemory(s scanner) (Memory, error) {
	var m Memory
	var sourceIDs, supers, supBy, doms string
	if err := s.Scan(&m.ID, &m.UserID, &m.MemoryText, &m.MemoryType, &m.Slot, &m.Value, &m.Status, &m.Confidence, &sourceIDs, &supers, &supBy, &doms, &m.Reason, &m.ValidFrom, &m.ValidUntil, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(orDefault(sourceIDs, "[]")), &m.SourceMessageIDs)
	_ = json.Unmarshal([]byte(orDefault(supers, "[]")), &m.Supersedes)
	_ = json.Unmarshal([]byte(orDefault(supBy, "[]")), &m.SupersededBy)
	_ = json.Unmarshal([]byte(orDefault(doms, "[]")), &m.AffectedDomains)
	return m, nil
}

// CreateMemory inserts a new memory row.
func CreateMemory(ctx context.Context, db *sql.DB, m Memory) (*Memory, error) {
	if m.ID == "" {
		m.ID = genID("mem")
	}
	if m.Status == "" {
		m.Status = "ACTIVE"
	}
	if m.Confidence == 0 {
		m.Confidence = 0.8
	}
	srcIDs, _ := json.Marshal(m.SourceMessageIDs)
	supers, _ := json.Marshal(m.Supersedes)
	supBy, _ := json.Marshal(m.SupersededBy)
	doms, _ := json.Marshal(m.AffectedDomains)
	now := time.Now().Unix()
	var validFrom, validUntil any
	if m.ValidFrom > 0 {
		validFrom = m.ValidFrom
	}
	if m.ValidUntil > 0 {
		validUntil = m.ValidUntil
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO memories(id, user_id, memory_text, memory_type, slot, value, status, confidence, source_message_ids, supersedes, superseded_by, affected_domains, reason, valid_from, valid_until, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.UserID, m.MemoryText, m.MemoryType, m.Slot, m.Value, m.Status, m.Confidence, string(srcIDs), string(supers), string(supBy), string(doms), m.Reason, validFrom, validUntil, now, now)
	if err != nil {
		return nil, err
	}
	return GetMemory(ctx, db, m.ID, m.UserID)
}

// GetMemory returns one row with ownership check.
func GetMemory(ctx context.Context, db *sql.DB, id, userID string) (*Memory, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, memory_text, memory_type, slot, value, status, confidence, source_message_ids, supersedes, superseded_by, affected_domains, reason, COALESCE(valid_from, 0), COALESCE(valid_until, 0), created_at, updated_at FROM memories WHERE id=? AND user_id=?`, id, userID)
	m, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMemoryText updates memory_text/status/reason — used by the user-facing
// memory edit page.
func UpdateMemoryText(ctx context.Context, db *sql.DB, id, userID, text, status, reason string) (*Memory, error) {
	if text == "" {
		return nil, errors.New("text required")
	}
	if status == "" {
		status = "ACTIVE"
	}
	res, err := db.ExecContext(ctx,
		`UPDATE memories SET memory_text=?, status=?, reason=?, updated_at=? WHERE id=? AND user_id=?`,
		text, status, reason, time.Now().Unix(), id, userID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return GetMemory(ctx, db, id, userID)
}

// DeleteMemory removes the row.
func DeleteMemory(ctx context.Context, db *sql.DB, id, userID string) error {
	res, err := db.ExecContext(ctx, "DELETE FROM memories WHERE id=? AND user_id=?", id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMemoriesActive returns the memories the orchestrator should inject into
// the system prompt — ACTIVE plus QUERY_DEPENDENT per design.md §4.16.
func ListMemoriesActive(ctx context.Context, db *sql.DB, userID string) ([]Memory, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, memory_text, memory_type, slot, value, status, confidence, source_message_ids, supersedes, superseded_by, affected_domains, reason, COALESCE(valid_from, 0), COALESCE(valid_until, 0), created_at, updated_at FROM memories WHERE user_id=? AND status IN ('ACTIVE','QUERY_DEPENDENT') ORDER BY updated_at DESC LIMIT 20`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LogUsage writes a single usage row. Best-effort — callers ignore errors.
func LogUsage(ctx context.Context, db *sql.DB, u UsageLog) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO usage_logs(user_id, conversation_id, message_id, model_id, purpose, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, images_count, cost, currency, credits, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.UserID, nullable(u.ConversationID), nullable(u.MessageID), u.ModelID, u.Purpose,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ImagesCount,
		u.Cost, u.Currency, u.Credits, time.Now().Unix())
	return err
}

// CreditsUsedInWindow sums the timed credits a user has spent since the given
// window start — used to seed the cold credit-window cache and compute remaining
// timed credits (§ credits). usage_logs.credits holds ONLY the timed portion of a
// charge; permanent-credit debits live on the user row, so this is purely the
// timed-window consumption.
func CreditsUsedInWindow(ctx context.Context, db *sql.DB, userID string, sinceUnix int64) (float64, error) {
	var c sql.NullFloat64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(credits),0) FROM usage_logs WHERE user_id=? AND created_at>=?`,
		userID, sinceUnix).Scan(&c)
	return c.Float64, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SumUsageByUser returns total cost and message count over the past N days.
func SumUsageByUser(ctx context.Context, db *sql.DB, userID string, days int) (float64, int, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	var cost float64
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost),0), COUNT(*) FROM usage_logs WHERE user_id=? AND created_at>=?`, userID, since,
	).Scan(&cost, &count)
	return cost, count, err
}

// UsageRow is a single row of the report.
type UsageRow struct {
	UserID            string  `json:"user_id"`
	UserEmail         string  `json:"user_email"`
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle string  `json:"conversation_title"`
	ModelID           string  `json:"model_id"`
	Purpose           string  `json:"purpose"`
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	Calls             int     `json:"calls"`
	Cost              float64 `json:"cost"`
	Currency          string  `json:"currency"`
}

// AdminUsageReport returns an aggregated report across users/conversations/
// models/purpose over the past `days` days. Grouping by conversation lets the
// admin jump from a usage row to the exact conversation that produced it.
func AdminUsageReport(ctx context.Context, db *sql.DB, days int) ([]UsageRow, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	rows, err := db.QueryContext(ctx,
		`SELECT u.user_id, COALESCE(usr.email, ''), COALESCE(u.conversation_id, ''), COALESCE(c.title, ''), u.model_id, u.purpose,
		        SUM(u.input_tokens), SUM(u.output_tokens), COUNT(*), SUM(u.cost), MAX(u.currency)
		 FROM usage_logs u
		 LEFT JOIN users usr ON usr.id = u.user_id
		 LEFT JOIN conversations c ON c.id = u.conversation_id
		 WHERE u.created_at >= ?
		 GROUP BY u.user_id, usr.email, u.conversation_id, c.title, u.model_id, u.purpose
		 ORDER BY SUM(u.cost) DESC
		 LIMIT 10000`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.UserID, &r.UserEmail, &r.ConversationID, &r.ConversationTitle, &r.ModelID, &r.Purpose, &r.InputTokens, &r.OutputTokens, &r.Calls, &r.Cost, &r.Currency); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageBucket is one time-bucket row of the usage trend (§8.3 admin charts).
type UsageBucket struct {
	BucketStart  int64   `json:"bucket_start"` // unix seconds
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Calls        int     `json:"calls"`
	Cost         float64 `json:"cost"`
}

// AdminUsageTrend returns hourly buckets when `days<=2`, otherwise daily, so a
// "last 24h" view shows 24 points and a "last 30d" view shows 30. SQLite has
// no date_trunc, so we floor to the bucket width with integer math.
func AdminUsageTrend(ctx context.Context, db *sql.DB, days int) ([]UsageBucket, error) {
	if days <= 0 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	bucket := int64(86400) // daily
	if days <= 2 {
		bucket = 3600 // hourly
	}
	rows, err := db.QueryContext(ctx,
		`SELECT (created_at / ?) * ? AS b,
		        SUM(input_tokens), SUM(output_tokens), COUNT(*), SUM(cost)
		 FROM usage_logs WHERE created_at >= ?
		 GROUP BY b ORDER BY b ASC`, bucket, bucket, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageBucket{}
	for rows.Next() {
		var b UsageBucket
		if err := rows.Scan(&b.BucketStart, &b.InputTokens, &b.OutputTokens, &b.Calls, &b.Cost); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UsageBucketWidth mirrors AdminUsageTrend's choice so trend + per-series points
// share one time axis (hourly for ≤2-day windows, otherwise daily).
func UsageBucketWidth(days int) int64 {
	if days <= 2 {
		return 3600
	}
	return 86400
}

// UsageTotals is the headline aggregate for the analytics dashboard (§ admin
// analytics).
type UsageTotals struct {
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Cost         float64 `json:"cost"`
	Users        int     `json:"users"` // distinct active users in the window
}

// AdminUsageTotals returns the period totals plus the distinct active-user count.
func AdminUsageTotals(ctx context.Context, db *sql.DB, days int) (UsageTotals, error) {
	if days <= 0 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	var t UsageTotals
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cost),0), COUNT(DISTINCT user_id)
		 FROM usage_logs WHERE created_at >= ?`, since).
		Scan(&t.Calls, &t.InputTokens, &t.OutputTokens, &t.Cost, &t.Users)
	return t, err
}

// UsageBreakdownRow is one row of a by-model or by-user breakdown. Label holds
// the user email for the by-user breakdown; model labels are resolved on the
// frontend from the model list.
type UsageBreakdownRow struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Calls        int     `json:"calls"`
	Cost         float64 `json:"cost"`
}

// AdminUsageBreakdown returns the top-`limit` keys for the dimension (groupCol
// must be "model_id" or "user_id"), ordered by cost then call volume.
func AdminUsageBreakdown(ctx context.Context, db *sql.DB, days int, groupCol string, limit int) ([]UsageBreakdownRow, error) {
	if groupCol != "model_id" && groupCol != "user_id" {
		return nil, fmt.Errorf("AdminUsageBreakdown: invalid group column %q", groupCol)
	}
	if limit <= 0 {
		limit = 8
	}
	if days <= 0 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	labelExpr, join, groupBy := "''", "", "u."+groupCol
	if groupCol == "user_id" {
		labelExpr = "COALESCE(usr.email,'')"
		join = "LEFT JOIN users usr ON usr.id = u.user_id"
		groupBy = "u.user_id, usr.email"
	}
	q := fmt.Sprintf(
		`SELECT u.%s, %s, SUM(u.input_tokens), SUM(u.output_tokens), COUNT(*), SUM(u.cost)
		 FROM usage_logs u %s WHERE u.created_at >= ?
		 GROUP BY %s ORDER BY SUM(u.cost) DESC, COUNT(*) DESC LIMIT ?`,
		groupCol, labelExpr, join, groupBy)
	rows, err := db.QueryContext(ctx, q, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageBreakdownRow{}
	for rows.Next() {
		var r UsageBreakdownRow
		if err := rows.Scan(&r.Key, &r.Label, &r.InputTokens, &r.OutputTokens, &r.Calls, &r.Cost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageSeriesPoint is one (bucket, key) cell of a per-dimension time series.
type UsageSeriesPoint struct {
	BucketStart  int64   `json:"bucket_start"`
	Key          string  `json:"key"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Calls        int     `json:"calls"`
	Cost         float64 `json:"cost"`
}

// AdminUsageSeries returns the time series for the given keys of a dimension
// (groupCol must be "model_id" or "user_id"), sharing AdminUsageTrend's bucket
// width so the points line up with the overall trend axis. keys is typically
// the output of AdminUsageBreakdown so the payload stays bounded.
func AdminUsageSeries(ctx context.Context, db *sql.DB, days int, groupCol string, keys []string) ([]UsageSeriesPoint, error) {
	if groupCol != "model_id" && groupCol != "user_id" {
		return nil, fmt.Errorf("AdminUsageSeries: invalid group column %q", groupCol)
	}
	if len(keys) == 0 {
		return []UsageSeriesPoint{}, nil
	}
	if days <= 0 {
		days = 7
	}
	bucket := UsageBucketWidth(days)
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := []any{bucket, bucket, since}
	for _, k := range keys {
		args = append(args, k)
	}
	q := fmt.Sprintf(
		`SELECT (created_at / ?) * ? AS b, %s, SUM(input_tokens), SUM(output_tokens), COUNT(*), SUM(cost)
		 FROM usage_logs WHERE created_at >= ? AND %s IN (%s)
		 GROUP BY b, %s ORDER BY b ASC`,
		groupCol, groupCol, placeholders, groupCol)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSeriesPoint{}
	for rows.Next() {
		var p UsageSeriesPoint
		if err := rows.Scan(&p.BucketStart, &p.Key, &p.InputTokens, &p.OutputTokens, &p.Calls, &p.Cost); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SessionMeta carries the device/network context recorded alongside a refresh
// token so the user can review and revoke their active sessions. CreatedAt is
// preserved across refresh rotation (0 means "now") so the "signed in" time
// survives; LastSeen is always stamped to now on each issue.
type SessionMeta struct {
	IP        string
	UserAgent string
	Location  string
	CreatedAt int64
}

// SaveRefreshToken records a non-revoked refresh token for the user along with
// the device/network context in meta.
func SaveRefreshToken(ctx context.Context, db *sql.DB, jti, userID string, expiresAt time.Time, meta SessionMeta) error {
	now := time.Now().Unix()
	created := meta.CreatedAt
	if created == 0 {
		created = now
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO refresh_tokens(jti, user_id, expires_at, revoked, created_at, user_agent, ip, location, last_seen)
		 VALUES(?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		jti, userID, expiresAt.Unix(), created, meta.UserAgent, meta.IP, meta.Location, now)
	return err
}

// RefreshTokenCreatedAt returns the original sign-in time of a refresh token so
// rotation can preserve it across token swaps. Returns 0 when not found.
func RefreshTokenCreatedAt(ctx context.Context, db *sql.DB, jti string) int64 {
	var c int64
	_ = db.QueryRowContext(ctx, `SELECT created_at FROM refresh_tokens WHERE jti=?`, jti).Scan(&c)
	return c
}

// RevokeRefreshToken marks a single refresh token revoked.
func RevokeRefreshToken(ctx context.Context, db *sql.DB, jti string) error {
	_, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE jti=?`, jti)
	return err
}

// SessionInfo is the user-facing view of one active session (one non-revoked
// refresh token = one signed-in device).
type SessionInfo struct {
	ID        string `json:"id"` // the jti — opaque handle used to revoke
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	Location  string `json:"location"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
}

// ListUserSessions returns the user's active (non-revoked, unexpired) sessions,
// most-recently-active first.
func ListUserSessions(ctx context.Context, db *sql.DB, userID string) ([]SessionInfo, error) {
	// Tokens issued before this feature have last_seen=0; fall back to created_at
	// so they don't render as a 1970 timestamp until their next refresh.
	rows, err := db.QueryContext(ctx,
		`SELECT jti, ip, user_agent, location, created_at,
		        CASE WHEN last_seen > 0 THEN last_seen ELSE created_at END AS seen
		 FROM refresh_tokens
		 WHERE user_id=? AND revoked=0 AND expires_at > ?
		 ORDER BY seen DESC`, userID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.ID, &s.IP, &s.UserAgent, &s.Location, &s.CreatedAt, &s.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeUserSession revokes one refresh token, scoped to its owner. Returns true
// when a matching active row was revoked.
func RevokeUserSession(ctx context.Context, db *sql.DB, userID, jti string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked=1 WHERE jti=? AND user_id=? AND revoked=0`, jti, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RevokeOtherUserSessions revokes every active session for the user except the
// one identified by keepJTI (the caller's current session).
func RevokeOtherUserSessions(ctx context.Context, db *sql.DB, userID, keepJTI string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND jti<>? AND revoked=0`, userID, keepJTI)
	return err
}

// IsRefreshTokenValid returns true when the jti exists, not revoked, not
// expired.
func IsRefreshTokenValid(ctx context.Context, db *sql.DB, jti, userID string) (bool, error) {
	var (
		exp int64
		rev int
	)
	err := db.QueryRowContext(ctx,
		`SELECT expires_at, revoked FROM refresh_tokens WHERE jti=? AND user_id=?`, jti, userID,
	).Scan(&exp, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rev == 1 {
		return false, nil
	}
	if time.Now().Unix() > exp {
		return false, nil
	}
	return true, nil
}

// Artifact is a file produced by a tool and attached to one message.
type Artifact struct {
	ID          string `json:"id"`
	MessageID   string `json:"message_id"`
	Filename    string `json:"filename"`
	StoragePath string `json:"-"`
	MimeType    string `json:"mime_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   int64  `json:"created_at"`
}

// GetArtifact loads one artifact scoped to userID. An empty userID means "any
// owner" — for admin triage of another user's conversation (A12).
func GetArtifact(ctx context.Context, db *sql.DB, id, userID string) (*Artifact, error) {
	var a Artifact
	q := `SELECT a.id, a.message_id, a.filename, a.storage_path, a.mime_type, a.size_bytes, a.created_at
		 FROM artifacts a JOIN messages m ON m.id = a.message_id
		 JOIN conversations c ON c.id = m.conversation_id
		 WHERE a.id=?`
	args := []any{id}
	if userID != "" {
		q += ` AND c.user_id=?`
		args = append(args, userID)
	}
	err := db.QueryRowContext(ctx, q, args...).Scan(
		&a.ID, &a.MessageID, &a.Filename, &a.StoragePath, &a.MimeType, &a.SizeBytes, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListImageArtifactsByConversation returns image artifacts (generated images)
// produced anywhere in a conversation, oldest first. Used to stage generated
// images into the sandbox so a follow-up python_execute can embed them
// (e.g. into a PPTX). Scoped by owner.
func ListImageArtifactsByConversation(ctx context.Context, db *sql.DB, convID, userID string) ([]Artifact, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT a.id, a.message_id, a.filename, a.storage_path, a.mime_type, a.size_bytes, a.created_at
		 FROM artifacts a
		 JOIN messages m ON m.id = a.message_id
		 JOIN conversations c ON c.id = m.conversation_id
		 WHERE m.conversation_id=? AND c.user_id=? AND a.mime_type LIKE 'image/%'
		 ORDER BY a.created_at ASC`, convID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.StoragePath, &a.MimeType, &a.SizeBytes, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateArtifact inserts a new artifact row (called by tool wrappers when
// they write a file to ArtifactDir).
func CreateArtifact(ctx context.Context, db *sql.DB, a Artifact) (*Artifact, error) {
	if a.ID == "" {
		a.ID = genID("art")
	}
	if a.MimeType == "" {
		a.MimeType = "application/octet-stream"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO artifacts(id, message_id, filename, storage_path, mime_type, size_bytes, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.MessageID, a.Filename, a.StoragePath, a.MimeType, a.SizeBytes, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	a.CreatedAt = time.Now().Unix()
	return &a, nil
}
