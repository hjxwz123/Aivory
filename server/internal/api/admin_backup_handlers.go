package api

import (
	"archive/zip"
	"bytes"
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

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/store"
)

// Tunable knobs — envcfg overrides; defaults preserve original behaviour.
var (
	configImportMultipartMemoryBuffer = envcfg.Int64("AURELIA_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER", 16<<20)
	backupImportMultipartMemoryBuffer = envcfg.Int64("AURELIA_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER", 32<<20)
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
	IncludesQdrant    bool             `json:"includes_qdrant"`
	QdrantPoints      int64            `json:"qdrant_points"`
	SourceUploadDir   string           `json:"source_upload_dir"`
	SourceArtifactDir string           `json:"source_artifact_dir"`
}

// configManifest describes the lighter admin-configuration archive. It carries
// no users, conversations, messages, user uploads, KBs, sessions, workspaces, or
// usage logs; import is an UPSERT merge into config tables plus admin assets.
type configManifest struct {
	Format          string           `json:"format"` // always "aurelia-config"
	Version         int              `json:"version"`
	CreatedAt       int64            `json:"created_at"`
	App             string           `json:"app"`
	Dialect         string           `json:"dialect"`
	Tables          []string         `json:"tables"`
	Counts          map[string]int64 `json:"counts"`
	MergeMode       string           `json:"merge_mode"`       // upsert
	SecretsIncluded bool             `json:"secrets_included"` // true: admin-only archive
	IncludesAssets  bool             `json:"includes_assets"`
	SourceUploadDir string           `json:"source_upload_dir"`
}

const backupZipUploads = "files/uploads/"
const backupZipArtifacts = "files/artifacts/"
const configZipIcons = "assets/icons/"
const configZipSkillAssets = "assets/skill-assets/"
const configArchiveVersion = 1

type backupArchiveOptions struct {
	IncludeFiles  bool
	IncludeQdrant bool
}

type backupArchiveResult struct {
	Counts       map[string]int64
	IncludesFile bool
	QdrantPoints int64
}

// exportBackupAdmin streams the full backup archive. `?files=1` bundles the
// on-disk uploads + artifacts alongside the database rows.
func exportBackupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	includeFiles := r.URL.Query().Get("files") == "1" || r.URL.Query().Get("files") == "true"
	includeQdrant := shouldIncludeQdrant(d, r)

	// A read transaction gives a consistent point-in-time snapshot. Open it before
	// writing response headers so a connection failure can still return JSON.
	tx, err := d.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	ts := time.Now().Unix()
	w.Header().Set("content-type", "application/zip")
	name := "aurelia-backup"
	if includeQdrant {
		name = "aurelia-docker-backup"
	}
	w.Header().Set("content-disposition", fmt.Sprintf(`attachment; filename="%s-%d.zip"`, name, ts))
	w.Header().Set("x-content-type-options", "nosniff")
	if _, err := writeBackupArchive(ctx, d, tx, w, backupArchiveOptions{IncludeFiles: includeFiles, IncludeQdrant: includeQdrant}); err != nil {
		// Headers/stream may already be committed. Log and let the truncated zip
		// fail manifest validation for the importer.
		d.Logger.Printf("backup export: %v", err)
	}
}

func writeBackupArchive(ctx context.Context, d Deps, tx *sql.Tx, w io.Writer, opts backupArchiveOptions) (backupArchiveResult, error) {
	dialect := "sqlite"
	if store.IsPostgres() {
		dialect = "postgres"
	}
	zw := zip.NewWriter(w)
	closed := false
	defer func() {
		if !closed {
			_ = zw.Close()
		}
	}()

	// Database: one JSONL per table, FK-safe order.
	counts := make(map[string]int64)
	for _, t := range store.BackupTableOrder() {
		fw, err := zw.Create("db/" + t + ".jsonl")
		if err != nil {
			return backupArchiveResult{}, fmt.Errorf("create entry %s: %w", t, err)
		}
		n, err := store.ExportTable(ctx, tx, t, fw)
		if err != nil {
			return backupArchiveResult{}, fmt.Errorf("table %s: %w", t, err)
		}
		counts[t] = n
	}

	// On-disk files (optional).
	if opts.IncludeFiles {
		if err := addDirToZip(zw, d.Config.UploadDir, backupZipUploads); err != nil {
			return backupArchiveResult{}, fmt.Errorf("uploads: %w", err)
		}
		if err := addDirToZip(zw, d.Config.ArtifactDir, backupZipArtifacts); err != nil {
			return backupArchiveResult{}, fmt.Errorf("artifacts: %w", err)
		}
	}

	var qdrantPoints int64
	includesQdrant := false
	if opts.IncludeQdrant && strings.TrimSpace(d.Config.QdrantURL) != "" {
		n, err := exportQdrantToZip(ctx, d, zw)
		if err != nil {
			return backupArchiveResult{}, fmt.Errorf("qdrant export: %w", err)
		}
		qdrantPoints = n
		includesQdrant = true
	}

	// Manifest last (random-access zip — order doesn't matter to the reader).
	man := backupManifest{
		Format:            "aurelia-backup",
		Version:           store.BackupVersion,
		CreatedAt:         time.Now().Unix(),
		App:               "aurelia",
		Dialect:           dialect,
		Tables:            store.BackupTableOrder(),
		Counts:            counts,
		IncludesFiles:     opts.IncludeFiles,
		IncludesQdrant:    includesQdrant,
		QdrantPoints:      qdrantPoints,
		SourceUploadDir:   filepath.Clean(d.Config.UploadDir),
		SourceArtifactDir: filepath.Clean(d.Config.ArtifactDir),
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		return backupArchiveResult{}, fmt.Errorf("manifest: %w", err)
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		return backupArchiveResult{}, fmt.Errorf("encode manifest: %w", err)
	}
	if err := zw.Close(); err != nil {
		return backupArchiveResult{}, fmt.Errorf("close archive: %w", err)
	}
	closed = true
	return backupArchiveResult{Counts: counts, IncludesFile: opts.IncludeFiles, QdrantPoints: qdrantPoints}, nil
}

