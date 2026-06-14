package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ListKBs returns the user's knowledge bases.
func ListKBs(ctx context.Context, db *sql.DB, userID string) ([]KnowledgeBase, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at FROM knowledge_bases WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnowledgeBase{}
	for rows.Next() {
		kb, err := scanKB(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

// OwnedKBIDs filters ids down to the ones actually owned by userID (§C1 — the
// retrieval scope must never include another user's KB). On a DB error it fails
// closed (returns none) rather than risk leaking another user's chunks.
func OwnedKBIDs(ctx context.Context, db *sql.DB, userID string, ids []string) []string {
	if len(ids) == 0 {
		return ids
	}
	ph := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	args = append(args, userID)
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM knowledge_bases WHERE id IN (`+strings.Join(ph, ",")+`) AND user_id=?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	owned := make([]string, 0, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			owned = append(owned, id)
		}
	}
	return owned
}

// GetKB reads one row with ownership check.
func GetKB(ctx context.Context, db *sql.DB, id, userID string) (*KnowledgeBase, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at FROM knowledge_bases WHERE id=? AND user_id=?`, id, userID)
	kb, err := scanKB(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

func scanKB(s scanner) (KnowledgeBase, error) {
	var kb KnowledgeBase
	if err := s.Scan(&kb.ID, &kb.UserID, &kb.Name, &kb.Description, &kb.EmbeddingModelID, &kb.EmbeddingDim, &kb.ProjectID, &kb.CreatedAt); err != nil {
		return kb, err
	}
	return kb, nil
}

// CreateKB inserts a row.
func CreateKB(ctx context.Context, db *sql.DB, kb KnowledgeBase) (*KnowledgeBase, error) {
	if kb.ID == "" {
		kb.ID = genID("kb")
	}
	var pid any
	if kb.ProjectID == "" {
		pid = nil
	} else {
		pid = kb.ProjectID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO knowledge_bases(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		kb.ID, kb.UserID, kb.Name, kb.Description, kb.EmbeddingModelID, kb.EmbeddingDim, pid, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return GetKB(ctx, db, kb.ID, kb.UserID)
}

// SetKBEmbeddingDim corrects the stored vector width for a KB. Called during
// ingest when the embedding model's actual output dimension differs from what
// was configured, so retrieval resolves the same (real) dim and hits the right
// Qdrant collection instead of falling back to brute-force forever.
func SetKBEmbeddingDim(ctx context.Context, db *sql.DB, kbID string, dim int) error {
	_, err := db.ExecContext(ctx, `UPDATE knowledge_bases SET embedding_dim=? WHERE id=?`, dim, kbID)
	return err
}

// DeleteKB removes the KB and cascades to documents/chunks.
func DeleteKB(ctx context.Context, db *sql.DB, id, userID string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM knowledge_bases WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDocuments lists documents for either a KB or a conversation. Scope is
// "kb" or "conversation" — empty returns all the user's own (joined via FK).
// ConversationHasReadyDocs reports whether a conversation has at least one
// successfully-ingested (retrievable) document — used to decide whether to run
// inline RAG even when no knowledge base is bound (§C/§4.11-B chat uploads).
func ConversationHasReadyDocs(ctx context.Context, db *sql.DB, convID string) bool {
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT 1 FROM documents WHERE conversation_id=? AND status='ready' LIMIT 1`, convID).Scan(&n)
	return n == 1
}

// ListIncompleteDocuments returns documents stuck in a non-terminal state —
// used at boot to requeue ingest jobs lost to a restart (the queue is in-memory).
func ListIncompleteDocuments(ctx context.Context, db *sql.DB) ([]Document, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, created_at
		   FROM documents WHERE status IN ('pending','parsing','embedding') ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.ConversationID, &d.Filename, &d.MimeType, &d.SizeBytes, &d.Status, &d.Error, &d.ChunkCount, &d.StoragePath, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func ListDocuments(ctx context.Context, db *sql.DB, scope, parentID string) ([]Document, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch scope {
	case "kb":
		rows, err = db.QueryContext(ctx,
			`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, created_at FROM documents WHERE kb_id=? ORDER BY created_at DESC`, parentID)
	case "conversation":
		rows, err = db.QueryContext(ctx,
			`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, created_at FROM documents WHERE conversation_id=? ORDER BY created_at DESC`, parentID)
	default:
		return nil, errors.New("unknown scope")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KBID, &d.ConversationID, &d.Filename, &d.MimeType, &d.SizeBytes, &d.Status, &d.Error, &d.ChunkCount, &d.StoragePath, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDocument returns one row.
func GetDocument(ctx context.Context, db *sql.DB, id string) (*Document, error) {
	var d Document
	err := db.QueryRowContext(ctx,
		`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, created_at FROM documents WHERE id=?`, id,
	).Scan(&d.ID, &d.KBID, &d.ConversationID, &d.Filename, &d.MimeType, &d.SizeBytes, &d.Status, &d.Error, &d.ChunkCount, &d.StoragePath, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDocument inserts a placeholder doc with status=pending. Either kbID or
// conversationID must be set; the other should be "" so the column stays null.
func CreateDocument(ctx context.Context, db *sql.DB, d Document) (*Document, error) {
	if d.ID == "" {
		d.ID = genID("doc")
	}
	if d.Status == "" {
		d.Status = "pending"
	}
	var kbID, convID any
	if d.KBID != "" {
		kbID = d.KBID
	}
	if d.ConversationID != "" {
		convID = d.ConversationID
	}
	if kbID == nil && convID == nil {
		return nil, errors.New("document must belong to a kb or a conversation")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO documents(id, kb_id, conversation_id, filename, mime_type, size_bytes, status, storage_path, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, kbID, convID, d.Filename, d.MimeType, d.SizeBytes, d.Status, d.StoragePath, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return GetDocument(ctx, db, d.ID)
}

// UpdateDocumentStatus moves the document along the pipeline state machine.
func UpdateDocumentStatus(ctx context.Context, db *sql.DB, id, status, errMsg string, chunkCount int) error {
	_, err := db.ExecContext(ctx,
		`UPDATE documents SET status=?, error=?, chunk_count=? WHERE id=?`, status, errMsg, chunkCount, id)
	return err
}

// RenameDocument updates just the filename of a document.
func RenameDocument(ctx context.Context, db *sql.DB, id, filename string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE documents SET filename=? WHERE id=?`, filename, id)
	return err
}

// DeleteDocument removes the row.
func DeleteDocument(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM documents WHERE id=?", id)
	return err
}

// PromoteDocumentToKB switches a conversation-temp doc into a knowledge base
// without re-embedding (used by "add to project library").
// DeleteChunksByDocument removes a document's chunk rows. Used when re-embedding
// a document on promote (§C5) — its vectors are dropped separately via the
// vector store.
func DeleteChunksByDocument(ctx context.Context, db *sql.DB, docID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, docID)
	return err
}

func PromoteDocumentToKB(ctx context.Context, db *sql.DB, docID, kbID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET kb_id=?, conversation_id=NULL WHERE id=?`, kbID, docID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chunks SET kb_id=?, conversation_id=NULL WHERE document_id=?`, kbID, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateChunk inserts a single text chunk (back-compat convenience wrapper).
func CreateChunk(ctx context.Context, db *sql.DB, docID, kbID, convID string, seq int, content string, embedding []byte, embeddingModel string) error {
	_, err := CreateChunkFull(ctx, db, ChunkInsert{
		DocumentID: docID, KBID: kbID, ConversationID: convID,
		Seq: seq, ChunkType: "text", Content: content,
		Embedding: embedding, EmbeddingModel: embeddingModel,
	})
	return err
}

// ChunkInsert is the full insert shape, supporting the small-to-big layout
// (§4.11-C-2: parent rows carry section context, children carry vectors) and
// image-caption chunks referencing the original image.
type ChunkInsert struct {
	DocumentID     string
	KBID           string
	ConversationID string
	Seq            int
	ParentID       string
	ChunkType      string // text | parent | table | image_caption
	Content        string
	ImageRef       string
	Embedding      []byte
	EmbeddingModel string
}

// sanitizeChunkText strips NUL bytes and invalid UTF-8 from parsed text. Postgres
// TEXT columns reject these (SQLSTATE 22021 "invalid byte sequence for encoding
// UTF8: 0x00") and binary documents (docx/pdf/xls) routinely carry them, which
// otherwise fails the whole ingest.
func sanitizeChunkText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "")
}

// CreateChunkFull inserts a chunk row and returns its id.
func CreateChunkFull(ctx context.Context, db *sql.DB, c ChunkInsert) (string, error) {
	id := genID("ch")
	c.Content = sanitizeChunkText(c.Content)
	var kb, conv, parent, imgRef any
	if c.KBID != "" {
		kb = c.KBID
	}
	if c.ConversationID != "" {
		conv = c.ConversationID
	}
	if c.ParentID != "" {
		parent = c.ParentID
	}
	if c.ImageRef != "" {
		imgRef = c.ImageRef
	}
	if c.ChunkType == "" {
		c.ChunkType = "text"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, parent_id, chunk_type, content, image_ref, meta, embedding, embedding_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
		id, c.DocumentID, kb, conv, c.Seq, parent, c.ChunkType, c.Content, imgRef, c.Embedding, c.EmbeddingModel)
	return id, err
}

// GetChunkContent returns one chunk's content — used for small-to-big parent
// expansion at retrieval time (§4.11-C-2).
func GetChunkContent(ctx context.Context, db *sql.DB, id string) (string, error) {
	var content string
	err := db.QueryRowContext(ctx, `SELECT content FROM chunks WHERE id=?`, id).Scan(&content)
	return content, err
}

// Chunk is a denormalised chunk row used by the retrieve engine.
type Chunk struct {
	ID             string
	DocumentID     string
	KBID           string
	ConversationID string
	Seq            int
	ParentID       string
	ChunkType      string
	Content        string
	ImageRef       string
	Meta           json.RawMessage
	Embedding      []byte
	EmbeddingModel string
	Filename       string // joined from documents
}

// ListChunksInScope returns chunks whose kb_id ∈ kbIDs OR conversation_id =
// convID. Filename is joined for citation rendering.
func ListChunksInScope(ctx context.Context, db *sql.DB, kbIDs []string, convID string) ([]Chunk, error) {
	parts := []string{}
	args := []any{}
	if len(kbIDs) > 0 {
		ph := []string{}
		for _, id := range kbIDs {
			ph = append(ph, "?")
			args = append(args, id)
		}
		parts = append(parts, "c.kb_id IN ("+strings.Join(ph, ",")+")")
	}
	if convID != "" {
		parts = append(parts, "c.conversation_id=?")
		args = append(args, convID)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	q := `SELECT c.id, c.document_id, COALESCE(c.kb_id,''), COALESCE(c.conversation_id,''), c.seq, COALESCE(c.parent_id,''), c.chunk_type, c.content, COALESCE(c.image_ref,''), c.meta, c.embedding, c.embedding_model, d.filename
		FROM chunks c JOIN documents d ON d.id = c.document_id WHERE ` + strings.Join(parts, " OR ")
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		var ch Chunk
		var meta string
		if err := rows.Scan(&ch.ID, &ch.DocumentID, &ch.KBID, &ch.ConversationID, &ch.Seq, &ch.ParentID, &ch.ChunkType, &ch.Content, &ch.ImageRef, &meta, &ch.Embedding, &ch.EmbeddingModel, &ch.Filename); err != nil {
			return nil, err
		}
		ch.Meta = json.RawMessage(meta)
		out = append(out, ch)
	}
	return out, rows.Err()
}
