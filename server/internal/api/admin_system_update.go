package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const systemUpdateResponseLimit = 2 << 20

type systemUpdateJob struct {
	ID          string `json:"id,omitempty"`
	Status      string `json:"status"`
	Progress    string `json:"progress,omitempty"`
	Version     string `json:"version,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

type systemUpdateRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

type systemUpdateState struct {
	CurrentVersion  string           `json:"current_version"`
	LatestVersion   string           `json:"latest_version,omitempty"`
	UpdateAvailable bool             `json:"update_available"`
	Configured      bool             `json:"configured"`
	ReleaseName     string           `json:"release_name,omitempty"`
	ReleaseNotes    string           `json:"release_notes,omitempty"`
	ReleaseURL      string           `json:"release_url,omitempty"`
	PublishedAt     string           `json:"published_at,omitempty"`
	CheckError      string           `json:"check_error,omitempty"`
	UpdaterError    string           `json:"updater_error,omitempty"`
	Job             *systemUpdateJob `json:"job,omitempty"`
}

var systemUpdateReleases = struct {
	sync.Mutex
	release   systemUpdateRelease
	fetchedAt time.Time
	err       error
}{}

func getSystemUpdateAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildSystemUpdateState(d, false))
}

func checkSystemUpdateAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildSystemUpdateState(d, true))
}

func buildSystemUpdateState(d Deps, force bool) systemUpdateState {
	current := strings.TrimSpace(d.AppVersion)
	if current == "" {
		current = "dev"
	}
	state := systemUpdateState{CurrentVersion: current}
	release, err := latestSystemUpdateRelease(d, force)
	if err != nil {
		state.CheckError = "release_check_failed"
	} else {
		latest := normalizeSystemUpdateVersion(release.TagName)
		state.LatestVersion = latest
		state.ReleaseName = release.Name
		state.ReleaseNotes = release.Body
		state.ReleaseURL = release.HTMLURL
		state.PublishedAt = release.PublishedAt
		state.UpdateAvailable = compareSystemVersions(latest, normalizeSystemUpdateVersion(current)) > 0
	}

	job, configured, updaterErr := systemUpdaterStatus(d)
	state.Configured = configured
	state.Job = job
	if updaterErr != nil && configured {
		state.UpdaterError = "updater_unavailable"
	}
	return state
}

func startSystemUpdateAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	requested := normalizeSystemUpdateVersion(input.Version)
	if !validSystemUpdateVersion(requested) {
		writeError(w, http.StatusBadRequest, errors.New("invalid update version"))
		return
	}
	release, err := latestSystemUpdateRelease(d, true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "release_check_failed"})
		return
	}
	latest := normalizeSystemUpdateVersion(release.TagName)
	if requested != latest || !validSystemUpdateVersion(latest) {
		writeError(w, http.StatusConflict, errors.New("requested version is no longer the latest release"))
		return
	}
	if compareSystemVersions(latest, normalizeSystemUpdateVersion(d.AppVersion)) <= 0 {
		writeError(w, http.StatusConflict, errors.New("application is already up to date"))
		return
	}
	payload, _ := json.Marshal(map[string]string{"version": latest})
	status, response, configured, err := callSystemUpdater(d, http.MethodPost, "/v1/update", payload)
	if !configured {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "updater_not_configured"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "updater_unavailable"})
		return
	}
	if status != http.StatusAccepted && status != http.StatusConflict {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "updater_rejected_request"})
		return
	}
	var result struct {
		Job systemUpdateJob `json:"job"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "invalid_updater_response"})
		return
	}
	writeJSON(w, status, map[string]any{"job": result.Job})
}

func latestSystemUpdateRelease(d Deps, force bool) (systemUpdateRelease, error) {
	systemUpdateReleases.Lock()
	defer systemUpdateReleases.Unlock()
	if !force && !systemUpdateReleases.fetchedAt.IsZero() && time.Since(systemUpdateReleases.fetchedAt) < 5*time.Minute {
		return systemUpdateReleases.release, systemUpdateReleases.err
	}
	apiURL := strings.TrimSpace(d.Config.ReleaseAPIURL)
	if apiURL == "" {
		return systemUpdateRelease{}, errors.New("release API is not configured")
	}
	request, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return systemUpdateRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Aivory-System-Updater")
	client := d.SystemUpdateHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	response, err := client.Do(request)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			err = fmt.Errorf("release API returned %s", response.Status)
		} else {
			var release systemUpdateRelease
			reader := io.LimitReader(response.Body, systemUpdateResponseLimit+1)
			var body []byte
			body, err = io.ReadAll(reader)
			if err == nil && len(body) > systemUpdateResponseLimit {
				err = errors.New("release response is too large")
			}
			if err == nil {
				err = json.Unmarshal(body, &release)
			}
			if err == nil && (release.Draft || release.Prerelease || !validSystemUpdateVersion(normalizeSystemUpdateVersion(release.TagName))) {
				err = errors.New("latest release is not a stable semantic version")
			}
			if err == nil {
				systemUpdateReleases.release = release
			}
		}
	}
	systemUpdateReleases.err = err
	systemUpdateReleases.fetchedAt = time.Now()
	return systemUpdateReleases.release, err
}

func systemUpdaterStatus(d Deps) (*systemUpdateJob, bool, error) {
	status, response, configured, err := callSystemUpdater(d, http.MethodGet, "/v1/status", nil)
	if !configured || err != nil {
		return nil, configured, err
	}
	if status != http.StatusOK {
		return nil, true, fmt.Errorf("updater returned status %d", status)
	}
	var result struct {
		Job systemUpdateJob `json:"job"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, true, err
	}
	return &result.Job, true, nil
}

func callSystemUpdater(d Deps, method, path string, body []byte) (int, []byte, bool, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(d.Config.SystemUpdaterURL), "/")
	if baseURL == "" || strings.TrimSpace(d.Config.SystemUpdaterTokenFile) == "" {
		return 0, nil, false, nil
	}
	tokenBytes, err := os.ReadFile(d.Config.SystemUpdaterTokenFile)
	if err != nil || len(strings.TrimSpace(string(tokenBytes))) < 32 {
		return 0, nil, false, nil
	}
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, true, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenBytes)))
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	client := d.SystemUpdateHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, true, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, systemUpdateResponseLimit+1))
	if err != nil {
		return 0, nil, true, err
	}
	if len(responseBody) > systemUpdateResponseLimit {
		return 0, nil, true, errors.New("updater response is too large")
	}
	return response.StatusCode, responseBody, true, nil
}

func normalizeSystemUpdateVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func validSystemUpdateVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func compareSystemVersions(a, b string) int {
	if !validSystemUpdateVersion(a) || !validSystemUpdateVersion(b) {
		return 0
	}
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, _ := strconv.ParseUint(ap[i], 10, 32)
		bv, _ := strconv.ParseUint(bp[i], 10, 32)
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
