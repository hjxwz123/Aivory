package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// buildZip builds an in-memory zip from name -> content entries.
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	w := zip.NewWriter(buf)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const sampleSkillMD = `---
name: pdf-tools
description: Use when the user asks to fill or extract from PDF files.
---

# PDF tools

Run python scripts/build.py with the uploaded file.
See references/formats.md for supported fields.
`

func TestParseSkillMarkdownVariants(t *testing.T) {
	name, desc, inst, err := parseSkillMarkdown(sampleSkillMD)
	if err != nil || name != "pdf-tools" || !strings.HasPrefix(inst, "# PDF tools") {
		t.Fatalf("parse=%q %q %v", name, inst, err)
	}
	if desc != "Use when the user asks to fill or extract from PDF files." {
		t.Fatalf("desc=%q", desc)
	}

	folded := "---\nname: x\ndescription: >-\n  Use when the user asks\n  for slides.\n---\nBody\n"
	_, desc, inst, err = parseSkillMarkdown(folded)
	if err != nil || desc != "Use when the user asks for slides." || inst != "Body" {
		t.Fatalf("folded=%q inst=%q err=%v", desc, inst, err)
	}

	literal := "---\nname: x\ndescription: |\n  line one\n  line two\n---\nBody\n"
	_, desc, _, err = parseSkillMarkdown(literal)
	if err != nil || desc != "line one\nline two" {
		t.Fatalf("literal=%q err=%v", desc, err)
	}

	quoted := "---\nname: \"x\"\ndescription: 'quoted desc'\n---\nBody\n"
	name, desc, _, err = parseSkillMarkdown(quoted)
	if err != nil || name != "x" || desc != "quoted desc" {
		t.Fatalf("quoted=%q %q err=%v", name, desc, err)
	}

	if _, _, _, err := parseSkillMarkdown("# no frontmatter\nbody"); err == nil {
		t.Fatal("missing frontmatter accepted")
	}
	if _, _, _, err := parseSkillMarkdown("---\ndescription: d\n---\nBody\n"); err == nil {
		t.Fatal("missing name accepted")
	}
	if _, _, _, err := parseSkillMarkdown("---\nname: n\ndescription: d\n---\n\n"); err == nil {
		t.Fatal("empty body accepted")
	}
}

func TestAnalyzeSkillZipHappyPath(t *testing.T) {
	entries := map[string]string{
		"pdf-tools-main/SKILL.md":         sampleSkillMD,
		"pdf-tools-main/scripts/build.py": "print('build')",
		"pdf-tools-main/LICENSE":          "MIT",
	}
	data := buildZip(t, entries)
	analysis, err := analyzeSkillZip(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Name != "pdf-tools" {
		t.Fatalf("name=%q", analysis.Name)
	}
	// Sub-directory layout is preserved verbatim, so the instructions'
	// relative references stay valid without rewriting.
	if len(analysis.Assets) != 1 || analysis.Assets[0].Filename != "scripts/build.py" {
		t.Fatalf("assets=%+v", analysis.Assets)
	}
	if !strings.Contains(analysis.Instructions, "python scripts/build.py") {
		t.Fatalf("instructions rewritten unexpectedly: %q", analysis.Instructions)
	}
	joined := strings.Join(analysis.Warnings, "\n")
	if !strings.Contains(joined, "LICENSE") {
		t.Fatalf("missing skip warning: %v", analysis.Warnings)
	}
}

func TestAnalyzeSkillZipMultipleSkillsRejected(t *testing.T) {
	entries := map[string]string{
		"one/SKILL.md": sampleSkillMD,
		"two/SKILL.md": sampleSkillMD,
	}
	if _, err := analyzeSkillZip(buildZip(t, entries), ""); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("err=%v", err)
	}
}

