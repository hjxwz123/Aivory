package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"aivory/server/internal/store"
)

const desktopDownloadSettingKey = "desktop_download"

type desktopDownloadConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`
}

func normalizeDesktopDownloadConfig(raw json.RawMessage) (desktopDownloadConfig, error) {
	var cfg desktopDownloadConfig
	if len(raw) > 4096 || json.Unmarshal(raw, &cfg) != nil || strings.TrimSpace(string(raw)) == "null" {
		return cfg, errInvalidInput
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if len(cfg.URL) > 2048 || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return cfg, errInvalidInput
		}
	}
	if cfg.Enabled && cfg.URL == "" {
		return cfg, errInvalidInput
	}
	return cfg, nil
}

func desktopDownloadPublicHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if raw, err := store.GetSetting(d.DB, desktopDownloadSettingKey); err == nil {
		if cfg, err := normalizeDesktopDownloadConfig(raw); err == nil && cfg.Enabled {
			writeJSON(w, http.StatusOK, cfg)
			return
		}
	}
	writeJSON(w, http.StatusOK, desktopDownloadConfig{})
}
