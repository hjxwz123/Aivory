package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"aivory/server/internal/store"
)

// uploadPolicy is the live, admin-configured upload safety policy (design.md
// §4.6 / §8.2). Every file ingest path — `/api/files`, `/api/kbs/:id/documents`,
// `/api/projects/:id/documents` — runs each upload through `validate()` BEFORE
// the bytes are streamed to disk. Rejected uploads never touch the filesystem.
//
// What this enforces:
//   - Filename hygiene: no NUL, no control chars, no path separators, no
//     leading dots, length cap.
//   - Extension allowlist: lowercased, leading-dot stripped, LAST extension
//     only (so `evil.pdf.exe` is checked as `exe`, not `pdf`). When the
//     admin sets `upload_allowed_extensions=""`, we fall back to a safe
//     default set rather than "allow everything" — the explicit user request
//     was for an allowlist, so an empty value should still be safe-by-default.
//   - Empty / no-extension files: rejected. A file with no extension can be
//     anything; we'd rather force the user to rename than guess from bytes.
//
// What this DOES NOT do (out of scope this iteration):
//   - Magic-byte sniffing. MIME from the multipart header is captured for
//     display only and never used as a security decision.
//   - Virus / malware scanning. Operators wanting that should put an
//     intercepting proxy in front of /api/files.
//   - Per-user quota or rate-limiting beyond §8.1's request limits.
type uploadPolicy struct {
	AllowedExt map[string]bool // lowercased, no leading dot
}

// defaultUploadExtensions is the safe-by-default allowlist used when the admin
// has not configured one. Covers the formats Aivory's other subsystems
// actually understand:
//   - documents the RAG pipeline ingests (MinerU + local text)
//   - images displayed in the composer attachments rail
//   - code / data dumps users frequently paste into chats
//
// Intentionally NOT included: executable wrappers (`.exe`, `.dll`, `.so`,
// `.bin`, `.jar`, `.msi`, `.bat`, `.cmd`, `.ps1`, `.sh`), Office macro-enabled
// files (`.docm`, `.xlsm`, `.pptm`), archives that could be polyglots
// (`.zip`, `.tar`, `.gz`, `.7z`, `.rar`) and anything HTML-rendered that the
// browser might interpret if served back inline (`.html`, `.htm`, `.svg` —
// SVG can carry XSS; PDF is fine because §4.5 forces attachment disposition).
var defaultUploadExtensions = []string{
	// docs
	"pdf", "docx", "pptx", "xlsx", "doc", "ppt", "xls",
	// plaintext / source
	"txt", "md", "markdown", "csv", "json", "yaml", "yml", "xml", "log", "rtf",
	// images
	"png", "jpg", "jpeg", "gif", "webp", "bmp",
	// code (some users upload snippets)
	"py", "go", "js", "ts", "tsx", "jsx", "rs", "java", "c", "cc", "cpp", "h", "hpp",
	"sql", "toml", "ini", "env",
}

// extAliases maps an extension to OTHER spellings of the SAME format, so an admin
// who allows one spelling automatically accepts the equivalent ones. Most
// importantly jpg↔jpeg: they are byte-identical JPEG; allowing "jpg" but
// rejecting "jpeg" (a very common allowlist typo) blocks ordinary photos.
var extAliases = map[string][]string{
	"jpg":      {"jpeg", "jpe", "jfif"},
	"jpeg":     {"jpg", "jpe", "jfif"},
	"tif":      {"tiff"},
	"tiff":     {"tif"},
	"yml":      {"yaml"},
	"yaml":     {"yml"},
	"md":       {"markdown"},
	"markdown": {"md"},
	"htm":      {"html"},
	"html":     {"htm"},
}

// applyExtAliases expands the allowlist in place with equivalent spellings.
func applyExtAliases(exts map[string]bool) {
	cur := make([]string, 0, len(exts)) // snapshot keys: don't mutate while ranging
	for e := range exts {
		cur = append(cur, e)
	}
	for _, e := range cur {
		for _, a := range extAliases[e] {
			exts[a] = true
		}
	}
}

