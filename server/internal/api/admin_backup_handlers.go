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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/fileguard"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

// Tunable knobs — envcfg overrides; defaults preserve original behaviour.
var (
	configImportMultipartMemoryBuffer = envcfg.Int64("AIVORY_API_CONFIG_IMPORT_MULTIPART_MEMORY_BUFFER", 16<<20)
	backupImportMultipartMemoryBuffer = envcfg.Int64("AIVORY_API_BACKUP_IMPORT_MULTIPART_MEMORY_BUFFER", 32<<20)
	errInvalidPaymentConfigArchive    = errors.New("invalid payment configuration in config archive")
	errInvalidOAuthConfigArchive      = errors.New("invalid OAuth configuration in config archive")
	errInvalidBillingConfigArchive    = errors.New("invalid billing configuration in config archive")
	errInvalidCompactionConfigArchive = errors.New("invalid context compaction configuration in archive")
	errInvalidBackupStoragePath       = errors.New("invalid storage path in backup archive")
	errInvalidBackupArchive           = errors.New("invalid or incomplete backup archive")
	errBackupImportAdminUnauthorized  = errors.New("backup import requires a current active administrator")
	errConfigImportAdminUnauthorized  = errors.New("config import requires a current active administrator")
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

// legacyBackupFormat reports whether a manifest format string is an accepted
// alias for the given current format. The product was renamed twice
// (Aurelia -> Auven -> Aivory); archives exported by those builds carry the
// old prefix but are byte-compatible — same tables, same layout, same version
// counter. Rejecting them would strand every pre-rename backup.
func acceptedArchiveFormat(got, want string) bool {
	if got == want {
		return true
	}
	suffix := strings.TrimPrefix(want, "aivory")
	return got == "aurelia"+suffix || got == "auven"+suffix
}

func validateArchiveVersion(label string, version, current int) error {
	if version < 1 || version > current {
		return fmt.Errorf("%s format v%d is unsupported (supported versions: v1 through v%d)", label, version, current)
	}
	return nil
}

// backupManifest is the archive's self-description (manifest.json).
type backupManifest struct {
	Format            string                          `json:"format"` // always "aivory-backup"
	Version           int                             `json:"version"`
	CreatedAt         int64                           `json:"created_at"`
	App               string                          `json:"app"`
	Dialect           string                          `json:"dialect"` // sqlite | postgres (source)
	Tables            []string                        `json:"tables"`
	Counts            map[string]int64                `json:"counts"`
	IncludesFiles     bool                            `json:"includes_files"`
	IncludesQdrant    bool                            `json:"includes_qdrant"`
	QdrantPoints      int64                           `json:"qdrant_points"`
	SourceUploadDir   string                          `json:"source_upload_dir"`
	SourceArtifactDir string                          `json:"source_artifact_dir"`
	Entries           map[string]backupEntryIntegrity `json:"entries,omitempty"`
}

type backupImportOAuthIdentity struct {
	ProviderID string
	Subject    string
	Email      string
	CreatedAt  int64
}

type backupImportAdminSnapshot struct {
	ID                string
	Email             string
	PasswordHash      string
	Name              string
	TokenVer          int
	Settings          string
	GroupID           string
	GroupExpiresAt    int64
	PreviousGroupID   string
	TotpSecret        string
	TotpEnabled       int
	PasswordSet       int
	PasswordChangedAt int64
	LastSeenAt        int64
	CreditsPermanent  float64
	CreditsMicros     int64
	CreditCycleAnchor int64
	QuotaCycleAnchor  int64
	SortOrder         int
	CreatedAt         int64
	Identities        []backupImportOAuthIdentity
	Providers         map[string]store.OAuthProvider
}

// configManifest describes the lighter admin-configuration archive. It carries
// no users, conversations, messages, user uploads, KBs, sessions, workspaces, or
// usage logs; import is an UPSERT merge into config tables plus admin assets.
type configManifest struct {
	Format          string           `json:"format"` // always "aivory-config"
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
const configArchiveVersion = 2

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
	unlockQdrant := lockQdrantArchiveIfNeeded(d, includeQdrant)
	defer unlockQdrant()

	// A read transaction gives a consistent point-in-time snapshot. Open it before
	// writing response headers so a connection failure can still return JSON.
	tx, err := beginBackupSnapshot(ctx, d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	ts := time.Now().Unix()
	w.Header().Set("content-type", "application/zip")
	name := "aivory-backup"
	if includeQdrant {
		name = "aivory-docker-backup"
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
	tracked := newTrackedZipWriter(zw)
	closed := false
	defer func() {
		if !closed {
			_ = zw.Close()
		}
	}()

	// Database: one JSONL per table, FK-safe order.
	counts := make(map[string]int64)
	for _, t := range store.BackupTableOrder() {
		fw, err := tracked.Create("db/" + t + ".jsonl")
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
		if err := addRequiredDirToZip(tracked, d.Config.UploadDir, backupZipUploads); err != nil {
			return backupArchiveResult{}, fmt.Errorf("uploads: %w", err)
		}
		if err := addRequiredDirToZip(tracked, d.Config.ArtifactDir, backupZipArtifacts); err != nil {
			return backupArchiveResult{}, fmt.Errorf("artifacts: %w", err)
		}
	}

	var qdrantPoints int64
	includesQdrant := false
	if opts.IncludeQdrant && strings.TrimSpace(d.Config.QdrantURL) != "" {
		n, err := exportQdrantToZip(ctx, d, tracked)
		if err != nil {
			return backupArchiveResult{}, fmt.Errorf("qdrant export: %w", err)
		}
		qdrantPoints = n
		includesQdrant = true
	}

	entries := tracked.Finish()
	if opts.IncludeFiles {
		if err := validateExportedStorageReferences(ctx, tx, d, entries); err != nil {
			return backupArchiveResult{}, fmt.Errorf("validate stored files: %w", err)
		}
	}
	// Manifest last (random-access zip — order doesn't matter to the reader).
	man := backupManifest{
		Format:            "aivory-backup",
		Version:           store.BackupVersion,
		CreatedAt:         time.Now().Unix(),
		App:               "aivory",
		Dialect:           dialect,
		Tables:            store.BackupTableOrder(),
		Counts:            counts,
		IncludesFiles:     opts.IncludeFiles,
		IncludesQdrant:    includesQdrant,
		QdrantPoints:      qdrantPoints,
		SourceUploadDir:   filepath.Clean(d.Config.UploadDir),
		SourceArtifactDir: filepath.Clean(d.Config.ArtifactDir),
		Entries:           entries,
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
	tx, err := beginBackupSnapshot(ctx, d.DB)
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
	w.Header().Set("content-disposition", fmt.Sprintf(`attachment; filename="aivory-config-%d.zip"`, ts))
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
		Format:          "aivory-config",
		Version:         configArchiveVersion,
		CreatedAt:       ts,
		App:             "aivory",
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
func addRequiredDirToZip(zw zipEntryCreator, root, prefix string) error {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return addDirToZip(zw, root, prefix)
}

func addDirToZip(zw zipEntryCreator, root, prefix string) error {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(root, func(p string, de fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if de.IsDir() || de.Type()&os.ModeSymlink != 0 || !de.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		safePath, err := fileguard.ResolveExisting(p, root)
		if err != nil {
			return err
		}
		f, err := os.Open(safePath)
		if err != nil {
			return err
		}
		before, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		fw, err := zw.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			_ = f.Close()
			return err
		}
		_, copyErr := io.Copy(fw, f)
		after, statErr := f.Stat()
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("file changed while backup was reading it: %s", p)
		}
		return nil
	})
}

// importConfigAdmin merges an admin-configuration archive into this deployment.
// It never wipes or imports users/conversations/messages/user uploads/KBs/
// sessions/logs. Existing rows with the same primary key are updated; local rows
// absent from the archive are kept, which avoids breaking user data references.
func importConfigAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxConfigSize := envcfg.Int64("AIVORY_API_MAX_CONFIG_SIZE", 512<<20) // 512 MiB; config archives are normally tiny.
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

	zr, err := zip.NewReader(file, header.Size)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid config archive: expected a ZIP file"))
		return
	}
	man, err := readConfigManifest(zr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !acceptedArchiveFormat(man.Format, "aivory-config") {
		writeError(w, http.StatusBadRequest, errors.New("unrecognized config archive (missing aivory-config manifest)"))
		return
	}
	if err := validateArchiveVersion("config archive", man.Version, configArchiveVersion); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	// The request context is only a pre-upload snapshot. Keep the caller id, then
	// re-authorize it under the config merge transaction's database row lock.
	// This closes the window where a slow upload outlives an admin demotion/ban.
	importingAdmin := authUser(r)
	if importingAdmin == nil || strings.TrimSpace(importingAdmin.ID) == "" {
		writeError(w, http.StatusForbidden, errConfigImportAdminUnauthorized)
		return
	}
	// Assets are restored before the merge commits so the authorization lock
	// remains held until the last persistent write has completed.
	assetsRestored := 0
	counts, err := mergeConfigArchive(ctx, d, zr, man, importingAdmin.ID, func() error {
		if man.IncludesAssets {
			assetsRestored = restoreConfigAssetsFromZip(d, zr)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errConfigImportAdminUnauthorized):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, errInvalidPaymentConfigArchive), errors.Is(err, errInvalidOAuthConfigArchive),
			errors.Is(err, errInvalidBillingConfigArchive), errors.Is(err, errInvalidCompactionConfigArchive),
			errors.Is(err, errInvalidBackupStoragePath),
			errors.Is(err, errOAuthClientSecretReentryRequired):
			writeError(w, http.StatusBadRequest, err)
		case errors.Is(err, store.ErrOAuthProviderChanged):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, store.ErrPaymentChannelHasPending),
			errors.Is(err, store.ErrPaymentChannelHasMethods),
			errors.Is(err, store.ErrPaymentChannelNameExists),
			errors.Is(err, store.ErrPaymentMethodNameExists):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, fmt.Errorf("config import failed (no changes committed): %w", err))
		}
		return
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
	if !acceptedArchiveFormat(man.Format, "aivory-backup") {
		writeError(w, http.StatusBadRequest, errors.New("unrecognized archive (missing aivory-backup manifest)"))
		return
	}
	if err := validateArchiveVersion("archive", man.Version, store.BackupVersion); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateBackupArchive(zr, man); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%w: %v", errInvalidBackupArchive, err))
		return
	}

	// Keep only the subject id from the request context here. The authoritative
	// role/status/security snapshot is loaded again inside the restore
	// transaction, after taking a database write lock, so a concurrent ban or
	// demotion cannot turn a valid pre-check into a destructive unauthorised wipe.
	importingAdmin := authUser(r)
	if importingAdmin == nil || strings.TrimSpace(importingAdmin.ID) == "" {
		writeError(w, http.StatusForbidden, errBackupImportAdminUnauthorized)
		return
	}

	// Prepare all external state before opening the destructive DB transaction.
	// Files are extracted into sibling staging directories; Qdrant's current
	// collections are captured for compensation. Neither step changes live state.
	fileRestore, err := prepareBackupFileRestore(d, zr, man)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("prepare file restore: %w", err))
		return
	}
	if fileRestore != nil {
		defer func() { _ = fileRestore.Rollback() }()
	}
	unlockQdrant := lockQdrantArchiveIfNeeded(d, man.IncludesQdrant)
	defer unlockQdrant()
	qdrantRestore, err := prepareBackupQdrantRestore(ctx, d, zr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("prepare Qdrant restore: %w", err))
		return
	}
	if qdrantRestore != nil {
		defer func() {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), qdrantArchiveRequestTimeout)
			defer cancel()
			_ = qdrantRestore.Rollback(rollbackCtx)
			_ = qdrantRestore.Commit()
		}()
	}

	filesRestored := 0
	if fileRestore != nil {
		filesRestored = fileRestore.files
	}
	var qdrantRestored int64
	counts, err := restoreDatabase(ctx, d, zr, man, importingAdmin.ID, func() error {
		if err := fileRestore.Apply(); err != nil {
			return fmt.Errorf("publish restored files: %w", err)
		}
		if err := qdrantRestore.Apply(ctx); err != nil {
			return err
		}
		if qdrantRestore != nil {
			qdrantRestored = qdrantRestore.restored
		}
		return nil
	})
	if err != nil {
		var rollbackErrs []error
		if qdrantRestore != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), qdrantArchiveRequestTimeout)
			if rollbackErr := qdrantRestore.Rollback(rollbackCtx); rollbackErr != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("Qdrant rollback failed: %w", rollbackErr))
			}
			cancel()
		}
		if rollbackErr := fileRestore.Rollback(); rollbackErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("file rollback failed: %w", rollbackErr))
		}
		if len(rollbackErrs) > 0 {
			err = errors.Join(append([]error{err}, rollbackErrs...)...)
		}
		if errors.Is(err, errBackupImportAdminUnauthorized) {
			writeError(w, http.StatusForbidden, err)
		} else if errors.Is(err, errInvalidBillingConfigArchive) || errors.Is(err, errInvalidCompactionConfigArchive) || errors.Is(err, errInvalidOAuthConfigArchive) || errors.Is(err, errInvalidBackupStoragePath) || errors.Is(err, errInvalidBackupArchive) {
			writeError(w, http.StatusBadRequest, err)
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("restore failed (no changes committed): %w", err))
		}
		return
	}
	if cleanupErr := fileRestore.Commit(); cleanupErr != nil {
		d.Logger.Printf("backup import: clean previous file trees: %v", cleanupErr)
	}
	if cleanupErr := qdrantRestore.Commit(); cleanupErr != nil {
		d.Logger.Printf("backup import: clean Qdrant rollback archive: %v", cleanupErr)
	}

	// The settings cache (and the admin's own session) now reflect wiped data.
	broadcastConfigInvalidate(d)
	bumpAuthCacheEpoch(d)
	d.Logger.Printf("backup import: restored %d tables, %d files, %d qdrant points (source dialect=%s)", len(counts), filesRestored, qdrantRestored, man.Dialect)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"tables":           counts,
		"files_restored":   filesRestored,
		"includes_files":   man.IncludesFiles,
		"qdrant_restored":  qdrantRestored,
		"qdrant_error":     "",
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
func restoreDatabase(ctx context.Context, d Deps, zr *zip.Reader, man backupManifest, importingAdminID string, beforeCommit ...func() error) (map[string]int64, error) {
	if store.IsPostgres() {
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		var admin *backupImportAdminSnapshot
		if strings.TrimSpace(importingAdminID) != "" {
			admin, err = loadBackupImportAdmin(ctx, tx, importingAdminID)
			if err != nil {
				return nil, err
			}
		}
		counts, err := restoreInto(ctx, tx, zr, man, d, admin)
		if err != nil {
			return nil, err
		}
		if err := store.ResetSerialSequences(ctx, tx); err != nil {
			return nil, err
		}
		if err := runImportBeforeCommit(beforeCommit); err != nil {
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
	var admin *backupImportAdminSnapshot
	if strings.TrimSpace(importingAdminID) != "" {
		admin, err = loadBackupImportAdmin(ctx, tx, importingAdminID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	counts, err := restoreInto(ctx, tx, zr, man, d, admin)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := runImportBeforeCommit(beforeCommit); err != nil {
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
func restoreInto(ctx context.Context, ex store.RowExecer, zr *zip.Reader, man backupManifest, d Deps, importingAdmin *backupImportAdminSnapshot) (map[string]int64, error) {
	// A current archive's usage_stats file is authoritative. Disable the
	// usage_logs insert mirror during the load so restored logs cannot duplicate
	// or synthesize facts; old archives are backfilled explicitly below.
	if err := store.DisableUsageStatsMirror(ctx, ex); err != nil {
		return nil, err
	}
	if err := store.WipeAll(ctx, ex); err != nil {
		return nil, err
	}
	counts := make(map[string]int64)
	hasUsageStatsArchive := findZipFile(zr, "db/usage_stats.jsonl") != nil
	hasMessageFeedbackArchive := findZipFile(zr, "db/message_feedback.jsonl") != nil
	// messages.parent_id is a self-referencing FK. Old archives (and any
	// same-second creation ties) don't guarantee parents sort before children,
	// so on Postgres the constraint is detached for the load and re-attached
	// after — all inside the restore transaction, so failure rolls back both
	// the rows and the DDL. (SQLite runs the whole restore with FKs off.)
	if store.IsPostgres() {
		if _, err := ex.ExecContext(ctx, `ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_parent_id_fkey`); err != nil {
			return nil, fmt.Errorf("detach messages parent fk: %w", err)
		}
	}
	for _, t := range store.BackupTableOrder() {
		entry := findZipFile(zr, "db/"+t+".jsonl")
		if entry == nil {
			continue // table absent from an older/partial archive
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, err
		}
		reader, err := normalizeArchiveTableReader(t, rc)
		if err != nil {
			_ = rc.Close()
			return nil, err
		}
		n, err := store.RestoreTable(ctx, ex, t, reader)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		counts[t] = n
	}
	if err := validateImportedContextCompactionModel(ctx, ex); err != nil {
		return nil, err
	}
	if err := store.MigrateLegacyOAuthProviderKinds(ctx, ex); err != nil {
		return nil, fmt.Errorf("migrate restored legacy UserInfo OAuth providers: %w", err)
	}
	if !hasUsageStatsArchive {
		n, err := store.BackfillUsageStats(ctx, ex)
		if err != nil {
			return nil, err
		}
		counts["usage_stats"] = n
	}
	if !hasMessageFeedbackArchive {
		n, err := store.BackfillLegacyMessageFeedback(ctx, ex)
		if err != nil {
			return nil, err
		}
		counts["message_feedback"] = n
	}
	if err := store.EnableUsageStatsMirror(ctx, ex); err != nil {
		return nil, err
	}
	if err := store.EnsureSettlementCurrencySetting(ctx, ex); err != nil {
		return nil, fmt.Errorf("ensure settlement currency setting: %w", err)
	}
	if err := store.MigrateLegacyCreditPackage(ctx, ex); err != nil {
		return nil, fmt.Errorf("migrate legacy permanent-credit package: %w", err)
	}
	if err := validateBillingConfiguration(ctx, ex); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidBillingConfigArchive, err)
	}
	if store.IsPostgres() {
		// Ancient data may carry genuinely dangling parent ids (pre-FK eras).
		// Promote those replies to roots instead of failing the whole import.
		if _, err := ex.ExecContext(ctx,
			`UPDATE messages SET parent_id=NULL WHERE parent_id IS NOT NULL AND parent_id NOT IN (SELECT id FROM messages)`); err != nil {
			return nil, fmt.Errorf("clear dangling message parents: %w", err)
		}
		if _, err := ex.ExecContext(ctx,
			`ALTER TABLE messages ADD CONSTRAINT messages_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES messages(id) ON DELETE CASCADE`); err != nil {
			return nil, fmt.Errorf("re-attach messages parent fk: %w", err)
		}
	}
	if err := rewriteStoragePaths(ctx, ex, man, d); err != nil {
		return nil, err
	}
	if importingAdmin != nil {
		if err := reconcileBackupImportAdmin(ctx, ex, importingAdmin); err != nil {
			return nil, err
		}
		// The reconciliation may have retained the verified admin in addition to
		// rows from the archive, so report the committed user count accurately.
		var n int64
		if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err == nil {
			counts["users"] = n
		}
	}
	tx, ok := ex.(*sql.Tx)
	if !ok {
		return nil, errors.New("backup restore requires a transaction")
	}
	if err := store.LockAuthConfigurationTx(ctx, tx); err != nil {
		return nil, err
	}
	adminID := ""
	if importingAdmin != nil {
		adminID = importingAdmin.ID
	}
	if err := validateCurrentAuthPolicyTx(ctx, tx, adminID); err != nil {
		return nil, fmt.Errorf("%w: enterprise authentication policy: %v", errInvalidOAuthConfigArchive, err)
	}
	return counts, nil
}

// loadBackupImportAdmin takes the security snapshot while the restore
// transaction owns a write lock on the caller's row. The request's cached
// role is deliberately ignored: this query is the last authorization check
// before WipeAll.
func loadBackupImportAdmin(ctx context.Context, ex store.RowExecer, userID string) (*backupImportAdminSnapshot, error) {
	userID = strings.TrimSpace(userID)
	active, err := lockActiveImportAdmin(ctx, ex, userID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, errBackupImportAdminUnauthorized
	}

	s := &backupImportAdminSnapshot{ID: userID, Providers: map[string]store.OAuthProvider{}}
	var totpEnabled, passwordSet int
	err = ex.QueryRowContext(ctx, `
		SELECT id, email, password_hash, name, token_ver, settings, group_id,
		       group_expires_at, previous_group_id, totp_secret, totp_enabled,
		       password_set, password_changed_at, last_seen_at,
		       COALESCE(credits_permanent,0), COALESCE(credits_permanent_micros,0),
		       credit_cycle_anchor, quota_cycle_anchor, sort_order, created_at
		  FROM users WHERE id=?`, userID).Scan(
		&s.ID, &s.Email, &s.PasswordHash, &s.Name, &s.TokenVer, &s.Settings, &s.GroupID,
		&s.GroupExpiresAt, &s.PreviousGroupID, &s.TotpSecret, &totpEnabled, &passwordSet,
		&s.PasswordChangedAt, &s.LastSeenAt, &s.CreditsPermanent, &s.CreditsMicros,
		&s.CreditCycleAnchor, &s.QuotaCycleAnchor, &s.SortOrder, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errBackupImportAdminUnauthorized
	}
	if err != nil {
		return nil, err
	}
	s.TotpEnabled = totpEnabled
	s.PasswordSet = passwordSet
	if strings.TrimSpace(s.Settings) == "" {
		s.Settings = "{}"
	}

	rows, err := ex.QueryContext(ctx, `
		SELECT i.provider_id, i.subject, i.email, i.created_at,
		       p.id, p.kind, p.name, p.icon, p.client_id, p.client_secret,
		       p.issuer_url, p.jwks_url, p.auth_url, p.token_url, p.userinfo_url,
		       p.scopes, p.team_id, p.key_id, p.subject_namespace, p.enabled,
		       p.sort_order, p.updated_at
		  FROM oauth_identities i
		  JOIN oauth_providers p ON p.id=i.provider_id
		 WHERE i.user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var identity backupImportOAuthIdentity
		var provider store.OAuthProvider
		var enabled int
		if err := rows.Scan(
			&identity.ProviderID, &identity.Subject, &identity.Email, &identity.CreatedAt,
			&provider.ID, &provider.Kind, &provider.Name, &provider.Icon, &provider.ClientID, &provider.ClientSecret,
			&provider.IssuerURL, &provider.JWKSURL, &provider.AuthURL, &provider.TokenURL, &provider.UserInfoURL,
			&provider.Scopes, &provider.TeamID, &provider.KeyID, &provider.SubjectNamespace,
			&enabled, &provider.SortOrder, &provider.UpdatedAt,
		); err != nil {
			return nil, err
		}
		provider.Enabled = enabled != 0
		provider.HasSecret = provider.ClientSecret != ""
		s.Identities = append(s.Identities, identity)
		s.Providers[provider.ID] = provider
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	usablePassword := s.PasswordSet != 0 && strings.TrimSpace(s.PasswordHash) != ""
	usableOAuth := false
	for _, identity := range s.Identities {
		if strings.TrimSpace(identity.Subject) == "" {
			continue
		}
		if provider, ok := s.Providers[identity.ProviderID]; ok && provider.Enabled {
			usableOAuth = true
			break
		}
	}
	if !usablePassword && !usableOAuth {
		// Do this before WipeAll. A password-less administrator with no surviving
		// enabled provider would otherwise make a successful restore permanently
		// un-loginable.
		return nil, fmt.Errorf("%w: importing administrator has no usable login identity", errBackupImportAdminUnauthorized)
	}
	return s, nil
}

// lockActiveImportAdmin establishes the authorization linearization point for
// destructive or secret-bearing imports. PostgreSQL locks every active admin in
// the same stable order as the role/status guards; SQLite's conditional write
// obtains its database write lock. Both locks live until this transaction ends.
func lockActiveImportAdmin(ctx context.Context, ex store.RowExecer, userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	if store.IsPostgres() {
		// Match the lock order used by SetUserRole/SetUserStatusGuarded. Full
		// restore later deletes every user row; locking only the caller first could
		// otherwise deadlock with a guard that already locked an earlier admin id.
		rows, err := ex.QueryContext(ctx,
			`SELECT id FROM users WHERE role='admin' AND status='active' ORDER BY id FOR UPDATE`)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		active := false
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return false, err
			}
			if id == userID {
				active = true
			}
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		return active, nil
	}
	locked, err := ex.ExecContext(ctx,
		`UPDATE users SET token_ver=token_ver WHERE id=? AND role='admin' AND status='active'`, userID)
	if err != nil {
		return false, err
	}
	n, err := locked.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// runImportBeforeCommit executes persistent non-SQL writes at the end of the
// transaction, while its active-admin authorization locks are still held.
func runImportBeforeCommit(writes []func() error) error {
	for _, write := range writes {
		if write == nil {
			continue
		}
		if err := write(); err != nil {
			return err
		}
	}
	return nil
}

func reconcileBackupImportAdmin(ctx context.Context, ex store.RowExecer, snap *backupImportAdminSnapshot) error {
	if snap == nil || strings.TrimSpace(snap.Email) == "" {
		return errBackupImportAdminUnauthorized
	}
	// Invalidate imported administrator sessions before lowering their roles.
	// Both updates are inside the restore transaction, so no extra admin can
	// remain usable if a later step fails.
	if _, err := ex.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id IN (SELECT id FROM users WHERE role='admin')`); err != nil {
		return err
	}
	if _, err := ex.ExecContext(ctx, `UPDATE users SET role='user', token_ver=token_ver+1 WHERE role='admin'`); err != nil {
		return err
	}

	type candidate struct {
		id       string
		tokenVer int
	}
	rows, err := ex.QueryContext(ctx,
		`SELECT id, token_ver FROM users WHERE lower(trim(email))=lower(trim(?))`, snap.Email)
	if err != nil {
		return err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.tokenVer); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(candidates) > 1 {
		return fmt.Errorf("%w: backup contains duplicate administrator email", errBackupImportAdminUnauthorized)
	}

	chosenID := ""
	importedTokenVer := 0
	if len(candidates) == 1 {
		chosenID = candidates[0].id
		importedTokenVer = candidates[0].tokenVer
	} else {
		var exists int
		if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, snap.ID).Scan(&exists); err != nil {
			return err
		}
		chosenID = snap.ID
		if exists != 0 {
			// The original id belongs to a different imported user. Keep that
			// user's data and allocate a fresh id for the verified administrator.
			for attempt := 0; attempt < 8; attempt++ {
				candidateID := "backup_admin_" + randomHexStringForRestore()
				var n int
				if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, candidateID).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					chosenID = candidateID
					break
				}
			}
			if chosenID == snap.ID {
				return errors.New("allocate restored administrator id")
			}
		}
	}
	// The verified administrator may have been remapped to a fresh id when the
	// archive's original id belongs to another local user. Keep subsequent
	// policy validation anchored to the account that was actually restored.
	snap.ID = chosenID

	newTokenVer := snap.TokenVer + 1
	if importedTokenVer >= newTokenVer {
		newTokenVer = importedTokenVer + 1
	}
	if len(candidates) == 1 {
		if _, err := ex.ExecContext(ctx, `UPDATE users SET
			email=?, name=?, password_hash=?, role='admin', status='active', token_ver=?,
			settings=?, group_id=?, group_expires_at=?, previous_group_id=?,
			totp_secret=?, totp_enabled=?, password_set=?, password_changed_at=?, last_seen_at=?,
			credits_permanent=?, credits_permanent_micros=?, credit_cycle_anchor=?, quota_cycle_anchor=?,
			sort_order=?, created_at=? WHERE id=?`,
			snap.Email, snap.Name, snap.PasswordHash, newTokenVer,
			snap.Settings, snap.GroupID, snap.GroupExpiresAt, snap.PreviousGroupID,
			snap.TotpSecret, snap.TotpEnabled, snap.PasswordSet, snap.PasswordChangedAt, snap.LastSeenAt,
			snap.CreditsPermanent, snap.CreditsMicros, snap.CreditCycleAnchor, snap.QuotaCycleAnchor,
			snap.SortOrder, snap.CreatedAt, chosenID); err != nil {
			return err
		}
	} else {
		groupID := strings.TrimSpace(snap.GroupID)
		if groupID == "" {
			groupID = store.DefaultGroupID
		}
		createdAt := snap.CreatedAt
		if createdAt <= 0 {
			createdAt = time.Now().Unix()
		}
		if _, err := ex.ExecContext(ctx, `INSERT INTO users(
			id, email, password_hash, name, role, status, token_ver, settings, group_id,
			group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set,
			password_changed_at, last_seen_at, credits_permanent, credits_permanent_micros,
			credit_cycle_anchor, quota_cycle_anchor, sort_order, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			chosenID, snap.Email, snap.PasswordHash, snap.Name, "admin", "active", newTokenVer,
			snap.Settings, groupID, snap.GroupExpiresAt, snap.PreviousGroupID, snap.TotpSecret,
			snap.TotpEnabled, snap.PasswordSet, snap.PasswordChangedAt, snap.LastSeenAt,
			snap.CreditsPermanent, snap.CreditsMicros, snap.CreditCycleAnchor, snap.QuotaCycleAnchor,
			snap.SortOrder, createdAt); err != nil {
			return err
		}
	}

	// Never retain identities that came from the untrusted backup for the
	// account we are elevating. Only the pre-verified snapshot identities below
	// are restored.
	if _, err := ex.ExecContext(ctx, `DELETE FROM oauth_identities WHERE user_id=?`, chosenID); err != nil {
		return err
	}
	for _, provider := range snap.Providers {
		// Preserve the original provider id for callback compatibility, but never
		// delete or mutate an unrelated imported provider to make room. A name
		// collision makes the archive irreconcilable and rolls the restore back.
		var nameConflicts int
		if err := ex.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM oauth_providers WHERE lower(trim(name))=lower(trim(?)) AND id<>?`, provider.Name, provider.ID).Scan(&nameConflicts); err != nil {
			return err
		}
		if nameConflicts != 0 {
			return fmt.Errorf("%w: OAuth provider name conflicts with importing administrator identity", errBackupImportAdminUnauthorized)
		}
		if _, err := ex.ExecContext(ctx, `INSERT INTO oauth_providers(
			id, kind, name, icon, client_id, client_secret, issuer_url, jwks_url,
			auth_url, token_url, userinfo_url, scopes, team_id, key_id, subject_namespace,
			enabled, sort_order, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, name=excluded.name,
			icon=excluded.icon, client_id=excluded.client_id, client_secret=excluded.client_secret,
			issuer_url=excluded.issuer_url, jwks_url=excluded.jwks_url, auth_url=excluded.auth_url,
			token_url=excluded.token_url, userinfo_url=excluded.userinfo_url, scopes=excluded.scopes,
			team_id=excluded.team_id, key_id=excluded.key_id, subject_namespace=excluded.subject_namespace,
			enabled=excluded.enabled,
			sort_order=excluded.sort_order, updated_at=excluded.updated_at`,
			provider.ID, provider.Kind, provider.Name, provider.Icon, provider.ClientID, provider.ClientSecret,
			provider.IssuerURL, provider.JWKSURL, provider.AuthURL, provider.TokenURL, provider.UserInfoURL,
			provider.Scopes, provider.TeamID, provider.KeyID, provider.SubjectNamespace,
			boolToInt(provider.Enabled), provider.SortOrder, time.Now().Unix()); err != nil {
			return err
		}
	}
	for _, identity := range snap.Identities {
		var existingOwner string
		err := ex.QueryRowContext(ctx,
			`SELECT user_id FROM oauth_identities WHERE provider_id=? AND subject=?`, identity.ProviderID, identity.Subject).Scan(&existingOwner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && existingOwner != chosenID {
			return fmt.Errorf("%w: OAuth identity belongs to a different imported user", errBackupImportAdminUnauthorized)
		}
		if _, err := ex.ExecContext(ctx, `DELETE FROM oauth_identities WHERE provider_id=? AND subject=? AND user_id=?`,
			identity.ProviderID, identity.Subject, chosenID); err != nil {
			return err
		}
		if _, err := ex.ExecContext(ctx, `INSERT INTO oauth_identities(provider_id, subject, user_id, email)
			VALUES(?,?,?,?)`,
			identity.ProviderID, identity.Subject, chosenID, strings.ToLower(strings.TrimSpace(identity.Email))); err != nil {
			return err
		}
	}
	if _, err := ex.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, chosenID); err != nil {
		return err
	}

	var activeAdmins int
	if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&activeAdmins); err != nil {
		return err
	}
	if activeAdmins < 1 {
		return fmt.Errorf("%w: restore produced no active administrator", errBackupImportAdminUnauthorized)
	}
	if snap.PasswordSet == 0 {
		var usable int
		if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_identities i JOIN oauth_providers p ON p.id=i.provider_id WHERE i.user_id=? AND p.enabled=1`, chosenID).Scan(&usable); err != nil {
			return err
		}
		if usable == 0 {
			return fmt.Errorf("%w: restored administrator has no enabled OAuth identity", errBackupImportAdminUnauthorized)
		}
	}
	return nil
}

func randomHexStringForRestore() string {
	// randomHex is defined in admin_skill_assets.go and uses crypto/rand. Keep a
	// small wrapper here so the reconciliation code is self-documenting.
	if value, err := randomHex(16); err == nil && value != "" {
		return value
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
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
			next, err := fileguard.Remap(s.src, s.dst, p)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("%w: %s.%s: %v", errInvalidBackupStoragePath, s.table, id, err)
			}
			ups = append(ups, upd{id, next})
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
	// Full archives carry skill assets in the same upload tree, but the paths
	// are nested under a dedicated subdirectory and were historically omitted
	// from this rewrite. Validate and remap them just like files/documents;
	// malformed JSON is a restore error, never a reason to retain an attacker-
	// supplied path.
	rows, err := ex.QueryContext(ctx, `SELECT id, assets FROM skills`)
	if err != nil {
		return err
	}
	type skillUpdate struct{ id, assets string }
	var skillUpdates []skillUpdate
	skillSource := ""
	skillTarget := ""
	if strings.TrimSpace(man.SourceUploadDir) != "" {
		skillSource = filepath.Join(filepath.Clean(man.SourceUploadDir), skillAssetsSubdir)
		skillTarget = filepath.Join(filepath.Clean(d.Config.UploadDir), skillAssetsSubdir)
	}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "null" || trimmed == "[]" {
			continue
		}
		var assets []skillAssetRow
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: skills.%s.assets: invalid JSON: %v", errInvalidBackupStoragePath, id, err)
		}
		changed := false
		for i := range assets {
			if strings.TrimSpace(assets[i].StoragePath) == "" {
				continue
			}
			if skillSource == "" {
				_ = rows.Close()
				return fmt.Errorf("%w: skills.%s.assets[%d]: manifest source upload root is empty", errInvalidBackupStoragePath, id, i)
			}
			next, err := fileguard.Remap(skillSource, skillTarget, assets[i].StoragePath)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("%w: skills.%s.assets[%d]: %v", errInvalidBackupStoragePath, id, i, err)
			}
			if next != assets[i].StoragePath {
				assets[i].StoragePath = next
				changed = true
			}
		}
		if changed {
			encoded, err := json.Marshal(assets)
			if err != nil {
				_ = rows.Close()
				return err
			}
			skillUpdates = append(skillUpdates, skillUpdate{id: id, assets: string(encoded)})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, update := range skillUpdates {
		if _, err := ex.ExecContext(ctx, `UPDATE skills SET assets=? WHERE id=?`, update.assets, update.id); err != nil {
			return err
		}
	}
	return nil
}

// restoreFilesFromZip extracts the bundled uploads/artifacts back onto disk
// under the configured dirs. Best-effort: a failed file is logged-by-omission
// and skipped. The caller runs this before committing the locked DB restore, so
// authorization revocation cannot complete while files are still being written.
// Returns the number of files written. Guards against path traversal entries.
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
		if restoreZipEntry(base, filepath.FromSlash(rel), f) {
			n++
		}
	}
	return n
}

func mergeConfigArchive(ctx context.Context, d Deps, zr *zip.Reader, man configManifest, importingAdminID string, beforeCommit ...func() error) (map[string]int64, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Authentication settings and OAuth providers form one lockout-sensitive
	// configuration. Keep the same database lock order as the admin settings
	// and provider endpoints before authorizing and merging the archive.
	if err := store.LockAuthConfigurationTx(ctx, tx); err != nil {
		return nil, err
	}
	active, err := lockActiveImportAdmin(ctx, tx, importingAdminID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, errConfigImportAdminUnauthorized
	}

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
		reader, err := normalizeConfigArchiveTableReader(ctx, tx, t, rc)
		if err != nil {
			_ = rc.Close()
			if errors.Is(err, errInvalidCompactionConfigArchive) {
				return nil, err
			}
			if t == "settings" || t == "user_groups" || t == "models" || t == "model_group_quotas" || t == "credit_packages" {
				return nil, fmt.Errorf("%w: %v", errInvalidBillingConfigArchive, err)
			}
			return nil, err
		}
		n, err := store.UpsertTable(ctx, tx, t, reader)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		counts[t] = n
	}
	// Validate the transaction's final state even when the archive did not
	// include the setting itself. A config import can still disable or mutate
	// the currently selected model/channel and would otherwise leave a broken
	// compaction model reference behind.
	if err := validateImportedContextCompactionModel(ctx, tx); err != nil {
		return nil, err
	}
	if err := store.EnsureSettlementCurrencySetting(ctx, tx); err != nil {
		return nil, fmt.Errorf("ensure settlement currency setting: %w", err)
	}
	if err := store.MigrateLegacyCreditPackage(ctx, tx); err != nil {
		return nil, fmt.Errorf("migrate legacy permanent-credit package: %w", err)
	}
	if err := validateBillingConfiguration(ctx, tx); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidBillingConfigArchive, err)
	}
	if err := validateCurrentAuthPolicyTx(ctx, tx, importingAdminID); err != nil {
		return nil, fmt.Errorf("%w: enterprise authentication policy: %v", errInvalidOAuthConfigArchive, err)
	}
	if err := rewriteConfigSkillAssetPaths(ctx, tx, man, d); err != nil {
		return nil, err
	}
	if err := runImportBeforeCommit(beforeCommit); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

// normalizeConfigArchiveTableReader applies the same provider-specific
// validation as the payment admin API before generic config UPSERTs can write
// payment rows. State-dependent checks run on tx so a failure rolls back the
// entire archive, including tables merged before the payment tables.
func normalizeConfigArchiveTableReader(ctx context.Context, tx *sql.Tx, table string, r io.Reader) (io.Reader, error) {
	switch table {
	case "oauth_providers":
		return normalizeConfigOAuthProviderRows(ctx, tx, r)
	case "payment_channels":
		return normalizeConfigPaymentChannelRows(ctx, tx, r)
	case "payment_methods":
		return normalizeConfigPaymentMethodRows(ctx, tx, r)
	default:
		return normalizeArchiveTableReader(table, r)
	}
}

func normalizeConfigOAuthProviderRows(ctx context.Context, tx *sql.Tx, r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	seenIDs := map[string]bool{}
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, invalidOAuthConfigArchivef("decode oauth_providers row: %v", err)
		}

		id, present, err := backupStringField(row, "id")
		if err != nil || !present || id == "" {
			return nil, invalidOAuthConfigArchivef("oauth_providers row has an invalid id")
		}
		if seenIDs[id] {
			return nil, invalidOAuthConfigArchivef("oauth_providers contains duplicate id %s", id)
		}
		seenIDs[id] = true
		_, archiveMarkerPresent := row["subject_namespace"]
		incomingKind, kindPresent, kindErr := backupStringField(row, "kind")
		incomingUserInfo, userInfoPresent, userInfoErr := backupStringField(row, "userinfo_url")
		incomingIssuer, issuerPresent, issuerErr := backupStringField(row, "issuer_url")
		incomingJWKS, jwksPresent, jwksErr := backupStringField(row, "jwks_url")
		if kindErr != nil || userInfoErr != nil || issuerErr != nil || jwksErr != nil {
			return nil, invalidOAuthConfigArchivef("oauth_providers[%s] has malformed trust fields", id)
		}
		legacyUserInfoRow := !archiveMarkerPresent && kindPresent && incomingKind == "oidc" &&
			userInfoPresent && incomingUserInfo != "" &&
			(!issuerPresent || incomingIssuer == "") && (!jwksPresent || incomingJWKS == "")

		current, err := store.GetOAuthProviderForUpdate(ctx, tx, id)
		exists := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		final := store.OAuthProvider{ID: id, Enabled: true}
		var currentEffective store.OAuthProvider
		if exists {
			currentEffective = effectiveOAuthProvider(*current)
			currentNamespace := oauth.Resolve(toOAuthConfig(&currentEffective)).SubjectNamespace()
			current, err = store.InitializeOAuthProviderSubjectNamespaceTx(
				ctx, tx, *current, currentNamespace,
			)
			if err != nil {
				return nil, err
			}
			final = *current
		}

		applyString := func(key string, dst *string) error {
			value, fieldPresent, fieldErr := backupStringField(row, key)
			if fieldErr != nil {
				return invalidOAuthConfigArchivef("oauth_providers[%s].%s: %v", id, key, fieldErr)
			}
			if fieldPresent {
				*dst = value
			}
			return nil
		}
		for _, field := range []struct {
			name string
			dst  *string
		}{
			{"kind", &final.Kind}, {"name", &final.Name}, {"icon", &final.Icon},
			{"client_id", &final.ClientID}, {"issuer_url", &final.IssuerURL},
			{"jwks_url", &final.JWKSURL}, {"auth_url", &final.AuthURL},
			{"token_url", &final.TokenURL}, {"userinfo_url", &final.UserInfoURL},
			{"scopes", &final.Scopes}, {"team_id", &final.TeamID}, {"key_id", &final.KeyID},
		} {
			if err := applyString(field.name, field.dst); err != nil {
				return nil, err
			}
		}
		secretPresent := false
		if raw, ok := row["client_secret"]; ok {
			secretPresent = true
			if err := json.Unmarshal(raw, &final.ClientSecret); err != nil {
				return nil, invalidOAuthConfigArchivef("oauth_providers[%s].client_secret: %v", id, err)
			}
		}
		if enabled, fieldPresent, err := backupBoolField(row, "enabled"); err != nil {
			return nil, invalidOAuthConfigArchivef("oauth_providers[%s].enabled: %v", id, err)
		} else if fieldPresent {
			final.Enabled = enabled
		}

		if legacyUserInfoRow {
			// Version-1 config archives used kind=oidc for generic OAuth UserInfo
			// providers. A missing marker distinguishes that legacy shape from a
			// newly-created, intentionally incomplete OIDC draft.
			final.Kind = "oauth2"
			final.IssuerURL = ""
			final.JWKSURL = ""
			if strings.TrimSpace(final.Scopes) == "" {
				final.Scopes = "openid email profile"
			}
		}
		if err := validateOAuthKind(final.Kind); err != nil {
			return nil, invalidOAuthConfigArchivef("oauth_providers[%s].kind: %v", id, err)
		}
		final.Name = strings.TrimSpace(final.Name)
		if final.Name == "" {
			return nil, invalidOAuthConfigArchivef("oauth_providers[%s].name is required", id)
		}
		final = effectiveOAuthProvider(final)
		if exists && !secretPresent && oauthClientSecretReentryRequired(currentEffective, final, nil) {
			// Match the admin PATCH contract: a partial archive cannot silently
			// retain or clear a credential across a different trust boundary.
			return nil, fmt.Errorf("%w: oauth_providers[%s]", errOAuthClientSecretReentryRequired, id)
		}
		if err := validateOAuthProviderTrust(final); err != nil {
			return nil, invalidOAuthConfigArchivef("oauth_providers[%s]: %v", id, err)
		}
		final.SubjectNamespace = oauth.Resolve(toOAuthConfig(&final)).SubjectNamespace()

		setConfigArchiveString(row, "id", final.ID)
		setConfigArchiveString(row, "kind", final.Kind)
		setConfigArchiveString(row, "name", final.Name)
		setConfigArchiveString(row, "icon", final.Icon)
		setConfigArchiveString(row, "client_id", final.ClientID)
		setConfigArchiveString(row, "client_secret", final.ClientSecret)
		setConfigArchiveString(row, "issuer_url", final.IssuerURL)
		setConfigArchiveString(row, "jwks_url", final.JWKSURL)
		setConfigArchiveString(row, "auth_url", final.AuthURL)
		setConfigArchiveString(row, "token_url", final.TokenURL)
		setConfigArchiveString(row, "userinfo_url", final.UserInfoURL)
		setConfigArchiveString(row, "scopes", final.Scopes)
		setConfigArchiveString(row, "team_id", final.TeamID)
		setConfigArchiveString(row, "key_id", final.KeyID)
		setConfigArchiveString(row, "subject_namespace", final.SubjectNamespace)
		if final.Enabled {
			row["enabled"] = json.RawMessage("1")
		} else {
			row["enabled"] = json.RawMessage("0")
		}
		if err := enc.Encode(row); err != nil {
			return nil, invalidOAuthConfigArchivef("encode oauth_providers[%s]: %v", id, err)
		}
	}
}

func invalidOAuthConfigArchivef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidOAuthConfigArchive, fmt.Sprintf(format, args...))
}

type configPaymentChannelState struct {
	Name        string
	Provider    string
	Environment string
	Config      json.RawMessage
}

func invalidPaymentConfigArchivef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidPaymentConfigArchive, fmt.Sprintf(format, args...))
}

func setConfigArchiveString(row map[string]json.RawMessage, key, value string) {
	row[key], _ = json.Marshal(value)
}

func validateConfigArchiveCommonPaymentFields(table, id string, row map[string]json.RawMessage) error {
	if enabled, present, err := backupBoolField(row, "enabled"); err != nil {
		return invalidPaymentConfigArchivef("%s[%s].enabled: %v", table, id, err)
	} else if present {
		if enabled {
			row["enabled"] = json.RawMessage("1")
		} else {
			row["enabled"] = json.RawMessage("0")
		}
	}
	for _, field := range []string{"sort_order", "created_at", "updated_at"} {
		if _, _, err := backupIntField(row, field); err != nil {
			return invalidPaymentConfigArchivef("%s[%s].%s: %v", table, id, field, err)
		}
	}
	return nil
}

func configPaymentChannelForUpdate(ctx context.Context, tx *sql.Tx, id string) (configPaymentChannelState, bool, error) {
	query := `SELECT name, provider, environment, config FROM payment_channels WHERE id=?`
	if store.IsPostgres() {
		// CreatePaymentOrder takes FOR SHARE on this row. FOR UPDATE therefore
		// serializes credential/provider imports with checkout creation: either
		// the new order is visible to the pending check, or checkout starts only
		// after the import commits with the new channel configuration.
		query += ` FOR UPDATE`
	}
	var state configPaymentChannelState
	var config string
	err := tx.QueryRowContext(ctx, query, id).Scan(&state.Name, &state.Provider, &state.Environment, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	state.Config = json.RawMessage(config)
	return state, true, nil
}

func configPaymentChannelHasMethods(ctx context.Context, tx *sql.Tx, channelID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
		SELECT 1 FROM payment_methods WHERE channel_id=?
	) THEN 1 ELSE 0 END`, channelID).Scan(&exists)
	return exists != 0, err
}

