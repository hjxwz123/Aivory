package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestAdminSettingsKeysAreUniqueAndExcludeRetiredPurchasingSettings(t *testing.T) {
	seen := make(map[string]struct{}, len(settingsKeys))
	for _, key := range settingsKeys {
		if _, exists := seen[key]; exists {
			t.Fatalf("settingsKeys contains duplicate %q", key)
		}
		seen[key] = struct{}{}
	}

	for _, retired := range []string{
		"permanent_credit_purchase_credits",
		"permanent_credit_purchase_price_amount_minor",
		"group_buy_url",
		"credit_buy_url",
	} {
		if _, exists := seen[retired]; exists {
			t.Fatalf("retired setting %q is still exposed by the admin settings API", retired)
		}
	}

	for _, key := range []string{
		"rag_rerank_enabled",
		"rag_rerank_api_url",
		"rag_rerank_api_key",
		"rag_rerank_model",
	} {
		if _, exists := seen[key]; !exists {
			t.Fatalf("rerank setting %q is not exposed by the admin settings API", key)
		}
	}
}

func TestAdminSettingsReportsEffectiveSandboxAvailability(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "sandbox-availability.db"))
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	store.InvalidateConfig()

	readConfigured := func(d Deps) bool {
		t.Helper()
		rec := httptest.NewRecorder()
		adminSettingsGet(d, rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var configured bool
		if err := json.Unmarshal(response["sandbox_configured"], &configured); err != nil {
			t.Fatalf("decode sandbox_configured: %v", err)
		}
		return configured
	}

	if readConfigured(Deps{DB: db}) {
		t.Fatal("empty sandbox configuration reported available")
	}
	if !readConfigured(Deps{DB: db, Config: config.Config{SandboxBaseURL: "http://sandbox.internal"}}) {
		t.Fatal("boot-time SANDBOX_BASE_URL was not reported available")
	}
	if err := store.SetSetting(db, "sandbox_base_url", "https://sandbox.example.test"); err != nil {
		t.Fatal(err)
	}
	if !readConfigured(Deps{DB: db}) {
		t.Fatal("admin sandbox URL was not reported available")
	}
}

