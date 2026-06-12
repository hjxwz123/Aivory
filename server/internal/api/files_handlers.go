package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aurelia/server/internal/store"
)

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
	n, err := io.Copy(out, file)
	if err != nil {
		return nil, err
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
	var conv string
	if v := r.URL.Query().Get("conversation_id"); v != "" {
		conv = v
	}
	if err := r.ParseMultipartForm(d.Config.MaxUploadBytes); err != nil {
		writeError(w, 400, err)
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
	n, err := io.Copy(out, file)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	f, err := store.CreateFile(r.Context(), d.DB, store.File{
		UserID: u.ID, ConversationID: conv, Filename: safe,
		MimeType: header.Header.Get("Content-Type"), SizeBytes: n,
		Kind: kindOf(header.Header.Get("Content-Type"), safe), StoragePath: path,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}

	// §4.14 auto_add_uploads: when the file lands in a project conversation and
	// the project opted in, also register it as a project-KB document + ingest.
	// Best-effort — never fails the upload.
	if conv != "" && isDocKind(f.Kind) {
		if c, err := store.GetConversation(r.Context(), d.DB, conv, u.ID); err == nil && c.ProjectID != "" {
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
			d.RAG.Ingest(doc.ID)
		}
	}
	writeJSON(w, 201, f)
}

// isDocKind reports whether a file kind should be RAG-ingested as a document.
func isDocKind(kind string) bool {
	switch kind {
	case "pdf", "text", "doc", "sheet":
		return true
	}
	return false
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
	switch ext := filepath.Ext(name); ext {
	case ".pdf":
		return "pdf"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".csv", ".xlsx":
		return "sheet"
	case ".docx":
		return "doc"
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs":
		return "code"
	case ".txt", ".md", ".markdown":
		return "text"
	}
	return "other"
}

// downloadArtifactHandler streams an artifact to the user with ownership
// check + correct content type. Artifacts are written into the artifact
// directory by tools (image_generate, python_execute via sandbox); the route
// is wired so real generated files can be served once the tools are
// integrated. Returns 404 when the row is missing or the file is gone.
func downloadArtifactHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	a, err := store.GetArtifact(r.Context(), d.DB, id, u.ID)
	if err != nil || a == nil {
		writeError(w, 404, errNotFound)
		return
	}
	// Resolve a safe absolute path inside ArtifactDir.
	cleanName := filepath.Base(a.Filename)
	full := a.StoragePath
	if full == "" || !strings.HasPrefix(full, d.Config.ArtifactDir) {
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
	mime := a.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("content-type", mime)
	w.Header().Set("content-length", strconv.FormatInt(info.Size(), 10))
	// Disposition: inline for images, attachment for everything else (per A12).
	disp := "attachment"
	if strings.HasPrefix(mime, "image/") {
		disp = "inline"
	}
	w.Header().Set("content-disposition", disp+`; filename="`+cleanName+`"`)
	_, _ = io.Copy(w, f)
}
