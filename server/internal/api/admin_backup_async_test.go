package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
)

func TestDeleteBackupArchiveAdmin(t *testing.T) {
	dir := t.TempDir()
	d := Deps{Config: config.Config{BackupDir: dir}}
	mux := newMux()
	mux.handle(http.MethodDelete, "/api/admin/backup/archives/:name", func(w http.ResponseWriter, r *http.Request) {
		deleteBackupArchiveAdmin(d, w, r)
	})

	name := "aivory-docker-backup-20260730-123456-0123456789.zip"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/backup/archives/"+name, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("archive still exists after delete: %v", err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/backup/archives/"+name, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing archive status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestDeleteBackupArchiveAdminRejectsUnsafeTargets(t *testing.T) {
	dir := t.TempDir()
	d := Deps{Config: config.Config{BackupDir: dir}}

	for _, name := range []string{
		"backup.zip",
		"aivory-docker-backup-20260730-123456-0123456789.zip.tmp",
		"../aivory-docker-backup-20260730-123456-0123456789.zip",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/admin/backup/archives/archive", nil)
			req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"name": name}))
			rec := httptest.NewRecorder()
			deleteBackupArchiveAdmin(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestDeleteBackupArchiveAdminRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.zip")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	name := "aivory-docker-backup-20260730-123456-abcdef0123.zip"
	link := filepath.Join(dir, name)
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	d := Deps{Config: config.Config{BackupDir: dir}}
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/backup/archives/archive", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"name": name}))
	rec := httptest.NewRecorder()
	deleteBackupArchiveAdmin(d, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlink status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was affected: %v", err)
	}
	if archives := listBackupArchiveFiles(dir); len(archives) != 0 {
		t.Fatalf("symlink appeared in archive list: %+v", archives)
	}
}

func TestBackupArchiveDeleteRouteRequiresAdmin(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "backup-delete-auth.db"))
	defer db.Close()
	d := Deps{DB: db, Config: config.Config{BackupDir: t.TempDir()}}
	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, httptest.NewRequest(
		http.MethodDelete,
		"/api/admin/backup/archives/aivory-docker-backup-20260730-123456-0123456789.zip",
		nil,
	))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