func configPaymentChannelHasPendingOrders(ctx context.Context, tx *sql.Tx, channelID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
		SELECT 1 FROM payment_orders
		 WHERE channel_id=? AND status IN (?, ?)
	) THEN 1 ELSE 0 END`, channelID, store.PaymentOrderPending, store.PaymentOrderProcessing).Scan(&exists)
	return exists != 0, err
}

func ensureConfigPaymentChannelNameAvailable(ctx context.Context, tx *sql.Tx, id, name string) error {
	var otherID string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM payment_channels WHERE lower(trim(name))=lower(trim(?)) AND id<>? LIMIT 1`,
		name, id,
	).Scan(&otherID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: payment channel name %q", store.ErrPaymentChannelNameExists, name)
}

func normalizeConfigPaymentChannelRows(ctx context.Context, tx *sql.Tx, r io.Reader) (io.Reader, error) {
	settlementCurrency := configArchiveSettlementCurrency(ctx, tx)
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	seenIDs := map[string]bool{}
	seenNames := map[string]string{}
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, invalidPaymentConfigArchivef("decode payment_channels row: %v", err)
		}

		id, idPresent, err := backupStringField(row, "id")
		if err != nil || !idPresent || id == "" {
			return nil, invalidPaymentConfigArchivef("payment_channels row has an invalid id")
		}
		if seenIDs[id] {
			return nil, invalidPaymentConfigArchivef("payment_channels contains duplicate id %s", id)
		}
		seenIDs[id] = true
		existing, exists, err := configPaymentChannelForUpdate(ctx, tx, id)
		if err != nil {
			return nil, err
		}

		name, namePresent, err := backupStringField(row, "name")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].name: %v", id, err)
		}
		if !namePresent && exists {
			name = strings.TrimSpace(existing.Name)
		}
		if name == "" || len(name) > 120 {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].name is required or too long", id)
		}
		nameKey := strings.ToLower(name)
		if otherID, duplicate := seenNames[nameKey]; duplicate && otherID != id {
			return nil, fmt.Errorf("%w: payment channel name %q", store.ErrPaymentChannelNameExists, name)
		}
		seenNames[nameKey] = id

		providerInput, providerPresent, err := backupStringField(row, "provider")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].provider: %v", id, err)
		}
		if !providerPresent && exists {
			providerInput = existing.Provider
		}
		provider, err := normalizePaymentProvider(providerInput)
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].provider: %v", id, err)
		}
		providerChanged := exists && provider != existing.Provider

		configText, configPresent, err := backupStringField(row, "config")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].config: %v", id, err)
		}
		if !configPresent {
			if !exists || providerChanged {
				return nil, invalidPaymentConfigArchivef("payment_channels[%s].config is required", id)
			}
			configText = string(existing.Config)
		}
		// Config archives contain plaintext secrets. Starting with an empty base
		// rejects masked secrets instead of accidentally retaining credentials
		// from the deployment receiving the archive.
		mergedConfig, err := mergePaymentChannelConfig(provider, nil, json.RawMessage(configText))
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].config: %v", id, err)
		}
		config, err := normalizePaymentChannelConfig(provider, mergedConfig)
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].config: %v", id, err)
		}
		if err := validatePaymentChannelSettlementConfig(provider, config, settlementCurrency); err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].config: %v", id, err)
		}

		environmentInput, environmentPresent, err := backupStringField(row, "environment")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].environment: %v", id, err)
		}
		if !environmentPresent && exists {
			environmentInput = existing.Environment
		}
		environment, err := normalizePaymentEnvironment(provider, config, environmentInput)
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_channels[%s].environment: %v", id, err)
		}

		if err := ensureConfigPaymentChannelNameAvailable(ctx, tx, id, name); err != nil {
			return nil, err
		}
		if providerChanged {
			hasMethods, checkErr := configPaymentChannelHasMethods(ctx, tx, id)
			if checkErr != nil {
				return nil, checkErr
			}
			if hasMethods {
				return nil, fmt.Errorf("%w: payment channel %s", store.ErrPaymentChannelHasMethods, id)
			}
		}

		configChanged := false
		if exists {
			currentConfig, currentErr := normalizePaymentChannelConfig(existing.Provider, existing.Config)
			configChanged = currentErr != nil || providerChanged || !bytes.Equal(currentConfig, config)
		}
		environmentChanged := exists && environment != existing.Environment
		if exists && (providerChanged || configChanged || environmentChanged) {
			hasPending, checkErr := configPaymentChannelHasPendingOrders(ctx, tx, id)
			if checkErr != nil {
				return nil, checkErr
			}
			if hasPending {
				return nil, fmt.Errorf("%w: payment channel %s", store.ErrPaymentChannelHasPending, id)
			}
		}

		if err := validateConfigArchiveCommonPaymentFields("payment_channels", id, row); err != nil {
			return nil, err
		}
		setConfigArchiveString(row, "id", id)
		setConfigArchiveString(row, "name", name)
		setConfigArchiveString(row, "provider", provider)
		setConfigArchiveString(row, "environment", environment)
		setConfigArchiveString(row, "config", string(config))
		if err := enc.Encode(row); err != nil {
			return nil, invalidPaymentConfigArchivef("encode payment_channels[%s]: %v", id, err)
		}
	}
}

