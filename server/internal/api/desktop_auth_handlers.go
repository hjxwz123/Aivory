package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"aivory/server/internal/store"
)

const desktopAuthorizationTTL = 5 * time.Minute

var desktopProofPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type desktopAuthorization struct {
	Challenge string                      `json:"challenge"`
	ExpiresAt int64                       `json:"expires_at"`
	Client    *desktopAuthorizationClient `json:"client,omitempty"`
}

// Consent describes the initiating desktop request, never the browser that
// approves it. This metadata is informational; PKCE remains the binding proof.
type desktopAuthorizationClient struct {
	UserAgent string `json:"user_agent,omitempty"`
	IP        string `json:"ip,omitempty"`
	Location  string `json:"location,omitempty"`
}

type desktopGrant struct {
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	TokenVer  int    `json:"token_ver"`
	Denied    bool   `json:"denied"`
}

func desktopStartHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string `json:"challenge"`
	}
	if err := decodeJSON(r, &req); err != nil || !desktopProofPattern.MatchString(req.Challenge) {
		writeError(w, http.StatusBadRequest, errors.New("invalid desktop challenge"))
		return
	}
	if d.Cache == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("authorization unavailable"))
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeError(w, 500, err)
		return
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	ip := clientIP(r)
	a := desktopAuthorization{
		Challenge: req.Challenge, ExpiresAt: time.Now().Add(desktopAuthorizationTTL).Unix(),
		Client: &desktopAuthorizationClient{
			UserAgent: auditText(r.UserAgent(), 1024),
			IP:        auditText(ip, 128), Location: auditText(sessionLocation(r, ip), 256),
		},
	}
	encoded, _ := json.Marshal(a)
	if !d.Cache.SetNX("desktop:request:"+id, string(encoded), desktopAuthorizationTTL) {
		writeError(w, http.StatusServiceUnavailable, errors.New("authorization unavailable"))
		return
	}
	// The desktop constructs the browser URL from its baked base URL. The server
	// never accepts a callback URL or returns an external redirect destination.
	writeSessionJSON(w, 200, map[string]any{"request_id": id, "expires_at": a.ExpiresAt})
}

func desktopRequest(d Deps, id string) (desktopAuthorization, bool) {
	var a desktopAuthorization
	if d.Cache == nil || !desktopProofPattern.MatchString(id) {
		return a, false
	}
	raw, ok := d.Cache.Get("desktop:request:" + id)
	if !ok || json.Unmarshal([]byte(raw), &a) != nil || a.ExpiresAt <= time.Now().Unix() {
		return a, false
	}
	return a, true
}

func desktopAuthorizationInfoHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	a, ok := desktopRequest(d, r.URL.Query().Get("request_id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("desktop authorization expired"))
		return
	}
	// Do not expose the cached challenge or the native client's proof verifier.
	writeSessionJSON(w, 200, struct {
		ExpiresAt int64                       `json:"expires_at"`
		Client    *desktopAuthorizationClient `json:"client,omitempty"`
	}{ExpiresAt: a.ExpiresAt, Client: a.Client})
}

func desktopAuthorizeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"request_id"`
		Approve   bool   `json:"approve"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	a, ok := desktopRequest(d, req.RequestID)
	if !ok {
		writeError(w, 404, errors.New("desktop authorization expired"))
		return
	}
	user := authUser(r)
	claims, err := d.Auth.ParseAccess(readAccessToken(r))
	if err != nil || claims.SessionID == "" {
		writeError(w, 401, errSessionExpired)
		return
	}
	grant := desktopGrant{UserID: user.ID, SessionID: claims.SessionID, TokenVer: user.TokenVer, Denied: !req.Approve}
	encoded, _ := json.Marshal(grant)
	if !d.Cache.SetNX("desktop:grant:"+req.RequestID, string(encoded), time.Until(time.Unix(a.ExpiresAt, 0))) {
		writeError(w, 409, errors.New("desktop authorization already completed"))
		return
	}
	writeSessionJSON(w, 200, map[string]bool{"ok": true})
}

func desktopTokenHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"request_id"`
		Verifier  string `json:"verifier"`
	}
	if err := decodeJSON(r, &req); err != nil || !desktopProofPattern.MatchString(req.Verifier) {
		writeError(w, 400, errors.New("invalid desktop proof"))
		return
	}
	a, ok := desktopRequest(d, req.RequestID)
	if !ok {
		writeError(w, 404, errors.New("desktop authorization expired"))
		return
	}
	digest := sha256.Sum256([]byte(req.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(a.Challenge)) != 1 {
		writeError(w, 403, errors.New("invalid desktop proof"))
		return
	}
	key := "desktop:grant:" + req.RequestID
	raw, ok := d.Cache.Get(key)
	if !ok {
		writeSessionJSON(w, 200, map[string]string{"status": "pending"})
		return
	}
	var grant desktopGrant
	if json.Unmarshal([]byte(raw), &grant) != nil || !d.Cache.CompareAndDelete(key, raw) {
		writeError(w, 409, errors.New("desktop authorization already used"))
		return
	}
	d.Cache.Delete("desktop:request:" + req.RequestID)
	if grant.Denied {
		writeSessionJSON(w, 200, map[string]string{"status": "denied"})
		return
	}
	user, err := store.FindUserByID(r.Context(), d.DB, grant.UserID)
	if err != nil || user.Status != "active" || user.TokenVer != grant.TokenVer {
		writeError(w, 401, errSessionExpired)
		return
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, grant.TokenVer)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	access, exp, err := d.Auth.IssueAccessForSession(user.ID, user.Role, grant.TokenVer, jti)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.SaveRefreshTokenForDesktopAuthorization(r.Context(), d.DB, jti, user.ID, grant.SessionID, grant.TokenVer, refreshExp, sessionMeta(r, 0)); err != nil {
		if errors.Is(err, store.ErrLoginStateChanged) {
			writeError(w, 401, errSessionExpired)
		} else {
			writeError(w, 500, err)
		}
		return
	}
	invalidateAuthUser(d, user.ID)
	setSessionCookies(w, r, access, exp, refresh, refreshExp)
	setAuditActor(r, user)
	recordSuccessfulLogin(d, r, user.ID, store.LoginMethodDesktopBrowser)
	writeSessionJSON(w, 200, map[string]string{"status": "authorized"})
}
