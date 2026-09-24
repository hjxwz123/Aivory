package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// docmeeStubTransport serves the Docmee surface these tests need. API-mode
// behaviour (types, streaming, mirroring) is covered in aippt_handlers_test.go.
type docmeeStubTransport struct {
	t       *testing.T
	payload string
	status  int
}

func (s docmeeStubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	switch r.URL.Path {
	case "/api/user/createApiToken":
		return stubResponse(200, `{"code":0,"message":"ok","data":{"token":"tok_stub","expireTime":7200}}`), nil
	case "/api/ppt/v2/options":
		body := s.payload
		if body == "" {
			body = `{"code":0,"message":"ok","data":{"lang":[{"name":"简体中文","value":"zh"}]}}`
		}
		return stubResponse(status, body), nil
	default:
		s.t.Logf("unexpected upstream path %s", r.URL.Path)
		return stubResponse(404, `{"code":404,"message":"not found"}`), nil
	}
}

func stubResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

type docmeeFixture struct {
	deps Deps
	db   *sql.DB
	user *store.User
}

func docmeeTestDeps(t *testing.T, allowance float64, settings map[string]any) docmeeFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db,
		`INSERT INTO user_groups(id,name,is_default,is_public,credit_allowance,credit_period_seconds) VALUES('ug_free','Free',1,1,?,86400)`,
		allowance)
	mustExec(t, db,
		`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor) VALUES('u1','docmee@example.test','hash','ug_free',?)`,
		time.Now().Unix()-60)
	// The settings cache is process-global and keyed by setting name, not by
	// database, so each fixture must drop what a previous test cached.
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	for key, value := range settings {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	store.InvalidateConfig()
	return docmeeFixture{
		deps: Deps{
			DB:               db,
			Cache:            cache.NewMemory(),
			DocmeeHTTPClient: &http.Client{Transport: docmeeStubTransport{t: t}},
			Config:           config.Config{UploadDir: t.TempDir()},
		},
		db:   db,
		user: &store.User{ID: "u1", GroupID: "ug_free"},
	}
}

func docmeeRequest(t *testing.T, fixture docmeeFixture, method, path string, body any) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, fixture.user))
	return httptest.NewRecorder(), req
}

// docmeeBillingSettings is the minimal configuration that enables the feature and
// per-deck billing: an upstream key plus the platform-wide credit rate.
func docmeeBillingSettings(extra map[string]any) map[string]any {
	settings := map[string]any{
		"docmee_api_key":         "sk_test_key",
		"credits_per_usd":        100.0,
		"docmee_credits_per_ppt": 10.0,
	}
	for key, value := range extra {
		settings[key] = value
	}
	return settings
}

func TestDocmeeConfigReportsPricingWithoutSecrets(t *testing.T) {
	fixture := docmeeTestDeps(t, 40, docmeeBillingSettings(map[string]any{
		"docmee_edit_credits":        2.0,
		"docmee_max_upload_mb":       25,
		"docmee_default_template_id": "tpl_default",
	}))
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/config", nil)
	meDocmeeConfigHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != true || body["configured"] != true {
		t.Fatalf("flags = %v, want enabled and configured", body)
	}
	if body["credits_per_ppt"] != float64(10) || body["credits_available"] != float64(40) {
		t.Fatalf("pricing = %v/%v, want 10/40", body["credits_per_ppt"], body["credits_available"])
	}
	if body["edit_credits"] != float64(2) || body["edit_credits_enabled"] != true {
		t.Fatalf("edit pricing = %v/%v, want 2/true", body["edit_credits"], body["edit_credits_enabled"])
	}
	if body["max_upload_mb"] != float64(25) || body["default_template_id"] != "tpl_default" {
		t.Fatalf("config = %v/%v, want the upload cap and default template", body["max_upload_mb"], body["default_template_id"])
	}
	// The editor hand-off needs the SDK location; the retired iframe-creation knobs
	// (API proxy base, creator version) are gone.
	if body["sdk_url"] == nil || body["sdk_url"] == "" {
		t.Fatalf("config is missing the editor SDK URL: %s", rec.Body.String())
	}
	for _, retired := range []string{"sdk_base_url", "creator_version"} {
		if _, present := body[retired]; present {
			t.Fatalf("config still exposes retired field %q: %s", retired, rec.Body.String())
		}
	}
	for _, secret := range []string{"api_key", "docmee_api_key", "api_secret", "token"} {
		if _, present := body[secret]; present {
			t.Fatalf("config leaked %q: %s", secret, rec.Body.String())
		}
	}
}

func TestDocmeeRequiresConfigurationAndHonoursTheOffSwitch(t *testing.T) {
	// No key at all: the feature is unconfigured, and every entry point reports it
	// with a typed code instead of a generic 5xx.
	fixture := docmeeTestDeps(t, 100, map[string]any{"credits_per_usd": 100.0})
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/options", nil)
	meAiPPTOptionsHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("options status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("options body = %s, want a configuration hint", rec.Body.String())
	}

	// Key present but explicitly disabled: 503 with the disabled message.
	disabled := docmeeTestDeps(t, 100, map[string]any{
		"docmee_api_key":  "sk_test_key",
		"docmee_enabled":  false,
		"credits_per_usd": 100.0,
	})
	rec, req = docmeeRequest(t, disabled, http.MethodGet, "/api/me/ppt/options", nil)
	meAiPPTOptionsHandler(disabled.deps, rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("disabled body = %s, want the disabled message", rec.Body.String())
	}
}

