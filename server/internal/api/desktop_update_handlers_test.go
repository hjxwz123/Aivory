package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestDesktopUpdateConfigurationValidation(t *testing.T) {
	for _, input := range []string{
		`{"enabled":false,"version":"","downloads":{}}`,
		`{"enabled":true,"version":" v2.5.2-beta.1 ","downloads":{"macos_arm64":" https://cdn.example.test/download?id=abc "}}`,
		`{"enabled":false,"version":"2.5.2","downloads":{"windows_x64":"http://localhost:5173/installer.exe"}}`,
		`{"enabled":true,"source":"official","version":"2.5.2","downloads":{"macos_arm64":"https://github.com/hjxwz123/Aivory/releases/download/v2.5.2/Aivory-2.5.2-mac-arm64.dmg"}}`,
	} {
		if _, err := normalizeDesktopUpdateConfig(json.RawMessage(input)); err != nil {
			t.Fatalf("valid config rejected: %s: %v", input, err)
		}
	}
	for _, input := range []string{
		`null`, `[]`, `{"enabled":true}`, `{"enabled":true,"version":"2.5.2","downloads":{}}`,
		`{"enabled":false,"version":"latest"}`, `{"enabled":false,"version":"2.05.2"}`,
		`{"downloads":{"unknown":"https://cdn.example.test/installer"}}`,
		`{"downloads":{"macos_arm64":"javascript:alert(1)"}}`,
		`{"downloads":{"macos_arm64":"file:///installer.dmg"}}`,
		`{"downloads":{"macos_arm64":"https://user:secret@cdn.example.test/installer"}}`,
		`{"downloads":{"macos_arm64":"https://cdn.example.test/installer#redirect"}}`,
		`{"source":"unknown"}`,
		`{"source":"official","version":"2.5.2","downloads":{"macos_arm64":"https://cdn.example.test/installer.dmg"}}`,
		`{"source":"official","version":"2.5.2","downloads":{"macos_arm64":"https://github.com/hjxwz123/Aivory/releases/download/v2.5.1/Aivory-2.5.1-mac-arm64.dmg"}}`,
	} {
		if _, err := normalizeDesktopUpdateConfig(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid config accepted: %s", input)
		}
	}
}

func TestDesktopUpdateOfficialPackagesRequireCompletedAssets(t *testing.T) {
	const prefix = "https://github.com/hjxwz123/Aivory/releases/download/v2.5.2/"
	release := systemUpdateRelease{TagName: "v2.5.2", PublishedAt: "2026-10-08T00:00:00Z"}
	for _, name := range []string{"Aivory-2.5.2-mac-arm64.dmg", "Aivory-2.5.2-win-x64.exe", "Aivory-2.5.2-linux-x64.deb", "Aivory-2.5.2-linux-x64.AppImage"} {
		release.Assets = append(release.Assets, systemUpdateReleaseAsset{Name: name, URL: prefix + name, Size: 10, State: "uploaded"})
	}
	release.Assets = append(release.Assets,
		systemUpdateReleaseAsset{Name: "Aivory-2.5.2-mac-x64.dmg", URL: prefix + "Aivory-2.5.2-mac-x64.dmg", State: "uploaded"},
		systemUpdateReleaseAsset{Name: "Aivory-2.5.2-win-arm64.exe", URL: prefix + "Aivory-2.5.2-win-arm64.exe", Size: 10, State: "new"},
		systemUpdateReleaseAsset{Name: "Aivory-2.5.2-linux-arm64.AppImage", URL: "https://evil.example/Aivory-2.5.2-linux-arm64.AppImage", Size: 10, State: "uploaded"},
	)
	downloads := officialDesktopDownloads(release)
	if len(downloads) != 3 || downloads["linux_x64"] != prefix+"Aivory-2.5.2-linux-x64.AppImage" {
		t.Fatalf("incorrect official downloads: %+v", downloads)
	}
	release.Draft = true
	if len(officialDesktopDownloads(release)) != 0 {
		t.Fatal("draft installers exposed")
	}
	release.Draft, release.PublishedAt = false, ""
	if len(officialDesktopDownloads(release)) != 0 {
		t.Fatal("unpublished installers exposed")
	}
}

