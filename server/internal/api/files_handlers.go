package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/store"
)

// errFileTooLarge is returned when an upload exceeds MaxUploadBytes (§C3).
var errFileTooLarge = errors.New("file exceeds the maximum upload size")

var (
	uploadRateLimitMax    = envcfg.Int("AURELIA_API_UPLOAD_RATE_LIMIT_MAX", 20)
	uploadRateLimitWindow = envcfg.Dur("AURELIA_API_UPLOAD_RATE_LIMIT_WINDOW", time.Minute)
	artifactCacheTTL      = envcfg.Dur("AURELIA_API_ARTIFACT_CACHE_TTL", 31536000*time.Second)
	uploadedFileCacheTTL  = envcfg.Dur("AURELIA_API_UPLOADED_FILE_CACHE_TTL", 86400*time.Second)
)

// uploadLimitBytes returns the byte ceiling for a file of the given kind, read
// from admin settings (max_image_upload_mb / max_file_upload_mb, in MB) and
// clamped to the MAX_UPLOAD_BYTES env ceiling (the absolute hard cap). A blank
// or non-positive setting falls back to the default: 5 MB for images, the env
// ceiling for everything else. Admins can thus only tighten a per-kind cap.
func uploadLimitBytes(d Deps, kind string) int64 {
	hard := d.Config.MaxUploadBytes
	key := "max_file_upload_mb"
	def := hard
	if kind == "image" {
		key = "max_image_upload_mb"
		if def = 5 << 20; def > hard {
			def = hard
		}
	}
	mb := settingPositiveInt(d, key)
	if mb <= 0 {
		return def
	}
	lim := int64(mb) << 20
	if lim > hard {
		lim = hard
	}
	return lim
}

// settingPositiveInt reads an admin setting the number input persists as a JSON
// number (or, defensively, a numeric string) and returns it, or 0 when the key
// is absent, blank, or invalid.
func settingPositiveInt(d Deps, key string) int {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n != 0 {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(s))
		return n
	}
	return 0
}

// uploadTooLargeError phrases a per-kind cap breach with the actual limit so the
// client can show "images must be under N MB" even when it skipped its own
// pre-check.
func uploadTooLargeError(kind string, limit int64) error {
	mb := limit / (1 << 20)
	if kind == "image" {
		return fmt.Errorf("image exceeds the maximum size of %d MB", mb)
	}
	return fmt.Errorf("file exceeds the maximum size of %d MB", mb)
}

// isForeignKeyErr matches the SQLite and PostgreSQL FK-violation messages —
// used to translate a conversation deleted mid-upload into a clean 404.
func isForeignKeyErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "foreign key constraint") || strings.Contains(low, "23503")
}

// uploadDestPath returns the per-user destination for a fresh upload. We
// keep one subdirectory per user under UPLOAD_DIR (`uploads/<userID>/…`)
// so the joined path component never has the cross-user collision shape
// the audit flagged ("alice_bob_xyz_file.pdf" vs "alice/bob_xyz_file.pdf"):
// a path traversal segment from user A's content can never resolve into
// user B's namespace because the OS-level boundary IS the subdirectory.
//
// The kind prefix ("d" for documents, "f" for files) keeps the two flows'
// IDs from colliding under the same user dir.
func uploadDestPath(d Deps, userID, kindPrefix, safeName string) (string, error) {
	dir := filepath.Join(d.Config.UploadDir, userID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, store.GenID(kindPrefix)+"_"+safeName), nil
}

