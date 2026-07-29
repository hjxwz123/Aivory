package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
}

func TestAdminSettingsRejectNegativeNumericValues(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "negative-admin-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	for _, key := range []string{
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