func TestDesktopUpdatePublicManifestAndSettingsRoutes(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	router := NewRouter(d)
	t.Cleanup(store.InvalidateConfig)
	store.InvalidateConfig()
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	const publicPath = "/api/public/desktop-update"
	if w := request("GET", publicPath, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("unconfigured manifest: %d %s", w.Code, w.Body.String())
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/admin/desktop-update"}, {"POST", "/api/admin/desktop-update/check"},
	} {
		if w := request(route.method, route.path, "", ""); w.Code != 401 {
			t.Fatalf("missing auth: %d %s", w.Code, w.Body.String())
		}
		if w := request(route.method, route.path, "", userToken); w.Code != 403 {
			t.Fatalf("user reached admin route: %d %s", w.Code, w.Body.String())
		}
	}
	const draft = `{"desktop_update":{"enabled":false,"version":"v2.5.2","downloads":{"macos_arm64":"https://cdn.example.test/aivory.dmg"}}}`
	if w := request("PATCH", "/api/admin/settings", draft, adminToken); w.Code != 200 {
		t.Fatalf("save draft: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", publicPath, "", ""); strings.Contains(w.Body.String(), "cdn.example.test") || strings.Contains(w.Body.String(), "2.5.2") {
		t.Fatal("unpublished package was exposed")
	}
	published := strings.Replace(draft, `"enabled":false`, `"enabled":true`, 1)
	if w := request("PATCH", "/api/admin/settings", published, adminToken); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	if err := store.SetSetting(d.DB, "sandbox_api_key", "private-secret"); err != nil {
		t.Fatal(err)
	}
	w := request("GET", publicPath, "", "")
	var cfg desktopUpdateConfig
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cfg) != nil || !cfg.Enabled || cfg.Version != "2.5.2" || cfg.Downloads["macos_arm64"] == "" {
		t.Fatalf("published manifest: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("public manifest leaked secrets or is cacheable")
	}
	invalid := `{"desktop_update":{"enabled":true,"version":"2.5.3","downloads":{"macos_arm64":"file:///bad"}},"contact_email":"changed@example.test"}`
	if w := request("PATCH", "/api/admin/settings", invalid, adminToken); w.Code != 400 {
		t.Fatalf("invalid config persisted: %d %s", w.Code, w.Body.String())
	}
	if got := readDesktopUpdateConfig(d); got.Version != "2.5.2" {
		t.Fatalf("invalid update replaced published config: %+v", got)
	}
	if raw, _ := store.GetSetting(d.DB, "contact_email"); strings.Contains(string(raw), "changed@example.test") {
		t.Fatal("failed config validation partially applied unrelated settings")
	}
	if w := request("PATCH", "/api/admin/settings", draft, adminToken); w.Code != 200 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", publicPath, "", ""); strings.Contains(w.Body.String(), "cdn.example.test") {
		t.Fatal("withdrawn installer is still exposed")
	}
	var audits int
	if err := d.DB.QueryRow(`SELECT count(*) FROM admin_audit_logs WHERE route='/api/admin/settings' AND result='success'`).Scan(&audits); err != nil || audits < 3 {
		t.Fatalf("publication changes lack audit evidence: count=%d err=%v", audits, err)
	}
}

func TestDesktopUpdateAdminNoticeUsesPublishedVersionAndReleaseChannel(t *testing.T) {
	d := newAuthSecurityDeps(t, "desktop-update-notice.db")
	d.AppVersion = "2.5.1-beta.6"
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.5.2","published_at":"2026-10-08T00:00:00Z"},
			{"tag_name":"v2.6.0-beta.1","prerelease":true,"published_at":"2026-10-08T00:00:00Z"},
			{"tag_name":"v9.0.0","draft":true,"published_at":"2026-10-08T00:00:00Z"}
		]`))
	}))
	defer upstream.Close()
	d.Config.ReleaseAPIURL = upstream.URL
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	state := buildDesktopUpdateAdminState(d, true)
	if state.LatestVersion != "2.6.0-beta.1" || !state.UpdateAvailable || len(state.Releases) != 2 {
		t.Fatalf("first configuration notice: %+v", state)
	}
	d.AppVersion = "2.6.0-beta.1"
	if state := buildDesktopUpdateAdminState(d, false); state.UpdateAvailable {
		t.Fatalf("missing config must not report an older or current server release as new: %+v", state)
	}
	cfg := desktopUpdateConfig{Enabled: true, Version: "2.5.2", Downloads: map[string]string{"windows_x64": "https://cdn.example.test/installer.exe"}}
	if err := store.SetSetting(d.DB, desktopUpdateSettingKey, cfg); err != nil {
		t.Fatal(err)
	}
	state = buildDesktopUpdateAdminState(d, false)
	if state.LatestVersion != "2.5.2" || state.UpdateAvailable {
		t.Fatalf("stable operator release must not receive beta notice: %+v", state)
	}
	if calls != 1 {
		t.Fatalf("admin checks did not reuse release cache: calls=%d", calls)
	}
	w := httptest.NewRecorder()
	desktopUpdatePublicHandler(d, w, httptest.NewRequest("GET", "/api/public/desktop-update", nil))
	if calls != 1 {
		t.Fatal("public desktop polling made an upstream request")
	}
}
