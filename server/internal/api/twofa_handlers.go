package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/auth"
	"aurelia/server/internal/store"
)

// Two-factor (TOTP) login (§ 2FA). Setup hands the user a secret; enable proves
// possession with a code; once enabled, password login returns a short-lived
// ticket instead of a session, and the code must be supplied to finish.

const twofaIssuer = "Aurelia"

// twofaSetupHandler generates a fresh (not yet enabled) secret and returns the
// provisioning URI for the user's authenticator app.
func twofaSetupHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if u.TotpEnabled {
		writeError(w, 400, errors.New("two-factor is already enabled"))
		return
	}
	secret, err := auth.GenerateTotpSecret()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.SetUserTotp(r.Context(), d.DB, u.ID, secret, false); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{
		"secret":      secret,
		"otpauth_url": auth.TotpURI(secret, twofaIssuer, u.Email),
	})
}

type twofaCodeReq struct {
	Code string `json:"code"`
}

// twofaEnableHandler turns 2FA on after verifying a code against the pending
// secret created by setup.
func twofaEnableHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req twofaCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if u.TotpSecret == "" {
		writeError(w, 400, errors.New("start setup first"))
		return
	}
	// Replay-attack guard: reject a code that was already consumed within the
	// current 30-second window (±1 step tolerance = up to 90s of validity).
	usedKey := fmt.Sprintf("2fa:used:%s:%s", u.ID, req.Code)
	if _, already := d.Cache.Get(usedKey); already {
		writeError(w, 400, errors.New("TOTP code already used"))
		return
	}
	if !auth.VerifyTotp(u.TotpSecret, req.Code) {
		writeError(w, 400, errors.New("invalid code"))
		return
	}
	d.Cache.Set(usedKey, "1", 90*time.Second)
	if err := store.SetUserTotp(r.Context(), d.DB, u.ID, u.TotpSecret, true); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// twofaDisableHandler turns 2FA off after verifying a current code.
func twofaDisableHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var req twofaCodeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if !u.TotpEnabled {
		writeError(w, 400, errors.New("two-factor is not enabled"))
		return
	}
	// Replay-attack guard.
	usedKey := fmt.Sprintf("2fa:used:%s:%s", u.ID, req.Code)
	if _, already := d.Cache.Get(usedKey); already {
		writeError(w, 400, errors.New("TOTP code already used"))
		return
	}
	if !auth.VerifyTotp(u.TotpSecret, req.Code) {
		writeError(w, 400, errors.New("invalid code"))
		return
	}
	d.Cache.Set(usedKey, "1", 90*time.Second)
	if err := store.DisableUserTotp(r.Context(), d.DB, u.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// adminDisableTwofaHandler lets an admin reset a user's 2FA — the recovery path
// for a member who lost their authenticator device.
func adminDisableTwofaHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DisableUserTotp(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type login2faReq struct {
	Ticket string `json:"ticket"`
	Code   string `json:"code"`
}

// login2faHandler completes a 2FA-gated login: it exchanges the ticket from the
// password step plus a valid code for a real session.
func login2faHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req login2faReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	// Ticket comes from the JSON body (password-login flow) or, for the OAuth
	// flow, from the short-lived HttpOnly cookie set by the callback (§A10).
	ticket := req.Ticket
	if ticket == "" {
		if c, cerr := r.Cookie("aurelia_2fa"); cerr == nil {
			ticket = c.Value
		}
	}
	if ticket == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	// NOTE: do NOT clear the OAuth handoff cookie here. It used to be cleared
	// unconditionally on every call, which meant a single wrong/premature code
	// destroyed the cookie and ALL subsequent attempts failed with "expired" —
	// even a correct one. (The password flow keeps its ticket in the request
	// body, so it tolerated retries; the OAuth flow got exactly one shot.) We
	// only clear the cookie once the ticket is actually consumed or burned.
	uid, ok := d.Cache.Get("2fa:" + ticket)
	if !ok {
		clear2faCookie(w) // dead ticket → the handoff cookie is useless now
		d.Logger.Printf("[2fa] verify: ticket not found/expired (must redo sign-in)")
		writeError(w, 401, errors.New("login session expired, please sign in again"))
		return
	}
	user, err := store.FindUserByID(r.Context(), d.DB, uid)
	if err != nil || user.Status != "active" {
		clear2faCookie(w)
		writeError(w, 401, errors.New("invalid login session"))
		return
	}
	// Replay-attack guard: reject a code that was already consumed within the
	// current window before we call VerifyTotp (which only checks correctness).
	usedKey := fmt.Sprintf("2fa:used:%s:%s", uid, req.Code)
	if _, already := d.Cache.Get(usedKey); already {
		writeError(w, 401, errors.New("TOTP code already used"))
		return
	}
	if !auth.VerifyTotp(user.TotpSecret, req.Code) {
		// §A5: burn the ticket after 5 wrong codes so a captured ticket can't be
		// brute-forced for its full TTL — the user must redo the password step.
		// Until then KEEP the cookie so the user can retry (mirrors the
		// body-ticket password flow).
		burned := d.Cache.Incr("2fa:fail:"+ticket, 5*time.Minute) >= 5
		if burned {
			d.Cache.Delete("2fa:" + ticket)
			d.Cache.Delete("2fa:fail:" + ticket)
			clear2faCookie(w)
		}
		d.Logger.Printf("[2fa] verify: code rejected for user=%s (secretLen=%d, codeLen=%d, burned=%v)", uid, len(user.TotpSecret), len(strings.TrimSpace(req.Code)), burned)
		writeError(w, 401, errors.New("invalid code"))
		return
	}
	d.Cache.Set(usedKey, "1", 90*time.Second)
	d.Cache.Delete("2fa:" + ticket)
	d.Cache.Delete("2fa:fail:" + ticket)
	clear2faCookie(w)
	finaliseSession(d, w, r, user, 0)
}

// clear2faCookie expires the OAuth 2FA handoff cookie (Path must match the one
// set in the OAuth callback).
func clear2faCookie(w http.ResponseWriter) {
	// Deletion only (MaxAge -1) — leave Secure off so the removal also takes
	// effect over plain HTTP (a Secure Set-Cookie is ignored on http://).
	http.SetCookie(w, &http.Cookie{
		Name: "aurelia_2fa", Value: "", Path: "/api/auth",
		HttpOnly: true, Secure: false, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// issueTwofaTicket mints a short-lived ticket that stands in for a verified
// password until the user supplies their 2FA code.
func issueTwofaTicket(d Deps, userID string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	ticket := hex.EncodeToString(buf)
	d.Cache.Set("2fa:"+ticket, userID, 5*time.Minute)
	return ticket
}
