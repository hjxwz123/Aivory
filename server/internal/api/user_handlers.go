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
	if req.Name != nil {
		name = *req.Name
	}
	// Email is immutable for users (it's the account identity / login). Any
	// email in the request is ignored — only an admin can change it. This keeps
	// the current address regardless of what the client sends.
	upd, err := store.UpdateUserProfile(r.Context(), d.DB, u.ID, name, u.Email)
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

type setPasswordReq struct {
	New string `json:"new_password"`
}

// setPasswordHandler sets the FIRST password for an account that has none
// (created via OAuth). It requires no current password — the user has one only
// if they logged in via a third-party provider. It refuses if a password is
// already set (those users must use changePasswordHandler, which verifies the
// current one) and does NOT clear cookies, so the user continues straight into
// the app (§ third-party login has no password).
func setPasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if u.HasPassword {
		writeError(w, 409, errors.New("password already set"))
		return
	}
	var req setPasswordReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if len(req.New) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}
	newHash, err := store.HashPassword(req.New)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.SetInitialPassword(r.Context(), d.DB, u.ID, newHash); err != nil {
		writeError(w, 500, err)
		return
	}
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

// meUsageHandler returns the user's message-count over the last N days. Cost is
// deliberately NOT returned to users — only admins can view spend (/admin/usage).
func meUsageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	days := 30
	_, count, err := store.SumUsageByUser(r.Context(), d.DB, u.ID, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "messages": count})
}

// deleteMeHandler permanently deletes the authenticated user's account and all
// associated data — conversations, messages, memories, tokens, usage logs. The
// user must confirm by sending their password so accidental calls from JS can't
// silently wipe an account.
func deleteMeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Password == "" {
		writeError(w, 400, errors.New("password confirmation required"))
		return
	}
	hash, err := store.PasswordFor(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !store.CheckPassword(hash, req.Password) {
		writeError(w, 401, errors.New("incorrect password"))
		return
	}
	if err := store.DeleteUser(r.Context(), d.DB, u.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	clearCookie(w, "auth_token")
	clearCookie(w, "refresh_token")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