func TestSearchEngineSettingNormalizesAndRejectsUnsafeNames(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "search-engines-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	if rec := patch(`{"search_engines":" Bing, ddg bing wikipedia "}`); rec.Code != http.StatusOK {
		t.Fatalf("valid engine selection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, "search_engines").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `"bing,ddg,wikipedia"` {
		t.Fatalf("normalized search_engines = %q", stored)
	}

	if rec := patch(`{"search_engines":"bing,duck/duckgo"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsafe engine selection status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, "search_engines").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `"bing,ddg,wikipedia"` {
		t.Fatalf("invalid patch changed search_engines to %q", stored)
	}
}

func TestRerankSettingsSeedDisabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "rerank-seed-settings.db"))
	defer db.Close()
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"rag_rerank_enabled": "false",
		"rag_rerank_api_url": `""`,
		"rag_rerank_api_key": `""`,
		"rag_rerank_model":   `""`,
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestAdminRerankSettingsValidateNormalizeAndMask(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "rerank-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}
	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	for key, value := range map[string]string{
		"rag_rerank_enabled": `"true"`,
		"rag_rerank_api_url": `123`,
		"rag_rerank_api_key": `false`,
		"rag_rerank_model":   `{"name":"reranker"}`,
	} {
		rec := patch(`{"` + key + `":` + value + `}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted invalid type: status=%d body=%s", key, rec.Code, rec.Body.String())
		}
	}
	for _, baseURL := range []string{
		"https://rerank.example.com",
		"https://rerank.example.com/v1/rerank",
		"ftp://rerank.example.com/v1",
		"/v1",
		"https://rerank.example.com/v1?tenant=one",
	} {
		rec := patch(`{"rag_rerank_api_url":` + string(mustJSON(t, baseURL)) + `}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("base URL %q: status=%d body=%s", baseURL, rec.Code, rec.Body.String())
		}
	}
	if rec := patch(`{"rag_rerank_enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("incomplete enable status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := patch(`{
		"rag_rerank_enabled":true,
		"rag_rerank_api_url":"  https://rerank.example.com/openai/v1/  ",
		"rag_rerank_api_key":"  secret-token  ",
		"rag_rerank_model":"  BAAI/bge-reranker-v2-m3  "
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid settings status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var masked string
	if err := json.Unmarshal(response["rag_rerank_api_key"], &masked); err != nil || masked != "••••••" {
		t.Fatalf("masked API key = %q, err=%v", masked, err)
	}
	for key, want := range map[string]string{
		"rag_rerank_api_url": `"https://rerank.example.com/openai/v1"`,
		"rag_rerank_api_key": `"secret-token"`,
		"rag_rerank_model":   `"BAAI/bge-reranker-v2-m3"`,
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}

	// A masked secret is a no-op, while another field in the same partial PATCH
	// is validated against the already-stored enabled configuration.
	if rec := patch(`{"rag_rerank_api_key":"••••••","rag_rerank_model":"  reranker-v2  "}`); rec.Code != http.StatusOK {
		t.Fatalf("partial patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	for key, want := range map[string]string{
		"rag_rerank_api_key": `"secret-token"`,
		"rag_rerank_model":   `"reranker-v2"`,
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s after partial patch = %q, want %q", key, got, want)
		}
	}
}

func TestAdminRerankSettingsRejectInvalidEffectivePatchAtomically(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "rerank-atomic-settings.db"))
	defer db.Close()
	d := Deps{DB: db}
	for key, value := range map[string]any{
		"rag_rerank_enabled": true,
		"rag_rerank_api_url": "https://rerank.example.com/v1",
		"rag_rerank_model":   "reranker-v1",
	} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"rag_rerank_api_url":"https://replacement.example.com/v1",
		"rag_rerank_model":"  "
	}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for key, want := range map[string]string{
		"rag_rerank_api_url": `"https://rerank.example.com/v1"`,
		"rag_rerank_model":   `"reranker-v1"`,
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("failed patch changed %s to %q, want %q", key, got, want)
		}
	}

	// Disabling and clearing the independent service in one PATCH is valid.
	req = httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"rag_rerank_enabled":false,
		"rag_rerank_api_url":"",
		"rag_rerank_model":""
	}`))
	req.Header.Set("content-type", "application/json")
	rec = httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminSettingsRejectNegativeNumericValues(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "negative-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for _, key := range []string{
		"keep_recent_rounds",
		"summary_max_tokens",
		"summary_merge_max_tokens",
		"compaction_request_max_tokens",
		"compaction_token_trigger",
		"compaction_token_cap",
		"compaction_token_target_percentage",
		"daily_message_limit",
		"daily_image_limit",
		"daily_token_limit",
		"max_concurrent_generations",
		"register_ip_daily_limit",
		"fallback_ttft_sec",
		"credits_per_usd",
		"storage_archive_ttl_days",
	} {
		t.Run(key, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(fmt.Sprintf(`{"%s":-1}`, key)))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestAdminSettingsRejectOutOfRangeCompactionPercentages(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-percentage-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for key, values := range map[string][]int{
		"summary_target_percent":             {4, 81},
		"compaction_retention_percentage":    {9, 51},
		"compaction_token_target_percentage": {24, 81},
	} {
		for _, value := range values {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(fmt.Sprintf(`{"%s":%d}`, key, value)))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s=%d: status = %d, want %d; body=%s", key, value, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		}
	}
}

func TestAdminSettingsRejectCompactionValuesThatRuntimeWouldReplace(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-runtime-clamp-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for key, values := range map[string][]int{
		"keep_recent_rounds":            {0},
		"summary_max_tokens":            {0, 255},
		"summary_merge_max_tokens":      {0, 255},
		"compaction_request_max_tokens": {0, 8191},
	} {
		for _, value := range values {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(fmt.Sprintf(`{"%s":%d}`, key, value)))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s=%d: status = %d, want %d; body=%s", key, value, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		}
	}
}

func TestAdminSettingsAcceptCompactionConfiguration(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}
	channel, err := store.CreateChannel(context.Background(), db, "Summary", "openai", "chat", "https://example.invalid/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(context.Background(), db, store.Model{
		ID: "summary-model", ChannelID: channel.ID, Kind: "chat", RequestID: "summary-model", Label: "Summary", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"context_compaction_model_id":"summary-model",
		"compaction_token_trigger":32000,
		"compaction_token_cap":80000,
		"compaction_token_target_percentage":60,
		"compaction_retention_percentage":40,
		"summary_max_tokens":8192,
		"summary_target_percent":35,
		"summary_merge_max_tokens":8192,
		"compaction_request_max_tokens":32768,
		"context_compaction_prompt":"Preserve decisions and pending work."
	}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	for key, want := range map[string]string{
		"context_compaction_model_id":        `"` + model.ID + `"`,
		"compaction_token_cap":               "80000",
		"compaction_token_target_percentage": "60",
		"compaction_retention_percentage":    "40",
		"summary_target_percent":             "35",
		"summary_merge_max_tokens":           "8192",
		"compaction_request_max_tokens":      "32768",
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestAdminSettingsRejectNonIntegerCompactionRequestBudgetAtomically(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-request-budget-type.db"))
	defer db.Close()
	d := Deps{DB: db}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]string{
		"float":   `8192.5`,
		"string":  `"32768"`,
		"boolean": `true`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"keep_recent_rounds":9,"compaction_request_max_tokens":`+value+`}`))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var raw string
			if err := db.QueryRow(`SELECT value FROM settings WHERE key='keep_recent_rounds'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw != "6" {
				t.Fatalf("failed patch partially changed keep_recent_rounds to %q", raw)
			}
		})
	}
}