func configArchiveSettlementCurrency(ctx context.Context, tx *sql.Tx) string {
	currency := store.DefaultSettlementCurrency
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='settlement_currency'`).Scan(&raw); err == nil {
		var configured string
		if json.Unmarshal([]byte(raw), &configured) == nil {
			configured = strings.ToUpper(strings.TrimSpace(configured))
			if validSettlementCurrency(configured) {
				currency = configured
			}
		}
	}
	return currency
}

type configPaymentMethodState struct {
	ChannelID string
	Name      string
	Icon      string
	Config    json.RawMessage
}

func configPaymentMethodForUpdate(ctx context.Context, tx *sql.Tx, id string) (configPaymentMethodState, bool, error) {
	query := `SELECT channel_id, name, icon, config FROM payment_methods WHERE id=?`
	if store.IsPostgres() {
		query += ` FOR UPDATE`
	}
	var state configPaymentMethodState
	var config string
	err := tx.QueryRowContext(ctx, query, id).Scan(&state.ChannelID, &state.Name, &state.Icon, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	state.Config = json.RawMessage(config)
	return state, true, nil
}

func configPaymentChannelProviderForMethod(ctx context.Context, tx *sql.Tx, channelID string) (string, error) {
	query := `SELECT provider FROM payment_channels WHERE id=?`
	if store.IsPostgres() {
		// A method-only archive must not validate against a provider that changes
		// concurrently before the method UPSERT commits.
		query += ` FOR SHARE`
	}
	var provider string
	if err := tx.QueryRowContext(ctx, query, channelID).Scan(&provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", invalidPaymentConfigArchivef("payment method references unknown channel %s", channelID)
		}
		return "", err
	}
	provider, err := normalizePaymentProvider(provider)
	if err != nil {
		return "", invalidPaymentConfigArchivef("payment channel %s provider: %v", channelID, err)
	}
	return provider, nil
}

func ensureConfigPaymentMethodNameAvailable(ctx context.Context, tx *sql.Tx, id, channelID, name string) error {
	var otherID string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM payment_methods
		  WHERE channel_id=? AND lower(trim(name))=lower(trim(?)) AND id<>? LIMIT 1`,
		channelID, name, id,
	).Scan(&otherID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: payment method name %q", store.ErrPaymentMethodNameExists, name)
}

