package api

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aurelia/server/internal/store"
)

// Database backup / migration (§ admin → data migration).
//
// Export streams a single .zip: a manifest, one JSONL file per table (logical,
// engine-neutral rows), and — when requested — the on-disk uploads + artifacts
// trees. Import replaces ALL data from such an archive inside one transaction
// and restores the files, rewriting stored paths to this deployment's dirs.
//
// The whole point is portability: the archive migrates a deployment between
// machines AND between SQLite and Postgres, through the admin UI alone.

// backupManifest is the archive's self-description (manifest.json).
type backupManifest struct {
	Format            string           `json:"format"` // always "aurelia-backup"
	Version           int              `json:"version"`
	CreatedAt         int64            `json:"created_at"`
	App               string           `json:"app"`
	Dialect           string           `json:"dialect"` // sqlite | postgres (source)
	Tables            []string         `json:"tables"`
	Counts            map[string]int64 `json:"counts"`
	IncludesFiles     bool             `json:"includes_files"`
	SourceUploadDir   string           `json:"source_upload_dir"`
	SourceArtifactDir string           `json:"source_artifact_dir"`
}

const backupZipUploads = "files/uploads/"
const backupZipArtifacts = "files/artifacts/"

// exportBackupAdmin streams the full backup archive. `?files=1` bundles the
// on-disk uploads + artifacts alongside the database rows.
func exportBackupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	includeFiles := r.URL.Query().Get("files") == "1" || r.URL.Query().Get("files") == "true"

	// A read transaction gives a consistent point-in-time snapshot. Open it
	// BEFORE writing any response bytes so a failure can still return JSON.
	tx, err := d.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	dialect := "sqlite"
	if store.IsPostgres() {
		dialect = "postgres"
	}
	ts := time.Now().Unix()
	w.Header().Set("content-type", "application/zip")
	w.Header().Set("content-disposition", fmt.Sprintf(`attachment; filename="aurelia-backup-%d.zip"`, ts))
	w.Header().Set("x-content-type-options", "nosniff")

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	// Database: one JSONL per table, FK-safe order.
	counts := make(map[string]int64)
	for _, t := range store.BackupTableOrder() {
		fw, err := zw.Create("db/" + t + ".jsonl")
		if err != nil {
			d.Logger.Printf("backup export: create entry %s: %v", t, err)
			return
		}
		n, err := store.ExportTable(ctx, tx, t, fw)
		if err != nil {
			// Headers/stream already committed — can't change status. Log and bail;
			// the truncated zip (no manifest) will fail the importer's validation.
			d.Logger.Printf("backup export: table %s: %v", t, err)
			return
		}
		counts[t] = n
	}

	// On-disk files (optional).
	if includeFiles {
		if err := addDirToZip(zw, d.Config.UploadDir, backupZipUploads); err != nil {
			d.Logger.Printf("backup export: uploads: %v", err)
		}
		if err := addDirToZip(zw, d.Config.ArtifactDir, backupZipArtifacts); err != nil {
			d.Logger.Printf("backup export: artifacts: %v", err)
		}
	}

	// Manifest last (random-access zip — order doesn't matter to the reader).
	man := backupManifest{
		Format:            "aurelia-backup",
		Version:           store.BackupVersion,
		CreatedAt:         ts,
		App:               "aurelia",
		Dialect:           dialect,
		Tables:            store.BackupTableOrder(),
		Counts:            counts,
		IncludesFiles:     includeFiles,
		SourceUploadDir:   filepath.Clean(d.Config.UploadDir),
		SourceArtifactDir: filepath.Clean(d.Config.ArtifactDir),
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		d.Logger.Printf("backup export: manifest: %v", err)
		return
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		d.Logger.Printf("backup export: encode manifest: %v", err)
	}
}