// The vendor answers HTTP 200 with a non-zero `code`; that must become a typed
// 502 without echoing the vendor's message (it can quote the configured key).
func TestDocmeeUpstreamCodeBecomesTypedErrorWithoutLeaking(t *testing.T) {
	fixture := docmeeTestDeps(t, 100, docmeeBillingSettings(nil))
	fixture.deps.DocmeeHTTPClient = &http.Client{Transport: docmeeStubTransport{
		t:       t,
		status:  http.StatusOK,
		payload: `{"code":1010,"message":"Request method 'POST' not supported (key sk_test_key)"}`,
	}}
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/options", nil)
	meAiPPTOptionsHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_code") {
		t.Fatalf("body = %s, want the upstream code surfaced", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk_test_key") || strings.Contains(rec.Body.String(), "not supported") {
		t.Fatalf("body leaked upstream detail: %s", rec.Body.String())
	}
}

// The admin UI saves the whole Docmee block in one PATCH and then reads it back.
// A round trip must keep the key (masked on read) and the chosen enable flag.
func TestDocmeeAdminSettingsRoundTripKeepsTheIntegrationEnabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	d := Deps{DB: db}

	write := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	rec := write(`{
		"credits_per_usd": 100,
		"docmee_enabled": true,
		"docmee_api_key": "sk_live_key",
		"docmee_credits_per_ppt": 10,
		"docmee_edit_credits": 1,
		"docmee_default_template_id": "tpl_default",
		"docmee_max_upload_mb": 20,
		"docmee_api_base_url": "https://docmee.cn",
		"docmee_token_hours": 2
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings PATCH status = %d; body=%s", rec.Code, rec.Body.String())
	}
	cfg := docmeeConfigFor(d)
	if !cfg.Enabled || cfg.APIKey != "sk_live_key" || cfg.CreditsPerPPT != 10 ||
		cfg.EditCredits != 1 || cfg.DefaultTemplateID != "tpl_default" || cfg.MaxUploadMB != 20 {
		t.Fatalf("resolved config = %+v, want the saved values", cfg)
	}

	getRec := httptest.NewRecorder()
	adminSettingsGet(d, getRec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("settings GET status = %d", getRec.Code)
	}
	var stored map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if stored["docmee_enabled"] != true {
		t.Fatalf("reloaded docmee_enabled = %v, want true", stored["docmee_enabled"])
	}
	if stored["docmee_api_key"] != "••••••" {
		t.Fatalf("reloaded docmee_api_key = %v, want the display mask", stored["docmee_api_key"])
	}
	if stored["docmee_edit_credits"] != float64(1) || stored["docmee_max_upload_mb"] != float64(20) {
		t.Fatalf("reloaded edit/upload = %v/%v", stored["docmee_edit_credits"], stored["docmee_max_upload_mb"])
	}

	// Echoing the mask back (what the UI does) must not clear the key.
	rec = write(`{"docmee_enabled": true, "docmee_api_key": "••••••"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("masked re-save status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); !cfg.Enabled || cfg.APIKey != "sk_live_key" {
		t.Fatalf("config after masked re-save = %+v, want unchanged key + enabled", cfg)
	}

	// Clearing the key turns the feature off even with the flag left true.
	rec = write(`{"docmee_api_key": ""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear-key status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); cfg.configured() {
		t.Fatalf("config after clearing the key = %+v, want unconfigured", cfg)
	}
}

// Admin-entered URLs are normalized rather than rejected, because a rejected
// PATCH would also silently drop the enable flag saved in the same request.
func TestDocmeeAdminSettingsNormalizesURLs(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee-urls.db"))
	t.Cleanup(func() { _ = db.Close() })
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	d := Deps{DB: db}

	write := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	rec := write(`{"docmee_enabled": true, "docmee_api_key": "sk_live_key", "docmee_api_base_url": "docmee.cn"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bare-host PATCH status = %d; body=%s", rec.Code, rec.Body.String())
	}
	cfg := docmeeConfigFor(d)
	if cfg.APIBaseURL != "https://docmee.cn" {
		t.Fatalf("docmee_api_base_url = %q, want https://docmee.cn", cfg.APIBaseURL)
	}
	if !cfg.Enabled {
		t.Fatalf("enable flag did not survive the same PATCH: %+v", cfg)
	}

	rec = write(`{"docmee_api_base_url": "not a url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("junk URL status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); cfg.APIBaseURL != "https://docmee.cn" {
		t.Fatalf("docmee_api_base_url = %q, want the previous value left untouched", cfg.APIBaseURL)
	}
}
