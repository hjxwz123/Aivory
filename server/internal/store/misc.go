package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// CreateFile inserts a file metadata row.
func CreateFile(ctx context.Context, db *sql.DB, f File) (*File, error) {
	if f.ID == "" {
		f.ID = genID("f")
	}
	if len(f.ProviderRefs) == 0 {
		f.ProviderRefs = json.RawMessage("{}")
	}
	var conv any
	if f.ConversationID != "" {
		conv = f.ConversationID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO files(id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, provider_refs, kind, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.UserID, conv, f.Filename, f.MimeType, f.SizeBytes, f.StoragePath, string(f.ProviderRefs), f.Kind, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return GetFile(ctx, db, f.ID, f.UserID)
}

// ListFilesByConversation returns a conversation's uploaded files (oldest
// first) — used to stage data files into the sandbox /workspace/uploads (§4.5).
func ListFilesByConversation(ctx context.Context, db *sql.DB, convID, userID string) ([]File, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, provider_refs, kind, created_at
		 FROM files WHERE conversation_id=? AND user_id=? ORDER BY created_at ASC`, convID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		var conv sql.NullString
		var provRefs string
		if err := rows.Scan(&f.ID, &f.UserID, &conv, &f.Filename, &f.MimeType, &f.SizeBytes, &f.StoragePath, &provRefs, &f.Kind, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.ConversationID = conv.String
		f.ProviderRefs = json.RawMessage(provRefs)
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFile returns one row with ownership check.
func GetFile(ctx context.Context, db *sql.DB, id, userID string) (*File, error) {
	var f File
	var conv sql.NullString
	var provRefs string
	err := db.QueryRowContext(ctx,
		`SELECT id, user_id, conversation_id, filename, mime_type, size_bytes, storage_path, provider_refs, kind, created_at FROM files WHERE id=? AND user_id=?`, id, userID,
	).Scan(&f.ID, &f.UserID, &conv, &f.Filename, &f.MimeType, &f.SizeBytes, &f.StoragePath, &provRefs, &f.Kind, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.ConversationID = conv.String
	f.ProviderRefs = json.RawMessage(provRefs)
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
		`INSERT INTO usage_logs(user_id, conversation_id, message_id, model_id, purpose, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, images_count, cost, currency, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.UserID, nullable(u.ConversationID), nullable(u.MessageID), u.ModelID, u.Purpose,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.ImagesCount,
		u.Cost, u.Currency, time.Now().Unix())
	return err
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
	UserID       string  `json:"user_id"`
	UserEmail    string  `json:"user_email"`
	ModelID      string  `json:"model_id"`
	Purpose      string  `json:"purpose"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Calls        int     `json:"calls"`
	Cost         float64 `json:"cost"`
	Currency     string  `json:"currency"`
}

// AdminUsageReport returns an aggregated report across users/models/purpose
// over the past `days` days.
func AdminUsageReport(ctx context.Context, db *sql.DB, days int) ([]UsageRow, error) {
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	rows, err := db.QueryContext(ctx,
		`SELECT u.user_id, COALESCE(usr.email, ''), u.model_id, u.purpose,
		        SUM(u.input_tokens), SUM(u.output_tokens), COUNT(*), SUM(u.cost), MAX(u.currency)
		 FROM usage_logs u LEFT JOIN users usr ON usr.id = u.user_id
		 WHERE u.created_at >= ?
		 GROUP BY u.user_id, u.model_id, u.purpose
		 ORDER BY SUM(u.cost) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.UserID, &r.UserEmail, &r.ModelID, &r.Purpose, &r.InputTokens, &r.OutputTokens, &r.Calls, &r.Cost, &r.Currency); err != nil {
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

// SaveRefreshToken records a non-revoked refresh token for the user.
func SaveRefreshToken(ctx context.Context, db *sql.DB, jti, userID string, expiresAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO refresh_tokens(jti, user_id, expires_at, revoked, created_at) VALUES(?, ?, ?, 0, ?)`,
		jti, userID, expiresAt.Unix(), time.Now().Unix())
	return err
}

// RevokeRefreshToken marks a single refresh token revoked.
func RevokeRefreshToken(ctx context.Context, db *sql.DB, jti string) error {
	_, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE jti=?`, jti)
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

// GetArtifact loads one artifact ensuring it belongs to a message belonging
// to a conversation owned by userID. Returns ErrNotFound otherwise (A12).
func GetArtifact(ctx context.Context, db *sql.DB, id, userID string) (*Artifact, error) {
	var a Artifact
	err := db.QueryRowContext(ctx,
		`SELECT a.id, a.message_id, a.filename, a.storage_path, a.mime_type, a.size_bytes, a.created_at
		 FROM artifacts a JOIN messages m ON m.id = a.message_id
		 JOIN conversations c ON c.id = m.conversation_id
		 WHERE a.id=? AND c.user_id=?`, id, userID).Scan(
		&a.ID, &a.MessageID, &a.Filename, &a.StoragePath, &a.MimeType, &a.SizeBytes, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
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