func normalizeConfigPaymentMethodRows(ctx context.Context, tx *sql.Tx, r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	seenIDs := map[string]bool{}
	seenNames := map[string]string{}
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, invalidPaymentConfigArchivef("decode payment_methods row: %v", err)
		}

		id, idPresent, err := backupStringField(row, "id")
		if err != nil || !idPresent || id == "" {
			return nil, invalidPaymentConfigArchivef("payment_methods row has an invalid id")
		}
		if seenIDs[id] {
			return nil, invalidPaymentConfigArchivef("payment_methods contains duplicate id %s", id)
		}
		seenIDs[id] = true
		existing, exists, err := configPaymentMethodForUpdate(ctx, tx, id)
		if err != nil {
			return nil, err
		}

		channelID, channelPresent, err := backupStringField(row, "channel_id")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].channel_id: %v", id, err)
		}
		if !channelPresent && exists {
			channelID = strings.TrimSpace(existing.ChannelID)
		}
		if channelID == "" {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].channel_id is required", id)
		}
		provider, err := configPaymentChannelProviderForMethod(ctx, tx, channelID)
		if err != nil {
			return nil, err
		}

		name, namePresent, err := backupStringField(row, "name")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].name: %v", id, err)
		}
		if !namePresent && exists {
			name = strings.TrimSpace(existing.Name)
		}
		icon, iconPresent, err := backupStringField(row, "icon")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].icon: %v", id, err)
		}
		if !iconPresent && exists {
			icon = strings.TrimSpace(existing.Icon)
		}
		if err := validatePaymentMethodText(name, icon); err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s]: %v", id, err)
		}
		nameKey := channelID + "\x00" + strings.ToLower(name)
		if otherID, duplicate := seenNames[nameKey]; duplicate && otherID != id {
			return nil, fmt.Errorf("%w: payment method name %q", store.ErrPaymentMethodNameExists, name)
		}
		seenNames[nameKey] = id

		configText, configPresent, err := backupStringField(row, "config")
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].config: %v", id, err)
		}
		if !configPresent {
			if !exists {
				return nil, invalidPaymentConfigArchivef("payment_methods[%s].config is required", id)
			}
			configText = string(existing.Config)
		}
		config, err := normalizePaymentMethodConfig(provider, json.RawMessage(configText))
		if err != nil {
			return nil, invalidPaymentConfigArchivef("payment_methods[%s].config: %v", id, err)
		}
		if err := ensureConfigPaymentMethodNameAvailable(ctx, tx, id, channelID, name); err != nil {
			return nil, err
		}
		if err := validateConfigArchiveCommonPaymentFields("payment_methods", id, row); err != nil {
			return nil, err
		}

		setConfigArchiveString(row, "id", id)
		setConfigArchiveString(row, "channel_id", channelID)
		setConfigArchiveString(row, "name", name)
		setConfigArchiveString(row, "icon", icon)
		setConfigArchiveString(row, "type", provider)
		setConfigArchiveString(row, "config", string(config))
		if err := enc.Encode(row); err != nil {
			return nil, invalidPaymentConfigArchivef("encode payment_methods[%s]: %v", id, err)
		}
	}
}

