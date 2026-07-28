package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestLegalConfigPublicDefaultsAndOverrides(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "legal-config.db"))
	defer db.Close()
	d := Deps{DB: db}

	read := func(t *testing.T) publicLegalConfig {
		t.Helper()
		store.InvalidateConfig()
		rec := httptest.NewRecorder()
		legalConfigPublicHandler(d, rec, httptest.NewRequest(http.MethodGet, "/api/public/legal-config", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw response: %v", err)
		}
		if len(raw) != 3 {
			t.Fatalf("public legal response exposed unexpected fields: %s", rec.Body.String())
		}
		var got publicLegalConfig
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return got
	}

	t.Run("defaults", func(t *testing.T) {
		got := read(t)
		if got.ContactEmail != defaultContactEmail || got.TermsText != "" || got.PrivacyText != "" {
			t.Fatalf("defaults = %+v", got)
		}
	})

	t.Run("configured", func(t *testing.T) {
		for key, value := range map[string]string{
			"contact_email": "support@example.test",
			"terms_text":    "# Custom terms",
			"privacy_text":  "# Custom privacy",
		} {
			if err := store.SetSetting(db, key, value); err != nil {
				t.Fatalf("set %s: %v", key, err)
			}
		}
		got := read(t)
		if got.ContactEmail != "support@example.test" || got.TermsText != "# Custom terms" || got.PrivacyText != "# Custom privacy" {
			t.Fatalf("configured response = %+v", got)
		}
	})
}

func TestAdminLegalSettingsValidation(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "legal-settings-validation.db"))
	defer db.Close()
	d := Deps{DB: db}

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "invalid email", body: `{"contact_email":"not-an-email"}`},
		{name: "oversized terms", body: `{"terms_text":"` + strings.Repeat("x", legalPolicyTextMaxBytes+1) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(tc.body))
			req.Header.Set("content-type", "application/json")
			rec := httptest.NewRecorder()
			adminSettingsSet(d, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}
