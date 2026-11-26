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

const (
	DocumentRetrievalModeSettingKey = "document_retrieval_mode"
	DocumentRetrievalModeRAG        = "rag"
	DocumentRetrievalModeGrep       = "grep"
)

// NormalizeDocumentRetrievalMode maps persisted or client-supplied retrieval
// mode values to the two supported global modes ("rag" | "grep").
func NormalizeDocumentRetrievalMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), DocumentRetrievalModeGrep) {
		return DocumentRetrievalModeGrep
	}
	return DocumentRetrievalModeRAG
}

// DocumentRetrievalMode reads the live global document retrieval policy.
// Defaults to "rag" when unset or unreadable so existing deployments keep
// their historical behaviour.
func DocumentRetrievalMode(db *sql.DB) string {
	if db == nil {
		return DocumentRetrievalModeRAG
	}
	raw, err := GetSetting(db, DocumentRetrievalModeSettingKey)
	if err != nil || len(raw) == 0 {
		return DocumentRetrievalModeRAG
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		return NormalizeDocumentRetrievalMode(mode)
	}
	return NormalizeDocumentRetrievalMode(string(raw))
}

// DocumentHeading records one structural heading extracted from a document.
type DocumentHeading struct {
	Level int    `json:"level"`
	Line  int    `json:"line"`
	Title string `json:"title"`
}

// DocumentText stores the normalized plain/markdown text extracted from a
// document for fast Grep and line-range reading without re-parsing binaries.
type DocumentText struct {
	DocumentID     string `json:"document_id"`
	KBID           string `json:"kb_id"`
	ConversationID string `json:"conversation_id"`
	Filename       string `json:"filename,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	Content        string `json:"content"`
	LineCount      int    `json:"line_count"`
	CharCount      int    `json:"char_count"`
	TokenEstimate  int    `json:"token_estimate"`
	OutlineJSON    string `json:"outline_json"`
	UpdatedAt      int64  `json:"updated_at"`
}

// UpsertDocumentText saves or refreshes the normalized full text of a document.
func UpsertDocumentText(ctx context.Context, db *sql.DB, dt DocumentText) error {
	if db == nil || strings.TrimSpace(dt.DocumentID) == "" {
		return nil
	}
	content := sanitizeChunkText(dt.Content)
	outline := strings.TrimSpace(dt.OutlineJSON)
	if outline == "" {
		outline = "[]"
	}
	now := dt.UpdatedAt
	if now <= 0 {
		now = time.Now().Unix()
	}
	var kbArg any
	if strings.TrimSpace(dt.KBID) != "" {
		kbArg = strings.TrimSpace(dt.KBID)
	}
	var convArg any
	if strings.TrimSpace(dt.ConversationID) != "" {
		convArg = strings.TrimSpace(dt.ConversationID)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO document_texts(document_id, kb_id, conversation_id, content, line_count, char_count, token_estimate, outline_json, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(document_id) DO UPDATE SET
		   kb_id=excluded.kb_id,
		   conversation_id=excluded.conversation_id,
		   content=excluded.content,
		   line_count=excluded.line_count,
		   char_count=excluded.char_count,
		   token_estimate=excluded.token_estimate,
		   outline_json=excluded.outline_json,
		   updated_at=excluded.updated_at`,
		dt.DocumentID, kbArg, convArg, content, dt.LineCount, dt.CharCount, dt.TokenEstimate, outline, now,
	)
	return err
}

