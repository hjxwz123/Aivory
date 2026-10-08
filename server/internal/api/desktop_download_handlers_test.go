package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestDesktopDownloadConfigurationValidation(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":false}`, `{"enabled":false,"url":""}`,
		`{"enabled":true,"url":" https://example.test/download#desktop "}`,
		`{"enabled":true,"url":"http://localhost:5173/app.exe?token=signed"}`,
	} {
		if _, err := normalizeDesktopDownloadConfig(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid download config rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `{"enabled":true}`, `{"enabled":true,"url":" "}`,
		`{"url":"javascript:alert(1)"}`, `{"url":"file:///app.dmg"}`,
		`{"url":"https://user:secret@example.test/download"}`, `{"url":"//example.test/download"}`,
		`{"url":"https://"}`, `{"enabled":"true"}`,
	} {
		if _, err := normalizeDesktopDownloadConfig(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid download config accepted: %s", raw)
		}
	}
}

func TestDesktopDownloadRoutesVisibilityAndIndependentPublication(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	router := NewRouter(d)
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	request := func(method, address, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, address, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	const publicPath = "/api/public/desktop-download"
	if w := request("GET", publicPath, "", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
		t.Fatalf("unconfigured download entry: %d %s", w.Code, w.Body.String())
	}
	const draft = `{"desktop_download":{"enabled":false,"url":"https://example.test/download#desktop"}}`
	for _, token := range []string{"", userToken} {
		if w := request("PATCH", "/api/admin/settings", draft, token); w.Code != 401 && w.Code != 403 {
			t.Fatalf("non-admin saved download entry: %d", w.Code)
		}
	}
	if w := request("PATCH", "/api/admin/settings", draft, adminToken); w.Code != 200 {
		t.Fatalf("save download draft: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", publicPath, "", ""); strings.Contains(w.Body.String(), "example.test") {
		t.Fatal("disabled download URL exposed")
	}
	published := strings.Replace(draft, `"enabled":false`, `"enabled":true`, 1)
	if w := request("PATCH", "/api/admin/settings", published, adminToken); w.Code != 200 {
		t.Fatalf("publish download entry: %d %s", w.Code, w.Body.String())
	}
	w := request("GET", publicPath, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://example.test/download#desktop") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("published entry: %d %s", w.Code, w.Body.String())
	}
	if readDesktopUpdateConfig(d).Enabled {
		t.Fatal("download entry incorrectly enabled desktop update publication")
	}
	if w := request("GET", "/api/public/desktop-update", "", ""); strings.Contains(w.Body.String(), "example.test") {
		t.Fatal("download entry leaked into update manifest")
	}
	invalid := `{"desktop_download":{"enabled":true,"url":"javascript:alert(1)"},"contact_email":"changed@example.test"}`
	if w := request("PATCH", "/api/admin/settings", invalid, adminToken); w.Code != 400 {
		t.Fatalf("invalid download link persisted: %d", w.Code)
	}
	if w := request("GET", publicPath, "", ""); !strings.Contains(w.Body.String(), "https://example.test/download#desktop") {
		t.Fatal("invalid patch replaced existing download URL")
	}
	if raw, _ := store.GetSetting(d.DB, "contact_email"); strings.Contains(string(raw), "changed@example.test") {
		t.Fatal("invalid download patch partially wrote other settings")
	}
	if w := request("PATCH", "/api/admin/settings", draft, adminToken); w.Code != 200 {
		t.Fatalf("hide download entry: %d", w.Code)
	}
	if w := request("GET", publicPath, "", ""); strings.Contains(w.Body.String(), "example.test") {
		t.Fatal("hidden download URL still exposed")
	}
	if err := store.SetSetting(d.DB, desktopDownloadSettingKey, map[string]any{"enabled": true, "url": "file:///app"}); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", publicPath, "", ""); strings.TrimSpace(w.Body.String()) != `{"enabled":false}` {
		t.Fatal("invalid legacy download configuration exposed")
	}
}
