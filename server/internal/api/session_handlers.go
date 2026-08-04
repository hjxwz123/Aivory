package api

import (
	"errors"
	"fmt"
	"net/http"

	"aivory/server/internal/store"
)

// currentSessionJTI returns the jti of the refresh token presented with the
// request, or "" if absent/invalid.
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

func currentSessionID(d Deps, r *http.Request, userID string) string {
	// Every accepted access token is bound to a stable session family. Prefer it
	// over the refresh cookie so Bearer-only API clients can identify and preserve
	// their current session when asking to revoke all others.
	if token := readAccessToken(r); token != "" {
		if claims, err := d.Auth.ParseAccess(token); err == nil && claims.UID == userID {
			return claims.SessionID
		}
	}
	jti := currentSessionJTI(d, r)
	if jti == "" {
		return ""
	}
	sessionID, err := store.ResolveUserSessionID(r.Context(), d.DB, userID, jti)
	if err != nil {
		return ""
	}
	return sessionID
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
		"current":  currentSessionID(d, r, user.ID),
	})
}

// revokeSessionHandler revokes one of the user's sessions. Revoking the current
// session also clears this request's cookies (an explicit self sign-out).
func revokeSessionHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	current := currentSessionID(d, r, user.ID)
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
	// The route historically called this parameter `jti`; clients may still send
	// a consumed predecessor from before rotation. Resolve it after the revoke so
	// revoking the current family clears this request's cookies as well.
	target := jti
	if resolved, resolveErr := store.ResolveUserSessionID(r.Context(), d.DB, user.ID, jti); resolveErr == nil {
		target = resolved
	}
	if target == current {
		clearCookie(w, "auth_token")
		clearCookie(w, "refresh_token")
	}
	// Kill any active generation streams for this user so a revoked session
	// cannot keep a live SSE connection open after sign-out.
	if d.Cache != nil {
		d.Cache.Publish(fmt.Sprintf("user:%s:kill", user.ID), "session_revoked")
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// revokeOtherSessionsHandler signs the user out of every session except the one
// making this request ("sign out everywhere else").
func revokeOtherSessionsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	user := authUser(r)
	if err := store.RevokeOtherUserSessions(r.Context(), d.DB, user.ID, currentSessionID(d, r, user.ID)); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