// receiveDocument handles multipart or JSON-encoded document uploads. When
// the request is JSON, the "filename" and "content" fields are required —
// the server writes content to a real file on disk. Multipart support is
// also wired so a real frontend can upload binaries.
//
// Every code path runs the filename through `uploadPolicy.validateUpload`
// BEFORE allocating a destination, so rejected uploads never write bytes to
// the filesystem (§4.6 upload safety baseline).
//
// Returns the persisted store.Document, ready for RAG ingestion.
func receiveDocument(d Deps, r *http.Request, kbID, convID string) (*store.Document, error) {
	u := authUser(r)
	// mime.ParseMediaType handles uppercase, parameters, and whitespace per
	// RFC 7231. We previously hand-rolled a `ct[:16] == "application/json"`
	// check that rejected `APPLICATION/JSON` outright; that was a correctness
	// bug, not a security gap, but it cost us legitimate uppercase clients.
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("content-type"))
	policy := loadUploadPolicy(d)

	// JSON path — simpler for the frontend that mocks document text.
	if mediaType == "application/json" {
		var body struct {
			Filename string `json:"filename"`
			Content  string `json:"content"`
			MimeType string `json:"mime_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Filename == "" {
			return nil, errors.New("filename required")
		}
		// §C3 size cap on the JSON content path.
		if int64(len(body.Content)) > d.Config.MaxUploadBytes {
			return nil, errFileTooLarge
		}
		safe, _, verr := policy.validateUpload(body.Filename)
		if verr != nil {
			return nil, verr
		}
		path, err := uploadDestPath(d, u.ID, "d", safe)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(body.Content), 0o600); err != nil {
			return nil, err
		}
		doc := store.Document{
			KBID: kbID, ConversationID: convID, Filename: safe,
			MimeType: body.MimeType, SizeBytes: int64(len(body.Content)),
			Status: "pending", StoragePath: path,
		}
		return store.CreateDocument(r.Context(), d.DB, doc)
	}

	// Multipart path — for real uploads.
	if err := r.ParseMultipartForm(d.Config.MaxUploadBytes); err != nil {
		return nil, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	safe, _, verr := policy.validateUpload(header.Filename)
	if verr != nil {
		return nil, verr
	}
	path, err := uploadDestPath(d, u.ID, "d", safe)
	if err != nil {
		return nil, err
	}
	out, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	// §C3: bound the copy so an oversized upload can't fill the disk. ParseMultipartForm's
	// arg is only the in-memory threshold; the stream itself must be capped here.
	n, err := io.Copy(out, io.LimitReader(file, d.Config.MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if n > d.Config.MaxUploadBytes {
		_ = out.Close()
		_ = os.Remove(path)
		return nil, errFileTooLarge
	}
	doc := store.Document{
		KBID: kbID, ConversationID: convID, Filename: safe,
		MimeType: header.Header.Get("Content-Type"), SizeBytes: n,
		Status: "pending", StoragePath: path,
	}
	return store.CreateDocument(r.Context(), d.DB, doc)
}

// uploadFileHandler stores a file in /uploads and returns the metadata. Used
// by composers that want to attach a file to a single user message (without
// turning it into a knowledge-base document).
//
// Validation order matters: parse the multipart envelope (gives us the size
// cap via MaxUploadBytes), then check the filename through `uploadPolicy`
// BEFORE allocating the destination on disk. Invalid uploads never write
// bytes to the filesystem (§4.6).
func uploadFileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if !rateLimitUser(d, u.ID, "upload", uploadRateLimitMax, uploadRateLimitWindow) { // §C4
		writeError(w, 429, errUploadRateLimited)
		return
	}
	var conv string
	if v := r.URL.Query().Get("conversation_id"); v != "" {
		conv = v
	}
	// Validate the scope BEFORE writing anything. A dangling conversation id
	// used to surface as an opaque 500 (FK violation in store.CreateFile) —
	// and the route never checked ownership, so any authenticated user could
	// attach files (and rag=1 documents) into another user's conversation
	// retrieval scope. GetConversation is workspace-member-aware, so shared-
	// space uploads keep working.
	var scopeConv *store.Conversation
	if conv != "" {
		c, err := store.GetConversation(r.Context(), d.DB, conv, u.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errors.New("conversation not found"))
			return
		}
		if err != nil {
			writeError(w, 500, err)
			return
		}
		scopeConv = c
	}
	// §C3 hard cap: reject the whole request body past the limit (+overhead) so a
	// huge upload can't exhaust memory/disk before the per-file copy check.
	r.Body = http.MaxBytesReader(w, r.Body, d.Config.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(d.Config.MaxUploadBytes); err != nil {
		writeError(w, 413, errFileTooLarge)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	defer file.Close()
	policy := loadUploadPolicy(d)
	safe, _, verr := policy.validateUpload(header.Filename)
	if verr != nil {
		writeError(w, 400, verr)
		return
	}
	// §4.6 per-kind size cap (admin-tunable). Images inline to vision models as
	// base64, so they carry a tighter cap than documents; enforce it HERE so an
	// oversize file is rejected at upload instead of being silently dropped at
	// chat time. header.Size is the declared part size — a cheap reject before we
	// write our own copy; the post-copy check on n stays authoritative.
	kind := kindOf(header.Header.Get("Content-Type"), safe)
	limit := uploadLimitBytes(d, kind)
	if header.Size > limit {
		writeError(w, 413, uploadTooLargeError(kind, limit))
		return
	}
	path, err := uploadDestPath(d, u.ID, "f", safe)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	out, err := os.Create(path)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(file, limit+1))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n > limit {
		_ = out.Close()
		_ = os.Remove(path)
		writeError(w, 413, uploadTooLargeError(kind, limit))
		return
	}
	f, err := store.CreateFile(r.Context(), d.DB, store.File{
		UserID: u.ID, ConversationID: conv, Filename: safe,
		MimeType: header.Header.Get("Content-Type"), SizeBytes: n,
		Kind: kind, StoragePath: path,
		Draft: conv != "" && (r.URL.Query().Get("draft") == "1" || r.URL.Query().Get("draft") == "true"),
	})
	if err != nil {
		// The row never landed — don't leave the copied bytes orphaned on disk.
		_ = os.Remove(path)
		// TOCTOU with the scope check above: an explicit conversation deletion
		// can race a body that is still streaming. Surface that as the same 404
		// rather than an opaque FK 500.
		if isForeignKeyErr(err) {
			writeError(w, 404, errors.New("conversation not found"))
			return
		}
		writeError(w, 500, err)
		return
	}

	// §4.14 auto_add_uploads: when the file lands in a project conversation and
	// the project opted in, also register it as a project-KB document + ingest.
	// Best-effort — never fails the upload. Reuses the conversation fetched by
	// the scope check above.
	if scopeConv != nil && isDocKind(f.Kind) {
		if c := scopeConv; c.ProjectID != "" {
			if p, err := store.GetProject(r.Context(), d.DB, c.ProjectID, u.ID); err == nil && p.AutoAddUploads && p.KBID != "" {
				if doc, derr := store.CreateDocument(r.Context(), d.DB, store.Document{
					KBID: p.KBID, Filename: f.Filename, MimeType: f.MimeType,
					SizeBytes: f.SizeBytes, Status: "pending", StoragePath: f.StoragePath,
				}); derr == nil && doc != nil {
					d.RAG.Ingest(doc.ID)
				}
			}
		}
	}

	// §4.11.2 session-scoped temp documents (the third scope in "user uploads ∪
	// project KB ∪ session"). When the client passes `rag=1` on a conversation-
	// scoped upload, we also register a conversation-scoped Document and ingest
	// it. The chunks live ONLY for this conversation (cascade-deleted on conv
	// delete via FK), so they don't pollute the project KB.
	wantRAG := r.URL.Query().Get("rag")
	if conv != "" && isDocKind(f.Kind) && (wantRAG == "1" || wantRAG == "true") {
		if doc, derr := store.CreateDocument(r.Context(), d.DB, store.Document{
			ConversationID: conv, Filename: f.Filename, MimeType: f.MimeType,
			SizeBytes: f.SizeBytes, Status: "pending", StoragePath: f.StoragePath,
		}); derr == nil && doc != nil {
			// Surface the doc id so the client can poll its ingest status and
			// block the first send until it's 'ready' (§ chat uploads).
			f.DocumentID = doc.ID
			d.RAG.Ingest(doc.ID)
		}
	}
	// Surface a persistent download URL so the frontend can render thumbnails
	// after the local blob URL is revoked (§ user-bubble image preview).
	f.URL = "/api/files/" + f.ID
	writeJSON(w, 201, f)
}

// isDocKind reports whether a file kind should be RAG-ingested as a document.
// Spreadsheets ("sheet") are deliberately excluded: CSV/XLS(X) are data files
// analysed in the code sandbox (python_execute), never parsed or embedded.
// "code" IS ingested so the model can read & explain uploaded source files.
func isDocKind(kind string) bool {
	switch kind {
	case "pdf", "text", "doc", "code":
		return true
	}
	return false
}

// codeExts is the set of source-code / plain-text extensions we treat as "code"
// — readable text the model can explain. Kept broad so an uploaded .v (Verilog)
// or any common source file is recognized instead of falling through to "other"
// (which is neither ingested nor staged, so the model never sees it).
var codeExts = map[string]bool{
	".v": true, ".sv": true, ".svh": true, ".vh": true, ".vhd": true, ".vhdl": true,
	".c": true, ".h": true, ".cpp": true, ".cxx": true, ".cc": true, ".hpp": true, ".hh": true,
	".cs": true, ".java": true, ".kt": true, ".kts": true, ".swift": true, ".go": true, ".rs": true,
	".rb": true, ".php": true, ".py": true, ".pyw": true, ".js": true, ".jsx": true, ".ts": true,
	".tsx": true, ".mjs": true, ".cjs": true, ".vue": true, ".svelte": true,
	".sh": true, ".bash": true, ".zsh": true, ".ps1": true, ".bat": true, ".sql": true,
	".scala": true, ".r": true, ".jl": true, ".lua": true, ".pl": true, ".pm": true, ".dart": true,
	".ex": true, ".exs": true, ".erl": true, ".hrl": true, ".clj": true, ".hs": true,
	".ml": true, ".mli": true, ".fs": true, ".f90": true, ".asm": true, ".s": true,
	".proto": true, ".graphql": true, ".gql": true, ".tcl": true, ".groovy": true, ".gradle": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true,
	".conf": true, ".env": true, ".properties": true, ".xml": true, ".html": true, ".htm": true,
	".css": true, ".scss": true, ".sass": true, ".less": true, ".rst": true,
}

// kindOf maps mime + filename to one of the kind buckets the frontend uses.
func kindOf(mime, name string) string {
	switch {
	case len(mime) >= 6 && mime[:6] == "image/":
		return "image"
	case mime == "application/pdf":
		return "pdf"
	case len(mime) >= 4 && mime[:4] == "text":
		return "text"
	}
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".pdf":
		return "pdf"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".csv", ".tsv", ".xlsx", ".xls", ".xlsm":
		return "sheet"
	case ".docx", ".doc", ".pptx", ".ppt":
		return "doc"
	case ".txt", ".md", ".markdown", ".log":
		return "text"
	default:
		if codeExts[ext] {
			return "code"
		}
	}
	// Unknown extension → treat as plain text so it's still ingested and the
	// model can read it. Genuinely-binary uploads are caught at parse time
	// (isProbablyText) and degrade to a placeholder rather than garbage.
	return "text"
}

// safeMIMEType derives a server-controlled content type for a filename.
// It never returns a type from client-supplied data — this prevents stored XSS
// via uploaded HTML/SVG/JS served with an attacker-controlled Content-Type.
// Dangerous types (HTML, SVG, XML, JS) are forced to application/octet-stream
// so the browser downloads them rather than rendering/executing them inline.
func safeMIMEType(filename string) string {
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		return "application/octet-stream"
	}
	dangerousTypes := []string{
		"text/html", "image/svg+xml", "text/xml", "application/xml",
		"text/javascript", "application/javascript",
	}
	for _, dt := range dangerousTypes {
		if strings.HasPrefix(ct, dt) {
			return "application/octet-stream"
		}
	}
	return ct
}

// previewableInline reports whether a content type is safe to render inline in
// the browser (image preview, PDF viewer, plain text) so a preview pane / iframe
// shows it instead of triggering a download. safeMIMEType has ALREADY forced the
// dangerous types (HTML/SVG/XML/JS) to application/octet-stream, and we send
// X-Content-Type-Options: nosniff, so inline here stays XSS-safe.
func previewableInline(contentType string) bool {
	return strings.HasPrefix(contentType, "image/") ||
		contentType == "application/pdf" ||
		strings.HasPrefix(contentType, "text/")
}

// contentDispositionHeader builds an RFC 6266 header with BOTH an ASCII fallback
// (`filename=`) and an RFC 5987 UTF-8 form (`filename*=`), so non-ASCII names
// (e.g. Chinese) label/download correctly instead of as mojibake. The old code
// put raw UTF-8 bytes inside `filename="…"`, which browsers garble.
func contentDispositionHeader(disp, filename string) string {
	return fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s", disp, asciiFilenameFallback(filename), url.PathEscape(filename))
}

// asciiFilenameFallback replaces control/quote/non-ASCII runes with '_' for the
// legacy `filename=` parameter (modern browsers use the UTF-8 `filename*=`).
func asciiFilenameFallback(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "file"
	}
	return b.String()
}

// downloadArtifactHandler streams an artifact to the user with ownership
// check + correct content type. Artifacts are written into the artifact
// directory by tools (image_generate, python_execute via sandbox); the route
// is wired so real generated files can be served once the tools are
// integrated. Returns 404 when the row is missing or the file is gone.
func downloadArtifactHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	owner := u.ID
	if u.Role == "admin" {
		owner = "" // admins may view any user's artifacts (§ triage)
	}
	a, err := store.GetArtifact(r.Context(), d.DB, id, owner)
	if err != nil || a == nil {
		writeError(w, 404, errNotFound)
		return
	}
	serveStoredArtifact(d, w, a)
}

// serveStoredArtifact streams an artifact row's bytes with the standard safety
// headers. ACCESS CONTROL IS THE CALLER'S JOB — owner/admin auth on the private
// route, share-snapshot membership on the public share route (§ sharing).
func serveStoredArtifact(d Deps, w http.ResponseWriter, a *store.Artifact) {
	// Resolve a safe absolute path inside ArtifactDir.
	cleanName := filepath.Base(a.Filename)
	full := filepath.Clean(a.StoragePath)
	artDir := filepath.Clean(d.Config.ArtifactDir) + string(filepath.Separator)
	if full == "" || !strings.HasPrefix(full, artDir) {
		// Reject path traversal — only files under the configured artifact dir
		// can be served.
		writeError(w, 404, errNotFound)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Derive a server-side MIME type — never trust the client-supplied value
	// stored in DB, which enables stored XSS via HTML/SVG uploads (§ FIX-1).
	contentType := safeMIMEType(cleanName)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("content-type", contentType)
	w.Header().Set("content-length", strconv.FormatInt(info.Size(), 10))
	// Artifacts are immutable — an id maps to one generated file (image-model /
	// image_generate output, etc.) that never changes — so cache hard. The gallery
	// and chat re-render the same generated images constantly; without this every
	// <img> re-streams from the server (slow first paint, wasted bandwidth).
	// Private because artifacts are owner-scoped. (§ image caching)
	w.Header().Set("cache-control", fmt.Sprintf("private, max-age=%d, immutable", int(artifactCacheTTL.Seconds())))
	// Disposition: inline for browser-previewable, XSS-safe types (images, PDF,
	// plain text); attachment for everything else. RFC 6266 encoding keeps
	// non-ASCII names intact.
	disp := "attachment"
	if previewableInline(contentType) {
		disp = "inline"
	}
	w.Header().Set("content-disposition", contentDispositionHeader(disp, cleanName))
	_, _ = io.Copy(w, f)
}

// downloadFileHandler streams an uploaded file (multipart attachment) to its
// owner. Symmetric with downloadArtifactHandler but reads from `files` rather
// than `artifacts`. The user-bubble image preview + lightbox call this URL —
// the previous behaviour leaned on the local blob URL, which was revoked once
// the composer cleared its draft, leaving the gallery broken.
func downloadFileHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	// Admins may view any user's files (§ admin conversation triage); regular
	// users stay scoped to their own.
	owner := u.ID
	if u.Role == "admin" {
		owner = ""
	}
	f, err := store.GetFile(r.Context(), d.DB, id, owner)
	if err != nil || f == nil {
		writeError(w, 404, errNotFound)
		return
	}
	serveStoredFile(d, w, f)
}

// serveStoredFile streams an uploaded file row's bytes with the standard safety
// headers. ACCESS CONTROL IS THE CALLER'S JOB — owner/admin auth on the private
// route, share-snapshot membership on the public share route (§ sharing).
func serveStoredFile(d Deps, w http.ResponseWriter, f *store.File) {
	// Resolve a safe absolute path inside UploadDir.
	cleanName := filepath.Base(f.Filename)
	full := filepath.Clean(f.StoragePath)
	upDir := filepath.Clean(d.Config.UploadDir) + string(filepath.Separator)
	if full == "" || !strings.HasPrefix(full, upDir) {
		writeError(w, 404, errNotFound)
		return
	}
	fp, err := os.Open(full)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	defer fp.Close()
	info, err := fp.Stat()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Derive a server-side MIME type — never trust the client-supplied value
	// stored in DB, which enables stored XSS via HTML/SVG uploads (§ FIX-1).
	contentType := safeMIMEType(cleanName)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("content-type", contentType)
	w.Header().Set("content-length", strconv.FormatInt(info.Size(), 10))
	// Cache so a single conversation page's repeated image-tag fetches don't
	// re-stream the same file on every navigation. The file_id never collides,
	// so a long TTL is safe; we keep it private since the file is owner-scoped.
	w.Header().Set("cache-control", fmt.Sprintf("private, max-age=%d", int(uploadedFileCacheTTL.Seconds())))
	disp := "attachment"
	if previewableInline(contentType) {
		disp = "inline"
	}
	w.Header().Set("content-disposition", contentDispositionHeader(disp, cleanName))
	_, _ = io.Copy(w, fp)
}
