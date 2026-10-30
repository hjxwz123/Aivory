package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"aivory/server/internal/fileguard"
	"aivory/server/internal/netsafe"
	"aivory/server/internal/store"
)

// Skill import (§ skills editor "import" tabs). The admin pastes a URL or
// uploads a zip; the server parses the Agent Skills SKILL.md layout, stages
// the asset files through the same skill-assets pipeline as manual uploads,
// and returns a preview the editor fills the form with. Nothing is written to
// the skills table here — saving still goes through createSkillAdmin and its
// validateSkillAssets re-check, so an imported skill can never bypass the
// guarantees that apply to a hand-written one.

const (
	// maxSkillImportZipBytes caps the uploaded / downloaded archive itself.
	maxSkillImportZipBytes = 64 * 1024 * 1024
	// maxSkillImportExtractBytes caps the summed inflated size of the entries
	// we read, so a small zip bomb cannot exhaust memory.
	maxSkillImportExtractBytes = 100 * 1024 * 1024
	// maxSkillImportEntries caps the number of files considered, bounding
	// parse work on pathological archives (GitHub monorepo zips stay within).
	maxSkillImportEntries = 500
	// maxSkillImportMarkdownBytes caps a plain SKILL.md fetched from a URL.
	maxSkillImportMarkdownBytes = 1024 * 1024
	// skillImportHTTPTimeout bounds one remote fetch including redirects.
	skillImportHTTPTimeout = 45 * time.Second
)

var (
	errSkillImportNoSkillMD     = errors.New("archive does not contain a SKILL.md")
	errSkillImportNoFrontmatter = errors.New("SKILL.md is missing a YAML frontmatter block with name and description")
	errSkillImportTooLarge      = fmt.Errorf("skill import: archive too large (max %d MiB)", maxSkillImportZipBytes/(1024*1024))
)

// skillImportPreview is the wire shape returned by both import endpoints. The
// editor fills the create form with it; warnings list skipped files so the
// admin knows what did not make it (licence files, disallowed types, …).
type skillImportPreview struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Instructions  string          `json:"instructions"`
	Assets        []skillAssetRow `json:"assets"`
	Warnings      []string        `json:"warnings"`
	NameExists    bool            `json:"name_exists"`
	SourceSummary string          `json:"source_summary,omitempty"`
}

// pendingSkillAsset is an analysed asset awaiting the persist step. Parsing is
// all-or-nothing: nothing touches disk until every validation passed.
type pendingSkillAsset struct {
	Filename string // path inside the skill root, sub-directories kept, e.g. "scripts/build.py"
	Ext      string
	MimeType string
	Data     []byte
}

type skillZipAnalysis struct {
	Name         string
	Description  string
	Instructions string
	Assets       []pendingSkillAsset
	Warnings     []string
}

// ===== SKILL.md frontmatter =====

// unquoteYAMLScalar mirrors the frontend's unquoteYamlScalar: strip one layer
// of matching single/double quotes.
func unquoteYAMLScalar(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 {
		first, last := trimmed[0], trimmed[len(trimmed)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
	}
	return trimmed
}

// isYAMLBlockIndicator reports whether a description value introduces a
// literal ("|") or folded (">") block, optionally with a chomping indicator.
func isYAMLBlockIndicator(value string) bool {
	if value == "" {
		return false
	}
	if value[0] != '|' && value[0] != '>' {
		return false
	}
	return strings.Trim(value[1:], "-+") == ""
}

// foldYAMLBlockLines renders the indented lines following a "|"/">" indicator.
// Literal blocks keep line breaks, folded blocks join with spaces — enough for
// the multi-line descriptions the official skills packages use. Blank lines
// act as separators and trailing blank lines are dropped.
func foldYAMLBlockLines(lines []string, literal bool) string {
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			kept = append(kept, "")
			continue
		}
		kept = append(kept, strings.TrimSpace(line))
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	sep := " "
	if literal {
		sep = "\n"
	}
	return strings.Join(kept, sep)
}