func shouldIncludeQdrant(d Deps, r *http.Request) bool {
	if strings.TrimSpace(d.Config.QdrantURL) == "" {
		return false
	}
	v := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("qdrant")))
	return v == "" || v == "1" || v == "true" || v == "yes"
}

// exportConfigAdmin streams an admin-configuration archive. Unlike the full
// backup this intentionally excludes user/business data and includes plaintext
// config secrets (channel API keys, OAuth secrets, SMTP/storage/search keys).
func exportConfigAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
	w.Header().Set("content-disposition", fmt.Sprintf(`attachment; filename="aurelia-config-%d.zip"`, ts))
	w.Header().Set("x-content-type-options", "nosniff")

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	counts := make(map[string]int64)
	for _, t := range store.ConfigTableOrder() {
		fw, err := zw.Create("db/" + t + ".jsonl")
		if err != nil {
			d.Logger.Printf("config export: create entry %s: %v", t, err)
			return
		}
		var n int64
		if t == "settings" {
			n, err = exportAdminSettingsTable(ctx, tx, fw)
		} else {
			n, err = store.ExportTable(ctx, tx, t, fw)
		}
		if err != nil {
			d.Logger.Printf("config export: table %s: %v", t, err)
			return
		}
		counts[t] = n
	}

	if err := addDirToZip(zw, filepath.Join(d.Config.UploadDir, "icons"), configZipIcons); err != nil {
		d.Logger.Printf("config export: icons: %v", err)
	}
	if err := addDirToZip(zw, filepath.Join(d.Config.UploadDir, skillAssetsSubdir), configZipSkillAssets); err != nil {
		d.Logger.Printf("config export: skill assets: %v", err)
	}

	man := configManifest{
		Format:          "aurelia-config",
		Version:         configArchiveVersion,
		CreatedAt:       ts,
		App:             "aurelia",
		Dialect:         dialect,
		Tables:          store.ConfigTableOrder(),
		Counts:          counts,
		MergeMode:       "upsert",
		SecretsIncluded: true,
		IncludesAssets:  true,
		SourceUploadDir: filepath.Clean(d.Config.UploadDir),
	}
	mw, err := zw.Create("manifest.json")
	if err != nil {
		d.Logger.Printf("config export: manifest: %v", err)
		return
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		d.Logger.Printf("config export: encode manifest: %v", err)
	}
}

