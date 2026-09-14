package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// Passkey (WebAuthn) login — an optional second login surface alongside
// password (+TOTP) and OAuth, mirroring the 2FA feature's self-service shape:
// the user registers device credentials under /api/me/passkeys, then the login
// page offers a usernameless discoverable-credential ceremony under
// /api/auth/passkey/*. A verified passkey assertion is a complete
// authentication factor (it does not additionally require TOTP).

var (
	errPasskeyLoginDisabled      = errors.New("passkey_login_disabled")
	errPasskeyLoginFailed        = errors.New("passkey_login_failed")
	errPasskeySetupExpired       = errors.New("passkey_setup_expired")
	errPasskeyRegistrationFailed = errors.New("passkey_registration_failed")
	errPasskeyLimit              = errors.New("passkey_limit")
	errPasskeyInsecureOrigin     = errors.New("passkey_insecure_origin")
	errPasskeyUnavailable        = errors.New("passkey_unavailable")

	passkeyTicketTTL           = securityDuration("AIVORY_API_ISSUE_PASSKEY_TICKET", 5*time.Minute)
	passkeyTicketBurnThreshold = envcfg.Int64("AIVORY_API_PASSKEY_TICKET_BURN_THRESHOLD", 5)
	passkeyBodyLimit           = int64(64 << 10)
	maxPasskeysPerUser         = 10
)

type passkeyRegisterBeginReq struct {
	Name string `json:"name"`
}

type passkeyRegisterTicket struct {
	Session json.RawMessage `json:"session"`
	Name    string          `json:"name,omitempty"`
}

type passkeyLoginTicket struct {
	Session json.RawMessage `json:"session"`
}

type passkeyLoginBeginResp struct {
	Ticket  string          `json:"ticket"`
	Options json.RawMessage `json:"options"`
}

type passkeyLoginVerifyReq struct {
	Ticket   string          `json:"ticket"`
	Response json.RawMessage `json:"response"`
}

type passkeyListItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

func passkeysUnavailable(w http.ResponseWriter, d Deps) bool {
	if d.Passkeys == nil {
		writeError(w, http.StatusServiceUnavailable, errPasskeyLoginDisabled)
		return true
	}
	return false
}

// passkeyRPOrigin returns the canonical browser origin used to verify the
// ceremony. The Origin header wins: behind a TLS-terminating reverse proxy the
// backend request is plain http, so externalBaseURL alone would misclassify a
// genuine HTTPS deployment as an insecure origin.
func passkeyRPOrigin(r *http.Request) string {
	if origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/"); origin != "" && origin != "null" {
		return origin
	}
	return externalBaseURL(r)
}

// passkeyOriginFailure maps the service's origin sentinels to explicit client
// errors (logging the full cause) so operators never see a bare 500. Returns
// true when the error was handled.
func passkeyOriginFailure(d Deps, w http.ResponseWriter, op, origin string, err error) bool {
	switch {
	case errors.Is(err, ErrPasskeyInsecureOrigin):
		d.Logger.Printf("[passkey] %s rejected origin=%q: %v", op, origin, err)
		writeError(w, http.StatusBadRequest, errPasskeyInsecureOrigin)
	case errors.Is(err, ErrPasskeyHostUnsupported):
		d.Logger.Printf("[passkey] %s rejected origin=%q: %v", op, origin, err)
		writeError(w, http.StatusBadRequest, errPasskeyUnavailable)
	default:
		return false
	}
	return true
}

// passkeyAccountUser loads the account + ALL its credentials as the WebAuthn
// user view used for a ceremony.
func passkeyAccountUser(r *http.Request, d Deps, userID, email, displayName string) (*PasskeyUser, error) {
	rows, err := store.ListPasskeyCredentials(r.Context(), d.DB, userID)
	if err != nil {
		return nil, err
	}
	user := &PasskeyUser{ID: userID, Email: email, DisplayName: displayName}
	for _, row := range rows {
		user.Credentials = append(user.Credentials, PasskeyCredential{
			CredentialID:       row.CredentialID,
			PublicKey:          row.PublicKey,
			SignCount:          row.SignCount,
			AuthenticatorFlags: row.AuthenticatorFlags,
			FlagsKnown:         row.FlagsKnown,
		})
	}
	return user, nil
}

func passkeyListHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	userID := authUser(r).ID
	rows, err := store.ListPasskeys(r.Context(), d.DB, userID)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("[passkey] list failed user=%s err=%v", userID, err)
		}
		writeError(w, 500, err)
		return
	}
	items := make([]passkeyListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, passkeyListItem{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt})
	}
	writeJSON(w, 200, items)
}

func passkeyRegisterBeginHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if passkeysUnavailable(w, d) || !requirePasskeyLoginEnabled(d, w) {
		return
	}
	u := authUser(r)
	count, err := store.CountPasskeys(r.Context(), d.DB, u.ID)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("[passkey] registration begin: count failed user=%s err=%v", u.ID, err)
		}
		writeError(w, 500, err)
		return
	}
	if count >= maxPasskeysPerUser {
		writeError(w, 400, errPasskeyLimit)
		return
	}
	var req passkeyRegisterBeginReq
	_ = decodeJSON(r, &req) // optional body — a bare begin is fine
	user, err := passkeyAccountUser(r, d, u.ID, u.Email, u.Name)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("[passkey] registration begin: load credentials failed user=%s err=%v", u.ID, err)
		}
		writeError(w, 500, err)
		return
	}
	origin := passkeyRPOrigin(r)
	options, session, err := d.Passkeys.BeginRegistration(origin, user)
	if err != nil {
		if !passkeyOriginFailure(d, w, "registration begin", origin, err) {
			d.Logger.Printf("[passkey] registration begin failed user=%s err=%v", u.ID, err)
			writeError(w, 500, err)
		}
		return
	}
	ticket, err := json.Marshal(passkeyRegisterTicket{Session: session, Name: req.Name})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// One active registration per user: a fresh begin replaces the old challenge.
	d.Cache.Set("pkreg:"+u.ID, string(ticket), passkeyTicketTTL)
	w.Header().Set("Cache-Control", "no-store")
	writeRawJSON(w, 200, options)
}

func passkeyRegisterFinishHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if passkeysUnavailable(w, d) || !requirePasskeyLoginEnabled(d, w) {
		return
	}
	u := authUser(r)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, passkeyBodyLimit))
	if err != nil || len(raw) == 0 || !json.Valid(raw) {
		writeError(w, 400, errInvalidInput)
		return
	}
	cached, ok := d.Cache.Get("pkreg:" + u.ID)
	if !ok {
		writeError(w, 400, errPasskeySetupExpired)
		return
	}
	var ticket passkeyRegisterTicket
	if err := json.Unmarshal([]byte(cached), &ticket); err != nil {
		d.Cache.Delete("pkreg:" + u.ID)
		writeError(w, 400, errPasskeySetupExpired)
		return
	}
	user, err := passkeyAccountUser(r, d, u.ID, u.Email, u.Name)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Printf("[passkey] registration finish: load credentials failed user=%s err=%v", u.ID, err)
		}
		writeError(w, 500, err)
		return
	}
	origin := passkeyRPOrigin(r)
	credential, err := d.Passkeys.FinishRegistration(origin, user, ticket.Session, raw)
	if err != nil {
		if passkeyOriginFailure(d, w, "registration finish", origin, err) {
			return
		}
		d.Logger.Printf("[passkey] registration failed user=%s err=%v", u.ID, err)
		writeError(w, 400, errPasskeyRegistrationFailed)
		return
	}
	if err := store.CreatePasskey(r.Context(), d.DB, &store.Passkey{
		UserID: u.ID, CredentialID: credential.CredentialID, PublicKey: credential.PublicKey,
		SignCount: credential.SignCount, AuthenticatorFlags: credential.AuthenticatorFlags,
		FlagsKnown: credential.FlagsKnown, Name: ticket.Name,
	}); err != nil {
		if d.Logger != nil {
			d.Logger.Printf("[passkey] registration finish: store credential failed user=%s err=%v", u.ID, err)
		}
		writeError(w, 500, err)
		return
	}
	d.Cache.Delete("pkreg:" + u.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func passkeyDeleteHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeletePasskey(r.Context(), d.DB, authUser(r).ID, pathParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrPasskeyNotFound) {
			writeError(w, 404, errors.New("passkey_not_found"))
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func passkeyLoginBeginHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if passkeysUnavailable(w, d) || !requirePasskeyLoginEnabled(d, w) {
		return
	}
	origin := passkeyRPOrigin(r)
	options, session, err := d.Passkeys.BeginLogin(origin)
	if err != nil {
		if !passkeyOriginFailure(d, w, "login begin", origin, err) {
			d.Logger.Printf("[passkey] login begin failed err=%v", err)
			writeError(w, 500, err)
		}
		return
	}
	ticket, err := json.Marshal(passkeyLoginTicket{Session: session})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	key, err := newPasskeyTicketKey()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	d.Cache.Set("pkauth:"+key, string(ticket), passkeyTicketTTL)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, passkeyLoginBeginResp{Ticket: key, Options: options})
}

func passkeyLoginVerifyHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if passkeysUnavailable(w, d) || !requirePasskeyLoginEnabled(d, w) {
		return
	}
	var req passkeyLoginVerifyReq
	if err := decodeJSON(r, &req); err != nil || req.Ticket == "" || len(req.Response) == 0 {
		writeError(w, 400, errInvalidInput)
		return
	}
	failKey := "pkauth:fail:" + req.Ticket
	// Burn only after repeated FAILURES (same posture as the 2FA ticket): a
	// captured ticket cannot be brute-forced for its full TTL, while a single
	// retry after a misread prompt stays possible.
	burnTicket := func() {
		if d.Cache.Incr(failKey, passkeyTicketTTL) >= passkeyTicketBurnThreshold {
			d.Cache.Delete("pkauth:" + req.Ticket)
			d.Cache.Delete(failKey)
		}
	}
	cached, ok := d.Cache.Get("pkauth:" + req.Ticket)
	if !ok {
		// Generic: never reveal whether a ticket/account exists.
		writeError(w, 401, errPasskeyLoginFailed)
		return
	}
	var ticket passkeyLoginTicket
	if err := json.Unmarshal([]byte(cached), &ticket); err != nil {
		_ = d.Cache.CompareAndDelete("pkauth:"+req.Ticket, cached)
		writeError(w, 401, errPasskeyLoginFailed)
		return
	}
	var (
		verifiedUser  *store.User
		verifiedRowID string
	)
	lookup := func(rawID, userHandle []byte) (*PasskeyUser, error) {
		row, err := store.GetPasskeyByCredentialID(r.Context(), d.DB, rawID)
		if err != nil {
			return nil, errPasskeyLoginFailed
		}
		user, err := store.FindUserByID(r.Context(), d.DB, row.UserID)
		if err != nil || user.Status != "active" {
			return nil, errPasskeyLoginFailed
		}
		// The credential must vouch for exactly the account it belongs to.
		if len(userHandle) > 0 && !bytes.Equal(userHandle, []byte(user.ID)) {
			return nil, errPasskeyLoginFailed
		}
		verifiedUser = user
		verifiedRowID = row.ID
		return passkeyAccountUser(r, d, user.ID, user.Email, user.Name)
	}
	// Keep the client-side answer generic (no account/config enumeration),
	// but log the cause so a misconfigured origin is diagnosable server-side.
	credential, err := d.Passkeys.FinishLogin(passkeyRPOrigin(r), ticket.Session, req.Response, lookup)
	if err != nil || verifiedUser == nil {
		burnTicket()
		if d.Logger != nil {
			// The single most useful line when a device's assertion is refused:
			// names the origin and the library's verification failure.
			d.Logger.Printf("[passkey] assertion rejected origin=%q err=%v", passkeyRPOrigin(r), err)
		}
		writeError(w, 401, errPasskeyLoginFailed)
		return
	}
	_ = credential // sign count persisted below
	if !d.Cache.CompareAndDelete("pkauth:"+req.Ticket, cached) {
		// Ticket was already redeemed (or replaced) — one-shot, like the 2FA flow.
		writeError(w, 401, errPasskeyLoginFailed)
		return
	}
	d.Cache.Delete(failKey)
	if err := store.TouchPasskey(r.Context(), d.DB, verifiedRowID, credential.SignCount, credential.AuthenticatorFlags); err != nil && d.Logger != nil {
		d.Logger.Printf("[passkey] touch failed row=%s err=%v", verifiedRowID, err)
	}
	finaliseLoginSession(d, w, r, verifiedUser, store.LoginMethodPasskey)
}

func newPasskeyTicketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// writeRawJSON emits pre-serialized JSON (WebAuthn option documents) untouched.
func writeRawJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
