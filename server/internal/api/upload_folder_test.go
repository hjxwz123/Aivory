package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
	"aivory/server/internal/tools"
)

func folderUploadRequest(t *testing.T, userID, folder string, paths []string, names []string, files [][]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("folder_name", folder); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("paths", string(encoded)); err != nil {
		t.Fatal(err)
	}
	for index, data := range files {
		part, err := writer.CreateFormFile("file", names[index])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/sandbox/folders", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"}))
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"}))
}

func seedSandboxFolderDB(t *testing.T) *Deps {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "folder.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES
		('u1','u1@example.test','h','user','active'),
		('other','other@example.test','h','user','active')`)
	if _, err := store.CreateConversation(context.Background(), db, store.Conversation{ID: "c1", UserID: "u1", Title: "Folder"}); err != nil {
		t.Fatal(err)
	}
	d := &Deps{DB: db, Cache: cache.NewMemory(), Config: config.Config{UploadDir: filepath.Join(t.TempDir(), "uploads"), MaxUploadBytes: 40 << 20}}
	d.Tools = tools.NewRegistry(db, d.Config, log.New(io.Discard, "", 0))
	return d
}

func TestSandboxFolderUploadStoresOriginalBytesWithoutApplicationFiles(t *testing.T) {
	d := seedSandboxFolderDB(t)
	var uploaded []struct {
		Path string
		Data []byte
	}
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions":
			_, _ = io.WriteString(w, `{"session_id":"sid"}`)
		case "/files":
			var body struct {
				SessionID string `json:"session_id"`
				Path      string `json:"path"`
				Data      string `json:"data_base64"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			data, err := base64.StdEncoding.DecodeString(body.Data)
			if err != nil || body.SessionID != "sid" {
				t.Errorf("invalid sidecar body: %v", err)
			}
			uploaded = append(uploaded, struct {
				Path string
				Data []byte
			}{body.Path, data})
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer sidecar.Close()
	if err := store.SetSetting(d.DB, "sandbox_base_url", sidecar.URL); err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 1, 2, 255}
	req := folderUploadRequest(t, "u1", "project", []string{"project/src/main", "project/.env"}, []string{"main", ".env"}, [][]byte{data, []byte("SECRET=x")})
	rec := httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(uploaded) != 2 || uploaded[0].Path != "/workspace/folders/project/src/main" || !bytes.Equal(uploaded[0].Data, data) || uploaded[1].Path != "/workspace/folders/project/.env" {
		t.Fatalf("sidecar uploads=%+v", uploaded)
	}
	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM files WHERE conversation_id='c1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("application files=%d error=%v", count, err)
	}
	if entries, err := filepath.Glob(filepath.Join(d.Config.UploadDir, "*")); err != nil || len(entries) != 0 {
		t.Fatalf("upload directory entries=%v error=%v", entries, err)
	}
}

func TestSandboxFolderUploadRequiresSandboxAndSafePaths(t *testing.T) {
	d := seedSandboxFolderDB(t)
	if err := store.SetSetting(d.DB, "sandbox_base_url", ""); err != nil {
		t.Fatal(err)
	}
	req := folderUploadRequest(t, "u1", "project", []string{"project/file.txt"}, []string{"file.txt"}, [][]byte{[]byte("x")})
	rec := httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d", rec.Code)
	}
	if err := store.SetSetting(d.DB, "sandbox_base_url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"project/../file.txt", "project//file.txt", "project/src/../../file.txt", "/project/file.txt"} {
		rec = httptest.NewRecorder()
		uploadSandboxFolderHandler(*d, rec, folderUploadRequest(t, "u1", "project", []string{unsafe}, []string{"file.txt"}, [][]byte{[]byte("x")}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path=%q status=%d", unsafe, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, folderUploadRequest(t, "other", "project", []string{"project/file.txt"}, []string{"file.txt"}, [][]byte{[]byte("x")}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign conversation status=%d", rec.Code)
	}
}

func TestLegacyFileEndpointRejectsFolderMetadata(t *testing.T) {
	d := seedSandboxFolderDB(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("folder_name", "project")
	_ = writer.WriteField("rel_path", "project/file.txt")
	part, _ := writer.CreateFormFile("file", "file.txt")
	_, _ = part.Write([]byte("x"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/files?conversation_id=c1", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	uploadFileHandler(*d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("legacy folder status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSandboxFolderUploadIntersectsGroupAndWorkspacePermissions(t *testing.T) {
	d := seedSandboxFolderDB(t)
	if err := store.SetSetting(d.DB, "sandbox_base_url", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	request := func(userID string) *http.Request {
		return folderUploadRequest(t, userID, "project", []string{"project/a.txt"}, []string{"a.txt"}, [][]byte{[]byte("a")})
	}
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowFileUpload = false
	setIntersectionGroup(t, *d, "u1", permissions)
	rec := httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, request("u1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("group file-upload deny status=%d", rec.Code)
	}
	permissions.AllowFileUpload = true
	permissions.Tools.Mode = store.ResourceAccessNone
	setIntersectionGroup(t, *d, "u1", permissions)
	rec = httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, request("u1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("group tool deny status=%d", rec.Code)
	}
	permissions = store.DefaultUserGroupPermissions()
	setIntersectionGroup(t, *d, "u1", permissions)
	workspace, err := store.CreateWorkspace(t.Context(), d.DB, "u1", "Folder permissions")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d.DB, `UPDATE conversations SET workspace_id=?, is_public=1 WHERE id='c1'`, workspace.ID)
	denied := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspace.ID, "u1", store.WorkspacePolicyPatch{AllowToolCalling: &denied}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, request("u1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("workspace tool deny status=%d", rec.Code)
	}
	allowed := true
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspace.ID, "u1", store.WorkspacePolicyPatch{AllowToolCalling: &allowed}); err != nil {
		t.Fatal(err)
	}
	legacySandbox := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspace.ID, "u1", store.WorkspacePolicyPatch{AllowSandbox: &legacySandbox}); err != nil {
		t.Fatal(err)
	}
	if status, err := sandboxFolderAccess(*d, request("u1"), "c1"); err != nil || status != 0 {
		t.Fatalf("retired sandbox switch denied upload: status=%d error=%v", status, err)
	}
	allowedTools := []string{"builtin:web_search"}
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspace.ID, "u1", store.WorkspacePolicyPatch{AllowedToolIDs: &allowedTools}); err != nil {
		t.Fatal(err)
	}
	if status, err := sandboxFolderAccess(*d, request("u1"), "c1"); err == nil || status != http.StatusForbidden {
		t.Fatalf("workspace tool allowlist status=%d error=%v", status, err)
	}
	if err := store.JoinWorkspace(t.Context(), d.DB, workspace.ID, "other"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d.DB, `UPDATE workspace_members SET role='guest' WHERE workspace_id=? AND user_id='other'`, workspace.ID)
	rec = httptest.NewRecorder()
	uploadSandboxFolderHandler(*d, rec, request("other"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("workspace guest status=%d", rec.Code)
	}
}
