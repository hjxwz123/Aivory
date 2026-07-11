package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"aivory/server/internal/envcfg"
)

// Skill assets (§4.17) are templates / scripts / data files an admin bundles
// with a skill; use_skill stages them into the sandbox at
// /workspace/skills/<name>/ for python_execute. Unlike icons they're never
// served back inline to a browser — they're sandbox inputs — so the checks here
// are a sane size + extension allowlist and keeping stored paths contained.

// maxSkillAssetBytes caps one asset. Templates / scripts / small data, not media
// libraries.
var maxSkillAssetBytes = envcfg.Int64("AIVORY_API_MAX_SKILL_ASSET_BYTES", 20*1024*1024) // 20 MiB

// skillAssetsSubdir is the flat directory under UploadDir where skill assets
// live. createSkillAdmin / updateSkillAdmin only accept assets whose
// storage_path resolves inside it (defence-in-depth: the assets array
// round-trips through the admin client on save, so a crafted storage_path must
// not be able to point the sandbox stager — os.ReadFile — at an arbitrary
// server file).
const skillAssetsSubdir = "skill-assets"

// allowedSkillAssetExt — extension allowlist. Office templates, common
// data/text formats, plain scripts, and small images for slides. No archives /
// executables / shell.
var allowedSkillAssetExt = map[string]bool{
	"pptx": true, "docx": true, "xlsx": true, "pdf": true,
	"csv": true, "tsv": true, "json": true, "txt": true, "md": true,
	"py": true, "html": true, "css": true,
	"png": true, "jpg": true, "jpeg": true, "svg": true, "gif": true,
}

var errAssetBadExt = errors.New("skill asset: unsupported file type")
var errAssetTooLarge = errors.New("skill asset: too large (max 20 MiB)")
var errAssetEmpty = errors.New("skill asset: empty file")

// skillAssetRow is the persisted shape of one entry in skills.assets (mirrors
// store.ListSkillAssets' reader).
type skillAssetRow struct {
	Filename    string `json:"filename"`
	StoragePath string `json:"storage_path"`
	MimeType    string `json:"mime_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
}

// uploadSkillAssetAdmin — POST /api/admin/skills/assets, multipart "file".
// Stores the file under <UploadDir>/skill-assets/<hex>.<ext> and returns an
// asset descriptor the editor appends to the skill's `assets` array:
// {filename, storage_path, mime_type, size_bytes}.
func uploadSkillAssetAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxSkillAssetBytes + 4096); err != nil {
		writeError(w, 400, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	defer file.Close()

	orig := filepath.Base(header.Filename)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(orig), "."))
	if !allowedSkillAssetExt[ext] {
		writeError(w, 400, errAssetBadExt)
		return
	}

	// Read with a hard byte cap (+1 so we can tell oversize from exactly-at-cap).
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, io.LimitReader(file, maxSkillAssetBytes+1))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n > maxSkillAssetBytes {
		writeError(w, 400, errAssetTooLarge)
		return
	}
	if n == 0 {
		writeError(w, 400, errAssetEmpty)
		return
	}
	data := buf.Bytes()

	id, err := randomHex(16)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	dir := filepath.Join(d.Config.UploadDir, skillAssetsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, 500, err)
		return
	}
	path := filepath.Join(dir, id+"."+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeError(w, 500, err)
		return
	}

	mime := http.DetectContentType(data)
	if i := strings.IndexByte(mime, ';'); i > 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	writeJSON(w, 200, skillAssetRow{
		Filename:    sanitizeAssetName(orig),
		StoragePath: path,
		MimeType:    mime,
		SizeBytes:   n,
	})
}

// sanitizeAssetName keeps a clean display / sandbox filename: base name only, no
// path separators. The sandbox stager re-applies filepath.Base too; we keep the
// stored name tidy. Falls back to "asset" when empty.
func sanitizeAssetName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" || name == ".." {
		return "asset"
	}
	return name
}

// validateSkillAssets normalises the assets JSON sent on skill create/update and
// rejects any storage_path that resolves outside <UploadDir>/skill-assets. The
// array round-trips through the admin client, so this is what stops a tampered
// storage_path from steering the sandbox stager at an arbitrary file. Returns
// the JSON to persist (cleaned absolute paths, empty entries dropped).
func validateSkillAssets(d Deps, assets json.RawMessage) (json.RawMessage, error) {
	if len(assets) == 0 || string(assets) == "null" {
		return json.RawMessage("[]"), nil
	}
	var rows []skillAssetRow
	if err := json.Unmarshal(assets, &rows); err != nil {
		return nil, errors.New("assets: invalid JSON")
	}
	base, err := filepath.Abs(filepath.Join(d.Config.UploadDir, skillAssetsSubdir))
	if err != nil {
		return nil, err
	}
	out := make([]skillAssetRow, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Filename) == "" || strings.TrimSpace(row.StoragePath) == "" {
			continue
		}
		abs, err := filepath.Abs(filepath.Clean(row.StoragePath))
		if err != nil {
			return nil, errors.New("assets: bad storage_path")
		}
		if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
			return nil, errors.New("assets: storage_path outside skill-assets directory")
		}
		row.Filename = sanitizeAssetName(row.Filename)
		row.StoragePath = abs
		out = append(out, row)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