// parseSkillMarkdown parses the Agent Skills SKILL.md subset: a leading YAML
// frontmatter block with `name` and `description` (single-line scalars or
// block scalars), and the body as instructions. Keys we do not understand
// (license, allowed-tools, metadata…) are ignored. Mirrors the frontend's
// parseSkillDocument but additionally supports multi-line descriptions.
func parseSkillMarkdown(markdown string) (name, description, instructions string, err error) {
	normalized := strings.ToValidUTF8(strings.ReplaceAll(markdown, "\r\n", "\n"), "")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", errSkillImportNoFrontmatter
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", errSkillImportNoFrontmatter
	}

	for i := 1; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		var key, value string
		if idx := strings.Index(trimmed, ":"); idx >= 0 {
			key = strings.ToLower(strings.TrimSpace(trimmed[:idx]))
			value = strings.TrimSpace(trimmed[idx+1:])
		}
		switch key {
		case "name":
			name = unquoteYAMLScalar(value)
		case "description":
			if isYAMLBlockIndicator(value) {
				var block []string
				j := i + 1
				for ; j < end; j++ {
					next := lines[j]
					if strings.TrimSpace(next) != "" && !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
						break
					}
					block = append(block, next)
				}
				i = j - 1
				description = foldYAMLBlockLines(block, value[0] == '|')
			} else if value == "" {
				// "description:" with the value on following indented lines.
				var block []string
				j := i + 1
				for ; j < end; j++ {
					next := lines[j]
					if strings.TrimSpace(next) != "" && !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
						break
					}
					block = append(block, next)
				}
				if j > i+1 {
					i = j - 1
					description = foldYAMLBlockLines(block, false)
				}
			} else {
				description = unquoteYAMLScalar(value)
			}
		}
	}

	instructions = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || description == "" {
		return "", "", "", errSkillImportNoFrontmatter
	}
	if instructions == "" {
		return "", "", "", errors.New("SKILL.md has an empty body (instructions)")
	}
	return name, description, instructions, nil
}

// ===== zip analysis =====

// cleanZipEntryPath normalises a zip entry name into a safe relative slash
// path. Cleaning against a virtual root absorbs any "..", so traversal
// segments can never survive; empty results (directory entries) are rejected.
func cleanZipEntryPath(raw string) (string, bool) {
	cleaned := path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", false
	}
	return cleaned, true
}

// skipZipEntryName filters macOS packaging noise and dotfiles.
func skipZipEntryName(name string) bool {
	base := path.Base(name)
	if strings.HasPrefix(base, "._") || strings.HasPrefix(base, ".") {
		return true
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "__MACOSX" {
			return true
		}
	}
	return false
}

// stripTopDir drops the first path segment (a GitHub archive wraps everything
// in "<repo>-<ref>/"). A path with a single segment has no wrapper to drop and
// yields "".
func stripTopDir(p string) string {
	idx := strings.IndexByte(p, '/')
	if idx < 0 {
		return ""
	}
	return p[idx+1:]
}

// locateSkillMD picks the SKILL.md entry the import should use. With a hint
// (a GitHub tree URL's directory) only entries directly inside that directory
// match. Without one, entries at the archive root — wrapped or unwrapped —
// are preferred; several equally-shallow candidates means the zip bundles
// multiple skills and we refuse with the list so the admin can point at a
// narrower source.
func locateSkillMD(entries []string, hint string) (string, error) {
	var lowered []string
	loweredSet := map[string]string{}
	for _, e := range entries {
		l := strings.ToLower(e)
		lowered = append(lowered, l)
		loweredSet[l] = e
	}
	normalize := func(p string) string {
		p = strings.ToLower(strings.Trim(p, "/"))
		return strings.TrimSuffix(p, "/")
	}

	if hint != "" {
		target := normalize(hint) + "/skill.md"
		var matches []string
		for _, l := range lowered {
			rest := stripTopDir(l)
			if l == target || rest == target {
				matches = append(matches, loweredSet[l])
			}
		}
		if len(matches) == 0 {
			return "", fmt.Errorf("no SKILL.md found under %q in the archive%s", hint, skillCandidatesSuffix(lowered))
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("archive contains multiple matching SKILL.md files: %s", strings.Join(matches, ", "))
		}
		return matches[0], nil
	}

	pick := func(match func(string) bool) []string {
		var found []string
		for _, l := range lowered {
			if match(l) {
				found = append(found, loweredSet[l])
			}
		}
		return found
	}
	for _, match := range []func(string) bool{
		func(l string) bool { return stripTopDir(l) == "skill.md" && l != "skill.md" },
		func(l string) bool { return l == "skill.md" },
	} {
		if found := pick(match); len(found) == 1 {
			return found[0], nil
		} else if len(found) > 1 {
			return "", fmt.Errorf("archive bundles multiple skills (SKILL.md found in several roots: %s); import one skill's zip or a GitHub subdirectory URL instead", strings.Join(found, ", "))
		}
	}
	return "", errSkillImportNoSkillMD
}