// alwaysAllowedImageExtensions is the family of common raster image formats
// Aivory natively displays in the composer attachments rail and feeds to
// vision models. They are ALWAYS accepted regardless of the admin allowlist:
// the allowlist is meant to gate document/code/data uploads, while ordinary
// photos must just work. All are served with attachment disposition and carry
// no script (unlike SVG), so admitting them unconditionally does not weaken the
// executable/polyglot/XSS protection the allowlist exists for.
var alwaysAllowedImageExtensions = []string{
	"png", "jpg", "jpeg", "jpe", "jfif", "gif", "webp", "bmp", "tif", "tiff", "heic", "heif", "avif", "ico",
}

// applyImageFamily unconditionally admits the common image formats — images
// bypass the admin allowlist entirely (per product decision: only non-image
// formats are gated).
func applyImageFamily(exts map[string]bool) {
	for _, e := range alwaysAllowedImageExtensions {
		exts[e] = true
	}
}

// loadUploadPolicy reads the live admin setting. Robust to: nil DB
// (returns the default policy so tests don't need a DB), invalid JSON
// (falls back to default), the empty string (uses default — see policy note
// in the struct comment).
func loadUploadPolicy(d Deps) uploadPolicy {
	raw, err := store.GetSetting(d.DB, "upload_allowed_extensions")
	var configured string
	if err == nil {
		_ = json.Unmarshal(raw, &configured)
	}
	exts := parseExtensionList(configured)
	if len(exts) == 0 {
		exts = make(map[string]bool, len(defaultUploadExtensions))
		for _, e := range defaultUploadExtensions {
			exts[e] = true
		}
	}
	// Accept equivalent spellings (jpg↔jpeg, …) regardless of which the admin
	// listed — the validateUpload check and the frontend's `<input accept>` both
	// read this expanded set.
	applyExtAliases(exts)
	// If images are allowed at all, keep the safe-image family whole (allowed
	// png but not jpg is a typo, not a policy).
	applyImageFamily(exts)
	return uploadPolicy{AllowedExt: exts}
}

// parseExtensionList accepts the admin setting value verbatim: a string of
// extensions separated by any of ", ; \n \t". Each entry is lowercased, has
// a leading dot stripped (so the admin can write either `pdf` or `.pdf`),
// and empty entries are dropped.
func parseExtensionList(s string) map[string]bool {
	out := map[string]bool{}
	if s == "" {
		return out
	}
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\t', ' ':
			return true
		}
		return false
	}) {
		e := strings.ToLower(strings.TrimSpace(raw))
		e = strings.TrimPrefix(e, ".")
		if e == "" {
			continue
		}
		// Belt-and-braces: an extension itself shouldn't contain a separator;
		// if the admin pasted `pdf.exe` reject that entry — it makes no sense
		// as one extension and silently accepting it would be a footgun.
		if strings.ContainsAny(e, "./\\:*?\"<>|") {
			continue
		}
		out[e] = true
	}
	return out
}

// errInvalidUpload is what every entry point returns when validation fails.
// Wrap-friendly so handlers can map to 400 without re-typing the message.
var errInvalidUpload = errors.New("invalid upload")

// validateUpload sanitises the multipart filename and checks the extension
// against the policy. Returns the safe filename to use on disk + the
// lowercase extension (without dot) for logging / status reporting.
//
// All upload paths must call this BEFORE creating the destination file so
// rejected uploads never write bytes anywhere.
func (p uploadPolicy) validateUpload(rawName string) (string, string, error) {
	// 1. Strip directory components (defence-in-depth — multipart shouldn't
	//    carry them, but a hand-crafted client might). filepath.Base honours
	//    OS separators; on Linux it ignores backslashes which is fine.
	name := filepath.Base(rawName)
	// 2. Reject anything weird: NUL byte (filesystem boundary), control chars
	//    (could mess with logging / terminals), absolute paths, parent-dir
	//    sentinels, hidden-file leaders.
	if name == "" || name == "." || name == ".." {
		return "", "", fmt.Errorf("%w: empty or reserved name", errInvalidUpload)
	}
	if strings.ContainsRune(name, 0) {
		return "", "", fmt.Errorf("%w: NUL byte in filename", errInvalidUpload)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", "", fmt.Errorf("%w: control character in filename", errInvalidUpload)
		}
	}
	if strings.HasPrefix(name, ".") {
		// Hidden-file convention on Unix; also tends to break MinerU's
		// extension classifier. Force the user to rename.
		return "", "", fmt.Errorf("%w: filename must not start with '.'", errInvalidUpload)
	}
	// 3. Length cap. Most filesystems handle 255 bytes; cap a touch lower so
	//    the gen-id prefix we add in receiveDocument fits.
	if len(name) > 200 {
		return "", "", fmt.Errorf("%w: filename too long (>200 bytes)", errInvalidUpload)
	}
	// 4. Extract the LAST extension (so `report.pdf.exe` is judged on `exe`).
	//    `filepath.Ext` returns ".exe" here; we lower + strip the dot.
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "", "", fmt.Errorf("%w: file has no extension", errInvalidUpload)
	}
	ext = strings.TrimPrefix(ext, ".")
	if !p.AllowedExt[ext] {
		return "", "", fmt.Errorf("%w: extension %q is not allowed", errInvalidUpload, ext)
	}
	return name, ext, nil
}

