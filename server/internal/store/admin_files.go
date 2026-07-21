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
	Type   string // ""/"all" | "pdf" | "document" | "presentation" | "spreadsheet" | "image" | "text" | "other"
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

// adminFileTypeExpression classifies every row in the files/documents union
// using only server-owned SQL and a fixed extension/MIME allowlist. The CASE is
// deliberately ordered so the categories are mutually exclusive: for example,
// text/csv is a spreadsheet rather than also appearing under text. LOWER,
// COALESCE, CASE, and LIKE are supported by both SQLite and PostgreSQL.
var adminFileTypeExpression = func() string {
	filename := "LOWER(COALESCE(t.filename,''))"
	mimeType := "LOWER(COALESCE(t.mime_type,''))"
	extensions := func(values ...string) string {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, filename+" LIKE '%."+value+"'")
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	mimeTypes := func(values ...string) string {
		parts := make([]string, 0, len(values)*2)
		for _, value := range values {
			parts = append(parts, mimeType+" = '"+value+"'", mimeType+" LIKE '"+value+";%'")
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}

	image := "(" + mimeType + " LIKE 'image/%' OR " + extensions(
		"png", "apng", "jpg", "jpeg", "jpe", "jfif", "gif", "webp", "bmp",
		"tif", "tiff", "heic", "heif", "avif", "ico", "cur", "jxl", "psd", "svg",
	) + ")"
	pdf := "(" + mimeTypes("application/pdf") + " OR " + extensions("pdf") + ")"
	document := "(" + mimeTypes("application/msword", "application/rtf", "application/vnd.ms-word.document.macroenabled.12", "application/vnd.ms-word.template.macroenabled.12", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.wordprocessingml.template", "application/vnd.oasis.opendocument.text", "text/rtf") + " OR " + extensions(
		"doc", "docx", "docm", "dot", "dotm", "dotx", "odt", "rtf",
	) + ")"
	presentation := "(" + mimeTypes("application/vnd.ms-powerpoint", "application/vnd.ms-powerpoint.presentation.macroenabled.12", "application/vnd.ms-powerpoint.slideshow.macroenabled.12", "application/vnd.ms-powerpoint.template.macroenabled.12", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.presentationml.slideshow", "application/vnd.openxmlformats-officedocument.presentationml.template", "application/vnd.oasis.opendocument.presentation") + " OR " + extensions(
		"ppt", "pptx", "pptm", "pot", "potm", "potx", "pps", "ppsm", "ppsx", "odp",
	) + ")"
	spreadsheet := "(" + mimeTypes("text/csv", "text/tab-separated-values", "application/csv", "application/vnd.ms-excel", "application/vnd.ms-excel.sheet.binary.macroenabled.12", "application/vnd.ms-excel.sheet.macroenabled.12", "application/vnd.ms-excel.template.macroenabled.12", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.template", "application/vnd.oasis.opendocument.spreadsheet") + " OR " + extensions(
		"csv", "tsv", "xls", "xlsx", "xlsm", "xlsb", "xlt", "xltm", "xltx", "ods",
	) + ")"
	text := "(" + mimeType + " LIKE 'text/%' OR " + mimeTypes("application/ecmascript", "application/javascript", "application/json", "application/ld+json", "application/sql", "application/toml", "application/x-httpd-php", "application/x-javascript", "application/x-sh", "application/x-yaml", "application/xhtml+xml", "application/xml") + " OR " + extensions(
		"txt", "md", "markdown", "log", "json", "jsonl", "xml", "yaml", "yml", "toml", "ini", "cfg", "conf", "env", "properties",
		"c", "h", "cc", "cpp", "cxx", "hpp", "cs", "java", "kt", "kts", "swift", "go", "rs", "rb", "php", "py", "pyw",
		"js", "jsx", "ts", "tsx", "mjs", "cjs", "vue", "svelte", "sh", "bash", "zsh", "fish", "ps1", "bat", "sql", "css", "scss", "sass", "less",
		"r", "scala", "lua", "pl", "dart", "ex", "exs", "erl", "clj", "hs", "fs", "proto", "graphql", "gql", "rst", "tex", "htm", "html",
	) + ")"

	return `(CASE
		WHEN ` + image + ` THEN 'image'
		WHEN ` + pdf + ` THEN 'pdf'
		WHEN ` + document + ` THEN 'document'
		WHEN ` + presentation + ` THEN 'presentation'
		WHEN ` + spreadsheet + ` THEN 'spreadsheet'
		WHEN ` + text + ` THEN 'text'
		ELSE 'other'
	END)`
}()

func normalizedAdminFileType(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "pdf", "document", "presentation", "spreadsheet", "image", "text", "other":
		return value
	default:
		// Blank, "all", and unknown caller input all mean no type filter. Never
		// interpolate the caller's value into the SQL expression.
		return ""
	}
}

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
	if fileType := normalizedAdminFileType(f.Type); fileType != "" {
		conds = append(conds, adminFileTypeExpression+" = ?")
		args = append(args, fileType)
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