// GetDocumentText returns the normalized full text row for one document.
func GetDocumentText(ctx context.Context, db *sql.DB, documentID string) (*DocumentText, error) {
	if db == nil {
		return nil, ErrNotFound
	}
	var dt DocumentText
	err := db.QueryRowContext(ctx,
		`SELECT dt.document_id, COALESCE(dt.kb_id,''), COALESCE(dt.conversation_id,''),
		        COALESCE(d.filename,''), COALESCE(d.mime_type,''), COALESCE(d.size_bytes,0),
		        dt.content, dt.line_count, dt.char_count, dt.token_estimate, dt.outline_json, dt.updated_at
		   FROM document_texts dt
		   JOIN documents d ON d.id = dt.document_id
		  WHERE dt.document_id=?`,
		documentID,
	).Scan(
		&dt.DocumentID, &dt.KBID, &dt.ConversationID,
		&dt.Filename, &dt.MimeType, &dt.SizeBytes,
		&dt.Content, &dt.LineCount, &dt.CharCount, &dt.TokenEstimate, &dt.OutlineJSON, &dt.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &dt, nil
}

// ListDocumentTextsInScope loads normalized full-text rows for all ready
// documents inside the authorized knowledge bases and conversation.
func ListDocumentTextsInScope(ctx context.Context, db *sql.DB, kbIDs []string, convID string) ([]DocumentText, error) {
	if db == nil {
		return nil, nil
	}
	where, args := documentScopeWhere("d", kbIDs, convID)
	if len(where) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT dt.document_id, COALESCE(dt.kb_id,''), COALESCE(dt.conversation_id,''),
		        d.filename, d.mime_type, d.size_bytes,
		        dt.content, dt.line_count, dt.char_count, dt.token_estimate, dt.outline_json, dt.updated_at
		   FROM document_texts dt
		   JOIN documents d ON d.id = dt.document_id
		  WHERE d.status='ready' AND (`+strings.Join(where, " OR ")+`)
		  ORDER BY d.created_at ASC, d.id ASC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DocumentText{}
	for rows.Next() {
		var dt DocumentText
		if err := rows.Scan(
			&dt.DocumentID, &dt.KBID, &dt.ConversationID,
			&dt.Filename, &dt.MimeType, &dt.SizeBytes,
			&dt.Content, &dt.LineCount, &dt.CharCount, &dt.TokenEstimate, &dt.OutlineJSON, &dt.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, dt)
	}
	return out, rows.Err()
}

// ListReadyDocumentsInScope lists ready documents in the given KB/conversation
// scope so callers can detect legacy documents lacking a document_texts row.
func ListReadyDocumentsInScope(ctx context.Context, db *sql.DB, kbIDs []string, convID string) ([]Document, error) {
	if db == nil {
		return nil, nil
	}
	where, args := documentScopeWhere("documents", kbIDs, convID)
	if len(where) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(kb_id,''), COALESCE(conversation_id,''), filename, mime_type, size_bytes, status, error, chunk_count, storage_path, COALESCE(uploaded_by_user_id,''), '', '', created_at
		   FROM documents
		  WHERE status='ready' AND (`+strings.Join(where, " OR ")+`)
		  ORDER BY created_at ASC, id ASC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

func documentScopeWhere(alias string, kbIDs []string, convID string) ([]string, []any) {
	where := []string{}
	args := []any{}
	if len(kbIDs) > 0 {
		ph := make([]string, len(kbIDs))
		for i, id := range kbIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		where = append(where, alias+`.kb_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if strings.TrimSpace(convID) != "" {
		where = append(where, alias+`.conversation_id=?`)
		args = append(args, strings.TrimSpace(convID))
	}
	return where, args
}

// migrateKnowledgeBasesOptionalEmbedding removes the legacy foreign-key
// requirement from knowledge_bases.embedding_model_id so Grep-mode knowledge
// bases can be created without configuring an embedding model.
func migrateKnowledgeBasesOptionalEmbedding(db *sql.DB) (err error) {
	if usePostgres {
		rows, qerr := db.Query(`
			SELECT c.conname
			  FROM pg_constraint c
			  JOIN pg_class t ON t.oid = c.conrelid
			  JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
			 WHERE t.relname = 'knowledge_bases'
			   AND c.contype = 'f'
			   AND a.attname = 'embedding_model_id'`)
		if qerr != nil {
			return qerr
		}
		var constraints []string
		for rows.Next() {
			var name string
			if serr := rows.Scan(&name); serr != nil {
				_ = rows.Close()
				return serr
			}
			constraints = append(constraints, name)
		}
		if cerr := errors.Join(rows.Err(), rows.Close()); cerr != nil {
			return cerr
		}
		for _, name := range constraints {
			quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
			if _, eerr := db.Exec(`ALTER TABLE knowledge_bases DROP CONSTRAINT IF EXISTS ` + quoted); eerr != nil {
				return eerr
			}
		}
		for _, stmt := range []string{
			`ALTER TABLE knowledge_bases ALTER COLUMN embedding_model_id SET DEFAULT ''`,
			`ALTER TABLE knowledge_bases ALTER COLUMN embedding_dim SET DEFAULT 0`,
		} {
			if _, eerr := db.Exec(stmt); eerr != nil {
				return eerr
			}
		}
		return nil
	}

	fkRows, err := db.Query(`PRAGMA foreign_key_list(knowledge_bases)`)
	if err != nil {
		return err
	}
	hasModelFK := false
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if serr := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); serr != nil {
			_ = fkRows.Close()
			return serr
		}
		if table == "models" && from == "embedding_model_id" {
			hasModelFK = true
		}
	}
	if cerr := errors.Join(fkRows.Err(), fkRows.Close()); cerr != nil {
		return cerr
	}
	if !hasModelFK {
		return nil
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if foreignKeys != 0 {
			_, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			err = errors.Join(err, restoreErr)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`CREATE TABLE knowledge_bases_optional_embedding (
			id                 TEXT PRIMARY KEY,
			user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name               TEXT NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			embedding_model_id TEXT NOT NULL DEFAULT '',
			embedding_dim      INTEGER NOT NULL DEFAULT 0,
			project_id         TEXT,
			created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			workspace_id       TEXT NOT NULL DEFAULT '',
			is_public          INTEGER NOT NULL DEFAULT 1
		)`,
		`INSERT INTO knowledge_bases_optional_embedding(id, user_id, name, description, embedding_model_id, embedding_dim, project_id, created_at, workspace_id, is_public)
		 SELECT id, user_id, name, COALESCE(description,''), COALESCE(embedding_model_id,''), COALESCE(embedding_dim,0), project_id, created_at, COALESCE(workspace_id,''), COALESCE(is_public,1)
		   FROM knowledge_bases`,
		`DROP TABLE knowledge_bases`,
		`ALTER TABLE knowledge_bases_optional_embedding RENAME TO knowledge_bases`,
		`CREATE INDEX IF NOT EXISTS idx_kbs_user ON knowledge_bases(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_kbs_workspace ON knowledge_bases(workspace_id)`,
		fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS idx_kbs_user_name_unique ON knowledge_bases(user_id, COALESCE(workspace_id,''), lower(trim(name)))`),
	} {
		if _, eerr := tx.ExecContext(ctx, stmt); eerr != nil {
			return eerr
		}
	}
	return tx.Commit()
}
