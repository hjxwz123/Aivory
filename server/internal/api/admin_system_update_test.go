package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
)

func TestSystemUpdateVersionValidation(t *testing.T) {
	for _, valid := range []string{"0.1.0", "2.4.8", "12.0.301", "2.4.9-beta.1", "3.0.0-rc.2"} {
		if !validSystemUpdateVersion(valid) {
			t.Fatalf("expected %q to be valid", valid)
		}
	}
	for _, invalid := range []string{"v2.4.8", "2.4", "2.04.8", "2.4.8-rc.01", "2.4.8+build", "2.4.8;id", "latest"} {
		if validSystemUpdateVersion(invalid) {
			t.Fatalf("expected %q to be invalid", invalid)
		}
	}
}

func TestCompareSystemVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.4.8", "2.4.7", 1},
		{"2.4.7", "2.4.7", 0},
		{"2.4.7", "2.5.0", -1},
		{"10.0.0", "9.99.99", 1},
		{"2.4.9-beta.2", "2.4.9-beta.1", 1},
		{"2.4.9-beta.1", "2.4.9", -1},
		{"2.4.9", "2.4.9-rc.9", 1},
		{"dev", "2.4.7", 0},
	}
	for _, test := range tests {
		if got := compareSystemVersions(test.a, test.b); got != test.want {
			t.Errorf("compareSystemVersions(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestSystemUpdateStateOffersTestReleaseWithoutStableBadge(t *testing.T) {
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.4.8","name":"Stable","prerelease":false},
			{"tag_name":"v2.4.9-beta.2","name":"Test","prerelease":true},
			{"tag_name":"v2.4.9-beta.1","name":"Old test","prerelease":true},
			{"tag_name":"v9.0.0-beta.1","name":"Draft","prerelease":true,"draft":true}
		]`))
	}))
	defer releaseServer.Close()

	state := buildSystemUpdateState(Deps{
		AppVersion: "2.4.9-beta.1",
		Config:     config.Config{ReleaseAPIURL: releaseServer.URL},
	}, true)
	if state.UpdateAvailable {
		t.Fatalf("a test release must not enable the stable update badge: %+v", state)
	}
	if state.LatestVersion != "2.4.8" {
		t.Fatalf("latest stable=%q, want 2.4.8", state.LatestVersion)
	}
	if len(state.Releases) != 2 || state.Releases[1].Version != "2.4.9-beta.2" || !state.Releases[1].Prerelease || !state.Releases[1].Installable {
		t.Fatalf("unexpected release catalog: %+v", state.Releases)
	}
}

func TestStartSystemUpdateRejectsVersionOutsideCatalog(t *testing.T) {
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v2.4.9-beta.2","prerelease":true}]`))
	}))
	defer releaseServer.Close()

	body, _ := json.Marshal(map[string]string{"version": "2.4.9-beta.3"})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/system-update/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	startSystemUpdateAdmin(Deps{
		AppVersion: "2.4.9-beta.1",
		Config:     config.Config{ReleaseAPIURL: releaseServer.URL},
	}, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", rec.Code, rec.Body.String())
	}
}

func TestBuildSystemUpdateStateFromStableRelease(t *testing.T) {
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name":"v2.4.8",
			"name":"Aivory 2.4.8",
			"body":"## Fixed\n\nAn update.",
			"html_url":"https://example.test/release",
			"published_at":"2026-09-15T00:00:00Z"
		}`))
	}))
	defer releaseServer.Close()

	state := buildSystemUpdateState(Deps{
		AppVersion: "2.4.7",
		Config: config.Config{
			ReleaseAPIURL: releaseServer.URL,
		},
	}, true)
	if state.CurrentVersion != "2.4.7" || state.LatestVersion != "2.4.8" || !state.UpdateAvailable {
		t.Fatalf("unexpected update state: %+v", state)
	}
	if state.ReleaseNotes == "" || state.Configured {
		t.Fatalf("expected release notes with an unconfigured updater: %+v", state)
	}
}

func TestCallSystemUpdaterAuthenticatesWithTokenFile(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	updater := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"job":{"status":"idle"}}`))
	}))
	defer updater.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, body, configured, err := callSystemUpdater(Deps{Config: config.Config{
		SystemUpdaterURL:       updater.URL,
		SystemUpdaterTokenFile: tokenFile,
	}}, http.MethodGet, "/v1/status", nil)
	if err != nil || !configured || status != http.StatusOK || len(body) == 0 {
		t.Fatalf("status=%d configured=%v err=%v body=%q", status, configured, err, body)
	}
}