func normalizeArchiveTableReader(table string, r io.Reader) (io.Reader, error) {
	switch table {
	case "settings":
		return normalizeSettingsArchiveRows(r)
	case "models":
		return normalizeModelOfficialToolsArchiveRows(r)
	case "model_group_quotas":
		return normalizeModelQuotaArchiveRows(r)
	case "credit_packages":
		return normalizeCreditPackageArchiveRows(r)
	case "conversations":
		return normalizeConversationRAGModeArchiveRows(r)
	case "user_groups":
		return normalizeLegacyUserGroupPriceArchiveRows(r)
	default:
		return r, nil
	}
}

func normalizeSettingsArchiveRows(r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, fmt.Errorf("decode settings row: %w", err)
		}
		key, present, err := backupStringField(row, "key")
		if err != nil {
			return nil, fmt.Errorf("invalid settings.key: %w", err)
		}
		// Old archives may still contain removed compaction controls. Accept the
		// archive for compatibility, but never restore those rows.
		if present && store.IsRetiredSettingKey(key) {
			continue
		}
		if present && key == "disabled_tools" {
			value, valuePresent, err := backupStringField(row, "value")
			if err != nil {
				return nil, fmt.Errorf("invalid settings.disabled_tools: %w", err)
			}
			if valuePresent {
				normalized, err := store.NormalizeBuiltinTools(json.RawMessage(value))
				if err != nil {
					return nil, fmt.Errorf("invalid settings.disabled_tools: %w", err)
				}
				if normalized == nil {
					normalized = json.RawMessage("[]")
				}
				row["value"], _ = json.Marshal(string(normalized))
			}
		}
		if present && key == "credits_per_usd" {
			value, valuePresent, err := backupStringField(row, "value")
			if err != nil || !valuePresent {
				return nil, fmt.Errorf("invalid settings.credits_per_usd")
			}
			var amount float64
			if json.Unmarshal([]byte(value), &amount) != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
				return nil, fmt.Errorf("invalid settings.credits_per_usd")
			}
		}
		if present {
			if err := normalizeCompactionArchiveSetting(row, key); err != nil {
				return nil, err
			}
		}
		if err := enc.Encode(row); err != nil {
			return nil, fmt.Errorf("encode settings row: %w", err)
		}
	}
}

