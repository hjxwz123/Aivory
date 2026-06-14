package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
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
	if !auth.VerifyTotp(u.TotpSecret, req.Code) {
		writeError(w, 400, errors.New("invalid code"))
		return
	}
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
	if !auth.VerifyTotp(u.TotpSecret, req.Code) {
		writeError(w, 400, errors.New("invalid code"))
		return
	}
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
	clear2faCookie(w)
	uid, ok := d.Cache.Get("2fa:" + ticket)
	if !ok {
		writeError(w, 401, errors.New("login session expired, please sign in again"))
		return
	}
	user, err := store.FindUserByID(r.Context(), d.DB, uid)
	if err != nil || user.Status != "active" {
		writeError(w, 401, errors.New("invalid login session"))
		return
	}
	if !auth.VerifyTotp(user.TotpSecret, req.Code) {
		// §A5: burn the ticket after 5 wrong codes so a captured ticket can't be
		// brute-forced for its full TTL — the user must redo the password step.
		if d.Cache.Incr("2fa:fail:"+ticket, 5*time.Minute) >= 5 {
			d.Cache.Delete("2fa:" + ticket)
			d.Cache.Delete("2fa:fail:" + ticket)
		}
		writeError(w, 401, errors.New("invalid code"))
		return
	}
	d.Cache.Delete("2fa:" + ticket)
	d.Cache.Delete("2fa:fail:" + ticket)
	finaliseSession(d, w, r, user, 0)
}

// clear2faCookie expires the OAuth 2FA handoff cookie (Path must match the one
// set in the OAuth callback).
func clear2faCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: "aurelia_2fa", Value: "", Path: "/api/auth",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
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