// skillCandidatesSuffix builds the "did you mean" tail for a failed hinted
// lookup: the skill directories the archive actually contains, so the admin
// can correct the URL without downloading and inspecting the zip by hand.
func skillCandidatesSuffix(lowered []string) string {
	var dirs []string
	seen := map[string]bool{}
	for _, l := range lowered {
		rest := stripTopDir(l)
		if rest == "" {
			rest = l
		}
		dir := path.Dir(rest)
		if dir == "." || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if len(dirs) == 0 {
		return ""
	}
	if len(dirs) > 8 {
		dirs = append(append([]string{}, dirs[:8]...), "…")
	}
	return "; the archive contains skills at: " + strings.Join(dirs, ", ")
}

// analyzeSkillZip validates the archive and extracts the skill preview without
// touching disk. Every rejection happens before persist, so a failed import
// never leaves half-written asset files behind.
//
// Assets keep their sub-directory layout (stored filename is a sanitised
// relative path like "scripts/build.py"): the sandbox stager reproduces the
// same tree under /workspace/skills/<name>/, so packaged Python skills with
// nested packages (multiple __init__.py) import exactly as authored and the
// instructions' relative references stay valid verbatim.
func analyzeSkillZip(data []byte, subdirHint string) (*skillZipAnalysis, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("not a valid zip archive")
	}

	type entry struct {
		name string
		data []byte
	}
	var files []entry
	var extracted int64
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name, ok := cleanZipEntryPath(f.Name)
		if !ok || skipZipEntryName(name) {
			continue
		}
		if len(files)+1 > maxSkillImportEntries {
			return nil, fmt.Errorf("archive has too many files (max %d)", maxSkillImportEntries)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read %q from archive: %w", name, err)
		}
		buf := &bytes.Buffer{}
		n, err := io.Copy(buf, io.LimitReader(rc, maxSkillAssetBytes+1))
		closeErr := rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %q from archive: %w", name, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("read %q from archive: %w", name, closeErr)
		}
		extracted += n
		if extracted > maxSkillImportExtractBytes {
			return nil, fmt.Errorf("archive expands beyond %d MiB", maxSkillImportExtractBytes/(1024*1024))
		}
		files = append(files, entry{name: name, data: buf.Bytes()})
	}

	var names []string
	byName := map[string][]byte{}
	for _, f := range files {
		names = append(names, f.name)
		byName[f.name] = f.data
	}
	skillMDPath, err := locateSkillMD(names, subdirHint)
	if err != nil {
		return nil, err
	}
	root := path.Dir(skillMDPath)

	name, description, instructions, err := parseSkillMarkdown(string(byName[skillMDPath]))
	if err != nil {
		return nil, err
	}

	analysis := &skillZipAnalysis{Name: name, Description: description, Instructions: instructions}

	// Assets: every whitelisted file under the skill root, keeping its
	// sub-directory path. Sibling directories (other skills inside a monorepo
	// zip) are ignored, not imported.
	for _, f := range files {
		if f.name == skillMDPath {
			continue
		}
		rel := f.name
		if root != "." && root != "" {
			if !strings.HasPrefix(f.name, root+"/") {
				continue
			}
			rel = strings.TrimPrefix(f.name, root+"/")
		}
		sanitized, ok := fileguard.SanitizeRelPath(rel)
		if !ok {
			analysis.warnSkip(rel, "unusable file path")
			continue
		}
		rel = sanitized
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(path.Base(rel)), "."))
		switch {
		case !allowedSkillAssetExt[ext]:
			analysis.warnSkip(rel, "unsupported file type")
			continue
		case int64(len(f.data)) > maxSkillAssetBytes:
			analysis.warnSkip(rel, "too large")
			continue
		case len(f.data) == 0:
			analysis.warnSkip(rel, "empty file")
			continue
		}
		mime := http.DetectContentType(f.data)
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		analysis.Assets = append(analysis.Assets, pendingSkillAsset{
			Filename: rel,
			Ext:      ext,
			MimeType: mime,
			Data:     f.data,
		})
	}
	return analysis, nil
}

func (a *skillZipAnalysis) warnSkip(rel, reason string) {
	a.Warnings = append(a.Warnings, fmt.Sprintf("skipped %s (%s)", rel, reason))
}

