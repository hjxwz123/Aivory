package store

import (
	"context"
	"database/sql"
	"strings"
)

// AdminFile is one row of the admin "all uploaded files" view: the union of
// the files table (conversation attachments) and the documents table (KB and
// conversation RAG documents). Conversation documents that share a storage
// path with a files row are folded into that row — deleting the file already
// removes them, so listing both would show the same physical upload twice.
type AdminFile struct {
	ID             string `json:"id"`
	Source         string `json:"source"` // "file" (files table) | "document" (documents table)
	Origin         string `json:"origin"` // "conversation" | "kb"
	UserID         string `json:"user_id"`
	UserEmail      string `json:"user_email"`
	UserName       string `json:"user_name"`
	Filename       string `json:"filename"`
	MimeType       string `json:"mime_type"`
	SizeBytes      int64  `json:"size_bytes"`
	CreatedAt      int64  `json:"created_at"`
	ConversationID string `json:"conversation_id"`
	KBID           string `json:"kb_id"`
	KBName         string `json:"kb_name"`
}

// AdminFileFilter narrows ListAdminFiles / CountAdminFiles.
type AdminFileFilter struct {
	Search string // case-insensitive filename substring
	UserID string // exact owner match
	// UserQ matches the owner by user_id exactly OR email/name substring
	// (case-insensitive) — same semantics as the usage page's user filter, so
	// the admin can type instead of scrolling a dropdown of every user.
	UserQ  string
	Origin string // "" (all) | "conversation" | "kb"
	Sort   string // "created_at" (default) | "size_bytes" | "filename"
	Order  string // "desc" (default) | "asc"
}

// adminFilesBaseQuery is the union both List and Count select from. documents
// rows whose storage path is also a files row are excluded (see AdminFile).
const adminFilesBaseQuery = `
SELECT f.id AS id, 'file' AS source, 'conversation' AS origin,
       f.user_id AS user_id, COALESCE(u.email,'') AS user_email, COALESCE(u.name,'') AS user_name,
       f.filename AS filename, f.mime_type AS mime_type, f.size_bytes AS size_bytes, f.created_at AS created_at,
       COALESCE(f.conversation_id,'') AS conversation_id, '' AS kb_id, '' AS kb_name
  FROM files f
  LEFT JOIN users u ON u.id = f.user_id
UNION ALL
SELECT d.id, 'document',
       CASE WHEN COALESCE(d.kb_id,'') <> '' THEN 'kb' ELSE 'conversation' END,
       COALESCE(k.user_id, c.user_id, ''), COALESCE(u2.email,''), COALESCE(u2.name,''),
       d.filename, d.mime_type, d.size_bytes, d.created_at,
       COALESCE(d.conversation_id,''), COALESCE(d.kb_id,''), COALESCE(k.name,'')
  FROM documents d
  LEFT JOIN knowledge_bases k ON k.id = d.kb_id
  LEFT JOIN conversations c ON c.id = d.conversation_id
  LEFT JOIN users u2 ON u2.id = COALESCE(k.user_id, c.user_id)
 WHERE NOT EXISTS (SELECT 1 FROM files f2 WHERE f2.storage_path = d.storage_path)
`

func adminFilesWhere(f AdminFileFilter) (string, []any) {
	conds := []string{}
	args := []any{}
	if s := strings.TrimSpace(f.Search); s != "" {
		conds = append(conds, "LOWER(t.filename) LIKE ?")
		args = append(args, "%"+strings.ToLower(s)+"%")
	}
	if f.UserID != "" {
		conds = append(conds, "t.user_id = ?")
		args = append(args, f.UserID)
	}
	if q := strings.TrimSpace(f.UserQ); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		conds = append(conds, "(t.user_id = ? OR LOWER(t.user_email) LIKE ? OR LOWER(t.user_name) LIKE ?)")
		args = append(args, q, like, like)
	}
	if f.Origin == "conversation" || f.Origin == "kb" {
		conds = append(conds, "t.origin = ?")
		args = append(args, f.Origin)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// adminFilesOrder whitelists sort columns — never interpolate caller input.
func adminFilesOrder(f AdminFileFilter) string {
	col := "created_at"
	switch f.Sort {
	case "size_bytes", "filename":
		col = f.Sort
	}
	dir := "DESC"
	if f.Order == "asc" {
		dir = "ASC"
	}
	// Stable tiebreaker so pagination never skips or repeats rows.
	return " ORDER BY t." + col + " " + dir + ", t.id " + dir
}

func ListAdminFiles(ctx context.Context, db *sql.DB, filter AdminFileFilter, limit, offset int) ([]AdminFile, error) {
	where, args := adminFilesWhere(filter)
	q := "SELECT t.* FROM (" + adminFilesBaseQuery + ") t" + where + adminFilesOrder(filter) + " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminFile{}
	for rows.Next() {
		var a AdminFile
		if err := rows.Scan(&a.ID, &a.Source, &a.Origin, &a.UserID, &a.UserEmail, &a.UserName,
			&a.Filename, &a.MimeType, &a.SizeBytes, &a.CreatedAt,
			&a.ConversationID, &a.KBID, &a.KBName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func CountAdminFiles(ctx context.Context, db *sql.DB, filter AdminFileFilter) (int, error) {
	where, args := adminFilesWhere(filter)
	q := "SELECT COUNT(*) FROM (" + adminFilesBaseQuery + ") t" + where
	var n int
	err := db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// AdminDeleteFile removes a files row regardless of owner (admin-only caller).
// Returns ErrNotFound when the row doesn't exist. RAG/storage cleanup is the
// API layer's job, mirroring deleteConversationFileHandler.
func AdminDeleteFile(ctx context.Context, db *sql.DB, id string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM files WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