// validateUploadRelPath sanitises the relative path a FOLDER upload reports for
// one of its files, and reports whether the path is usable.
//
// A direct folder upload carries client-supplied relative paths that become
// sandbox paths under /workspace/folders. Validate each path before writing.
//
//   - backslashes are folded to "/" (a Windows folder picker reports them);
//   - a single leading "./" is dropped, and a leading "/" is REJECTED rather
//     than silently stripped — an absolute path means the client is confused
//     about what it is sending, and quietly reinterpreting it as relative is how
//     a path bug becomes a security bug later;
//   - an empty segment ("a//b"), "." and ".." are rejected outright — the latter
//     is what would otherwise let a crafted upload escape the folder root;
//   - NUL and control characters are rejected (filesystem boundary + logging);
//   - each segment is length-capped, matching the filename rule.
//
// Returns ("", false) for anything unusable, so the caller can reject the
// request instead of silently flattening the file into the folder root.
func validateUploadRelPath(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	if strings.ContainsRune(raw, 0) {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	// A Windows client may report "sub\dir\file.txt"; the browser's
	// webkitRelativePath uses "/", but a hand-crafted request need not.
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return "", false
	}
	if strings.HasPrefix(normalized, "./") {
		normalized = normalized[2:]
	}
	segments := strings.Split(normalized, "/")
	cleaned := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			// Covers a leading/duplicated separator and a trailing one.
			return "", false
		}
		if segment == "." || segment == ".." {
			return "", false
		}
		if len(segment) > 200 {
			return "", false
		}
		cleaned = append(cleaned, segment)
	}
	if len(cleaned) == 0 || len(cleaned) > maxFolderRelPathSegments {
		return "", false
	}
	joined := strings.Join(cleaned, "/")
	if len(joined) > maxFolderRelPathBytes {
		return "", false
	}
	return joined, true
}

const (
	// A folder deeper or longer than this is almost certainly not a project the
	// user meant to share, and the path is echoed into a sandbox manifest.
	maxFolderRelPathSegments = 32
	maxFolderRelPathBytes    = 1024
)

// AllowedExtensionsSlice is the policy's allowlist as a sorted slice — used
// by the policy endpoint that hands the list back to the frontend so the
// composer can set `<input accept>` accordingly.
func (p uploadPolicy) AllowedExtensionsSlice() []string {
	out := make([]string, 0, len(p.AllowedExt))
	for e := range p.AllowedExt {
		out = append(out, e)
	}
	// stable order — easier to assert on, and the frontend doesn't care.
	sortStrings(out)
	return out
}

// sortStrings is a tiny in-place sort to avoid importing `sort` here for
// one call site.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// meUploadPolicyHandler exposes the live allowlist to the authenticated user.
// The composer uses it to set `<input accept="...">` so the file picker
// pre-filters; the server still re-validates because `accept` is advisory and
// scriptable clients can bypass it.
//
// We also surface the byte cap so the frontend can warn the user about a
// too-large file before the multipart upload begins.
func meUploadPolicyHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	p := loadUploadPolicy(d)
	writeJSON(w, 200, map[string]any{
		"allowed_extensions": p.AllowedExtensionsSlice(),
		"max_upload_bytes":   d.Config.MaxUploadBytes,
		// §4.6-A per-kind caps (admin-tunable). The composer rejects an oversize
		// file up front using these instead of the single env ceiling.
		"max_image_bytes": uploadLimitBytes(d, "image"),
		"max_file_bytes":  uploadLimitBytes(d, "text"),
	})
}
