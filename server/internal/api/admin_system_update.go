package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
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

type systemUpdateReleaseState struct {
	Version     string `json:"version"`
	Name        string `json:"name,omitempty"`
	Notes       string `json:"notes,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Prerelease  bool   `json:"prerelease"`
	Installable bool   `json:"installable"`
}

type systemUpdateState struct {
	CurrentVersion  string                     `json:"current_version"`
	LatestVersion   string                     `json:"latest_version,omitempty"`
	UpdateAvailable bool                       `json:"update_available"`
	Configured      bool                       `json:"configured"`
	ReleaseName     string                     `json:"release_name,omitempty"`
	ReleaseNotes    string                     `json:"release_notes,omitempty"`
	ReleaseURL      string                     `json:"release_url,omitempty"`
	PublishedAt     string                     `json:"published_at,omitempty"`
	Releases        []systemUpdateReleaseState `json:"releases,omitempty"`
	CheckError      string                     `json:"check_error,omitempty"`
	UpdaterError    string                     `json:"updater_error,omitempty"`
	Job             *systemUpdateJob           `json:"job,omitempty"`
}

var systemUpdateReleases = struct {
	sync.Mutex
	releases  []systemUpdateRelease
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
	releases, err := systemUpdateReleaseCatalog(d, force)
	if err != nil {
		state.CheckError = "release_check_failed"
	} else {
		currentVersion := normalizeSystemUpdateVersion(current)
		for _, release := range releases {
			version := normalizeSystemUpdateVersion(release.TagName)
			installable := compareSystemVersions(version, currentVersion) > 0
			state.Releases = append(state.Releases, systemUpdateReleaseState{
				Version: version, Name: release.Name, Notes: release.Body,
				URL: release.HTMLURL, PublishedAt: release.PublishedAt,
				Prerelease: release.Prerelease, Installable: installable,
			})
			// The sidebar consumes update_available. Keep these compatibility
			// fields stable-only so test releases never light the global badge.
			if !release.Prerelease && state.LatestVersion == "" {
				state.LatestVersion = version
				state.ReleaseName = release.Name
				state.ReleaseNotes = release.Body
				state.ReleaseURL = release.HTMLURL
				state.PublishedAt = release.PublishedAt
				state.UpdateAvailable = installable
			}
		}
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
	releases, err := systemUpdateReleaseCatalog(d, true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "release_check_failed"})
		return
	}
	found := false
	for _, release := range releases {
		if requested == normalizeSystemUpdateVersion(release.TagName) {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusConflict, errors.New("requested version is no longer a published release"))
		return
	}
	if compareSystemVersions(requested, normalizeSystemUpdateVersion(d.AppVersion)) <= 0 {
		writeError(w, http.StatusConflict, errors.New("application is already up to date"))
		return
	}
	payload, _ := json.Marshal(map[string]string{"version": requested})
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

func systemUpdateReleaseCatalog(d Deps, force bool) ([]systemUpdateRelease, error) {
	systemUpdateReleases.Lock()
	defer systemUpdateReleases.Unlock()
	if !force && !systemUpdateReleases.fetchedAt.IsZero() && time.Since(systemUpdateReleases.fetchedAt) < 5*time.Minute {
		return append([]systemUpdateRelease(nil), systemUpdateReleases.releases...), systemUpdateReleases.err
	}
	apiURL := strings.TrimSpace(d.Config.ReleaseAPIURL)
	if apiURL == "" {
		return nil, errors.New("release API is not configured")
	}
	request, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
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
			reader := io.LimitReader(response.Body, systemUpdateResponseLimit+1)
			body, readErr := io.ReadAll(reader)
			err = readErr
			if err == nil && len(body) > systemUpdateResponseLimit {
				err = errors.New("release response is too large")
			}
			var fetched []systemUpdateRelease
			if err == nil {
				trimmed := bytes.TrimSpace(body)
				if len(trimmed) > 0 && trimmed[0] == '[' {
					err = json.Unmarshal(trimmed, &fetched)
				} else {
					var release systemUpdateRelease
					err = json.Unmarshal(trimmed, &release)
					if err == nil {
						fetched = []systemUpdateRelease{release}
					}
				}
			}
			if err == nil {
				fetched = selectSystemUpdateReleases(fetched)
				if len(fetched) == 0 {
					err = errors.New("release API returned no valid published semantic versions")
				} else {
					systemUpdateReleases.releases = fetched
				}
			}
		}
	}
	systemUpdateReleases.err = err
	systemUpdateReleases.fetchedAt = time.Now()
	return append([]systemUpdateRelease(nil), systemUpdateReleases.releases...), err
}

// selectSystemUpdateReleases keeps the newest stable release and newest test
// release. Drafts and malformed tags never become installable.
func selectSystemUpdateReleases(releases []systemUpdateRelease) []systemUpdateRelease {
	var stable, prerelease *systemUpdateRelease
	for i := range releases {
		release := releases[i]
		version := normalizeSystemUpdateVersion(release.TagName)
		if release.Draft || !validSystemUpdateVersion(version) {
			continue
		}
		target := &stable
		if release.Prerelease {
			target = &prerelease
		}
		if *target == nil || compareSystemVersions(version, normalizeSystemUpdateVersion((*target).TagName)) > 0 {
			copy := release
			*target = &copy
		}
	}
	selected := make([]systemUpdateRelease, 0, 2)
	if stable != nil {
		selected = append(selected, *stable)
	}
	if prerelease != nil {
		selected = append(selected, *prerelease)
	}
	return selected
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

var systemUpdateVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func validSystemUpdateVersion(version string) bool {
	match := systemUpdateVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) > 1 && identifier[0] == '0' {
			if _, err := strconv.ParseUint(identifier, 10, 64); err == nil {
				return false
			}
		}
	}
	return true
}

type systemUpdateSemver struct {
	core       [3]uint64
	prerelease []string
}

func parseSystemUpdateVersion(version string) (systemUpdateSemver, bool) {
	match := systemUpdateVersionPattern.FindStringSubmatch(version)
	if match == nil || !validSystemUpdateVersion(version) {
		return systemUpdateSemver{}, false
	}
	var parsed systemUpdateSemver
	for i := 0; i < 3; i++ {
		value, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return systemUpdateSemver{}, false
		}
		parsed.core[i] = value
	}
	if match[4] != "" {
		parsed.prerelease = strings.Split(match[4], ".")
	}
	return parsed, true
}

func comparePrereleaseIdentifiers(a, b string) int {
	av, aErr := strconv.ParseUint(a, 10, 64)
	bv, bErr := strconv.ParseUint(b, 10, 64)
	switch {
	case aErr == nil && bErr == nil:
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
	return 0
}

func compareSystemVersions(a, b string) int {
	av, aOK := parseSystemUpdateVersion(a)
	bv, bOK := parseSystemUpdateVersion(b)
	if !aOK || !bOK {
		return 0
	}
	for i := 0; i < 3; i++ {
		if av.core[i] < bv.core[i] {
			return -1
		}
		if av.core[i] > bv.core[i] {
			return 1
		}
	}
	if len(av.prerelease) == 0 && len(bv.prerelease) == 0 {
		return 0
	}
	if len(av.prerelease) == 0 {
		return 1
	}
	if len(bv.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(av.prerelease) && i < len(bv.prerelease); i++ {
		if compared := comparePrereleaseIdentifiers(av.prerelease[i], bv.prerelease[i]); compared != 0 {
			return compared
		}
	}
	if len(av.prerelease) < len(bv.prerelease) {
		return -1
	}
	if len(av.prerelease) > len(bv.prerelease) {
		return 1
	}
	return 0
}
