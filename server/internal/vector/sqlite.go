package vector

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// SQLite persists vectors in the relational SQLite database and performs exact
// cosine search in-process. It is intended for a single-instance personal
// deployment: no daemon or native SQLite extension is required.
type SQLite struct {
	db *sql.DB
}

// NewSQLite creates an embedded vector store over the already-migrated app DB.
func NewSQLite(db *sql.DB) *SQLite { return &SQLite{db: db} }

func (*SQLite) Enabled() bool { return true }

func (s *SQLite) Upsert(ctx context.Context, dim int, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite vector: nil database")
	}
	if dim <= 0 {
		return fmt.Errorf("sqlite vector: invalid dimension %d", dim)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO vector_points(chunk_id, dimension, embedding)
		VALUES(?, ?, ?)
		ON CONFLICT(chunk_id, dimension) DO UPDATE SET embedding=excluded.embedding`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, point := range points {
		if strings.TrimSpace(point.ChunkID) == "" {
			return fmt.Errorf("sqlite vector: empty chunk id")
		}
		if len(point.Vector) != dim {
			return fmt.Errorf("sqlite vector: chunk %s has dimension %d, want %d", point.ChunkID, len(point.Vector), dim)
		}
		encoded, err := encodeNormalizedVector(point.Vector)
		if err != nil {
			return fmt.Errorf("sqlite vector: chunk %s: %w", point.ChunkID, err)
		}
		if _, err := stmt.ExecContext(ctx, point.ChunkID, dim, encoded); err != nil {
			return fmt.Errorf("sqlite vector: upsert chunk %s: %w", point.ChunkID, err)
		}
	}
	return tx.Commit()
}

func (s *SQLite) Search(ctx context.Context, dim int, query []float32, scope Scope, topK int) ([]Hit, error) {
	if topK <= 0 || len(query) == 0 {
		return nil, nil
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite vector: nil database")
	}
	if dim <= 0 || len(query) != dim {
		return nil, fmt.Errorf("sqlite vector: query dimension %d, want %d", len(query), dim)
	}
	where, scopeArgs := sqliteScopeClause(scope, "c")
	if where == "" {
		return nil, nil
	}
	normalized, err := normalizeVector(query)
	if err != nil {
		return nil, fmt.Errorf("sqlite vector: query: %w", err)
	}
	args := make([]any, 0, 1+len(scopeArgs))
	args = append(args, dim)
	args = append(args, scopeArgs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.chunk_id, v.embedding
		FROM vector_points v
		JOIN chunks c ON c.id=v.chunk_id
		WHERE v.dimension=? AND (`+where+`)`, args...)
	if err != nil {
		return nil, err
	}
	ranked := make([]sqliteRank, 0)
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored, err := decodeVector(raw, dim)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite vector: decode chunk %s: %w", id, err)
		}
		ranked = append(ranked, sqliteRank{id: id, score: dotProduct(normalized, stored)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// SQLite is configured with one connection. Release the scan before loading
	// the winning payload rows or the second query would wait on itself.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return s.ranksToHits(ctx, ranked, topK)
}

// SearchKeyword supplies the independent lexical leg used by hybrid retrieval.
// It intentionally uses a bounded in-process score rather than requiring FTS5,
// keeping the personal image identical across amd64 and arm64.
func (s *SQLite) SearchKeyword(ctx context.Context, dim int, query string, scope Scope, topK int) ([]Hit, error) {
	terms := sqliteKeywordTerms(query)
	if topK <= 0 || len(terms) == 0 {
		return nil, nil
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite vector: nil database")
	}
	if dim <= 0 {
		return nil, fmt.Errorf("sqlite vector: invalid dimension %d", dim)
	}
	where, scopeArgs := sqliteScopeClause(scope, "c")
	if where == "" {
		return nil, nil
	}
	args := make([]any, 0, 1+len(scopeArgs))
	args = append(args, dim)
	args = append(args, scopeArgs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.chunk_id, c.content
		FROM vector_points v
		JOIN chunks c ON c.id=v.chunk_id
		WHERE v.dimension=? AND (`+where+`)`, args...)
	if err != nil {
		return nil, err
	}
	ranked := make([]sqliteRank, 0)
	for rows.Next() {
		var id, content string
		if err := rows.Scan(&id, &content); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if score := sqliteKeywordScore(content, terms); score > 0 {
			ranked = append(ranked, sqliteRank{id: id, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return s.ranksToHits(ctx, ranked, topK)
}

func (s *SQLite) ExistingChunkIDs(ctx context.Context, dim int, scope Scope) (map[string]bool, error) {
	ids := map[string]bool{}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite vector: nil database")
	}
	if dim <= 0 {
		return ids, fmt.Errorf("sqlite vector: invalid dimension %d", dim)
	}
	where, scopeArgs := sqliteScopeClause(scope, "c")
	if where == "" {
		return ids, nil
	}
	args := append([]any{dim}, scopeArgs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.chunk_id
		FROM vector_points v
		JOIN chunks c ON c.id=v.chunk_id
		WHERE v.dimension=? AND (`+where+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

func (s *SQLite) VectorChunkStatuses(ctx context.Context, dim int, scope Scope) (map[string]ChunkVectorStatus, error) {
	where, args := sqliteScopeClause(scope, "c")
	if where == "" {
		return map[string]ChunkVectorStatus{}, nil
	}
	return s.vectorChunkStatuses(ctx, dim, where, args)
}

func (s *SQLite) AllVectorChunkStatuses(ctx context.Context, dim int) (map[string]ChunkVectorStatus, error) {
	return s.vectorChunkStatuses(ctx, dim, "", nil)
}

func (s *SQLite) vectorChunkStatuses(ctx context.Context, dim int, where string, scopeArgs []any) (map[string]ChunkVectorStatus, error) {
	status := map[string]ChunkVectorStatus{}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite vector: nil database")
	}
	if dim <= 0 {
		return status, fmt.Errorf("sqlite vector: invalid dimension %d", dim)
	}
	query := `
		SELECT v.chunk_id, length(v.embedding)
		FROM vector_points v
		JOIN chunks c ON c.id=v.chunk_id
		WHERE v.dimension=?`
	args := make([]any, 0, 1+len(scopeArgs))
	args = append(args, dim)
	if where != "" {
		query += " AND (" + where + ")"
		args = append(args, scopeArgs...)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var size int
		if err := rows.Scan(&id, &size); err != nil {
			return nil, err
		}
		status[id] = ChunkVectorStatus{Exists: true, HasVector: size == dim*4}
	}
	return status, rows.Err()
}

func (s *SQLite) DeleteByDocument(ctx context.Context, documentID string) error {
	return s.deleteByChunkField(ctx, "document_id", documentID)
}

func (s *SQLite) DeleteByKB(ctx context.Context, kbID string) error {
	return s.deleteByChunkField(ctx, "kb_id", kbID)
}

func (s *SQLite) DeleteByConversation(ctx context.Context, conversationID string) error {
	return s.deleteByChunkField(ctx, "conversation_id", conversationID)
}

func (s *SQLite) deleteByChunkField(ctx context.Context, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite vector: nil database")
	}
	switch field {
	case "document_id", "kb_id", "conversation_id":
	default:
		return fmt.Errorf("sqlite vector: unsupported delete field %q", field)
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM vector_points WHERE chunk_id IN (SELECT id FROM chunks WHERE "+field+"=?)", value) //nolint:gosec // field is allowlisted above
	return err
}

type sqliteRank struct {
	id    string
	score float32
}

func (s *SQLite) ranksToHits(ctx context.Context, ranked []sqliteRank, topK int) ([]Hit, error) {
	if len(ranked) == 0 || topK <= 0 {
		return nil, nil
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	ids := make([]string, len(ranked))
	for i := range ranked {
		ids[i] = ranked[i].id
	}
	payloads, err := s.loadPayloads(ctx, ids)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(ranked))
	for _, rank := range ranked {
		if payload, ok := payloads[rank.id]; ok {
			hits = append(hits, Hit{Score: rank.score, Payload: payload})
		}
	}
	return hits, nil
}

func (s *SQLite) loadPayloads(ctx context.Context, ids []string) (map[string]Payload, error) {
	payloads := make(map[string]Payload, len(ids))
	if len(ids) == 0 {
		return payloads, nil
	}
	args := make([]any, len(ids))
	for i := range ids {
		args[i] = ids[i]
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, COALESCE(c.kb_id,''), COALESCE(c.conversation_id,''),
		       COALESCE(c.parent_id,''), c.chunk_type, c.seq, c.content, d.filename
		FROM chunks c
		JOIN documents d ON d.id=c.document_id
		WHERE c.id IN (`+sqlitePlaceholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Payload
		if err := rows.Scan(&p.ChunkID, &p.DocumentID, &p.KBID, &p.ConversationID,
			&p.ParentID, &p.ChunkType, &p.Seq, &p.Content, &p.Filename); err != nil {
			return nil, err
		}
		payloads[p.ChunkID] = p
	}
	return payloads, rows.Err()
}

func sqliteScopeClause(scope Scope, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, len(scope.KBIDs)+len(scope.DocumentIDs)+1)
	if values := nonEmptyStrings(scope.KBIDs); len(values) > 0 {
		conditions = append(conditions, prefix+"kb_id IN ("+sqlitePlaceholders(len(values))+")")
		for _, value := range values {
			args = append(args, value)
		}
	}
	if values := nonEmptyStrings(scope.DocumentIDs); len(values) > 0 {
		conditions = append(conditions, prefix+"document_id IN ("+sqlitePlaceholders(len(values))+")")
		for _, value := range values {
			args = append(args, value)
		}
	} else if len(scope.DocumentIDs) == 0 {
		if conversationID := strings.TrimSpace(scope.ConversationID); conversationID != "" {
			conditions = append(conditions, prefix+"conversation_id=?")
			args = append(args, conversationID)
		}
	}
	return strings.Join(conditions, " OR "), args
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func sqlitePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func encodeNormalizedVector(vector []float32) ([]byte, error) {
	normalized, err := normalizeVector(vector)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, len(normalized)*4)
	for i, value := range normalized {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(value))
	}
	return raw, nil
}

func decodeVector(raw []byte, dim int) ([]float32, error) {
	if dim <= 0 || len(raw) != dim*4 {
		return nil, fmt.Errorf("invalid encoded size %d for dimension %d", len(raw), dim)
	}
	vector := make([]float32, dim)
	for i := range vector {
		value := math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("non-finite component at index %d", i)
		}
		vector[i] = value
	}
	return vector, nil
}

func normalizeVector(vector []float32) ([]float32, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("empty vector")
	}
	var squared float64
	for i, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite component at index %d", i)
		}
		squared += f * f
	}
	if squared == 0 || math.IsInf(squared, 0) {
		return nil, fmt.Errorf("zero or overflowing norm")
	}
	norm := math.Sqrt(squared)
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = float32(float64(value) / norm)
	}
	return out, nil
}

func dotProduct(a, b []float32) float32 {
	var score float64
	for i := range a {
		score += float64(a[i]) * float64(b[i])
	}
	if score > 1 {
		score = 1
	} else if score < -1 {
		score = -1
	}
	return float32(score)
}

func sqliteKeywordTerms(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return nonEmptyStrings(parts)
}

func sqliteKeywordScore(content string, terms []string) float32 {
	content = strings.ToLower(content)
	var score float32
	for _, term := range terms {
		if strings.Contains(content, term) {
			score++
		}
	}
	return score
}
