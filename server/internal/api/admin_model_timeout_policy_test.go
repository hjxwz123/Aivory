package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestModelTimeoutPolicyUsesGlobalSettingAndAllowsUnrelatedEditsWhenDisabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "global-ttft.db"))
	defer db.Close()
	channel, err := store.CreateChannel(t.Context(), db, "Global timeout", "openai", "chat", "https://provider.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(t.Context(), db, store.Model{ChannelID: channel.ID, RequestID: "global-timeout", Label: "Timeout model",
		Enabled: true, FallbackTTFTSec: 99, AutoDisableTimeouts: 2, AutoDisableMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) { updateModelAdmin(d, w, r) })
	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+model.ID, strings.NewReader(body)))
		return rec
	}
	if err := store.SetSetting(db, "fallback_ttft_sec", 0); err != nil {
		t.Fatal(err)
	}
	if rec := patch(`{"label":"Renamed","fallback_ttft_sec":200}`); rec.Code != http.StatusOK {
		t.Fatalf("ordinary edit with global TTFT disabled: %d %s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetModel(t.Context(), db, model.ID)
	if err != nil || updated.FallbackTTFTSec != 99 || updated.AutoDisableTimeouts != 2 || updated.Label != "Renamed" {
		t.Fatalf("legacy/timeout fields changed during ordinary edit: %+v, %v", updated, err)
	}
	if rec := patch(`{"auto_disable_timeouts":3,"fallback_ttft_sec":200}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("per-model TTFT incorrectly enabled timeout policy: %d %s", rec.Code, rec.Body.String())
	}
	if err := store.SetSetting(db, "fallback_ttft_sec", 8); err != nil {
		t.Fatal(err)
	}
	if rec := patch(`{"auto_disable_timeouts":3}`); rec.Code != http.StatusOK {
		t.Fatalf("timeout policy with global TTFT enabled: %d %s", rec.Code, rec.Body.String())
	}
	updated, err = store.GetModel(t.Context(), db, model.ID)
	if err != nil || updated.AutoDisableTimeouts != 3 {
		t.Fatalf("timeout trigger was not saved: %+v, %v", updated, err)
	}
}
