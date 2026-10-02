package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestDocmeeUSDPricesConvertAtTheCreditRate(t *testing.T) {
	f := aipptTestDeps(t, 0, map[string]any{
		"docmee_price_per_ppt_usd": 0.2,
		"docmee_edit_price_usd":    0.05,
	})
	cfg := docmeeConfigFor(f.deps)
	// credits_per_usd = 100 in the fixture.
	if cfg.PricePerPPTUSD != 0.2 || math.Abs(cfg.CreditsPerPPT-20) > 1e-9 {
		t.Fatalf("deck price = $%v / %v credits, want $0.2 / 20", cfg.PricePerPPTUSD, cfg.CreditsPerPPT)
	}
	if cfg.EditPriceUSD != 0.05 || math.Abs(cfg.EditCredits-5) > 1e-9 {
		t.Fatalf("edit price = $%v / %v credits, want $0.05 / 5", cfg.EditPriceUSD, cfg.EditCredits)
	}
	if !cfg.billingEnabled(f.deps) || !cfg.editBillingEnabled(f.deps) {
		t.Fatal("priced deck and edit must be billed while credits are on")
	}

	rec, req := aipptReq(t, f, f.user, http.MethodGet, "/api/me/ppt/config", nil)
	meDocmeeConfigHandler(f.deps, rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["price_per_ppt_usd"] != 0.2 || body["credits_per_ppt"] != float64(20) ||
		body["edit_price_usd"] != 0.05 || body["edit_credits"] != float64(5) {
		t.Fatalf("config pricing = %v", body)
	}
}

func TestDocmeeLegacyCreditPricesKeepChargingUntilAUSDPriceIsSaved(t *testing.T) {
	// The fixture stores only the legacy docmee_credits_per_ppt = 10.
	f := aipptTestDeps(t, 0, map[string]any{"docmee_edit_credits": 3.0})
	cfg := docmeeConfigFor(f.deps)
	if cfg.CreditsPerPPT != 10 || math.Abs(cfg.PricePerPPTUSD-0.1) > 1e-12 {
		t.Fatalf("legacy deck price = %v credits / $%v, want 10 / $0.1", cfg.CreditsPerPPT, cfg.PricePerPPTUSD)
	}
	if cfg.EditCredits != 3 || math.Abs(cfg.EditPriceUSD-0.03) > 1e-12 {
		t.Fatalf("legacy edit price = %v credits / $%v, want 3 / $0.03", cfg.EditCredits, cfg.EditPriceUSD)
	}

	// A saved USD price wins over the legacy amount, including an explicit $0.
	if err := store.SetSetting(f.db, "docmee_price_per_ppt_usd", 0); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	cfg = docmeeConfigFor(f.deps)
	if cfg.CreditsPerPPT != 0 || cfg.billingEnabled(f.deps) {
		t.Fatalf("a saved $0 price must make decks free, got %v credits", cfg.CreditsPerPPT)
	}
}

func TestDocmeeUSDPriceIsFreeWhileCreditsAreOff(t *testing.T) {
	f := aipptTestDeps(t, 0, map[string]any{"credits_per_usd": 0, "docmee_price_per_ppt_usd": 0.2})
	cfg := docmeeConfigFor(f.deps)
	if cfg.CreditsPerPPT != 0 || cfg.billingEnabled(f.deps) {
		t.Fatalf("credits off: %v credits, billing=%v; want free", cfg.CreditsPerPPT, cfg.billingEnabled(f.deps))
	}
	if cfg.PricePerPPTUSD != 0.2 {
		t.Fatalf("the configured USD price must still be reported, got %v", cfg.PricePerPPTUSD)
	}
}

func TestAdminSettingsValidateUSDPrices(t *testing.T) {
	f := aipptTestDeps(t, 0, nil)
	for _, key := range []string{"audio_transcribe_price_per_second", "docmee_price_per_ppt_usd", "docmee_edit_price_usd"} {
		for body, want := range map[string]int{
			`{"` + key + `": 0.25}`:  http.StatusOK,
			`{"` + key + `": 0}`:     http.StatusOK,
			`{"` + key + `": -1}`:    http.StatusBadRequest,
			`{"` + key + `": "abc"}`: http.StatusBadRequest,
		} {
			req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
			rec := httptest.NewRecorder()
			adminSettingsSet(f.deps, rec, req)
			if rec.Code != want {
				t.Fatalf("%s → %d (%s), want %d", body, rec.Code, rec.Body.String(), want)
			}
		}
	}
}