func exportAdminSettingsTable(ctx context.Context, q store.RowQuerier, w io.Writer) (int64, error) {
	keys := uniqueSettingsKeys()
	args := make([]any, 0, len(keys))
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := q.QueryContext(ctx,
		"SELECT key, value, updated_at FROM settings WHERE key IN ("+sqlPlaceholders(len(keys))+") ORDER BY key",
		args...,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	enc := json.NewEncoder(w)
	var n int64
	for rows.Next() {
		var key, value string
		var updatedAt int64
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return n, err
		}
		if strings.TrimSpace(value) == "null" {
			continue
		}
		if err := enc.Encode(map[string]any{"key": key, "value": value, "updated_at": updatedAt}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func uniqueSettingsKeys() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(settingsKeys))
	for _, k := range settingsKeys {
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
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

// importConfigAdmin merges an admin-configuration archive into this deployment.
// It never wipes or imports users/conversations/messages/user uploads/KBs/
// sessions/logs. Existing rows with the same primary key are updated; local rows
// absent from the archive are kept, which avoids breaking user data references.
func importConfigAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxConfigSize := envcfg.Int64("AURELIA_API_MAX_CONFIG_SIZE", 512<<20) // 512 MiB; config archives are normally tiny.
	r.Body = http.MaxBytesReader(w, r.Body, maxConfigSize)
	if err := r.ParseMultipartForm(configImportMultipartMemoryBuffer); err != nil {
		if err.Error() == "http: request body too large" {
			http.Error(w, fmt.Sprintf("config file too large (max %d MB)", maxConfigSize>>20), http.StatusRequestEntityTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("invalid multipart form"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("missing config file"))
		return
	}
	defer file.Close()

	if zr, err := zip.NewReader(file, header.Size); err == nil {
		man, err := readConfigManifest(zr)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if man.Format != "aurelia-config" {
			writeError(w, http.StatusBadRequest, errors.New("unrecognized config archive (missing aurelia-config manifest)"))
			return
		}
		if man.Version > configArchiveVersion {
			writeError(w, http.StatusBadRequest, fmt.Errorf("config archive format v%d is newer than this server supports (v%d)", man.Version, configArchiveVersion))
			return
		}
		if err := validateConfigArchiveEmbeddingModelLock(zr, d); err != nil {
			if errors.Is(err, errEmbeddingModelLocked) {
				writeError(w, http.StatusConflict, errEmbeddingModelLocked)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		counts, err := mergeConfigArchive(ctx, d, zr, man)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("config import failed (no changes committed): %w", err))
			return
		}
		assetsRestored := 0
		if man.IncludesAssets {
			assetsRestored = restoreConfigAssetsFromZip(d, zr)
		}
		broadcastConfigInvalidate(d)
		d.Logger.Printf("config import: merged %d tables, %d assets (source dialect=%s)", len(counts), assetsRestored, man.Dialect)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":               true,
			"tables":           counts,
			"assets_restored":  assetsRestored,
			"merge_mode":       "upsert",
			"relogin_required": false,
		})
		return
	}

	// Backward compatibility: accept the old frontend-only JSON shape
	// {"format":"aurelia-config","settings":{...}} and apply settings only.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid config file"))
		return
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid config file"))
		return
	}
	n, err := importLegacySettingsConfig(d, buf.Bytes())
	if err != nil {
		if errors.Is(err, errEmbeddingModelLocked) {
			writeError(w, http.StatusConflict, errEmbeddingModelLocked)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	broadcastConfigInvalidate(d)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"tables":           map[string]int64{"settings": n},
		"assets_restored":  0,
		"merge_mode":       "settings",
		"relogin_required": false,
	})
}

// importBackupAdmin replaces ALL data from an uploaded archive. Destructive by
// design — it wipes every table and re-inserts from the archive — so it demands
// an explicit `confirm=REPLACE` form field (the UI sends it after a typed
// confirmation).
func importBackupAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if running := adminBackupExports.running(); running != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "backup export already running",
			"running": running,
		})
		return
	}
	if running := adminVectorMaintenance.running(); running != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "vector maintenance already running",
			"running": running,
		})
		return
	}
	// Hard cap on total upload size to prevent DoS via huge archives (H-6). Full
	// Docker migration archives can legitimately be large once Qdrant vectors are
	// included, so operators can raise/lower it with MAX_BACKUP_BYTES.
	maxBackupSize := d.Config.MaxBackupBytes
	if maxBackupSize <= 0 {
		maxBackupSize = 20 * 1024 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupSize)
	// Large parts stream to temp files automatically. 32 MiB stays in memory.
	if err := r.ParseMultipartForm(backupImportMultipartMemoryBuffer); err != nil {
		if err.Error() == "http: request body too large" {
			http.Error(w, fmt.Sprintf("backup file too large (max %s)", humanBackupBytes(maxBackupSize)), http.StatusRequestEntityTooLarge)
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
	qdrantRestored, qdrantErr := restoreQdrantFromZip(ctx, d, zr)
	if qdrantErr != "" {
		d.Logger.Printf("backup import: qdrant restore warning: %s", qdrantErr)
	}

	// The settings cache (and the admin's own session) now reflect wiped data.
	store.InvalidateConfig()
	bumpAuthCacheEpoch(d)
	d.Logger.Printf("backup import: restored %d tables, %d files, %d qdrant points (source dialect=%s)", len(counts), filesRestored, qdrantRestored, man.Dialect)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"tables":           counts,
		"files_restored":   filesRestored,
		"includes_files":   man.IncludesFiles,
		"qdrant_restored":  qdrantRestored,
		"qdrant_error":     qdrantErr,
		"relogin_required": true,
	})
}