func TestAnalyzeSkillZipSubdirHintLocatesSkill(t *testing.T) {
	entries := map[string]string{
		"repo-main/SKILL.md":          sampleSkillMD,
		"repo-main/sub/pdf/SKILL.md":  sampleSkillMD,
		"repo-main/sub/docx/SKILL.md": sampleSkillMD,
		"repo-main/sub/pdf/run.py":    "print(1)",
	}
	data := buildZip(t, entries)
	analysis, err := analyzeSkillZip(data, "sub/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Assets) != 1 || analysis.Assets[0].Filename != "run.py" {
		t.Fatalf("assets=%+v", analysis.Assets)
	}
	// A wrong hint must name the directories the archive actually offers so
	// the admin can fix the URL without unzipping by hand.
	if _, err := analyzeSkillZip(data, "sub/missing"); err == nil || !strings.Contains(err.Error(), "skills at") {
		t.Fatalf("hint error lacks candidates: %v", err)
	}
}

// TestAnalyzeSkillZipKeepsNestedPackages — packaged skills legitimately
// contain same-named files in different directories (Python __init__.py).
// Preserving the sub-directory layout must accept them, not reject.
func TestAnalyzeSkillZipKeepsNestedPackages(t *testing.T) {
	entries := map[string]string{
		"wrap/SKILL.md":                           sampleSkillMD,
		"wrap/scripts/__init__.py":                "# pkg",
		"wrap/scripts/office/helpers/__init__.py": "# nested pkg",
		"wrap/scripts/office/run.py":              "print(1)",
	}
	analysis, err := analyzeSkillZip(buildZip(t, entries), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Assets) != 3 {
		t.Fatalf("assets=%+v", analysis.Assets)
	}
	byName := map[string]bool{}
	for _, a := range analysis.Assets {
		byName[a.Filename] = true
	}
	if !byName["scripts/__init__.py"] || !byName["scripts/office/helpers/__init__.py"] || !byName["scripts/office/run.py"] {
		t.Fatalf("nested layout lost: %v", byName)
	}
}

func TestAnalyzeSkillZipTraversalNeutralised(t *testing.T) {
	entries := map[string]string{
		"wrap/SKILL.md":          sampleSkillMD,
		"wrap/../../etc/evil.sh": "boom",
	}
	// The .. segments are absorbed by the virtual-root clean; the entry either
	// lands harmlessly inside the skill root or is ignored — never a traversal.
	analysis, err := analyzeSkillZip(buildZip(t, entries), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range analysis.Assets {
		if strings.Contains(filepath.ToSlash(asset.Filename), "..") {
			t.Fatalf("traversal survived: %q", asset.Filename)
		}
	}
}

func TestResolveSkillImportURL(t *testing.T) {
	cases := []struct {
		raw, kind, wantURL, hint string
		ok                       bool
	}{
		{"https://github.com/anthropics/skills", "zip", "https://github.com/anthropics/skills/archive/HEAD.zip", "", true},
		{"https://github.com/anthropics/skills/tree/main/document-skills/pdf", "zip", "https://github.com/anthropics/skills/archive/main.zip", "document-skills/pdf", true},
		{"https://github.com/o/r/blob/main/dir/SKILL.md", "markdown", "https://raw.githubusercontent.com/o/r/main/dir/SKILL.md", "", true},
		{"https://example.com/skill.zip", "zip", "https://example.com/skill.zip", "", true},
		{"https://example.com/SKILL.md", "markdown", "https://example.com/SKILL.md", "", true},
		{"ftp://example.com/x.zip", "", "", "", false},
		{"https://github.com/o/r/issues/1", "", "", "", false},
	}
	for _, tc := range cases {
		target, err := resolveSkillImportURL(tc.raw)
		if tc.ok {
			if err != nil || target.Kind != tc.kind || target.URL != tc.wantURL || target.SubdirHint != tc.hint {
				t.Fatalf("raw=%s target=%+v err=%v", tc.raw, target, err)
			}
		} else if err == nil {
			t.Fatalf("raw=%s accepted: %+v", tc.raw, target)
		}
	}
}

// multipartSkillArchive builds a POST body carrying one zip file.
func multipartSkillArchive(t *testing.T, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
}

func decodePreview(t *testing.T, rec *httptest.ResponseRecorder) skillImportPreview {
	t.Helper()
	var preview skillImportPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return preview
}

func importDeps(t *testing.T) Deps {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "skill-import.db"))
	uploadDir := t.TempDir()
	t.Cleanup(func() { db.Close() })
	return Deps{DB: db, Config: config.Config{UploadDir: uploadDir}}
}