// persistSkillImportAssets stores each pending asset through the same
// skill-assets path as manual uploads. Callers must hold the storage lock.
func persistSkillImportAssets(d Deps, assets []pendingSkillAsset) ([]skillAssetRow, error) {
	if len(assets) == 0 {
		return []skillAssetRow{}, nil
	}
	dir := filepath.Join(d.Config.UploadDir, skillAssetsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rows := make([]skillAssetRow, 0, len(assets))
	for _, asset := range assets {
		id, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(dir, id+"."+asset.Ext)
		if err := fileguard.WriteNewFile(target, asset.Data, 0o644); err != nil {
			return nil, err
		}
		rows = append(rows, skillAssetRow{
			Filename:    asset.Filename,
			StoragePath: target,
			MimeType:    asset.MimeType,
			SizeBytes:   int64(len(asset.Data)),
		})
	}
	return rows, nil
}

// importSkillZipData is the shared tail of both import endpoints: analyse,
// then persist.
func importSkillZipData(d Deps, data []byte, subdirHint string) (*skillZipAnalysis, []skillAssetRow, error) {
	analysis, err := analyzeSkillZip(data, subdirHint)
	if err != nil {
		return nil, nil, err
	}
	rows, err := persistSkillImportAssets(d, analysis.Assets)
	if err != nil {
		return nil, nil, err
	}
	return analysis, rows, nil
}

// writeSkillImportPreview adds the duplicate-name pre-check and responds.
func writeSkillImportPreview(ctx context.Context, d Deps, w http.ResponseWriter, analysis *skillZipAnalysis, assets []skillAssetRow, source string) {
	preview := &skillImportPreview{
		Name:          analysis.Name,
		Description:   analysis.Description,
		Instructions:  analysis.Instructions,
		Assets:        assets,
		Warnings:      analysis.Warnings,
		SourceSummary: source,
	}
	if preview.Warnings == nil {
		preview.Warnings = []string{}
	}
	if existing, err := store.GetSkillByName(ctx, d.DB, analysis.Name); err == nil && existing != nil {
		preview.NameExists = true
	}
	writeJSON(w, 200, preview)
}

// ===== endpoint: upload a zip =====

// importSkillArchiveAdmin — POST /api/admin/skills/import-archive, multipart
// "file" holding a zip of one skill (SKILL.md + assets, optionally wrapped in
// a top-level directory).
func importSkillArchiveAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	unlock, lockErr := fileguard.LockStorage(r.Context(), false, d.Config.UploadDir)
	if lockErr != nil {
		writeError(w, 500, lockErr)
		return
	}
	defer unlock()
	if err := r.ParseMultipartForm(maxSkillImportZipBytes + 4096); err != nil {
		writeError(w, 400, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, errors.New("missing file field"))
		return
	}
	defer file.Close()

	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, io.LimitReader(file, maxSkillImportZipBytes+1))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n > maxSkillImportZipBytes {
		writeError(w, 400, errSkillImportTooLarge)
		return
	}
	if n == 0 {
		writeError(w, 400, errors.New("skill import: empty archive"))
		return
	}

	analysis, assets, err := importSkillZipData(d, buf.Bytes(), "")
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeSkillImportPreview(r.Context(), d, w, analysis, assets, path.Base(header.Filename))
}

// ===== endpoint: import from a URL =====

// skillImportTarget is the resolved download for a remote import URL.
type skillImportTarget struct {
	Kind       string // "zip" or "markdown"
	URL        string
	SubdirHint string
}

// resolveSkillImportURL classifies the admin-supplied URL. GitHub web URLs are
// rewritten to their archive/raw equivalents; everything else is fetched
// as-is and classified by suffix / content type / magic bytes.
func resolveSkillImportURL(rawURL string) (*skillImportTarget, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("only http(s) URLs are supported")
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "github.com" || host == "www.github.com" {
		segments := []string{}
		for _, seg := range strings.Split(parsed.Path, "/") {
			if seg != "" {
				segments = append(segments, seg)
			}
		}
		if len(segments) >= 2 {
			owner, repo := segments[0], strings.TrimSuffix(segments[1], ".git")
			switch {
			case strings.HasSuffix(strings.ToLower(parsed.Path), ".zip") &&
				((len(segments) >= 4 && segments[2] == "archive") ||
					(len(segments) >= 6 && segments[2] == "releases" && segments[3] == "download")):
				// GitHub's archive and release asset URLs are already downloads.
				// Keep their escaped paths and query parameters intact; the same
				// SSRF-safe client validates every redirect when fetching them.
				return &skillImportTarget{Kind: "zip", URL: parsed.String()}, nil
			case len(segments) == 2:
				return &skillImportTarget{Kind: "zip", URL: fmt.Sprintf("https://github.com/%s/%s/archive/HEAD.zip", owner, repo)}, nil
			case len(segments) >= 4 && segments[2] == "tree":
				ref := segments[3]
				hint := strings.Join(segments[4:], "/")
				return &skillImportTarget{
					Kind:       "zip",
					URL:        fmt.Sprintf("https://github.com/%s/%s/archive/%s.zip", owner, repo, ref),
					SubdirHint: hint,
				}, nil
			case len(segments) >= 5 && segments[2] == "blob" && strings.EqualFold(path.Ext(segments[len(segments)-1]), ".md"):
				ref := segments[3]
				filePath := strings.Join(segments[4:], "/")
				return &skillImportTarget{
					Kind: "markdown",
					URL:  fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, filePath),
				}, nil
			}
		}
		return nil, errors.New("unsupported GitHub URL: use the repository, a /tree/<ref>/<dir> directory, or a /blob/<ref>/SKILL.md page URL")
	}

	kind := ""
	cleanPath := parsed.Path
	if strings.HasSuffix(strings.ToLower(cleanPath), ".zip") {
		kind = "zip"
	} else if strings.HasSuffix(strings.ToLower(cleanPath), ".md") {
		kind = "markdown"
	}
	return &skillImportTarget{Kind: kind, URL: parsed.String()}, nil
}

