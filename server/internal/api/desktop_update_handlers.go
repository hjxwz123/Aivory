package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"

	"aivory/server/internal/store"
)

const desktopUpdateSettingKey = "desktop_update"

var desktopDownloadPlatforms = map[string]bool{
	"windows_x64": true, "windows_arm64": true,
	"macos_x64": true, "macos_arm64": true,
	"linux_x64": true, "linux_arm64": true,
}

type desktopUpdateConfig struct {
	Enabled   bool              `json:"enabled"`
	Source    string            `json:"source,omitempty"`
	Version   string            `json:"version"`
	Downloads map[string]string `json:"downloads"`
}

type desktopUpdateReleaseState struct {
	systemUpdateReleaseState
	Downloads map[string]string `json:"downloads"`
}

type desktopUpdateAdminState struct {
	Config          desktopUpdateConfig         `json:"config"`
	LatestVersion   string                      `json:"latest_version,omitempty"`
	UpdateAvailable bool                        `json:"update_available"`
	Releases        []desktopUpdateReleaseState `json:"releases,omitempty"`
	CheckError      string                      `json:"check_error,omitempty"`
}

func normalizeDesktopUpdateConfig(raw json.RawMessage) (desktopUpdateConfig, error) {
	var cfg desktopUpdateConfig
	if len(raw) > 20_000 || json.Unmarshal(raw, &cfg) != nil || strings.TrimSpace(string(raw)) == "null" {
		return cfg, errInvalidInput
	}
	cfg.Version = normalizeSystemUpdateVersion(cfg.Version)
	if cfg.Source == "" {
		cfg.Source = "custom"
	}
	if cfg.Source != "custom" && cfg.Source != "official" {
		return cfg, errInvalidInput
	}
	if len(cfg.Version) > 128 || (cfg.Version != "" && !validSystemUpdateVersion(cfg.Version)) {
		return cfg, errInvalidInput
	}
	if cfg.Downloads == nil {
		cfg.Downloads = map[string]string{}
	}
	for platform, address := range cfg.Downloads {
		if !desktopDownloadPlatforms[platform] {
			return cfg, errInvalidInput
		}
		address = strings.TrimSpace(address)
		if address == "" {
			delete(cfg.Downloads, platform)
			continue
		}
		u, err := url.Parse(address)
		if len(address) > 2048 || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return cfg, errInvalidInput
		}
		cfg.Downloads[platform] = address
		if cfg.Source == "official" && officialDesktopAssetPlatform(cfg.Version, address) != platform {
			return cfg, errInvalidInput
		}
	}
	if cfg.Enabled && (cfg.Version == "" || len(cfg.Downloads) == 0) {
		return cfg, errInvalidInput
	}
	return cfg, nil
}

// Official URLs are snapshotted by the administrator, so public checks never
// depend on GitHub availability or change packages without another publication.
func officialDesktopAssetPlatform(version, address string) string {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	prefix := "/hjxwz123/Aivory/releases/download/"
	if !strings.HasPrefix(u.Path, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || normalizeSystemUpdateVersion(parts[0]) != version || !validSystemUpdateVersion(version) {
		return ""
	}
	name := parts[1]
	for platform := range desktopDownloadPlatforms {
		osArch := strings.Replace(platform, "windows_", "win-", 1)
		osArch = strings.Replace(osArch, "macos_", "mac-", 1)
		osArch = strings.Replace(osArch, "linux_", "linux-", 1)
		stem := "Aivory-" + version + "-" + osArch
		if strings.HasPrefix(platform, "windows_") && name == stem+".exe" {
			return platform
		}
		if strings.HasPrefix(platform, "macos_") && name == stem+".dmg" {
			return platform
		}
		if strings.HasPrefix(platform, "linux_") && (name == stem+".AppImage" || name == stem+".deb") {
			return platform
		}
	}
	return ""
}

func officialDesktopDownloads(release systemUpdateRelease) map[string]string {
	downloads := map[string]string{}
	if release.Draft || release.PublishedAt == "" {
		return downloads
	}
	version := normalizeSystemUpdateVersion(release.TagName)
	for _, asset := range release.Assets {
		if asset.Size <= 0 || asset.State != "uploaded" {
			continue
		}
		platform := officialDesktopAssetPlatform(version, asset.URL)
		parsed, err := url.Parse(asset.URL)
		if platform == "" || err != nil || path.Base(parsed.Path) != asset.Name {
			continue
		}
		if downloads[platform] == "" || strings.HasSuffix(asset.Name, ".AppImage") {
			downloads[platform] = asset.URL
		}
	}
	return downloads
}

func readDesktopUpdateConfig(d Deps) desktopUpdateConfig {
	if raw, err := store.GetSetting(d.DB, desktopUpdateSettingKey); err == nil {
		if cfg, err := normalizeDesktopUpdateConfig(raw); err == nil {
			return cfg
		}
	}
	return desktopUpdateConfig{Source: "custom", Downloads: map[string]string{}}
}

func desktopUpdatePublicHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg := readDesktopUpdateConfig(d)
	if !cfg.Enabled {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
		return
	}
	// Desktop checks only deployment-approved packages; this path never calls GitHub.
	writeJSON(w, http.StatusOK, cfg)
}

func buildDesktopUpdateAdminState(d Deps, force bool) desktopUpdateAdminState {
	cfg := readDesktopUpdateConfig(d)
	state := desktopUpdateAdminState{Config: cfg}
	releases, err := systemUpdateReleaseCatalog(d, force)
	if err != nil {
		state.CheckError = "release_check_failed"
		return state
	}
	current := cfg.Version
	if current == "" {
		current = normalizeSystemUpdateVersion(d.AppVersion)
	}
	allowPrerelease := strings.Contains(current, "-")
	for _, release := range releases {
		// Older GitHub payloads used by system updates may omit publication time.
		// A desktop notification requires an actually published release.
		if release.PublishedAt == "" {
			continue
		}
		version := normalizeSystemUpdateVersion(release.TagName)
		prerelease := release.Prerelease || strings.Contains(version, "-")
		state.Releases = append(state.Releases, desktopUpdateReleaseState{systemUpdateReleaseState: systemUpdateReleaseState{
			Version: version, Name: release.Name, URL: release.HTMLURL,
			PublishedAt: release.PublishedAt, Prerelease: prerelease,
		}, Downloads: officialDesktopDownloads(release)})
		if prerelease && !allowPrerelease {
			continue
		}
		if state.LatestVersion == "" || compareSystemVersions(version, state.LatestVersion) > 0 {
			state.LatestVersion = version
		}
	}
	state.UpdateAvailable = state.LatestVersion != "" && (!validSystemUpdateVersion(current) || compareSystemVersions(state.LatestVersion, current) > 0)
	return state
}

func getDesktopUpdateAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildDesktopUpdateAdminState(d, false))
}

func checkDesktopUpdateAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildDesktopUpdateAdminState(d, true))
}
