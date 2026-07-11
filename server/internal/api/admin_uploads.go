package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	// Image-format codecs registered for image.DecodeConfig (header-only —
	// these imports are import-for-side-effects, NOT a security-sensitive
	// pixel decode. We only read the structural header to confirm "yes, this
	// really is a PNG / JPEG" before persisting.
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aurelia/server/internal/envcfg"
)

// iconServingCacheAge is the GET-icon Cache-Control max-age (86400s = 24h).
var iconServingCacheAge = 86400 * time.Second

// Icon upload — admin-only POST that stores a small image and returns a URL
// the model record can reference.
//
// Design.md §4.6 / §8.2 generic upload safety apply, plus these extra
// icon-specific defences. Defence-in-depth order:
//
//  1. Multipart parse limit set to the configured icon cap so a 10 MB payload
//     never sits in memory.
//  2. Extension allowlist {png, jpg, jpeg}. No SVG — SVG is XML and can
//     carry <script>/<foreignObject> XSS when served back inline. No GIF —
//     the legacy decoder has had RCE history and we don't need animations on
//     a 9x9 chip. No WebP — would require pulling in golang.org/x/image,
//     not currently a dependency; users can re-export to PNG.
//  3. Read the full bytes, cap-checked, into memory.
//  4. http.DetectContentType sniff on the bytes (NOT the multipart header —
//     headers are attacker-controlled). Must agree with the extension.
//  5. image.DecodeConfig on the bytes (header-only parse). Rejects polyglots
//     whose magic bytes pass the sniff but whose declared dimensions are
//     malformed — also catches "real GIF renamed to .png" mismatches.
//  6. Random filename, lowercase extension only — the original filename never
//     touches disk. Stored under <UPLOAD_DIR>/icons/<hex>.<ext> in a flat
//     directory (icons are shared admin-managed assets, not per-user).
//  7. Returned URL is /api/icons/<hex>.<ext> — served by GET below with
//     requireAuth (any authenticated user can render an icon, but only
//     admins can mint one).

// maxIconBytes — small on purpose. Icons render at ~16-32 px in the model
// picker; anything over 256 KiB is either a photo (wrong tool) or an attempt
// to waste disk.
var maxIconBytes = envcfg.Int64("AURELIA_API_ADMIN_ICON_UPLOAD_SIZE", 256*1024)

// allowedIconExt — extension → expected DetectContentType prefix. The
// DetectContentType return value can also include charset on text/* but for
// image/* it's always a bare media type.
var allowedIconExt = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"svg":  "image/svg+xml",
}

// svgBadPatterns are the active-content vectors an SVG icon must NOT contain.
// SVG is XML and renders as a document on direct navigation, so a <script>,
// an on*= handler, a <foreignObject>, or an external/JS URL is an XSS/XXE risk.
// We REJECT (not strip) so a flagged file never lands on disk; the admin
// re-exports a clean SVG. Defence-in-depth: icons render via <img src> (scripts
// inert) and serveIcon adds a script-blocking CSP for the SVG case.
var svgEventAttrRe = regexp.MustCompile(`(?i)<[^>]*\son[a-z]+\s*=`)
var svgBadSubstrings = []string{
	"<script", "</script", "javascript:", "<foreignobject",
	"<!entity", "<!doctype", "<iframe", "<embed", "<object", "<animatetransform onbegin",
}

// validateSVG enforces the no-active-content rule above. Returns nil when the
// bytes are a plausible, script-free SVG.
func validateSVG(data []byte) error {
	if len(data) == 0 {
		return errIconBadBytes
	}
	lower := strings.ToLower(string(data))
	if !strings.Contains(lower, "<svg") {
		return errIconBadBytes
	}
	for _, bad := range svgBadSubstrings {
		if strings.Contains(lower, bad) {
			return errIconUnsafeSVG
		}
	}
	if svgEventAttrRe.MatchString(lower) {
		return errIconUnsafeSVG
	}
	return nil
}

var errIconBadExt = errors.New("icon: extension must be one of png/jpg/jpeg/svg")
var errIconTooLarge = errors.New("icon: too large (max 256 KiB)")
var errIconBadBytes = errors.New("icon: file bytes do not match a supported image format")
var errIconExtMismatch = errors.New("icon: extension does not match file bytes")
var errIconUnsafeSVG = errors.New("icon: SVG contains scripts or active content — re-export a static SVG")

