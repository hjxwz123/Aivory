package api

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"path/filepath"
	"strings"

	"aivory/server/internal/fileguard"
	"aivory/server/internal/store"
)

type backupStorageReferenceTable struct {
	table  string
	root   string
	prefix string
}

func beginBackupSnapshot(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if store.IsPostgres() {
		opts.Isolation = sql.LevelRepeatableRead
	}
	return db.BeginTx(ctx, opts)
}

func validateExportedStorageReferences(ctx context.Context, q store.RowQuerier, d Deps, entries map[string]backupEntryIntegrity) error {
	for _, item := range []backupStorageReferenceTable{
		{table: "files", root: d.Config.UploadDir, prefix: backupZipUploads},
		{table: "documents", root: d.Config.UploadDir, prefix: backupZipUploads},
		{table: "artifacts", root: d.Config.ArtifactDir, prefix: backupZipArtifacts},
	} {
		rows, err := q.QueryContext(ctx, "SELECT id, storage_path FROM "+item.table+" WHERE trim(storage_path)<>''") //nolint:gosec // fixed table names
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, path string
			if err := rows.Scan(&id, &path); err != nil {
				_ = rows.Close()
				return err
			}
			rel, err := fileguard.Relative(item.root, path)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("%s.%s path is outside its storage root: %w", item.table, id, err)
			}
			name := item.prefix + filepath.ToSlash(rel)
			if _, ok := entries[name]; !ok {
				_ = rows.Close()
				return fmt.Errorf("%s.%s references a file missing from the archive: %s", item.table, id, name)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}

	rows, err := q.QueryContext(ctx, `SELECT id, assets FROM skills WHERE trim(assets) NOT IN ('', 'null', '[]')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	assetRoot := filepath.Join(d.Config.UploadDir, skillAssetsSubdir)
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var assets []skillAssetRow
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			return fmt.Errorf("skills.%s assets: %w", id, err)
		}
		for i, asset := range assets {
			if strings.TrimSpace(asset.StoragePath) == "" {
				continue
			}
			rel, err := fileguard.Relative(assetRoot, asset.StoragePath)
			if err != nil {
				return fmt.Errorf("skills.%s assets[%d] path is outside its storage root: %w", id, i, err)
			}
			name := backupZipUploads + filepath.ToSlash(filepath.Join(skillAssetsSubdir, rel))
			if _, ok := entries[name]; !ok {
				return fmt.Errorf("skills.%s assets[%d] references a file missing from the archive: %s", id, i, name)
			}
		}
	}
	return rows.Err()
}

// backupEntryIntegrity authenticates the complete payload set of a v3 archive.
// The manifest itself is deliberately excluded because it contains this map.
type backupEntryIntegrity struct {
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type zipEntryCreator interface {
	Create(name string) (io.Writer, error)
}

type trackedZipWriter struct {
	zip         *zip.Writer
	entries     map[string]backupEntryIntegrity
	currentName string
	current     *trackedEntryWriter
}

type trackedEntryWriter struct {
	dst  io.Writer
	hash hash.Hash
	size int64
}

func newTrackedZipWriter(zw *zip.Writer) *trackedZipWriter {
	return &trackedZipWriter{zip: zw, entries: map[string]backupEntryIntegrity{}}
}

func (w *trackedZipWriter) Create(name string) (io.Writer, error) {
	w.finishCurrent()
	if _, exists := w.entries[name]; exists {
		return nil, fmt.Errorf("duplicate backup entry %q", name)
	}
	dst, err := w.zip.Create(name)
	if err != nil {
		return nil, err
	}
	w.currentName = name
	w.current = &trackedEntryWriter{dst: dst, hash: sha256.New()}
	return w.current, nil
}

func (w *trackedZipWriter) Finish() map[string]backupEntryIntegrity {
	w.finishCurrent()
	out := make(map[string]backupEntryIntegrity, len(w.entries))
	for name, integrity := range w.entries {
		out[name] = integrity
	}
	return out
}

func (w *trackedZipWriter) finishCurrent() {
	if w.current == nil {
		return
	}
	w.entries[w.currentName] = backupEntryIntegrity{
		SizeBytes: w.current.size,
		SHA256:    hex.EncodeToString(w.current.hash.Sum(nil)),
	}
	w.currentName = ""
	w.current = nil
}

func (w *trackedEntryWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		_, _ = w.hash.Write(p[:n])
		w.size += int64(n)
	}
	return n, err
}

func validateBackupArchive(zr *zip.Reader, man backupManifest) error {
	// v1/v2 predate per-entry digests. They remain importable for disaster
	// recovery compatibility, but referenced file completeness is still required.
	if man.Version < 3 {
		return validateArchivedStorageReferences(zr, man)
	}

	tables := store.BackupTableOrder()
	if len(man.Tables) != len(tables) {
		return fmt.Errorf("manifest tables=%d, want %d", len(man.Tables), len(tables))
	}
	for i, table := range tables {
		if man.Tables[i] != table {
			return fmt.Errorf("manifest table %d=%q, want %q", i, man.Tables[i], table)
		}
		if _, ok := man.Counts[table]; !ok || man.Counts[table] < 0 {
			return fmt.Errorf("manifest has no valid row count for %s", table)
		}
	}
	if len(man.Entries) == 0 {
		return fmt.Errorf("manifest has no entry integrity metadata")
	}

	actual := make(map[string]*zip.File, len(zr.File))
	for _, entry := range zr.File {
		if _, duplicate := actual[entry.Name]; duplicate {
			return fmt.Errorf("duplicate ZIP entry %q", entry.Name)
		}
		actual[entry.Name] = entry
	}
	if actual["manifest.json"] == nil {
		return fmt.Errorf("missing manifest.json")
	}
	if len(actual)-1 != len(man.Entries) {
		return fmt.Errorf("archive payload entries=%d, manifest entries=%d", len(actual)-1, len(man.Entries))
	}
	for name := range actual {
		if name == "manifest.json" {
			continue
		}
		if _, ok := man.Entries[name]; !ok {
			return fmt.Errorf("ZIP entry %q is not declared by manifest", name)
		}
	}

	for _, table := range tables {
		name := "db/" + table + ".jsonl"
		entry := actual[name]
		if entry == nil {
			return fmt.Errorf("missing required table entry %q", name)
		}
		count, err := validateJSONLEntry(entry, man.Entries[name])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if count != man.Counts[table] {
			return fmt.Errorf("%s rows=%d, manifest=%d", table, count, man.Counts[table])
		}
	}

	for name, integrity := range man.Entries {
		entry := actual[name]
		if entry == nil {
			return fmt.Errorf("manifest entry %q is missing", name)
		}
		if strings.HasPrefix(name, "db/") && strings.HasSuffix(name, ".jsonl") {
			continue // checked above, including JSON shape and row count
		}
		if err := validateRawEntry(entry, integrity); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	if man.IncludesFiles {
		// An empty upload/artifact tree is valid. Exact entry membership and digests
		// still prove that no declared file disappeared or changed in transit.
	} else {
		for name := range man.Entries {
			if strings.HasPrefix(name, backupZipUploads) || strings.HasPrefix(name, backupZipArtifacts) {
				return fmt.Errorf("manifest excludes files but declares %q", name)
			}
		}
	}
	if err := validateQdrantArchiveIntegrity(zr, man); err != nil {
		return err
	}
	return validateArchivedStorageReferences(zr, man)
}

func validateArchivedStorageReferences(zr *zip.Reader, man backupManifest) error {
	if !man.IncludesFiles {
		return nil
	}
	entryNames := make(map[string]bool, len(zr.File))
	for _, entry := range zr.File {
		entryNames[entry.Name] = true
	}
	for _, item := range []backupStorageReferenceTable{
		{table: "files", root: man.SourceUploadDir, prefix: backupZipUploads},
		{table: "documents", root: man.SourceUploadDir, prefix: backupZipUploads},
		{table: "artifacts", root: man.SourceArtifactDir, prefix: backupZipArtifacts},
	} {
		entry := findZipFile(zr, "db/"+item.table+".jsonl")
		if entry == nil {
			continue // v3 required-table validation reports this; old archives may omit empty tables.
		}
		if err := validateArchivedStorageTable(entry, item, entryNames); err != nil {
			return err
		}
	}
	return validateArchivedSkillAssets(zr, man.SourceUploadDir, entryNames)
}

func validateArchivedStorageTable(entry *zip.File, item backupStorageReferenceTable, entryNames map[string]bool) error {
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for rowNumber := 1; ; rowNumber++ {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("decode %s storage references: %w", item.table, err)
		}
		id, err := archivedStringField(row, "id")
		if err != nil {
			return fmt.Errorf("%s row %d id: %w", item.table, rowNumber, err)
		}
		path, err := archivedStringField(row, "storage_path")
		if err != nil {
			return fmt.Errorf("%s.%s storage_path: %w", item.table, id, err)
		}
		if strings.TrimSpace(path) == "" {
			continue
		}
		rel, err := fileguard.Relative(item.root, path)
		if err != nil {
			return fmt.Errorf("%s.%s storage path is outside the manifest root: %w", item.table, id, err)
		}
		name := item.prefix + filepath.ToSlash(rel)
		if !entryNames[name] {
			return fmt.Errorf("%s.%s references missing archive file %s", item.table, id, name)
		}
	}
}

func validateArchivedSkillAssets(zr *zip.Reader, sourceUploadDir string, entryNames map[string]bool) error {
	entry := findZipFile(zr, "db/skills.jsonl")
	if entry == nil {
		return nil
	}
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	assetRoot := filepath.Join(sourceUploadDir, skillAssetsSubdir)
	for rowNumber := 1; ; rowNumber++ {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("decode skill asset references: %w", err)
		}
		id, err := archivedStringField(row, "id")
		if err != nil {
			return fmt.Errorf("skills row %d id: %w", rowNumber, err)
		}
		raw, err := archivedStringField(row, "assets")
		if err != nil {
			return fmt.Errorf("skills.%s assets: %w", id, err)
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "null" || trimmed == "[]" {
			continue
		}
		var assets []skillAssetRow
		if err := json.Unmarshal([]byte(raw), &assets); err != nil {
			return fmt.Errorf("skills.%s assets: %w", id, err)
		}
		for i, asset := range assets {
			if strings.TrimSpace(asset.StoragePath) == "" {
				continue
			}
			rel, err := fileguard.Relative(assetRoot, asset.StoragePath)
			if err != nil {
				return fmt.Errorf("skills.%s assets[%d] path is outside the manifest root: %w", id, i, err)
			}
			name := backupZipUploads + filepath.ToSlash(filepath.Join(skillAssetsSubdir, rel))
			if !entryNames[name] {
				return fmt.Errorf("skills.%s assets[%d] references missing archive file %s", id, i, name)
			}
		}
	}
}

func archivedStringField(row map[string]json.RawMessage, name string) (string, error) {
	raw := row[name]
	if len(raw) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func validateRawEntry(entry *zip.File, want backupEntryIntegrity) error {
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return err
	}
	return compareEntryIntegrity(n, h.Sum(nil), want)
}

func validateJSONLEntry(entry *zip.File, want backupEntryIntegrity) (int64, error) {
	rc, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	h := sha256.New()
	counter := &byteCounter{}
	dec := json.NewDecoder(io.TeeReader(rc, io.MultiWriter(h, counter)))
	dec.UseNumber()
	var rows int64
	for {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err == io.EOF {
			break
		} else if err != nil {
			return rows, err
		}
		if row == nil {
			return rows, fmt.Errorf("row %d is not a JSON object", rows+1)
		}
		rows++
	}
	if err := compareEntryIntegrity(counter.n, h.Sum(nil), want); err != nil {
		return rows, err
	}
	return rows, nil
}

type byteCounter struct{ n int64 }

func (w *byteCounter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func compareEntryIntegrity(size int64, digest []byte, want backupEntryIntegrity) error {
	if want.SizeBytes < 0 || size != want.SizeBytes {
		return fmt.Errorf("size=%d, manifest=%d", size, want.SizeBytes)
	}
	got := hex.EncodeToString(digest)
	if len(want.SHA256) != sha256.Size*2 || !strings.EqualFold(got, want.SHA256) {
		return fmt.Errorf("sha256=%s, manifest=%s", got, want.SHA256)
	}
	return nil
}

func validateQdrantArchiveIntegrity(zr *zip.Reader, man backupManifest) error {
	entry := findZipFile(zr, qdrantZipManifest)
	if !man.IncludesQdrant {
		if entry != nil {
			return fmt.Errorf("manifest excludes Qdrant but archive contains its manifest")
		}
		if man.QdrantPoints != 0 {
			return fmt.Errorf("manifest excludes Qdrant but declares %d points", man.QdrantPoints)
		}
		return nil
	}
	if entry == nil {
		return fmt.Errorf("missing %s", qdrantZipManifest)
	}
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	var qman qdrantArchiveManifest
	err = json.NewDecoder(rc).Decode(&qman)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("decode Qdrant manifest: %w", err)
	}
	if qman.Format != "aivory-qdrant" || qman.Version < 1 || qman.Version > qdrantArchiveVersion {
		return fmt.Errorf("unsupported Qdrant manifest format/version")
	}
	var total int64
	seenCollections := map[string]bool{}
	seenEntries := map[string]bool{}
	for _, collection := range qman.Collections {
		if !validQdrantCollectionName(collection.Name) || collection.Dim <= 0 || collection.Points < 0 {
			return fmt.Errorf("invalid Qdrant collection metadata for %q", collection.Name)
		}
		if seenCollections[collection.Name] || seenEntries[collection.Entry] {
			return fmt.Errorf("duplicate Qdrant collection or entry %q", collection.Name)
		}
		seenCollections[collection.Name] = true
		seenEntries[collection.Entry] = true
		pointEntry := findZipFile(zr, collection.Entry)
		if pointEntry == nil {
			return fmt.Errorf("missing Qdrant collection entry %q", collection.Entry)
		}
		count, err := validateJSONLEntry(pointEntry, man.Entries[collection.Entry])
		if err != nil {
			return fmt.Errorf("%s: %w", collection.Entry, err)
		}
		if count != collection.Points {
			return fmt.Errorf("Qdrant collection %s points=%d, manifest=%d", collection.Name, count, collection.Points)
		}
		total += count
	}
	if total != man.QdrantPoints {
		return fmt.Errorf("Qdrant points=%d, manifest=%d", total, man.QdrantPoints)
	}
	return nil
}