// normalizeCompactionArchiveSetting applies the same strict types, ranges and
// canonical storage representation as the admin settings PATCH endpoint. A
// settings.value cell is a database TEXT value, so it contains a second JSON
// document that must be decoded and validated independently.
func normalizeCompactionArchiveSetting(row map[string]json.RawMessage, key string) error {
	switch key {
	case "context_compaction_prompt", "context_compaction_model_id", "compaction_enabled",
		"keep_recent_rounds", "summary_max_tokens",
		"compaction_retention_percentage", "compaction_token_target_percentage",
		"compaction_token_trigger", "compaction_token_cap", "compaction_request_max_tokens":
		// Handled below.
	default:
		return nil
	}

	raw, present, err := backupStringField(row, "value")
	if err != nil || !present {
		return fmt.Errorf("%w: %s must contain a JSON value", errInvalidCompactionConfigArchive, key)
	}

	switch key {
	case "context_compaction_prompt":
		prompt, err := decodeArchiveSettingString(raw)
		if err != nil {
			return fmt.Errorf("%w: context_compaction_prompt must be a JSON string", errInvalidCompactionConfigArchive)
		}
		prompt = strings.TrimSpace(prompt)
		if len(prompt) > contextCompactionPromptMaxBytes {
			return fmt.Errorf("%w: context_compaction_prompt exceeds %d bytes", errInvalidCompactionConfigArchive, contextCompactionPromptMaxBytes)
		}
		setArchiveSettingValue(row, prompt)
		return nil
	case "context_compaction_model_id":
		modelID, err := decodeArchiveSettingString(raw)
		if err != nil {
			return fmt.Errorf("%w: context_compaction_model_id must be a JSON string", errInvalidCompactionConfigArchive)
		}
		setArchiveSettingValue(row, strings.TrimSpace(modelID))
		return nil
	case "compaction_enabled":
		enabled, err := decodeArchiveSettingBool(raw)
		if err != nil {
			return fmt.Errorf("%w: compaction_enabled must be a JSON boolean", errInvalidCompactionConfigArchive)
		}
		setArchiveSettingValue(row, enabled)
		return nil
	}

	value, err := decodeArchiveSettingInt(raw)
	if err != nil {
		return fmt.Errorf("%w: %s must be a JSON integer", errInvalidCompactionConfigArchive, key)
	}
	min, max, hasMax := 0, 0, false
	switch key {
	case "keep_recent_rounds":
		min = 1
	case "summary_max_tokens":
		min = 256
	case "compaction_retention_percentage":
		min, max, hasMax = 10, 50, true
	case "compaction_token_target_percentage":
		min, max, hasMax = 25, 80, true
	case "compaction_request_max_tokens":
		min = 8192
	case "compaction_token_trigger", "compaction_token_cap":
		// Zero disables the corresponding limit, matching the PATCH endpoint.
	default:
		return fmt.Errorf("%w: unsupported context compaction setting %s", errInvalidCompactionConfigArchive, key)
	}
	if value < min || hasMax && value > max {
		if hasMax {
			return fmt.Errorf("%w: %s must be between %d and %d", errInvalidCompactionConfigArchive, key, min, max)
		}
		return fmt.Errorf("%w: %s must be at least %d", errInvalidCompactionConfigArchive, key, min)
	}
	setArchiveSettingValue(row, value)
	return nil
}