// addDirToZip walks a directory tree into the archive under prefix (trailing
// slash). A missing/empty dir is not an error — there may simply be no uploads.
func addDirToZip(zw *zip.Writer, root, prefix string) error {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(root, func(p string, de fs.DirEntry, walkErr error) error {
		if walkErr != nil || de.IsDir() {
			return nil // skip unreadable entries rather than abort the whole export
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		fw, err := zw.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
}

// importBackupAdmin replaces ALL data from an uploaded archive. Destructive by
// design — it wipes every table and re-inserts from the archive — so it demands
// an explicit `confirm=REPLACE` form field (the UI sends it after a typed
// confirmation).
func importBackupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Hard cap on the total upload size to prevent DoS via huge archive (H-6).
	const maxBackupSize = 2 << 30 // 2 GB
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupSize)
	// Large parts stream to temp files automatically. 32 MiB stays in memory.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if err.Error() == "http: request body too large" {
			http.Error(w, "backup file too large (max 2 GB)", http.StatusRequestEntityTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("invalid multipart form"))
		return
	}
	if r.FormValue("confirm") != "REPLACE" {
		writeError(w, http.StatusBadRequest, errors.New("import requires confirm=REPLACE"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("missing archive file"))
		return
	}
	defer file.Close()

	// multipart.File is an io.ReaderAt + io.Seeker, exactly what zip.NewReader needs.
	zr, err := zip.NewReader(file, header.Size)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("not a valid backup archive: %w", err))
		return
	}
	man, err := readBackupManifest(zr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if man.Format != "aurelia-backup" {
		writeError(w, http.StatusBadRequest, errors.New("unrecognized archive (missing aurelia-backup manifest)"))
		return
	}
	if man.Version > store.BackupVersion {
		writeError(w, http.StatusBadRequest, fmt.Errorf("archive format v%d is newer than this server supports (v%d)", man.Version, store.BackupVersion))
		return
	}

	// Record the importing admin's identity BEFORE the wipe — this is how we
	// track which account to protect during privilege reconciliation (§ FIX-6).
	importingAdmin := authUser(r)

	counts, err := restoreDatabase(ctx, d, zr, man)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("restore failed (no changes committed): %w", err))
		return
	}

	// Privilege escalation guard: a malicious backup could introduce extra admin
	// accounts. After restore, downgrade any admin whose email is not the
	// importing admin's email — they were not pre-authorized (§ FIX-6).
	// Best-effort: a failure here is logged but doesn't abort — the DB is already
	// committed and we'd rather have a partial restriction than a broken restore.
	if importingAdmin != nil {
		if _, err := d.DB.ExecContext(ctx,
			`UPDATE users SET role='user' WHERE role='admin' AND email != ?`,
			importingAdmin.Email,
		); err != nil {
			d.Logger.Printf("backup import: privilege reconciliation failed: %v", err)
		}
	}

	filesRestored := 0
	if man.IncludesFiles {
		filesRestored = restoreFilesFromZip(d, zr)
	}

	// The settings cache (and the admin's own session) now reflect wiped data.
	store.InvalidateConfig()
	bumpAuthCacheEpoch(d)
	d.Logger.Printf("backup import: restored %d tables, %d files (source dialect=%s)", len(counts), filesRestored, man.Dialect)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"tables":           counts,
		"files_restored":   filesRestored,
		"includes_files":   man.IncludesFiles,
		"relogin_required": true,
	})
}

