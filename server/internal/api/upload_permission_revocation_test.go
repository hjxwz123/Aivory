package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

type callbackReader struct {
	once     sync.Once
	reader   io.Reader
	callback func()
	called   bool
}

func (reader *callbackReader) Read(buffer []byte) (int, error) {
	reader.once.Do(func() {
		reader.called = true
		reader.callback()
	})
	return reader.reader.Read(buffer)
}

func TestUploadRevokedWhileReadingBodyLeavesNoFile(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "upload-revocation.db"))
	t.Cleanup(func() { _ = db.Close() })
	allowedJSON, err := json.Marshal(store.DefaultUserGroupPermissions())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions)
		VALUES('upload-group','Upload group',?)`, string(allowedJSON))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id)
		VALUES('upload-revoked','upload-revoked@example.test','Uploader','h','user','active','upload-group')`)

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, err := writer.CreateFormFile("file", "large-notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("permission boundary\n"), 4096)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	revoked := store.DefaultUserGroupPermissions()
	revoked.AllowFileUpload = false
	revokedJSON, err := json.Marshal(revoked)
	if err != nil {
		t.Fatal(err)
	}
	var revocationErr error
	var revocationObserved bool
	reader := &callbackReader{
		reader: bytes.NewReader(multipartBody.Bytes()),
		callback: func() {
			result, err := db.ExecContext(context.Background(),
				`UPDATE user_groups SET permissions=? WHERE id='upload-group'`, string(revokedJSON))
			revocationErr = err
			if revocationErr != nil {
				return
			}
			affected, rowsErr := result.RowsAffected()
			revocationErr = rowsErr
			if revocationErr != nil {
				return
			}
			if affected != 1 {
				revocationErr = store.ErrNotFound
				return
			}
			current, err := store.UserGroupPermissionsForUser(context.Background(), db, "upload-revoked")
			revocationErr = err
			revocationObserved = err == nil && !current.AllowFileUpload
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/files", reader)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
		ID: "upload-revoked", Role: "user", Status: "active", GroupID: "upload-group",
	}))

	uploadDir := filepath.Join(t.TempDir(), "uploads")
	deps := Deps{
		DB: db, Cache: cache.NewMemory(),
		Config: config.Config{UploadDir: uploadDir, MaxUploadBytes: 2 << 20},
	}
	handler := requireCapabilityHandler(
		errFileUploadGroupPermission,
		func(permissions store.UserGroupPermissions) bool { return permissions.AllowFileUpload },
		uploadFileHandler,
	)
	recorder := httptest.NewRecorder()
	handler(deps, recorder, req)

	if !reader.called {
		t.Fatal("request body reader callback was not invoked")
	}
	if revocationErr != nil {
		t.Fatalf("revoke upload permission: %v", revocationErr)
	}
	if !revocationObserved {
		t.Fatal("body reader did not observe the revoked upload permission")
	}
	if recorder.Code != http.StatusForbidden ||
		!strings.Contains(recorder.Body.String(), errFileUploadGroupPermission.Error()) {
		t.Fatalf("status=%d body=%s, want revoked upload permission", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE user_id='upload-revoked'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted files=%d, want none", count)
	}
	userDir := filepath.Join(uploadDir, "upload-revoked")
	entries, err := os.ReadDir(userDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("upload directory contains %d residual files", len(entries))
	}
}