// skillImportHTTPClient returns the injectable test client or the SSRF-hardened
// production one (public IPs, ports 80/443, dial-time re-validation).
func skillImportHTTPClient(d Deps) *http.Client {
	if d.SkillImportHTTPClient != nil {
		return d.SkillImportHTTPClient
	}
	return netsafe.SafeClient(skillImportHTTPTimeout)
}

var zipMagic = []byte("PK")

// looksLikeZip sniffs the zip local-file-header magic.
func looksLikeZip(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:2], zipMagic) && (data[2] == 3 || data[2] == 5 || data[2] == 7)
}

// importSkillRemoteAdmin — POST /api/admin/skills/import-remote, JSON {url}.
// Accepts a zip direct link, a raw SKILL.md link, or a GitHub repository /
// subdirectory / blob page URL.
func importSkillRemoteAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	target, err := resolveSkillImportURL(body.URL)
	if err != nil {
		writeError(w, 400, err)
		return
	}

	unlock, lockErr := fileguard.LockStorage(r.Context(), false, d.Config.UploadDir)
	if lockErr != nil {
		writeError(w, 500, lockErr)
		return
	}
	defer unlock()

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.URL, nil)
	if err != nil {
		writeError(w, 400, errors.New("invalid URL"))
		return
	}
	request.Header.Set("user-agent", "Aivory-Admin-SkillImport")
	request.Header.Set("accept", "*/*")
	response, err := skillImportHTTPClient(d).Do(request)
	if err != nil {
		// 400 (not 502): writeError hides the message of any >=500, but an
		// unreachable URL is an input problem the admin must be able to read
		// (DNS failure, blocked private IP, timeout, …).
		writeError(w, 400, fmt.Errorf("fetch %s failed: %v", target.URL, err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet := ""
		if body, readErr := io.ReadAll(io.LimitReader(response.Body, 256)); readErr == nil {
			snippet = strings.TrimSpace(strings.ToValidUTF8(string(body), ""))
		}
		if snippet != "" {
			writeError(w, 400, fmt.Errorf("download returned HTTP %d: %s", response.StatusCode, snippet))
			return
		}
		writeError(w, 400, fmt.Errorf("download returned HTTP %d (empty body)", response.StatusCode))
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSkillImportZipBytes+1))
	if err != nil {
		writeError(w, 400, fmt.Errorf("read download: %v", err))
		return
	}
	if int64(len(data)) > maxSkillImportZipBytes {
		writeError(w, 400, errSkillImportTooLarge)
		return
	}

	kind := target.Kind
	if kind == "" {
		switch {
		case looksLikeZip(data):
			kind = "zip"
		case isTextContentType(response.Header.Get("content-type")):
			kind = "markdown"
		default:
			writeError(w, 400, errors.New("unsupported remote content: expected a zip archive or a SKILL.md document"))
			return
		}
	}

	if kind == "markdown" {
		if int64(len(data)) > maxSkillImportMarkdownBytes {
			writeError(w, 400, errors.New("skill import: SKILL.md document too large (max 1 MiB)"))
			return
		}
		name, description, instructions, err := parseSkillMarkdown(string(data))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		writeSkillImportPreview(r.Context(), d, w, &skillZipAnalysis{
			Name:         name,
			Description:  description,
			Instructions: instructions,
		}, []skillAssetRow{}, target.URL)
		return
	}

	analysis, assets, err := importSkillZipData(d, data, target.SubdirHint)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeSkillImportPreview(r.Context(), d, w, analysis, assets, target.URL)
}

func isTextContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "text/markdown" || mediaType == "text/plain" || mediaType == "text/x-markdown"
}