func humanBackupBytes(n int64) string {
	if n >= 1024*1024*1024 && n%(1024*1024*1024) == 0 {
		return fmt.Sprintf("%d GB", n/(1024*1024*1024))
	}
	if n >= 1024*1024 && n%(1024*1024) == 0 {
		return fmt.Sprintf("%d MB", n/(1024*1024))
	}
	return fmt.Sprintf("%d bytes", n)
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

func mergeConfigArchive(ctx context.Context, d Deps, zr *zip.Reader, man configManifest) (map[string]int64, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	counts := make(map[string]int64)
	for _, t := range store.ConfigTableOrder() {
		entry := findZipFile(zr, "db/"+t+".jsonl")
		if entry == nil {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, err
		}
		n, err := store.UpsertTable(ctx, tx, t, rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		counts[t] = n
	}
	if err := rewriteConfigSkillAssetPaths(ctx, tx, man, d); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

func rewriteConfigSkillAssetPaths(ctx context.Context, ex store.RowExecer, man configManifest, d Deps) error {
	if strings.TrimSpace(man.SourceUploadDir) == "" {
		return nil
	}
	rows, err := ex.QueryContext(ctx, `SELECT id, assets FROM skills`)
	if err != nil {
		return err
	}
	type upd struct{ id, assets string }
	var ups []upd
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "[]" {
			continue
		}
		var assets []skillAssetRow
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rewrite skill assets %s: %w", id, err)
		}
		changed := false
		for i := range assets {
			next := remapConfigSkillAssetPath(assets[i].StoragePath, man.SourceUploadDir, d.Config.UploadDir)
			if next != "" && next != assets[i].StoragePath {
				assets[i].StoragePath = next
				changed = true
			}
		}
		if changed {
			b, err := json.Marshal(assets)
			if err != nil {
				_ = rows.Close()
				return err
			}
			ups = append(ups, upd{id: id, assets: string(b)})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, u := range ups {
		if _, err := ex.ExecContext(ctx, `UPDATE skills SET assets=? WHERE id=?`, u.assets, u.id); err != nil {
			return err
		}
	}
	return nil
}

func remapConfigSkillAssetPath(path, sourceUploadDir, targetUploadDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	srcBase := filepath.Join(filepath.Clean(sourceUploadDir), skillAssetsSubdir)
	dstBase := filepath.Join(filepath.Clean(targetUploadDir), skillAssetsSubdir)
	clean := filepath.Clean(path)
	if rel, err := filepath.Rel(srcBase, clean); err == nil && rel != "." && rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.Join(dstBase, rel)
	}
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(dstBase, base)
}

func restoreConfigAssetsFromZip(d Deps, zr *zip.Reader) int {
	n := 0
	n += restoreZipPrefix(zr, configZipIcons, filepath.Join(d.Config.UploadDir, "icons"))
	n += restoreZipPrefix(zr, configZipSkillAssets, filepath.Join(d.Config.UploadDir, skillAssetsSubdir))
	return n
}

func restoreZipPrefix(zr *zip.Reader, prefix, base string) int {
	n := 0
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		if rel == "" || strings.HasSuffix(f.Name, "/") {
			continue
		}
		dest := filepath.Join(base, filepath.FromSlash(rel))
		baseClean := filepath.Clean(base) + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(dest)+string(filepath.Separator), baseClean) &&
			filepath.Clean(dest) != filepath.Clean(base) {
			continue
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

func readConfigManifest(zr *zip.Reader) (configManifest, error) {
	var man configManifest
	entry := findZipFile(zr, "manifest.json")
	if entry == nil {
		return man, errors.New("config archive has no manifest.json")
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

func importLegacySettingsConfig(d Deps, data []byte) (int64, error) {
	var payload struct {
		Format   string                     `json:"format"`
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0, errors.New("not a valid Aurelia configuration export")
	}
	if payload.Format != "aurelia-config" || payload.Settings == nil {
		return 0, errors.New("not a valid Aurelia configuration export")
	}
	return applyAdminSettingsPatch(d, payload.Settings, true)
}

func validateConfigArchiveEmbeddingModelLock(zr *zip.Reader, d Deps) error {
	if err := validateConfigArchiveEmbeddingModelSettingLock(zr, d); err != nil {
		return err
	}
	return validateConfigArchiveLockedEmbeddingModelRow(zr, d)
}

func validateConfigArchiveEmbeddingModelSettingLock(zr *zip.Reader, d Deps) error {
	entry := findZipFile(zr, "db/settings.jsonl")
	if entry == nil {
		return nil
	}
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var row struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := dec.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if row.Key != "embedding_model_id" {
			continue
		}
		return ensureEmbeddingModelSettingCanChange(d, json.RawMessage(row.Value))
	}
}

func validateConfigArchiveLockedEmbeddingModelRow(zr *zip.Reader, d Deps) error {
	entry := findZipFile(zr, "db/models.jsonl")
	if entry == nil {
		return nil
	}
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if err := ensureLockedEmbeddingModelArchiveRowCanChange(d, row); err != nil {
			return err
		}
	}
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
