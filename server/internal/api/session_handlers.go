package api

import (
	"errors"
	"net/http"

	"aurelia/server/internal/store"
)

// currentSessionJTI returns the jti of the refresh token presented with the
// request, or "" if absent/invalid. The refresh cookie is scoped to /api/auth,
// so these handlers MUST be registered under that path for "current" detection
// to work.
func currentSessionJTI(d Deps, r *http.Request) string {
	c, err := r.Cookie("refresh_token")
	if err != nil {
		return ""
	}
	claims, err := d.Auth.ParseRefresh(c.Value)
	if err != nil {
		return ""
	}
	return claims.ID
}

// listSessionsHandler returns the user's active sessions plus the id of the one
// making this request, so the UI can mark "This device" and protect it from an
// accidental self-revoke.
func listSessionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	sessions, err := store.ListUserSessions(r.Context(), d.DB, user.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"sessions": sessions,
		"current":  currentSessionJTI(d, r),
	})
}

// revokeSessionHandler revokes one of the user's sessions. Revoking the current
// session also clears this request's cookies (an explicit self sign-out).
func revokeSessionHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	jti := pathParam(r, "jti")
	if jti == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	ok, err := store.RevokeUserSession(r.Context(), d.DB, user.ID, jti)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !ok {
		writeError(w, 404, errors.New("session not found"))
		return
	}
	if jti == currentSessionJTI(d, r) {
		clearCookie(w, "auth_token")
		clearCookie(w, "refresh_token")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// revokeOtherSessionsHandler signs the user out of every session except the one
// making this request ("sign out everywhere else").
func revokeOtherSessionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	if err := store.RevokeOtherUserSessions(r.Context(), d.DB, user.ID, currentSessionJTI(d, r)); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