// uploadIconAdmin — POST /api/admin/icons, multipart form, field name "file".
// Returns {"url": "/api/icons/<hex>.<ext>", "filename": "<hex>.<ext>"} so the
// frontend can drop the URL straight into the model's `icon` column.
func uploadIconAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	// 1. Multipart with a tight envelope cap. ParseMultipartForm enforces the
	//    in-memory ceiling; bytes beyond it stream to a temp file we control.
	if err := r.ParseMultipartForm(maxIconBytes + 1024); err != nil {
		writeError(w, 400, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	defer file.Close()

	// 2. Extension check on the multipart filename — only used to decide
	//    "is this file even worth reading". The bytes still have to match.
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	expectedMime, ok := allowedIconExt[ext]
	if !ok {
		writeError(w, 400, errIconBadExt)
		return
	}

	// 3. Read with a hard byte cap. io.LimitReader caps at +1 so we can
	//    detect oversize files vs files that happen to be exactly at the cap.
	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, io.LimitReader(file, maxIconBytes+1))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n > maxIconBytes {
		writeError(w, 400, errIconTooLarge)
		return
	}
	if n == 0 {
		writeError(w, 400, errIconBadBytes)
		return
	}
	data := buf.Bytes()

	if ext == "svg" {
		// 4a. SVG is XML, not a raster image — validate it carries no active
		//     content (script / event handlers / foreignObject / XXE) instead of
		//     the sniff + raster-decode checks below.
		if err := validateSVG(data); err != nil {
			writeError(w, 400, err)
			return
		}
	} else {
		// 4. Sniff Content-Type from the actual bytes — never trust the multipart
		//    header (Content-Type there is client-supplied). Must agree with the
		//    extension we accepted.
		sniff := http.DetectContentType(data)
		// DetectContentType may append "; charset=..." for some types; icons are
		// always media types but split to be safe.
		if i := strings.IndexByte(sniff, ';'); i > 0 {
			sniff = strings.TrimSpace(sniff[:i])
		}
		if sniff != expectedMime {
			writeError(w, 400, errIconExtMismatch)
			return
		}

		// 5. Structural decode — image.DecodeConfig parses the header (width /
		//    height / colorspace) without decoding the pixels. Catches polyglots
		//    where the first 512 bytes look like one format but the rest is
		//    something else. We don't keep the config; the call is purely a
		//    "does this parse" check.
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			writeError(w, 400, errIconBadBytes)
			return
		}
	}

	// 6. Random 12-byte filename. Hex-encoded → 24 chars + extension.
	id, err := randomHex(12)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	dir := filepath.Join(d.Config.UploadDir, "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, 500, err)
		return
	}
	// Canonicalise jpg→jpeg for stored extension so the lookup table on GET
	// stays small. The returned URL still uses what we wrote to disk.
	storedExt := ext
	if storedExt == "jpg" {
		storedExt = "jpeg"
	}
	filename := id + "." + storedExt
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeError(w, 500, err)
		return
	}

	writeJSON(w, 200, map[string]any{
		"url":      "/api/icons/" + filename,
		"filename": filename,
	})
}

// serveIcon — GET /api/icons/:filename. Any authenticated user can fetch an
// icon (they're rendered for every user in the model picker), but the
// directory is admin-write-only. The filename is validated against a strict
// safelist before touching the filesystem — even though only validated names
// can land here via upload, we re-check on read so a directory-traversal
// attempt (`/api/icons/..%2f..%2fetc%2fpasswd`) is rejected before we open
// anything.
//
// Cache-Control: shared CDN-safe with a short max-age so swapping the icon
// for a given model takes effect quickly. 24h is the right balance — the
// filename changes on every re-upload (new random hex), so we don't need
// long-lived caches.
func serveIcon(d Deps, w http.ResponseWriter, r *http.Request) {
	filename := pathParam(r, "filename")
	if !isSafeIconFilename(filename) {
		writeError(w, 404, errors.New("not found"))
		return
	}
	path := filepath.Join(d.Config.UploadDir, "icons", filename)
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, errors.New("not found"))
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Map extension → Content-Type. Already validated by isSafeIconFilename.
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ct, ok := allowedIconExt[ext]; ok {
		w.Header().Set("Content-Type", ct)
	}
	if ext == "svg" {
		// Even though uploaded SVGs are validated script-free and render inert via
		// <img>, a direct hit on this URL renders it as a document — block any
		// script/active content with a strict CSP + sandbox.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(iconServingCacheAge.Seconds())))
	// X-Content-Type-Options: belt-and-braces, prevent any sniff override
	// downstream.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

// isSafeIconFilename — pattern check used by serveIcon. The upload handler
// only ever writes `<24-hex-chars>.<ext>`; anything that doesn't match is
// either a typo or an attack and gets 404.
func isSafeIconFilename(name string) bool {
	dot := strings.IndexByte(name, '.')
	if dot <= 0 || dot >= len(name)-1 {
		return false
	}
	stem := name[:dot]
	ext := strings.ToLower(name[dot+1:])
	if _, ok := allowedIconExt[ext]; !ok {
		return false
	}
	if len(stem) < 8 || len(stem) > 64 {
		return false
	}
	for _, r := range stem {
		isLowerHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isLowerHex {
			return false
		}
	}
	return true
}

// randomHex returns a hex-encoded random string of n bytes (so the output is
// 2n characters). Used for icon filenames so we don't leak the original.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
