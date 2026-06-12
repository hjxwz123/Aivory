package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"aurelia/server/internal/store"
)

// meHandler returns the authenticated user profile.
func meHandler(_ Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, authUser(r))
}

type updateMeReq struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
}

// updateMeHandler updates the user's display fields.
func updateMeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req updateMeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	name := u.Name
	email := u.Email
	if req.Name != nil {
		name = *req.Name
	}
	if req.Email != nil {
		email = *req.Email
	}
	upd, err := store.UpdateUserProfile(r.Context(), d.DB, u.ID, name, email)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, upd)
}

type changePasswordReq struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// changePasswordHandler verifies the current password and rotates the hash.
func changePasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req changePasswordReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if len(req.New) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}
	hash, err := store.PasswordFor(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !store.CheckPassword(hash, req.Current) {
		writeError(w, 401, errors.New("current password incorrect"))
		return
	}
	newHash, err := store.HashPassword(req.New)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.UpdateUserPassword(r.Context(), d.DB, u.ID, newHash); err != nil {
		writeError(w, 500, err)
		return
	}
	clearCookie(w, "auth_token")
	clearCookie(w, "refresh_token")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// meSettingsHandler returns the user-level settings JSON.
func meSettingsHandler(_ Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	writeJSON(w, 200, json.RawMessage(u.Settings))
}

// updateMeSettingsHandler merges patch keys into settings.
func updateMeSettingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	patch := map[string]any{}
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	upd, err := store.UpdateUserSettings(r.Context(), d.DB, u.ID, patch)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, json.RawMessage(upd.Settings))
}

// meUsageHandler returns cost + message-count over the last N days.
func meUsageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	days := 30
	cost, count, err := store.SumUsageByUser(r.Context(), d.DB, u.ID, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "cost": cost, "messages": count})
}
