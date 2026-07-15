package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

// meHandler returns the authenticated user profile.
func meHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	attachGroupInfo(d, r, u)
	writeJSON(w, 200, u)
}

// attachGroupInfo enriches a user payload with display + capability fields
// derived from its membership group: the group NAME (the tier label shown in the
// sidebar) and the feature flags (e.g. "research"), so the client gets both
// without a second round-trip. Best-effort; transient (never persisted).
func attachGroupInfo(d Deps, r *http.Request, u *store.User) {
	if u == nil {
		return
	}
	gid := u.GroupID
	if gid == "" {
		gid = store.DefaultGroupID
	}
	if g, err := store.GetUserGroup(r.Context(), d.DB, gid); err == nil && g != nil {
		u.GroupName = g.Name
		var feats []string
		if json.Unmarshal(g.Features, &feats) == nil {
			u.Features = feats
		}
	}
	// Global memory master switch → lets the client show/hide the per-user toggle.
	u.MemoryAvailable = store.MemoryEnabledGlobal(d.DB)
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
	invalidateAuthUser(d, u.ID)
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
	invalidateAuthUser(d, u.ID)
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
	invalidateAuthUser(d, u.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// meSettingsHandler returns the user-level settings JSON.
func meSettingsHandler(_ Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	writeJSON(w, 200, json.RawMessage(u.Settings))
}

// settingsAllowlist is the exhaustive set of keys a user may write via PATCH
// /api/me/settings. Any key not in this map is silently stripped so that
// clients cannot inject arbitrary data into the settings blob (e.g. internal
// admin flags or future privilege-escalation vectors).
var settingsAllowlist = map[string]bool{
	"persona_custom":        true,
	"persona_nickname":      true,
	"persona_traits":        true,
	"response_length":       true,
	"accent_color":          true,
	"font_family":           true,
	"image_model_id":        true,
	"default_model_id":      true,
	"memory_enabled":        true,
	"avatar_url":            true,
	"language":              true,
	"theme":                 true,
	"chat_width":            true,
	"sidebar_collapsed":     true,
	"code_theme":            true,
	"user_message_markdown": true,
	"onboarded":             true,
	"tool_mode_default":     true,
	// Legacy clients still write this boolean. New clients use the tri-state
	// tool_mode_default setting; keep the old key writable during migration.
	"disable_tools_default": true,
}

// updateMeSettingsHandler merges patch keys into settings.
// Only keys present in settingsAllowlist are accepted; all others are dropped
// before the merge so users cannot write arbitrary data into their settings blob.
func updateMeSettingsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	patch := map[string]any{}
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	// Strip keys that are not on the allowlist.
	for k := range patch {
		if !settingsAllowlist[k] {
			delete(patch, k)
		}
	}
	if err := normalizeToolModeSettingsPatch(patch); err != nil {
		writeError(w, 400, err)
		return
	}
	upd, err := store.UpdateUserSettings(r.Context(), d.DB, u.ID, patch)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	invalidateAuthUser(d, u.ID)
	writeJSON(w, 200, json.RawMessage(upd.Settings))
}

// Keep old and new clients coherent during rolling upgrades. A new tri-state
// write wins when both keys are present; auto degrades to legacy "tools on".
// A legacy-only boolean write is promoted to the equivalent explicit mode so a
// previously stored tool_mode_default cannot make the old client's change inert.
func normalizeToolModeSettingsPatch(patch map[string]any) error {
	if value, ok := patch["tool_mode_default"]; ok {
		mode, isString := value.(string)
		if !isString || !validTurnToolMode(mode) {
			return errors.New("tool_mode_default must be one of: auto, disabled, enabled")
		}
		patch["disable_tools_default"] = mode == llm.ToolModeDisabled
		return nil
	}
	if value, ok := patch["disable_tools_default"]; ok {
		disabled, isBool := value.(bool)
		if !isBool {
			return errors.New("disable_tools_default must be a boolean")
		}
		if disabled {
			patch["tool_mode_default"] = llm.ToolModeDisabled
		} else {
			patch["tool_mode_default"] = llm.ToolModeEnabled
		}
	}
	return nil
}

// meUsageHandler returns the user's message-count over the last N days. Cost is
// deliberately NOT returned to users — only admins can view spend (/admin/usage).
func meUsageHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	days := envcfg.Int("AIVORY_API_SELF_USAGE_LOOKBACK_WINDOW", 30)
	_, count, err := store.SumUsageByUser(r.Context(), d.DB, u.ID, days)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "messages": count})
}

// deleteMeHandler permanently deletes the authenticated user's account and all
// associated data — conversations, messages, memories, tokens, usage logs. The
// user must confirm by sending their password (password accounts) or an exact
// confirmation string (OAuth-only accounts that have no password).
func deleteMeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errors.New("request body required"))
		return
	}
	if !u.HasPassword {
		// OAuth-only account: no password to check, require explicit confirmation.
		if req.Confirm != "DELETE MY ACCOUNT" {
			writeError(w, 400, errors.New("confirm field must be 'DELETE MY ACCOUNT'"))
			return
		}
	} else {
		// Password account: verify the current password.
		if req.Password == "" {
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
	}
	// Heavy cleanup (bulk SQL, vectors, disk) runs in a background job; this
	// request only locks the account out and returns. §async user delete.
	if _, err := startUserDeletion(d, u.ID, u.Email); err != nil {
		writeError(w, 500, err)
		return
	}
	clearCookie(w, "auth_token")
	clearCookie(w, "refresh_token")
	writeJSON(w, 202, map[string]any{"ok": true, "status": "deleting"})
}
