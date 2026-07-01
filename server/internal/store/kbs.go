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
// CountStandaloneKBsByUser counts a user's standalone knowledge bases — those
// not backing a project (§ user-group caps). Project-library KBs are created
// implicitly with a project and governed by the project cap instead.
func CountStandaloneKBsByUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM knowledge_bases WHERE user_id=? AND (project_id IS NULL OR project_id='')`, userID).Scan(&n)
	return n, err
}

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

// GetKBByName returns a user's KB by case-insensitive, trimmed name.
func GetKBByName(ctx context.Context, db *sql.DB, userID, name string) (*KnowledgeBase, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, user_id, name, description, embedding_model_id, embedding_dim, COALESCE(project_id, ''), created_at FROM knowledge_bases WHERE user_id=? AND lower(trim(name))=lower(trim(?)) LIMIT 1`,
		userID, name)
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
	kb.Name = strings.TrimSpace(kb.Name)
	kb.Description = strings.TrimSpace(kb.Description)
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
		if isUniqueIndexErr(err, "idx_kbs_user_name_unique", "knowledge_bases.user_id") {
			return nil, ErrKBNameExists
		}
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

// DeleteKB removes the KB and cascades to documents/chunks. It also removes
// the deleted KB's ID from the kb_ids JSON array in all conversations so stale
// references don't cause retrieval errors (§ FIX-5).
func DeleteKB(ctx context.Context, db *sql.DB, id, userID string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM knowledge_bases WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	// Clean up kb_ids references in conversations. kb_ids is stored as a JSON
	// TEXT array in both SQLite and Postgres. We use json_each to rebuild the
	// array without the deleted KB's ID (raw query — not in query files because
	// this dialect-switch logic would be awkward in sqlc templates).
	if IsPostgres() {
		// Postgres: use json_agg + json_array_elements_text to filter the array.
		_, _ = db.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = COALESCE(
				(SELECT json_agg(value ORDER BY ordinality)
				 FROM json_array_elements_text(kb_ids::json) WITH ORDINALITY
				 WHERE value != $1),
				'[]'::json
			)::text
			WHERE kb_ids LIKE '%' || $1 || '%'
		`, id)
	} else {
		// SQLite: use json_each + json_group_array to rebuild without the deleted ID.
		_, _ = db.ExecContext(ctx, `
			UPDATE conversations
			SET kb_ids = (
				SELECT COALESCE(json_group_array(value), '[]')
				FROM json_each(kb_ids)
				WHERE value != ?
			)
			WHERE json_type(kb_ids) = 'array' AND kb_ids LIKE '%' || ? || '%'
		`, id, id)
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

// ConversationDocReady reports whether a conversation-scoped document with this
// filename has finished RAG ingestion (status=ready). Used to skip re-sending a
// PDF as a slow native `document` block when its text is already retrievable via
// RAG (§4.6 / §perf — native PDF processing is minutes for a large file).
func ConversationDocReady(ctx context.Context, db *sql.DB, convID, filename string) bool {
	if convID == "" || filename == "" {
		return false
	}
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM documents WHERE conversation_id=? AND filename=? AND status='ready' LIMIT 1`,
		convID, filename).Scan(&n)
	return err == nil && n == 1
}

// ConversationHasPendingDocs reports whether a conversation has a document still
// being ingested (pending/parsing/embedding). Used to briefly wait for a
// just-uploaded chat file to finish indexing before answering, so the very first
// turn after an upload can actually use the file (§4.11-B chat uploads).
func ConversationHasPendingDocs(ctx context.Context, db *sql.DB, convID string) bool {
	var n int
	_ = db.QueryRowContext(ctx,
		`SELECT 1 FROM documents WHERE conversation_id=? AND status IN ('pending','parsing','embedding') LIMIT 1`, convID).Scan(&n)
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
	// ID, when set, is used verbatim (so a batched insert can pre-resolve
	// parent→child references); empty means "generate one".
	ID             string
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

// NewChunkID returns a fresh chunk id, so callers can pre-resolve parent→child
// references before a batched insert.
func NewChunkID() string { return genID("ch") }

// CreateChunksBatch inserts many chunks in ONE transaction — a single commit
// instead of one autonomous INSERT (and, on SQLite, one fsync) per chunk, which
// is the dominant cost when indexing a large document. Each chunk's ID must be
// pre-set (NewChunkID) and parents must precede the children that reference them.
// Rolls back on the first error.
func CreateChunksBatch(ctx context.Context, db *sql.DB, chunks []ChunkInsert) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, parent_id, chunk_type, content, image_ref, meta, embedding, embedding_model) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		id := c.ID
		if id == "" {
			id = genID("ch")
		}
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
		ct := c.ChunkType
		if ct == "" {
			ct = "text"
		}
		if _, err := stmt.ExecContext(ctx, id, c.DocumentID, kb, conv, c.Seq, parent, ct, sanitizeChunkText(c.Content), imgRef, c.Embedding, c.EmbeddingModel); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	// The kb-scope and conv-scope legs are UNION ALL'd rather than OR'd: a chunk
	// has either kb_id OR conversation_id (promoting a conv doc to a KB nulls the
	// other), so the legs are disjoint (no duplicates) and each can use its own
	// index (idx_chunks_kb / idx_chunks_conv) — an `OR` across the two columns
	// would force a full scan.
	const cols = `c.id, c.document_id, COALESCE(c.kb_id,''), COALESCE(c.conversation_id,''), c.seq, COALESCE(c.parent_id,''), c.chunk_type, c.content, COALESCE(c.image_ref,''), c.meta, c.embedding, c.embedding_model, d.filename`
	const from = ` FROM chunks c JOIN documents d ON d.id = c.document_id WHERE `
	legs := []string{}
	args := []any{}
	if len(kbIDs) > 0 {
		ph := []string{}
		for _, id := range kbIDs {
			ph = append(ph, "?")
			args = append(args, id)
		}
		legs = append(legs, `SELECT `+cols+from+`c.kb_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if convID != "" {
		legs = append(legs, `SELECT `+cols+from+`c.conversation_id=?`)
		args = append(args, convID)
	}
	if len(legs) == 0 {
		return nil, nil
	}
	// Deterministic DOCUMENT ORDER: full-text injection, map-reduce summarisation
	// and cross-document comparison all assume scope is in document order, but
	// UNION ALL guarantees no ordering (Postgres especially). Order by the output
	// columns document_id (2) then seq (5) — positional refs are portable across
	// SQLite/Postgres — so each doc's chunks stay contiguous and in sequence.
	q := strings.Join(legs, " UNION ALL ") + " ORDER BY 2, 5"
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
