package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestBackupExportRejectsReferencedMissingFile(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	artifacts := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	db := openMigrated(t, filepath.Join(root, "source.db"))
	t.Cleanup(func() { _ = db.Close() })
	missing := filepath.Join(uploads, "u1", "missing.txt")
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,storage_path) VALUES('f1','u1','missing.txt',?)`, missing)

	tx, err := beginBackupSnapshot(t.Context(), db)
	if err != nil {
		t.Fatalf("begin backup snapshot: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var archive bytes.Buffer
	_, err = writeBackupArchive(t.Context(), Deps{
		DB: db, Config: config.Config{UploadDir: uploads, ArtifactDir: artifacts}, Logger: log.New(io.Discard, "", 0),
	}, tx, &archive, backupArchiveOptions{IncludeFiles: true})
	if err == nil || !strings.Contains(err.Error(), "references a file missing from the archive") {
		t.Fatalf("backup error=%v, want missing referenced file failure", err)
	}
}

func TestV3BackupImportRejectsIncompleteOrTamperedArchiveBeforeWipe(t *testing.T) {
	archive := exportIntegrityArchiveForTest(t)
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
	}{
		{
			name: "missing database table",
			mutate: func(t *testing.T, data []byte) []byte {
				return rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
					return name != "db/messages.jsonl", content
				})
			},
		},
		{
			name: "tampered uploaded file",
			mutate: func(t *testing.T, data []byte) []byte {
				return rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
					if strings.HasPrefix(name, backupZipUploads) {
						return true, []byte("tampered")
					}
					return true, content
				})
			},
		},
		{
			name: "self-consistent manifest missing referenced file",
			mutate: func(t *testing.T, data []byte) []byte {
				const missing = backupZipUploads + "u1/file.txt"
				return rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
					if name == missing {
						return false, nil
					}
					if name != "manifest.json" {
						return true, content
					}
					var man backupManifest
					if err := json.Unmarshal(content, &man); err != nil {
						t.Fatalf("decode manifest: %v", err)
					}
					delete(man.Entries, missing)
					updated, err := json.Marshal(man)
					if err != nil {
						t.Fatalf("encode manifest: %v", err)
					}
					return true, updated
				})
			},
		},
		{
			name: "manifest row count mismatch",
			mutate: func(t *testing.T, data []byte) []byte {
				return rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
					if name != "manifest.json" {
						return true, content
					}
					var man backupManifest
					if err := json.Unmarshal(content, &man); err != nil {
						t.Fatalf("decode manifest: %v", err)
					}
					man.Counts["users"]++
					updated, err := json.Marshal(man)
					if err != nil {
						t.Fatalf("encode manifest: %v", err)
					}
					return true, updated
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newBackupAdminFixture(t, false)
			mustExec(t, d.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
			body, contentType := multipartArchive(t, tc.mutate(t, archive))
			req := httptest.NewRequest(http.MethodPost, "/api/admin/backup/import", body)
			req.Header.Set("content-type", contentType)
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
				ID: "adm", Role: "admin", Status: "active",
			}))
			rec := httptest.NewRecorder()
			importBackupAdmin(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("import status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), errInvalidBackupArchive.Error()) {
				t.Fatalf("import error=%s, want archive integrity error", rec.Body.String())
			}
			var sentinel string
			if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&sentinel); err != nil || sentinel != "keep" {
				t.Fatalf("target changed before rejected import: sentinel=%q err=%v", sentinel, err)
			}
		})
	}
}

func TestBackupFileRestorePublishFailureRestoresPreviousTree(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	artifacts := filepath.Join(root, "artifacts")
	writeFile(t, filepath.Join(uploads, "old.txt"), []byte("old upload"))
	if err := os.WriteFile(artifacts, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write artifact blocker: %v", err)
	}

	zr := zipReaderForEntries(t, map[string][]byte{
		backupZipUploads + "new.txt":   []byte("new upload"),
		backupZipArtifacts + "new.txt": []byte("new artifact"),
	})
	restore, err := prepareBackupFileRestore(Deps{Config: config.Config{
		UploadDir: uploads, ArtifactDir: artifacts,
	}}, zr, backupManifest{IncludesFiles: true})
	if err != nil {
		t.Fatalf("prepare file restore: %v", err)
	}
	defer func() { _ = restore.Rollback() }()
	if err := restore.Apply(); err == nil {
		t.Fatal("publish succeeded despite artifact target being a regular file")
	}
	content, err := os.ReadFile(filepath.Join(uploads, "old.txt"))
	if err != nil || string(content) != "old upload" {
		t.Fatalf("previous upload tree was not restored: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(uploads, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("partially published upload survived rollback: %v", err)
	}
	content, err = os.ReadFile(artifacts)
	if err != nil || string(content) != "not a directory" {
		t.Fatalf("artifact blocker changed: content=%q err=%v", content, err)
	}
}

func TestBackupQdrantRestoreFailureRestoresPreviousCollections(t *testing.T) {
	qdrant := newFakeQdrant(t)
	oldPoint := qdrantDumpPoint{
		ID: json.RawMessage(`"old"`), Vector: json.RawMessage(`[1,0]`), Payload: json.RawMessage(`{"chunk_id":"old"}`),
	}
	newPoint := qdrantDumpPoint{
		ID: json.RawMessage(`"new"`), Vector: json.RawMessage(`[0,1]`), Payload: json.RawMessage(`{"chunk_id":"new"}`),
	}
	qdrant.setPoints("aivory_c2", []qdrantDumpPoint{oldPoint})
	zr := qdrantArchiveReaderForTest(t, "aivory_c2", 2, []qdrantDumpPoint{newPoint})
	d := Deps{Config: config.Config{QdrantURL: qdrant.url}}

	unlock := lockQdrantArchiveIfNeeded(d, true)
	defer unlock()
	restore, err := prepareBackupQdrantRestore(t.Context(), d, zr)
	if err != nil {
		t.Fatalf("prepare Qdrant restore: %v", err)
	}
	defer func() { _ = restore.Commit() }()
	qdrant.failNextPointUpsert()
	if err := restore.Apply(t.Context()); err == nil {
		t.Fatal("Qdrant restore succeeded despite injected upsert failure")
	}
	got := qdrant.pointsFor("aivory_c2")
	if len(got) != 1 || string(got[0].ID) != string(oldPoint.ID) || string(got[0].Vector) != string(oldPoint.Vector) {
		t.Fatalf("previous Qdrant collection was not restored: %+v", got)
	}
}

func TestBackupQdrantExportRejectsInvalidPoint(t *testing.T) {
	qdrant := newFakeQdrant(t)
	qdrant.setPoints("aivory_c2", []qdrantDumpPoint{{
		ID: json.RawMessage(`"invalid"`), Vector: json.RawMessage(`null`), Payload: json.RawMessage(`{}`),
	}})
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	_, err := exportQdrantToZip(t.Context(), Deps{Config: config.Config{QdrantURL: qdrant.url}}, zw)
	_ = zw.Close()
	if err == nil || !strings.Contains(err.Error(), "invalid point") {
		t.Fatalf("Qdrant export error=%v, want invalid point failure", err)
	}
}

func TestBackupImportQdrantFailureRollsBackDatabaseAndVectors(t *testing.T) {
	qdrant := newFakeQdrant(t)
	newPoint := qdrantDumpPoint{
		ID: json.RawMessage(`"new"`), Vector: json.RawMessage(`[0,1]`), Payload: json.RawMessage(`{"chunk_id":"new"}`),
	}
	oldPoint := qdrantDumpPoint{
		ID: json.RawMessage(`"old"`), Vector: json.RawMessage(`[1,0]`), Payload: json.RawMessage(`{"chunk_id":"old"}`),
	}
	qdrant.setPoints("aivory_c2", []qdrantDumpPoint{newPoint})

	sourceRoot := t.TempDir()
	sourceDB := openMigrated(t, filepath.Join(sourceRoot, "source.db"))
	t.Cleanup(func() { _ = sourceDB.Close() })
	sourceDeps := Deps{
		DB: sourceDB,
		Config: config.Config{
			UploadDir: filepath.Join(sourceRoot, "uploads"), ArtifactDir: filepath.Join(sourceRoot, "artifacts"), QdrantURL: qdrant.url,
		},
		Logger: log.New(io.Discard, "", 0),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup/export?qdrant=true", nil)
	exportBackupAdmin(sourceDeps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body.String())
	}
	archive := append([]byte(nil), rec.Body.Bytes()...)

	target := newBackupAdminFixture(t, false)
	target.Config.QdrantURL = qdrant.url
	mustExec(t, target.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
	qdrant.setPoints("aivory_c2", []qdrantDumpPoint{oldPoint})
	qdrant.failNextPointUpsert()

	body, contentType := multipartArchive(t, archive)
	importReq := httptest.NewRequest(http.MethodPost, "/api/admin/backup/import", body)
	importReq.Header.Set("content-type", contentType)
	importReq = importReq.WithContext(context.WithValue(importReq.Context(), userCtxKey{}, &store.User{
		ID: "adm", Role: "admin", Status: "active",
	}))
	importRec := httptest.NewRecorder()
	importBackupAdmin(target, importRec, importReq)
	if importRec.Code != http.StatusInternalServerError {
		t.Fatalf("import status=%d body=%s, want 500", importRec.Code, importRec.Body.String())
	}
	var sentinel string
	if err := target.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&sentinel); err != nil || sentinel != "keep" {
		t.Fatalf("database was not rolled back: sentinel=%q err=%v", sentinel, err)
	}
	got := qdrant.pointsFor("aivory_c2")
	if len(got) != 1 || string(got[0].ID) != string(oldPoint.ID) || string(got[0].Vector) != string(oldPoint.Vector) {
		t.Fatalf("Qdrant was not rolled back: %+v", got)
	}
}

func TestBackupImportFilePublishFailureRollsBackDatabaseAndFiles(t *testing.T) {
	archive := exportIntegrityArchiveForTest(t)
	target := newBackupAdminFixture(t, false)
	mustExec(t, target.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
	writeFile(t, filepath.Join(target.Config.UploadDir, "old.txt"), []byte("old upload"))
	if err := os.WriteFile(target.Config.ArtifactDir, []byte("artifact blocker"), 0o600); err != nil {
		t.Fatalf("write artifact blocker: %v", err)
	}

	body, contentType := multipartArchive(t, archive)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/backup/import", body)
	req.Header.Set("content-type", contentType)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
		ID: "adm", Role: "admin", Status: "active",
	}))
	rec := httptest.NewRecorder()
	importBackupAdmin(target, rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("import status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
	var sentinel string
	if err := target.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&sentinel); err != nil || sentinel != "keep" {
		t.Fatalf("database was not rolled back: sentinel=%q err=%v", sentinel, err)
	}
	content, err := os.ReadFile(filepath.Join(target.Config.UploadDir, "old.txt"))
	if err != nil || string(content) != "old upload" {
		t.Fatalf("previous upload tree was not restored: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(target.Config.UploadDir, "u1", "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("incoming upload survived failed restore: %v", err)
	}
	content, err = os.ReadFile(target.Config.ArtifactDir)
	if err != nil || string(content) != "artifact blocker" {
		t.Fatalf("artifact target changed: content=%q err=%v", content, err)
	}
}

func exportIntegrityArchiveForTest(t *testing.T) []byte {
	t.Helper()
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	artifacts := filepath.Join(root, "artifacts")
	writeFile(t, filepath.Join(uploads, "u1", "file.txt"), []byte("original"))
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	db := openMigrated(t, filepath.Join(root, "source.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','hash','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,storage_path) VALUES('f1','u1','file.txt',?)`, filepath.Join(uploads, "u1", "file.txt"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup/export?files=1&qdrant=false", nil)
	exportBackupAdmin(Deps{DB: db, Config: config.Config{UploadDir: uploads, ArtifactDir: artifacts}}, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body.String())
	}
	return append([]byte(nil), rec.Body.Bytes()...)
}