// restoreDatabase wipes and reloads every table inside one transaction. On
// SQLite it runs with foreign_keys=OFF on a dedicated connection (the pragma is
// a no-op inside a transaction, and the import order alone can't satisfy the
// messages self-reference under every edit history). On Postgres the FK-safe
// table order keeps constraints satisfied, and serial sequences are realigned.
func restoreDatabase(ctx context.Context, d Deps, zr *zip.Reader, man backupManifest) (map[string]int64, error) {
	if store.IsPostgres() {
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		counts, err := restoreInto(ctx, tx, zr, man, d)
		if err != nil {
			return nil, err
		}
		if err := store.ResetSerialSequences(ctx, tx); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return counts, nil
	}

	// SQLite: hold one connection so the pragma and the transaction share it.
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return nil, err
	}
	// Re-enable FK enforcement on the way out, whatever happens.
	defer func() { _, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON") }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	counts, err := restoreInto(ctx, tx, zr, man, d)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

// restoreInto performs the wipe + per-table reload + path rewrite against one
// transaction handle.
func restoreInto(ctx context.Context, ex store.RowExecer, zr *zip.Reader, man backupManifest, d Deps) (map[string]int64, error) {
	if err := store.WipeAll(ctx, ex); err != nil {
		return nil, err
	}
	counts := make(map[string]int64)
	for _, t := range store.BackupTableOrder() {
		entry := findZipFile(zr, "db/"+t+".jsonl")
		if entry == nil {
			continue // table absent from an older/partial archive
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, err
		}
		n, err := store.RestoreTable(ctx, ex, t, rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		counts[t] = n
	}
	if err := rewriteStoragePaths(ctx, ex, man, d); err != nil {
		return nil, err
	}
	return counts, nil
}

// rewriteStoragePaths repoints files/documents/artifacts storage_path values
// from the archive's source directories to this deployment's configured dirs,
// so restored rows resolve locally even when UPLOAD_DIR/ARTIFACT_DIR differ.
func rewriteStoragePaths(ctx context.Context, ex store.RowExecer, man backupManifest, d Deps) error {
	type spec struct{ table, src, dst string }
	specs := []spec{
		{"files", man.SourceUploadDir, filepath.Clean(d.Config.UploadDir)},
		{"documents", man.SourceUploadDir, filepath.Clean(d.Config.UploadDir)},
		{"artifacts", man.SourceArtifactDir, filepath.Clean(d.Config.ArtifactDir)},
	}
	for _, s := range specs {
		if s.src == "" || s.src == s.dst {
			continue // nothing to remap
		}
		rows, err := ex.QueryContext(ctx, "SELECT id, storage_path FROM "+s.table) //nolint:gosec // literal table
		if err != nil {
			return err
		}
		type upd struct{ id, path string }
		var ups []upd
		for rows.Next() {
			var id, p string
			if err := rows.Scan(&id, &p); err != nil {
				_ = rows.Close()
				return err
			}
			if p == "" {
				continue
			}
			rel, err := filepath.Rel(s.src, filepath.Clean(p))
			if err != nil || strings.HasPrefix(rel, "..") {
				continue // path outside the source dir — leave it untouched
			}
			ups = append(ups, upd{id, filepath.Join(s.dst, rel)})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close() // must close before issuing UPDATEs on the same SQLite conn
		for _, u := range ups {
			if _, err := ex.ExecContext(ctx, "UPDATE "+s.table+" SET storage_path=? WHERE id=?", u.path, u.id); err != nil { //nolint:gosec // literal table
				return err
			}
		}
	}
	return nil
}

// restoreFilesFromZip extracts the bundled uploads/artifacts back onto disk
// under the configured dirs. Best-effort: a failed file is logged-by-omission
// and skipped rather than aborting an already-committed DB restore. Returns the
// number of files written. Guards against path traversal in archive entries.
func restoreFilesFromZip(d Deps, zr *zip.Reader) int {
	n := 0
	for _, f := range zr.File {
		var base, rel string
		switch {
		case strings.HasPrefix(f.Name, backupZipUploads):
			base, rel = d.Config.UploadDir, strings.TrimPrefix(f.Name, backupZipUploads)
		case strings.HasPrefix(f.Name, backupZipArtifacts):
			base, rel = d.Config.ArtifactDir, strings.TrimPrefix(f.Name, backupZipArtifacts)
		default:
			continue
		}
		if rel == "" || strings.HasSuffix(f.Name, "/") {
			continue
		}
		dest := filepath.Join(base, filepath.FromSlash(rel))
		baseClean := filepath.Clean(base) + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(dest)+string(filepath.Separator), baseClean) &&
			filepath.Clean(dest) != filepath.Clean(base) {
			continue // path traversal — refuse to escape the target dir
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			_ = rc.Close()
			continue
		}
		_, cerr := io.Copy(out, rc)
		_ = out.Close()
		_ = rc.Close()
		if cerr == nil {
			n++
		}
	}
	return n
}

func readBackupManifest(zr *zip.Reader) (backupManifest, error) {
	var man backupManifest
	entry := findZipFile(zr, "manifest.json")
	if entry == nil {
		return man, errors.New("archive has no manifest.json")
	}
	rc, err := entry.Open()
	if err != nil {
		return man, err
	}
	defer rc.Close()
	if err := json.NewDecoder(rc).Decode(&man); err != nil {
		return man, fmt.Errorf("invalid manifest.json: %w", err)
	}
	return man, nil
}

func findZipFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}