func setArchiveSettingValue(row map[string]json.RawMessage, value any) {
	normalized, _ := json.Marshal(value) // bool, int and string cannot fail to marshal.
	row["value"], _ = json.Marshal(string(normalized))
}

func decodeArchiveSettingString(raw string) (string, error) {
	var value *string
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return "", errInvalidInput
	}
	return *value, nil
}

func decodeArchiveSettingBool(raw string) (bool, error) {
	var value *bool
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return false, errInvalidInput
	}
	return *value, nil
}

func decodeArchiveSettingInt(raw string) (int, error) {
	var value *int
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return 0, errInvalidInput
	}
	return *value, nil
}

// validateImportedContextCompactionModel runs after all archive tables have
// been restored/upserted. Settings precede channels and models in both archive
// orders, so validating earlier would reject a valid setting that references a
// model created later by the same transaction.
func validateImportedContextCompactionModel(ctx context.Context, ex store.RowExecer) error {
	var raw string
	err := ex.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='context_compaction_model_id'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	modelID, err := decodeArchiveSettingString(raw)
	if err != nil {
		return fmt.Errorf("%w: context_compaction_model_id must be a JSON string", errInvalidCompactionConfigArchive)
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	var kind, channelType string
	var modelEnabled, channelEnabled int
	err = ex.QueryRowContext(ctx, `
		SELECT m.kind, m.enabled, c.type, c.enabled
		  FROM models m
		  JOIN channels c ON c.id=m.channel_id
		 WHERE m.id=?`, modelID).Scan(&kind, &modelEnabled, &channelType, &channelEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: context compaction model %q does not exist with a channel", errInvalidCompactionConfigArchive, modelID)
	}
	if err != nil {
		return err
	}
	if modelEnabled != 1 || kind != "chat" {
		return fmt.Errorf("%w: context compaction model %q must be an enabled chat model", errInvalidCompactionConfigArchive, modelID)
	}
	if channelEnabled != 1 || !isSupportedContextCompactionChannelType(channelType) {
		return fmt.Errorf("%w: context compaction model %q must use an enabled supported channel", errInvalidCompactionConfigArchive, modelID)
	}
	return nil
}

// normalizeConversationRAGModeArchiveRows prevents a legacy full backup from
// restoring the removed model-driven document-search mode. The rag_mode column
// remains because auto and inject are still supported.
func normalizeConversationRAGModeArchiveRows(r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, fmt.Errorf("decode conversations row: %w", err)
		}
		mode, present, err := backupStringField(row, "rag_mode")
		if err != nil {
			return nil, fmt.Errorf("invalid conversations.rag_mode: %w", err)
		}
		if present {
			row["rag_mode"], _ = json.Marshal(store.NormalizeConversationRAGMode(mode))
		}
		if err := enc.Encode(row); err != nil {
			return nil, fmt.Errorf("encode conversations row: %w", err)
		}
	}
}

// normalizeLegacyUserGroupPriceArchiveRows upgrades both previous price shapes:
// the single settlement price becomes the monthly price, while the older
// USD/CNY pair follows the product default (USD, with CNY as a zero-USD
// fallback). Historical archives never had a yearly offer, so it starts at 0.
func normalizeLegacyUserGroupPriceArchiveRows(r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, fmt.Errorf("decode user_groups row: %w", err)
		}
		if raw, current := row["monthly_price_amount_minor"]; current {
			var minor int64
			if json.Unmarshal(raw, &minor) != nil || minor < 0 {
				return nil, fmt.Errorf("invalid user_groups.monthly_price_amount_minor")
			}
		} else {
			minor := int64(0)
			if raw, ok := row["price_amount_minor"]; ok && string(raw) != "null" {
				if json.Unmarshal(raw, &minor) != nil || minor < 0 {
					return nil, fmt.Errorf("invalid user_groups.price_amount_minor")
				}
			} else {
				price := 0.0
				if raw, ok := row["price_usd"]; ok && string(raw) != "null" {
					if err := json.Unmarshal(raw, &price); err != nil {
						return nil, fmt.Errorf("invalid user_groups.price_usd: %w", err)
					}
				}
				if price == 0 {
					if raw, ok := row["price_cny"]; ok && string(raw) != "null" {
						if err := json.Unmarshal(raw, &price); err != nil {
							return nil, fmt.Errorf("invalid user_groups.price_cny: %w", err)
						}
					}
				}
				if price < 0 {
					return nil, fmt.Errorf("invalid negative user_groups price")
				}
				minor = int64(math.Round(price * 100))
			}
			row["monthly_price_amount_minor"], _ = json.Marshal(minor)
		}
		if raw, current := row["yearly_price_amount_minor"]; current {
			var minor int64
			if json.Unmarshal(raw, &minor) != nil || minor < 0 {
				return nil, fmt.Errorf("invalid user_groups.yearly_price_amount_minor")
			}
		} else {
			row["yearly_price_amount_minor"] = json.RawMessage("0")
		}
		allowance, allowancePresent, err := backupFloat64Field(row, "credit_allowance")
		if err != nil || allowancePresent && (math.IsNaN(allowance) || math.IsInf(allowance, 0) || allowance < 0) {
			return nil, fmt.Errorf("invalid user_groups.credit_allowance")
		}
		period, periodPresent, err := backupInt64Field(row, "credit_period_seconds")
		if err != nil || periodPresent && period < 0 || allowancePresent && periodPresent && allowance > 0 && period == 0 {
			return nil, fmt.Errorf("invalid user_groups.credit_period_seconds")
		}
		if allowancePresent {
			micros, err := store.CreditsToMicros(allowance)
			if err != nil {
				return nil, fmt.Errorf("invalid user_groups.credit_allowance")
			}
			row["credit_allowance_micros"], _ = json.Marshal(micros)
		}
		if err := enc.Encode(row); err != nil {
			return nil, fmt.Errorf("encode user_groups row: %w", err)
		}
	}
}

// normalizeModelOfficialToolsArchiveRows upgrades legacy hosted-tool arrays and
// validates/canonicalizes nullable local and MCP default policies before either
// importer writes model rows. The historical function name is retained to keep
// this compatibility path stable.
func normalizeModelOfficialToolsArchiveRows(r io.Reader) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, fmt.Errorf("decode models row: %w", err)
		}

		raw, present, err := backupStringField(row, "official_tools")
		if err != nil {
			return nil, fmt.Errorf("invalid models.official_tools: %w", err)
		}
		if present {
			normalized, err := store.NormalizeOfficialTools(json.RawMessage(raw))
			if err != nil {
				return nil, fmt.Errorf("invalid models.official_tools: %w", err)
			}
			cell, err := json.Marshal(string(normalized))
			if err != nil {
				return nil, fmt.Errorf("encode models.official_tools: %w", err)
			}
			row["official_tools"] = cell
		}
		builtinRaw, builtinPresent, builtinNull, err := backupNullableStringField(row, "builtin_tools")
		if err != nil {
			return nil, fmt.Errorf("invalid models.builtin_tools: %w", err)
		}
		if builtinPresent && !builtinNull {
			normalized, err := store.NormalizeBuiltinTools(json.RawMessage(builtinRaw))
			if err != nil {
				return nil, fmt.Errorf("invalid models.builtin_tools: %w", err)
			}
			if normalized == nil {
				row["builtin_tools"] = json.RawMessage("null")
			} else {
				cell, err := json.Marshal(string(normalized))
				if err != nil {
					return nil, fmt.Errorf("encode models.builtin_tools: %w", err)
				}
				row["builtin_tools"] = cell
			}
		}
		mcpRaw, mcpPresent, mcpNull, err := backupNullableStringField(row, "mcp_server_ids")
		if err != nil {
			return nil, fmt.Errorf("invalid models.mcp_server_ids: %w", err)
		}
		if mcpPresent && !mcpNull {
			normalized, err := store.NormalizeMCPServerIDs(json.RawMessage(mcpRaw))
			if err != nil {
				return nil, fmt.Errorf("invalid models.mcp_server_ids: %w", err)
			}
			if normalized == nil {
				row["mcp_server_ids"] = json.RawMessage("null")
			} else {
				cell, err := json.Marshal(string(normalized))
				if err != nil {
					return nil, fmt.Errorf("encode models.mcp_server_ids: %w", err)
				}
				row["mcp_server_ids"] = cell
			}
		}
		billing := store.Model{Currency: "USD"}
		billingPresent := false
		for key, target := range map[string]*float64{
			"price_input": &billing.PriceInput, "price_output": &billing.PriceOutput,
			"price_cache_read": &billing.PriceCacheRead, "price_cache_write": &billing.PriceCacheWrite,
			"price_per_image": &billing.PricePerImage,
		} {
			value, present, err := backupFloat64Field(row, key)
			if err != nil {
				return nil, fmt.Errorf("invalid models.%s", key)
			}
			if present {
				*target = value
				billingPresent = true
			}
		}
		if currency, present, err := backupStringField(row, "currency"); err != nil {
			return nil, fmt.Errorf("invalid models.currency")
		} else if present {
			billing.Currency = currency
			billingPresent = true
		}
		if billingPresent {
			if err := store.ValidateModelBilling(&billing); err != nil {
				return nil, fmt.Errorf("invalid models billing: %w", err)
			}
			row["currency"], _ = json.Marshal(billing.Currency)
		}
		if err := enc.Encode(row); err != nil {
			return nil, fmt.Errorf("encode models row: %w", err)
		}
	}
}