func rewriteBackupZipForTest(t *testing.T, data []byte, mutate func(string, []byte) (bool, []byte)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open source ZIP: %v", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range zr.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatalf("open %s: %v", entry.Name, err)
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name, err)
		}
		keep, content := mutate(entry.Name, content)
		if !keep {
			continue
		}
		writer, err := zw.Create(entry.Name)
		if err != nil {
			t.Fatalf("create %s: %v", entry.Name, err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatalf("write %s: %v", entry.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close rewritten ZIP: %v", err)
	}
	return out.Bytes()
}

func zipReaderForEntries(t *testing.T, entries map[string][]byte) *zip.Reader {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, content := range entries {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	data := append([]byte(nil), archive.Bytes()...)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}
	return zr
}

func qdrantArchiveReaderForTest(t *testing.T, name string, dim int, points []qdrantDumpPoint) *zip.Reader {
	t.Helper()
	entryName := qdrantZipCollectionDir + name + ".jsonl"
	var rows bytes.Buffer
	enc := json.NewEncoder(&rows)
	for _, point := range points {
		if err := enc.Encode(point); err != nil {
			t.Fatalf("encode Qdrant point: %v", err)
		}
	}
	manifest, err := json.Marshal(qdrantArchiveManifest{
		Format: "aivory-qdrant", Version: qdrantArchiveVersion,
		Collections: []qdrantCollectionArchive{{Name: name, Dim: dim, Entry: entryName, Points: int64(len(points))}},
	})
	if err != nil {
		t.Fatalf("encode Qdrant manifest: %v", err)
	}
	return zipReaderForEntries(t, map[string][]byte{
		qdrantZipManifest: manifest,
		entryName:         rows.Bytes(),
	})
}
