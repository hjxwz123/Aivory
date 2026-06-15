package api

import (
	"encoding/json"
	"net/http"

	"aurelia/server/internal/store"
)

// announcement is the wire shape of the global notice (§ announcement). image_url
// non-empty → render as an image announcement (image left, text right). When
// remember_dismiss is false the client re-shows it every visit; updated_at
// doubles as the dismiss version so editing the notice re-shows it.
type announcement struct {
	Enabled         bool   `json:"enabled"`
	Body            string `json:"body"`
	ImageURL        string `json:"image_url"`
	RememberDismiss bool   `json:"remember_dismiss"`
	UpdatedAt       int64  `json:"updated_at"`
}

// announcementHandler returns the active announcement for the client to render.
// Disabled / unset / malformed → {"enabled": false} so the client simply shows
// nothing. The admin edits this via the generic /api/admin/settings endpoint.
func announcementHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	raw, err := store.GetSetting(d.DB, "announcement")
	if err != nil {
		writeJSON(w, 200, announcement{})
		return
	}
	var a announcement
	if json.Unmarshal(raw, &a) != nil || !a.Enabled {
		writeJSON(w, 200, announcement{})
		return
	}
	writeJSON(w, 200, a)
}
