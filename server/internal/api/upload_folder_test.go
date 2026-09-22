package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// folderUploadFixture is the minimum state uploadFileHandler needs: an
// authenticated uploader with upload permission, and a conversation to scope
// the files to (folder contents are conversation files).
type folderUploadFixture struct {
	deps Deps
	user *store.User
	conv *store.Conversation
}

func seedFolderUploadFixture(t *testing.T) folderUploadFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "folder-upload.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','folder@example.com','h','user')`)
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Folder upload",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return folderUploadFixture{
		deps: Deps{
			DB: db, Cache: cache.NewMemory(),
			Config: config.Config{UploadDir: filepath.Join(t.TempDir(), "uploads"), MaxUploadBytes: 10 << 20},
		},
		user: &store.User{ID: "u1", Role: "user", Status: "active"},
		conv: conv,
	}
}

// folderUploadRequest posts ONE file of a folder batch, exactly the way the
// composer's directory picker does: the file plus folder_name and rel_path.
func folderUploadRequest(t *testing.T, user *store.User, filename string, data []byte, folder, relPath string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if folder != "" {
		if err := writer.WriteField("folder_name", folder); err != nil {
			t.Fatalf("write folder_name: %v", err)
		}
	}
	if relPath != "" {
		if err := writer.WriteField("rel_path", relPath); err != nil {
			t.Fatalf("write rel_path: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/files?conversation_id=c1&draft=1", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
}

func postFolderFile(t *testing.T, fx folderUploadFixture, filename, relPath string, data []byte) (*httptest.ResponseRecorder, store.File) {
	t.Helper()
	rec := httptest.NewRecorder()
	uploadFileHandler(fx.deps, rec, folderUploadRequest(t, fx.user, filename, data, "my-project", relPath))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload %s -> %d: %s", relPath, rec.Code, rec.Body.String())
	}
	var created store.File
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return rec, created
}

// A folder upload must remember each file's path inside the folder, prefixed
// with the folder's own name — that recorded path is what the sandbox stager and
// the folder manifest read on a later turn.
func TestFolderUploadPersistsRelativePath(t *testing.T) {
	fx := seedFolderUploadFixture(t)
	_, nested := postFolderFile(t, fx, "main.ts", "my-project/src/main.ts", []byte("export const x = 1\n"))

	if nested.RelPath != "my-project/src/main.ts" {
		t.Fatalf("rel_path = %q; want my-project/src/main.ts", nested.RelPath)
	}
	if nested.Filename != "main.ts" {
		t.Fatalf("filename = %q; want the basename main.ts", nested.Filename)
	}

	// Read it back through the store: the value must be durable, not just echoed.
	stored, err := store.GetFile(context.Background(), fx.deps.DB, nested.ID, "u1")
	if err != nil || stored == nil {
		t.Fatalf("get file: %v", err)
	}
	if stored.RelPath != "my-project/src/main.ts" {
		t.Fatalf("stored rel_path = %q; want my-project/src/main.ts", stored.RelPath)
	}
	// And through the conversation listing, which the drawer groups by.
	files, err := store.ListFilesByConversation(context.Background(), fx.deps.DB, "c1", "u1")
	if err != nil || len(files) != 1 {
		t.Fatalf("list files: %v (%d rows)", err, len(files))
	}
	if files[0].RelPath != "my-project/src/main.ts" {
		t.Fatalf("listed rel_path = %q", files[0].RelPath)
	}
}

// A plain single-file upload must keep rel_path empty: every existing consumer
// treats "" as "flat uploads/<filename>".
func TestSingleFileUploadKeepsEmptyRelativePath(t *testing.T) {
	fx := seedFolderUploadFixture(t)
	rec := httptest.NewRecorder()
	uploadFileHandler(fx.deps, rec, folderUploadRequest(t, fx.user, "notes.txt", []byte("hi"), "", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload -> %d: %s", rec.Code, rec.Body.String())
	}
	var created store.File
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.RelPath != "" {
		t.Fatalf("single-file rel_path = %q; want empty", created.RelPath)
	}
}

// The relative path is untrusted input that becomes a sandbox path, so every
// escape and malformed shape must be rejected before a byte is written.
func TestFolderUploadRejectsUnsafeRelativePaths(t *testing.T) {
	fx := seedFolderUploadFixture(t)
	cases := []struct {
		name    string
		folder  string
		relPath string
	}{
		{"traversal", "my-project", "my-project/../../etc/passwd"},
		{"nested traversal", "my-project", "my-project/src/../../../../evil.txt"},
		{"absolute", "my-project", "/etc/passwd"},
		{"empty segment", "my-project", "my-project//a.txt"},
		{"trailing separator", "my-project", "my-project/a.txt/"},
		{"dot segment", "my-project", "my-project/./a.txt"},
		{"path as folder name", "", "a/b.txt"},
		{"filename mismatch", "my-project", "my-project/other.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			uploadFileHandler(fx.deps, rec, folderUploadRequest(t, fx.user, "a.txt", []byte("x"), tc.folder, tc.relPath))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// A folder upload must NOT create RAG documents: a shared project is hundreds of
// files and the model reads them from the sandbox instead.
func TestFolderUploadSkipsRAGIngestion(t *testing.T) {
	fx := seedFolderUploadFixture(t)
	// rag=1 is what a single document upload sends; the folder path must ignore it.
	req := folderUploadRequest(t, fx.user, "readme.md", []byte("# hi"), "my-project", "my-project/readme.md")
	q := req.URL.Query()
	q.Set("rag", "1")
	req.URL.RawQuery = q.Encode()

	rec := httptest.NewRecorder()
	uploadFileHandler(fx.deps, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload -> %d: %s", rec.Code, rec.Body.String())
	}
	var created store.File
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.DocumentID != "" {
		t.Fatalf("folder file produced a RAG document %q; want none", created.DocumentID)
	}
	var count int
	if err := fx.deps.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM documents WHERE conversation_id='c1'`).Scan(&count); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("documents = %d; want 0 for a folder upload", count)
	}
}
