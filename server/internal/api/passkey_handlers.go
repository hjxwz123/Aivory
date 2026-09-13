package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

// passkeyAccountUser loads the account + ALL its credentials as the WebAuthn
// user view used for a ceremony.
func passkeyAccountUser(r *http.Request, d Deps, userID, email, displayName string) (*PasskeyUser, error) {
	rows, err := store.ListPasskeyCredentials(r.Context(), d.DB, userID)
	if err != nil {
		return nil, err
	}
	user := &PasskeyUser{ID: userID, Email: email, DisplayName: displayName}
	for _, row := range rows {
		user.Credentials = append(user.Credentials, PasskeyCredential{CredentialID: row.CredentialID, PublicKey: row.PublicKey, SignCount: row.SignCount})
	}
	return user, nil
}

func passkeyListHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListPasskeys(r.Context(), d.DB, authUser(r).ID)
	if err != nil {
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
		writeError(w, 500, err)
		return
	}
	options, session, err := d.Passkeys.BeginRegistration(externalBaseURL(r), user)
	if err != nil {
		writeError(w, 500, err)
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
		writeError(w, 500, err)
		return
	}
	credential, err := d.Passkeys.FinishRegistration(externalBaseURL(r), user, ticket.Session, raw)
	if err != nil {
		d.Logger.Printf("[passkey] registration failed user=%s err=%v", u.ID, err)
		writeError(w, 400, errPasskeyRegistrationFailed)
		return
	}
	if err := store.CreatePasskey(r.Context(), d.DB, &store.Passkey{
		UserID: u.ID, CredentialID: credential.CredentialID, PublicKey: credential.PublicKey,
		SignCount: credential.SignCount, Name: ticket.Name,
	}); err != nil {
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
	options, session, err := d.Passkeys.BeginLogin(externalBaseURL(r))
	if err != nil {
		writeError(w, 500, err)
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
	if d.Cache.Incr(failKey, passkeyTicketTTL) >= passkeyTicketBurnThreshold {
		d.Cache.Delete("pkauth:" + req.Ticket)
		d.Cache.Delete(failKey)
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
	credential, err := d.Passkeys.FinishLogin(externalBaseURL(r), ticket.Session, req.Response, lookup)
	if err != nil || verifiedUser == nil {
		d.Cache.Incr(failKey, passkeyTicketTTL)
		if verifiedUser != nil {
			d.Logger.Printf("[passkey] assertion failed user=%s err=%v", verifiedUser.ID, err)
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
	if err := store.TouchPasskey(r.Context(), d.DB, verifiedRowID, credential.SignCount); err != nil {
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