func TestAdminSettingsCompactionPatchIsAtomic(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-atomic-settings.db"))
	defer db.Close()
	d := Deps{DB: db}
	if err := store.SetSetting(db, "keep_recent_rounds", 6); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"keep_recent_rounds":9,
		"context_compaction_prompt":123
	}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='keep_recent_rounds'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "6" {
		t.Fatalf("failed patch partially changed keep_recent_rounds to %q", raw)
	}
}

func TestAdminSettingsValidateContextCompactionPrompt(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-prompt-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for name, value := range map[string]string{
		"number":    `123`,
		"object":    `{"instruction":"summarize"}`,
		"oversized": string(mustJSON(t, strings.Repeat("界", contextCompactionPromptMaxBytes/3+1))),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"context_compaction_prompt":`+value+`}`))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}

	boundary := strings.Repeat("x", contextCompactionPromptMaxBytes)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"context_compaction_prompt":`+string(mustJSON(t, "  "+boundary+"  "))+`}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("boundary status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var stored string
	if raw, err := store.GetSetting(db, "context_compaction_prompt"); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored != boundary {
		t.Fatalf("stored prompt length = %d, want trimmed boundary length %d", len(stored), len(boundary))
	}
}

func TestAdminSettingsValidateContextCompactionModel(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "compaction-model-settings.db"))
	defer db.Close()
	d := Deps{DB: db}
	ctx := context.Background()
	enabledChannel, err := store.CreateChannel(ctx, db, "Enabled", "openai", "chat", "https://example.invalid/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	disabledChannel, err := store.CreateChannel(ctx, db, "Disabled", "anthropic", "", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	unsupportedChannel, err := store.CreateChannel(ctx, db, "Unsupported", "unsupported", "", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	createModel := func(id, kind, channelID string, enabled bool) *store.Model {
		t.Helper()
		model, createErr := store.CreateModel(ctx, db, store.Model{
			ID: id, ChannelID: channelID, Kind: kind, RequestID: id, Label: id, Enabled: enabled,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return model
	}
	valid := createModel("summary-valid", "chat", enabledChannel.ID, true)
	disabledModel := createModel("summary-disabled", "chat", enabledChannel.ID, false)
	embeddingModel := createModel("summary-embedding", "embedding", enabledChannel.ID, true)
	disabledChannelModel := createModel("summary-channel-disabled", "chat", disabledChannel.ID, true)
	unsupportedChannelModel := createModel("summary-channel-unsupported", "chat", unsupportedChannel.ID, true)
	if _, err := db.Exec(`UPDATE channels SET enabled=0 WHERE id=?`, disabledChannel.ID); err != nil {
		t.Fatal(err)
	}

	set := func(value string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"context_compaction_model_id":`+value+`}`))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}
	if rec := set(string(mustJSON(t, "  "+valid.ID+"  "))); rec.Code != http.StatusOK {
		t.Fatalf("valid model status = %d; body=%s", rec.Code, rec.Body.String())
	}

	for name, value := range map[string]string{
		"non-string":          `123`,
		"missing":             `"missing-model"`,
		"disabled model":      string(mustJSON(t, disabledModel.ID)),
		"non-chat model":      string(mustJSON(t, embeddingModel.ID)),
		"disabled channel":    string(mustJSON(t, disabledChannelModel.ID)),
		"unsupported channel": string(mustJSON(t, unsupportedChannelModel.ID)),
	} {
		t.Run(name, func(t *testing.T) {
			rec := set(value)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			var stored string
			raw, err := store.GetSetting(db, "context_compaction_model_id")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			if stored != valid.ID {
				t.Fatalf("invalid patch changed model to %q, want %q", stored, valid.ID)
			}
		})
	}

	if rec := set(`""`); rec.Code != http.StatusOK {
		t.Fatalf("inherit status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAdminSettingsRejectInvalidStorageArchiveTTL(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "invalid-storage-ttl-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for _, value := range []string{`"not-a-number"`, `"1.5"`} {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"storage_archive_ttl_days":`+value+`}`))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("value %s: status = %d, want %d; body=%s", value, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
}

func TestAdminSettingsNormalizesLegacyStorageArchiveTTLString(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "storage-ttl-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"storage_archive_ttl_days":"45"}`))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='storage_archive_ttl_days'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "45" {
		t.Fatalf("stored TTL = %q, want normalized integer 45", raw)
	}
}
