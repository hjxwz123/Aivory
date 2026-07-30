package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestAnnouncementSeedIncludesOptionalTitle(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "announcement-seed.db"))
	defer db.Close()
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.InvalidateConfig()

	raw, err := store.GetSetting(db, "announcement")
	if err != nil {
		t.Fatalf("get announcement: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode seeded announcement: %v", err)
	}
	var title string
	if err := json.Unmarshal(got["title"], &title); err != nil {
		t.Fatalf("decode seeded title: %v", err)
	}
	if title != "" {
		t.Fatalf("seeded title = %q, want empty", title)
	}
}

func TestAnnouncementHandlerReturnsConfiguredTitle(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "announcement-title.db"))
	defer db.Close()
	d := Deps{DB: db}

	if err := store.SetSetting(db, "announcement", map[string]any{
		"enabled":          true,
		"title":            "Scheduled maintenance",
		"body":             "Service will be briefly unavailable.",
		"image_url":        "/api/icons/maintenance.png",
		"remember_dismiss": true,
		"updated_at":       int64(1234),
	}); err != nil {
		t.Fatalf("set announcement: %v", err)
	}

	got := readAnnouncement(t, d)
	if !got.Enabled || got.Title != "Scheduled maintenance" || got.Body != "Service will be briefly unavailable." {
		t.Fatalf("announcement = %+v", got)
	}
	if got.ImageURL != "/api/icons/maintenance.png" || !got.RememberDismiss || got.UpdatedAt != 1234 {
		t.Fatalf("announcement metadata = %+v", got)
	}
}

func TestAnnouncementHandlerAcceptsLegacyConfigWithoutTitle(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "announcement-legacy.db"))
	defer db.Close()
	d := Deps{DB: db}

	legacy := json.RawMessage(`{"enabled":true,"body":"Legacy notice","image_url":"","remember_dismiss":true,"updated_at":42}`)
	if err := store.SetSetting(db, "announcement", legacy); err != nil {
		t.Fatalf("set legacy announcement: %v", err)
	}

	got := readAnnouncement(t, d)
	if !got.Enabled || got.Title != "" || got.Body != "Legacy notice" || !got.RememberDismiss || got.UpdatedAt != 42 {
		t.Fatalf("legacy announcement = %+v", got)
	}
}

func TestAnnouncementHandlerReturnsBarWhenPopupIsDisabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "announcement-bar.db"))
	defer db.Close()
	d := Deps{DB: db}

	if err := store.SetSetting(db, "announcement", map[string]any{
		"enabled":        false,
		"title":          "Popup title",
		"bar_enabled":    true,
		"bar_html":       "Maintenance at 02:00",
		"bar_updated_at": int64(99),
	}); err != nil {
		t.Fatalf("set bar announcement: %v", err)
	}

	got := readAnnouncement(t, d)
	if got.Enabled || !got.BarEnabled || got.BarHTML != "Maintenance at 02:00" || got.BarUpdatedAt != 99 {
		t.Fatalf("bar announcement = %+v", got)
	}
	if got.Title != "Popup title" {
		t.Fatalf("title = %q, want %q", got.Title, "Popup title")
	}
}

func readAnnouncement(t *testing.T, d Deps) announcement {
	t.Helper()
	rec := httptest.NewRecorder()
	announcementHandler(d, rec, httptest.NewRequest(http.MethodGet, "/api/announcement", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got announcement
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode announcement: %v; body=%s", err, rec.Body.String())
	}
	return got
}
