package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillImportPreviewWarningsAreAlwaysAnArray(t *testing.T) {
	for _, kind := range []string{"archive", "markdown"} {
		t.Run(kind, func(t *testing.T) {
			d := importDeps(t)
			w := httptest.NewRecorder()
			if kind == "archive" {
				body, contentType := multipartSkillArchive(t, "skill.zip", buildZip(t, map[string]string{
					"SKILL.md":         sampleSkillMD,
					"scripts/build.py": "print('ok')",
				}))
				r := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-archive", body)
				r.Header.Set("content-type", contentType)
				importSkillArchiveAdmin(d, w, r)
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(sampleSkillMD))
				}))
				defer server.Close()
				d.SkillImportHTTPClient = server.Client()
				r := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import-remote",
					strings.NewReader(`{"url":"`+server.URL+`/SKILL.md"}`))
				r.Header.Set("content-type", "application/json")
				importSkillRemoteAdmin(d, w, r)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("import failed: %d %s", w.Code, w.Body.String())
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(response["warnings"], []byte("[]")) {
				t.Fatalf("warnings = %s, expected []", response["warnings"])
			}
		})
	}
}

func TestResolveSkillImportGitHubZIPDirectLinks(t *testing.T) {
	for _, input := range []string{
		"https://github.com/owner/skill/archive/refs/heads/main.zip",
		"https://github.com/owner/skill/archive/refs/tags/v1.0.0.zip?download=1",
		"https://www.github.com/owner/skill/archive/main.zip",
		"https://github.com/owner/skill/releases/download/v1.0.0/skill.zip",
		"https://github.com/owner/skill/releases/download/v1.0.0/skill%20bundle.ZIP?download=1",
	} {
		t.Run(input, func(t *testing.T) {
			target, err := resolveSkillImportURL(input)
			if err != nil {
				t.Fatalf("valid GitHub ZIP direct link rejected: %v", err)
			}
			if target.Kind != "zip" || target.URL != input || target.SubdirHint != "" {
				t.Fatalf("unexpected target: %+v", target)
			}
		})
	}
}