// TestImportSkillArchiveEndToEnd walks the full runtime read path: import →
// preview → createSkillAdmin save → model binding → SkillsForModel lookup →
// assets resolvable through validateSkillAssets (what the sandbox stager runs).
func TestImportSkillArchiveEndToEnd(t *testing.T) {
	d := importDeps(t)
	data := buildZip(t, map[string]string{
		"pdf-tools-main/SKILL.md":         sampleSkillMD,
		"pdf-tools-main/scripts/build.py": "print('build')",
	})

	body, contentType := multipartSkillArchive(t, "pdf-tools.zip", data)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-archive", body)
	req.Header.Set("content-type", contentType)
	rec := httptest.NewRecorder()
	importSkillArchiveAdmin(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("import=%d %s", rec.Code, rec.Body.String())
	}
	preview := decodePreview(t, rec)
	if preview.Name != "pdf-tools" || preview.Instructions == "" || len(preview.Assets) != 1 {
		t.Fatalf("preview=%+v", preview)
	}

	// The staged asset file really exists where the descriptor claims.
	asset := preview.Assets[0]
	if info, err := os.Stat(asset.StoragePath); err != nil || info.Size() != int64(len("print('build')")) {
		t.Fatalf("asset stat err=%v info=%v path=%s", err, info, asset.StoragePath)
	}

	// Save via the normal admin endpoint (validateSkillAssets re-check).
	assetsJSON, _ := json.Marshal(preview.Assets)
	saveBody := map[string]any{
		"name":         preview.Name,
		"description":  preview.Description,
		"instructions": preview.Instructions,
		"assets":       json.RawMessage(assetsJSON),
	}
	raw, _ := json.Marshal(saveBody)
	req = httptest.NewRequest(http.MethodPost, "/api/admin/skills", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	rec = httptest.NewRecorder()
	createSkillAdmin(d, rec, req)
	if rec.Code != 201 {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}

	saved, err := store.GetSkillByName(t.Context(), d.DB, "pdf-tools")
	if err != nil || saved == nil {
		t.Fatalf("saved skill err=%v", err)
	}
	mustExec(t, d.DB, `INSERT INTO channels(id,name,type,base_url,api_key,enabled) VALUES('ch-import','Import','openai','https://example.test','k',1)`)
	mustExec(t, d.DB, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('m-import','ch-import','chat','gpt-import','Import test',1,'')`)
	if err := store.SetSkillsForModel(t.Context(), d.DB, "m-import", []string{saved.ID}); err != nil {
		t.Fatal(err)
	}
	ids, err := store.SkillsForModel(t.Context(), d.DB, "m-import")
	if err != nil || len(ids) != 1 || ids[0] != saved.ID {
		t.Fatalf("binding=%v err=%v", ids, err)
	}

	// The stager resolves assets exactly like validateSkillAssets does.
	rows, err := store.ListSkillAssets(t.Context(), d.DB, saved.ID)
	if err != nil || len(rows) != 1 || rows[0].Filename != "scripts/build.py" {
		t.Fatalf("listed=%+v err=%v", rows, err)
	}
}

func TestImportSkillArchiveRejectsDupNameAndBundled(t *testing.T) {
	d := importDeps(t)
	if _, err := store.CreateSkill(t.Context(), d.DB, store.Skill{Name: "pdf-tools", Description: "d", Instructions: "i", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartSkillArchive(t, "pdf-tools.zip", buildZip(t, map[string]string{
		"wrap/SKILL.md": sampleSkillMD,
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-archive", body)
	req.Header.Set("content-type", contentType)
	rec := httptest.NewRecorder()
	importSkillArchiveAdmin(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("import=%d %s", rec.Code, rec.Body.String())
	}
	if !decodePreview(t, rec).NameExists {
		t.Fatal("name_exists not flagged")
	}

	body, contentType = multipartSkillArchive(t, "multi.zip", buildZip(t, map[string]string{
		"one/SKILL.md": sampleSkillMD,
		"two/SKILL.md": sampleSkillMD,
	}))
	req = httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-archive", body)
	req.Header.Set("content-type", contentType)
	rec = httptest.NewRecorder()
	importSkillArchiveAdmin(d, rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "multiple") {
		t.Fatalf("bundled=%d %s", rec.Code, rec.Body.String())
	}
}

func TestImportSkillRemoteAdmin(t *testing.T) {
	d := importDeps(t)
	d.SkillImportHTTPClient = &http.Client{}

	// A plain zip direct link must contain the skill at the archive root
	// (optionally behind one wrapper directory) — deep monorepo layouts need
	// a GitHub tree URL so the server knows which directory to pick.
	served := buildZip(t, map[string]string{
		"pdf-tools-main/SKILL.md": sampleSkillMD,
		"pdf-tools-main/run.py":   "print(1)",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			w.Header().Set("content-type", "application/zip")
			_, _ = w.Write(served)
			return
		}
		w.Header().Set("content-type", "text/markdown")
		_, _ = w.Write([]byte(sampleSkillMD))
	}))
	defer server.Close()

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-remote", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		importSkillRemoteAdmin(d, rec, req)
		return rec
	}

	// Zip direct link.
	rec := post(`{"url":"` + server.URL + `/skill.zip"}`)
	if rec.Code != 200 {
		t.Fatalf("remote zip=%d %s", rec.Code, rec.Body.String())
	}
	preview := decodePreview(t, rec)
	if preview.Name != "pdf-tools" || len(preview.Assets) != 1 {
		t.Fatalf("preview=%+v", preview)
	}
	if _, err := os.Stat(preview.Assets[0].StoragePath); err != nil {
		t.Fatalf("asset missing: %v", err)
	}

	// Markdown direct link: text body, no assets.
	rec = post(`{"url":"` + server.URL + `/SKILL.md"}`)
	if rec.Code != 200 {
		t.Fatalf("remote md=%d %s", rec.Code, rec.Body.String())
	}
	if preview := decodePreview(t, rec); preview.Name != "pdf-tools" || len(preview.Assets) != 0 {
		t.Fatalf("md preview=%+v", preview)
	}

	// GitHub URL rewriting is exercised against the test server through a
	// resolve-only check (network fetch of github.com stays out of unit tests).
	target, err := resolveSkillImportURL("https://github.com/o/r/tree/main/sub/pdf")
	if err != nil || target.SubdirHint != "sub/pdf" {
		t.Fatalf("github resolve=%+v err=%v", target, err)
	}

	if rec := post(`{"url":"ftp://nope"}`); rec.Code != 400 {
		t.Fatalf("scheme gate=%d", rec.Code)
	}
}

// TestImportSkillRemoteSurfacesUpstreamErrors — an unreachable / erroring URL
// must come back as 400 whose message the admin can read (writeError masks
// everything >=500 behind "internal server error").
func TestImportSkillRemoteSurfacesUpstreamErrors(t *testing.T) {
	d := importDeps(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such release asset"))
	}))
	defer server.Close()

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-remote",
			strings.NewReader(`{"url":"`+server.URL+`/missing.zip"}`))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		importSkillRemoteAdmin(d, rec, req)
		return rec
	}

	// With the loopback server reachable (injected client): the upstream 404
	// status and body snippet must reach the admin.
	d.SkillImportHTTPClient = &http.Client{}
	rec := post()
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "HTTP 404") || !strings.Contains(rec.Body.String(), "no such release asset") {
		t.Fatalf("upstream error=%d %s", rec.Code, rec.Body.String())
	}

	// Without injection the SSRF-hardened client blocks the loopback dial; the
	// dial reason must still be visible instead of "internal server error".
	d.SkillImportHTTPClient = nil
	rec = post()
	if rec.Code != 400 || strings.Contains(rec.Body.String(), "internal server error") || !strings.Contains(rec.Body.String(), "fetch") {
		t.Fatalf("dial error=%d %s", rec.Code, rec.Body.String())
	}
}