func normalizeModelQuotaArchiveRows(r io.Reader) (io.Reader, error) {
	return normalizeValidatedArchiveRows("model_group_quotas", r, func(row map[string]json.RawMessage) error {
		period, present, err := backupInt64Field(row, "period_seconds")
		if err != nil || present && period <= 0 {
			return fmt.Errorf("invalid model_group_quotas.period_seconds")
		}
		limit, present, err := backupFloat64Field(row, "limit_value")
		if err != nil || present && (math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0) {
			return fmt.Errorf("invalid model_group_quotas.limit_value")
		}
		limitType, present, err := backupStringField(row, "limit_type")
		if err != nil || present && limitType != "cost" && limitType != "count" {
			return fmt.Errorf("invalid model_group_quotas.limit_type")
		}
		return nil
	})
}

func normalizeCreditPackageArchiveRows(r io.Reader) (io.Reader, error) {
	return normalizeValidatedArchiveRows("credit_packages", r, func(row map[string]json.RawMessage) error {
		credits, creditsPresent, err := backupFloat64Field(row, "credits")
		if err != nil || creditsPresent && (math.IsNaN(credits) || math.IsInf(credits, 0) || credits < 0) {
			return fmt.Errorf("invalid credit_packages.credits")
		}
		price, pricePresent, err := backupInt64Field(row, "price_amount_minor")
		if err != nil || pricePresent && price < 0 {
			return fmt.Errorf("invalid credit_packages.price_amount_minor")
		}
		_, _, err = backupBoolField(row, "enabled")
		if err != nil {
			return fmt.Errorf("invalid credit_packages.enabled")
		}
		return nil
	})
}

func normalizeValidatedArchiveRows(table string, r io.Reader, validate func(map[string]json.RawMessage) error) (io.Reader, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(&out)
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return bytes.NewReader(out.Bytes()), nil
		} else if err != nil {
			return nil, fmt.Errorf("decode %s row: %w", table, err)
		}
		if err := validate(row); err != nil {
			return nil, err
		}
		if err := enc.Encode(row); err != nil {
			return nil, fmt.Errorf("encode %s row: %w", table, err)
		}
	}
}

func backupFloat64Field(row map[string]json.RawMessage, key string) (float64, bool, error) {
	raw, present := row[key]
	if !present || strings.TrimSpace(string(raw)) == "null" {
		return 0, false, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func backupInt64Field(row map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, present := row[key]
	if !present || strings.TrimSpace(string(raw)) == "null" {
		return 0, false, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func validateBillingConfiguration(ctx context.Context, ex store.RowExecer) error {
	var creditsRaw string
	if err := ex.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='credits_per_usd'`).Scan(&creditsRaw); err == nil {
		var amount float64
		if json.Unmarshal([]byte(creditsRaw), &amount) != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return fmt.Errorf("invalid settings.credits_per_usd")
		}
		if micros, err := store.CreditsToMicros(amount); err != nil || amount > 0 && micros == 0 {
			return fmt.Errorf("invalid settings.credits_per_usd")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	rows, err := ex.QueryContext(ctx, `SELECT price_input,price_output,price_cache_read,price_cache_write,price_per_image,currency FROM models`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var model store.Model
		if err := rows.Scan(&model.PriceInput, &model.PriceOutput, &model.PriceCacheRead, &model.PriceCacheWrite, &model.PricePerImage, &model.Currency); err != nil {
			_ = rows.Close()
			return err
		}
		if err := store.ValidateModelBilling(&model); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	rows, err = ex.QueryContext(ctx, `SELECT monthly_price_amount_minor,yearly_price_amount_minor,max_projects,max_kbs,credit_allowance,credit_period_seconds,max_workspaces,max_storage_mb FROM user_groups`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var group store.UserGroup
		if err := rows.Scan(&group.MonthlyPriceAmountMinor, &group.YearlyPriceAmountMinor, &group.MaxProjects, &group.MaxKBs, &group.CreditAllowance, &group.CreditPeriodSeconds, &group.MaxWorkspaces, &group.MaxStorageMB); err != nil {
			_ = rows.Close()
			return err
		}
		if err := store.ValidateUserGroupBilling(group); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	rows, err = ex.QueryContext(ctx, `SELECT credits,price_amount_minor,enabled FROM credit_packages`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pkg store.CreditPackage
		var enabled int
		if err := rows.Scan(&pkg.Credits, &pkg.PriceAmountMinor, &enabled); err != nil {
			_ = rows.Close()
			return err
		}
		pkg.Enabled = enabled != 0
		if err := store.ValidateCreditPackage(pkg); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}

	rows, err = ex.QueryContext(ctx, `SELECT period_seconds,limit_type,limit_value FROM model_group_quotas`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var period int
		var limitType string
		var limit float64
		if err := rows.Scan(&period, &limitType, &limit); err != nil {
			return err
		}
		if err := store.ValidateModelGroupQuota(store.ModelGroupQuota{
			PeriodSeconds: period, LimitType: limitType, LimitValue: limit,
		}); err != nil {
			return fmt.Errorf("invalid model quota billing configuration")
		}
	}
	return rows.Err()
}

func rewriteConfigSkillAssetPaths(ctx context.Context, ex store.RowExecer, man configManifest, d Deps) error {
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
		if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" || strings.TrimSpace(raw) == "[]" {
			continue
		}
		var assets []skillAssetRow
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rewrite skill assets %s: %w", id, err)
		}
		changed := false
		for i := range assets {
			if strings.TrimSpace(assets[i].StoragePath) == "" {
				continue
			}
			next, err := remapConfigSkillAssetPath(assets[i].StoragePath, man.SourceUploadDir, d.Config.UploadDir)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("%w: config skills.%s.assets[%d]: %v", errInvalidBackupStoragePath, id, i, err)
			}
			if next != assets[i].StoragePath {
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

func remapConfigSkillAssetPath(path, sourceUploadDir, targetUploadDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.TrimSpace(sourceUploadDir) == "" {
		return "", fileguard.ErrOutsideRoot
	}
	srcBase := filepath.Join(filepath.Clean(sourceUploadDir), skillAssetsSubdir)
	dstBase := filepath.Join(filepath.Clean(targetUploadDir), skillAssetsSubdir)
	return fileguard.Remap(srcBase, dstBase, path)
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
		if restoreZipEntry(base, filepath.FromSlash(rel), f) {
			n++
		}
	}
	return n
}

func restoreZipEntry(base, rel string, entry *zip.File) bool {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(rel) == "" || filepath.IsAbs(rel) {
		return false
	}
	dest, err := fileguard.PrepareWrite(base, filepath.Join(base, rel))
	if err != nil {
		return false
	}
	rc, err := entry.Open()
	if err != nil {
		return false
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".aivory-restore-*")
	if err != nil {
		return false
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, rc); err != nil {
		return false
	}
	if err := tmp.Close(); err != nil {
		return false
	}
	// Re-check the destination immediately before rename. Rename replaces a
	// regular destination atomically and never follows a final-component
	// symlink; PrepareWrite rejects a symlink target or parent chain.
	safeDest, err := fileguard.PrepareWrite(base, dest)
	if err != nil || safeDest != dest {
		return false
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return false
	}
	committed = true
	return true
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
		// Archive-preflight variant: the target model may be delivered in this
		// same archive, so only the lock semantics are enforced, not the target.
		return ensureEmbeddingModelSettingCanChangeFromArchive(d, json.RawMessage(row.Value))
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
		if err := validateConfigArchiveModelExtraParams(row); err != nil {
			return err
		}
		if err := validateConfigArchiveModelOfficialTools(row); err != nil {
			return err
		}
		if err := validateConfigArchiveModelBuiltinTools(row); err != nil {
			return err
		}
		if err := validateConfigArchiveModelMCPServerIDs(row); err != nil {
			return err
		}
		if err := ensureLockedEmbeddingModelArchiveRowCanChange(d, row); err != nil {
			return err
		}
	}
}

// validateConfigArchiveModelOfficialTools prevents config import from bypassing
// the same hosted-tool shape validation used by model POST/PATCH. Exported TEXT
// values arrive as JSON strings; both legacy name arrays and current definition
// arrays are accepted by the store normalizer.
func validateConfigArchiveModelOfficialTools(row map[string]json.RawMessage) error {
	raw, present, err := backupStringField(row, "official_tools")
	if err != nil {
		return fmt.Errorf("invalid models.official_tools: %w", err)
	}
	if !present {
		return nil
	}
	_, err = store.NormalizeOfficialTools(json.RawMessage(raw))
	return err
}

func validateConfigArchiveModelBuiltinTools(row map[string]json.RawMessage) error {
	raw, present, isNull, err := backupNullableStringField(row, "builtin_tools")
	if err != nil {
		return fmt.Errorf("invalid models.builtin_tools: %w", err)
	}
	if !present || isNull {
		return nil
	}
	_, err = store.NormalizeBuiltinTools(json.RawMessage(raw))
	return err
}

func validateConfigArchiveModelMCPServerIDs(row map[string]json.RawMessage) error {
	raw, present, isNull, err := backupNullableStringField(row, "mcp_server_ids")
	if err != nil {
		return fmt.Errorf("invalid models.mcp_server_ids: %w", err)
	}
	if !present || isNull {
		return nil
	}
	_, err = store.NormalizeMCPServerIDs(json.RawMessage(raw))
	return err
}

// backupNullableStringField reads an exported nullable TEXT cell. It differs
// from backupStringField because JSON null is a meaningful model tool policy
// value rather than malformed input.
func backupNullableStringField(row map[string]json.RawMessage, key string) (value string, present, isNull bool, err error) {
	raw, ok := row[key]
	if !ok {
		return "", false, false, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return "", true, true, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, false, err
	}
	return strings.TrimSpace(value), true, false, nil
}

// validateConfigArchiveModelExtraParams keeps the config-import path aligned
// with the model admin API: extra_params is a JSON object and is only active
// for chat models. Exported database TEXT values arrive as JSON strings here.
func validateConfigArchiveModelExtraParams(row map[string]json.RawMessage) error {
	raw, present, err := backupStringField(row, "extra_params")
	if err != nil {
		return fmt.Errorf("invalid models.extra_params: %w", err)
	}
	if !present {
		return nil
	}
	normalized, err := store.NormalizeModelExtraParams(json.RawMessage(raw))
	if err != nil {
		return err
	}
	kind := "chat"
	if value, present, err := backupStringField(row, "kind"); err != nil {
		return fmt.Errorf("invalid models.kind: %w", err)
	} else if present && value != "" {
		kind = value
	}
	if kind != "chat" && string(normalized) != "{}" {
		return errModelExtraParamsChatOnly
	}
	return nil
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
