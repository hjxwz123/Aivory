package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func mutateBackupManifestForTest(t *testing.T, archive []byte, mutate func(*backupManifest)) []byte {
	t.Helper()
	return rewriteBackupZipForTest(t, archive, func(name string, content []byte) (bool, []byte) {
		if name != "manifest.json" {
			return true, content
		}
		var man backupManifest
		if err := json.Unmarshal(content, &man); err != nil {
			t.Fatal(err)
		}
		mutate(&man)
		encoded, err := json.Marshal(man)
		if err != nil {
			t.Fatal(err)
		}
		return true, encoded
	})
}

func importBackupForCompatibilityTest(t *testing.T, d Deps, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartArchive(t, archive)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/backup/import", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "adm", Role: "admin", Status: "active"}))
	rec := httptest.NewRecorder()
	importBackupAdmin(d, rec, req)
	return rec
}

func TestV3BackupImportSupportsHistoricalSchemaAndOrder(t *testing.T) {
	archive := exportIntegrityArchiveForTest(t)
	for _, originalSchema := range []bool{false, true} {
		name := "current tables in historical order"
		if originalSchema {
			name = "original v3 schema before later features"
		}
		t.Run(name, func(t *testing.T) {
			data := archive
			if originalSchema {
				data = rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
					if strings.HasPrefix(name, "db/") {
						table := strings.TrimSuffix(strings.TrimPrefix(name, "db/"), ".jsonl")
						return slices.Contains(backupV3RequiredTables, table), content
					}
					return true, content
				})
			}
			data = mutateBackupManifestForTest(t, data, func(man *backupManifest) {
				if originalSchema {
					man.Tables = slices.Clone(backupV3RequiredTables)
					for table := range man.Counts {
						if !slices.Contains(man.Tables, table) {
							delete(man.Counts, table)
							delete(man.Entries, "db/"+table+".jsonl")
						}
					}
				} else {
					// The source order is descriptive; restore must use the target's FK order.
					slices.Reverse(man.Tables)
				}
			})
			d := newBackupAdminFixture(t, false)
			rec := importBackupForCompatibilityTest(t, d, data)
			if rec.Code != http.StatusOK {
				t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
			}
			var email, path string
			if err := d.DB.QueryRow(`SELECT email FROM users WHERE id='u1'`).Scan(&email); err != nil || email != "u1@example.test" {
				t.Fatalf("user=%q err=%v", email, err)
			}
			if err := d.DB.QueryRow(`SELECT storage_path FROM files WHERE id='f1'`).Scan(&path); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "original" {
				t.Fatalf("restored file=%q err=%v", content, err)
			}
			var count int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM registration_domains`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("new table count=%d err=%v", count, err)
			}
			assertOneRestoredAdmin(t, d.DB, "admin@example.test", "adm", "current-admin-hash")
		})
	}
}

func TestV3BackupImportRejectsInvalidTableDeclarationsBeforeWipe(t *testing.T) {
	archive := exportIntegrityArchiveForTest(t)
	cases := []struct {
		name   string
		mutate func(*backupManifest)
	}{
		{"duplicate table", func(m *backupManifest) { m.Tables = append(m.Tables, "users") }},
		{"unknown table", func(m *backupManifest) { m.Tables = append(m.Tables, "unknown_table"); m.Counts["unknown_table"] = 0 }},
		{"missing baseline table", func(m *backupManifest) {
			m.Tables = slices.DeleteFunc(m.Tables, func(s string) bool { return s == "users" })
			delete(m.Counts, "users")
		}},
		{"empty table list", func(m *backupManifest) { m.Tables = nil; m.Counts = nil }},
		{"undeclared optional database entry", func(m *backupManifest) {
			m.Tables = slices.DeleteFunc(m.Tables, func(s string) bool { return s == "passkeys" })
			delete(m.Counts, "passkeys")
		}},
		{"extra row count", func(m *backupManifest) { m.Counts["unknown_table"] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newBackupAdminFixture(t, false)
			mustExec(t, d.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
			data := mutateBackupManifestForTest(t, archive, tc.mutate)
			rec := importBackupForCompatibilityTest(t, d, data)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
			}
			var marker string
			if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&marker); err != nil || marker != "keep" {
				t.Fatalf("target changed: %q %v", marker, err)
			}
		})
	}
}

func TestConfigImportPreservesTargetSkillAssets(t *testing.T) {
	for _, mode := range []string{"no skills", "new skill", "update without assets", "update with assets"} {
		t.Run(mode, func(t *testing.T) {
			source := newBackupAdminFixture(t, false)
			target := newBackupAdminFixture(t, false)
			localPath := filepath.Join(target.Config.UploadDir, skillAssetsSubdir, "local.txt")
			writeFile(t, localPath, []byte("existing bytes"))
			localAssets, _ := json.Marshal([]skillAssetRow{{Filename: "local.txt", StoragePath: localPath}})
			mustExec(t, target.DB, `INSERT INTO skills(id,name,description,instructions,assets) VALUES('local','Local skill','desc','instructions',?)`, string(localAssets))
			rows := map[string][]map[string]any{"settings": {{"key": "audit-marker", "value": "imported"}}}
			var extras []configArchiveEntryForTest
			id := "incoming"
			if mode == "update without assets" || mode == "update with assets" {
				id = "local"
			}
			if mode != "no skills" {
				row := map[string]any{"id": id, "name": "Imported skill", "description": "desc", "instructions": "new instructions"}
				if mode != "update without assets" {
					assets, _ := json.Marshal([]skillAssetRow{{Filename: "incoming.txt", StoragePath: filepath.Join(source.Config.UploadDir, skillAssetsSubdir, "incoming.txt")}})
					row["assets"] = string(assets)
					extras = append(extras, configArchiveEntryForTest{name: configZipSkillAssets + "incoming.txt", data: []byte("imported bytes")})
				}
				rows["skills"] = []map[string]any{row}
			}
			data := paymentConfigArchiveForTest(t, rows, extras...)
			data = rewriteBackupZipForTest(t, data, func(name string, content []byte) (bool, []byte) {
				if name != "manifest.json" {
					return true, content
				}
				var man configManifest
				if err := json.Unmarshal(content, &man); err != nil {
					t.Fatal(err)
				}
				man.SourceUploadDir = source.Config.UploadDir
				encoded, err := json.Marshal(man)
				if err != nil {
					t.Fatal(err)
				}
				return true, encoded
			})
			rec := importPaymentConfigArchiveForTest(t, target, data)
			if rec.Code != http.StatusOK {
				t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
			}
			var raw string
			if err := target.DB.QueryRow(`SELECT assets FROM skills WHERE id='local'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if mode != "update with assets" && raw != string(localAssets) {
				t.Fatalf("local assets changed: %s", raw)
			}
			content, err := os.ReadFile(localPath)
			if err != nil || string(content) != "existing bytes" {
				t.Fatalf("local bytes changed: %q %v", content, err)
			}
			if mode == "new skill" || mode == "update with assets" {
				if err := target.DB.QueryRow(`SELECT assets FROM skills WHERE id=?`, id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var assets []skillAssetRow
				if err := json.Unmarshal([]byte(raw), &assets); err != nil {
					t.Fatal(err)
				}
				want := filepath.Join(target.Config.UploadDir, skillAssetsSubdir, "incoming.txt")
				if len(assets) != 1 || assets[0].StoragePath != want {
					t.Fatalf("imported paths: %+v", assets)
				}
				content, err := os.ReadFile(want)
				if err != nil || string(content) != "imported bytes" {
					t.Fatalf("imported bytes: %q %v", content, err)
				}
			}
		})
	}
}

func backupZipReaderForCompatibilityTest(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}
