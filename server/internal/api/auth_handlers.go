package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"aurelia/server/internal/mail"
	"aurelia/server/internal/store"
)

// signupOpenHandler reports whether new registrations are accepted.
func signupOpenHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	raw, err := store.GetSetting(d.DB, "signup_open")
	open := true
	if err == nil {
		_ = json.Unmarshal(raw, &open)
	}
	writeJSON(w, 200, map[string]bool{"open": open})
}

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type authResp struct {
	User        *store.User `json:"user"`
	AccessToken string      `json:"access_token"`
	ExpiresAt   int64       `json:"expires_at"`
}

// registerHandler creates a new account (default role=user) and sets the
// access-token cookie. When email_verification_required is on, the account
// starts as "pending" and a 6-digit code is sent via the configured mailer.
func registerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, 400, errors.New("valid email required"))
		return
	}
	if len(req.Password) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}

	// Domain whitelist check.
	if err := mail.CheckDomainWhitelist(d.DB, req.Email); err != nil {
		writeError(w, 403, err)
		return
	}

	// Check signup open.
	raw, _ := store.GetSetting(d.DB, "signup_open")
	open := true
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &open)
	}
	if !open {
		writeError(w, 403, errors.New("signups are closed"))
		return
	}

	if u, _ := store.FindUserByEmail(r.Context(), d.DB, req.Email); u != nil {
		writeError(w, 409, errors.New("email already registered"))
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	user, err := store.CreateUser(r.Context(), d.DB, req.Email, req.Name, hash)
	if err != nil {
		writeError(w, 500, err)
		return
	}

	// Email verification check.
	verifyRequired := false
	if raw, _ := store.GetSetting(d.DB, "email_verification_required"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &verifyRequired)
	}
	if verifyRequired {
		code := genCode6()
		d.Cache.Set("verify:"+req.Email, code, 10*time.Minute)
		_ = store.SetUserStatus(r.Context(), d.DB, user.ID, "pending")
		// Send off the request path: even with timeouts a slow SMTP server would
		// otherwise make "Create account" spin for seconds. The code is already
		// cached, so the client can move to the code screen immediately; a failed
		// send is logged and the user can hit "resend".
		email := req.Email
		go func() {
			if err := d.Mailer.SendCode(email, code, "verify"); err != nil {
				d.Logger.Printf("[mail] failed to send verification to %s: %v", email, err)
			}
		}()
		writeJSON(w, 200, map[string]any{"verification_required": true, "email": req.Email})
		return
	}
	finaliseSession(d, w, r, user, 0)
}

// sendCodeHandler resends a 6-digit verification code for a pending account.
// Rate-limited at the router level (3/min per IP).
func sendCodeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email   string `json:"email"`
		Purpose string `json:"purpose"` // "verify" | "reset"
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Purpose == "" {
		req.Purpose = "verify"
	}

	// Always return 200 to avoid email-enumeration leaks. We only actually
	// send when the account exists.
	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}

	code := genCode6()
	if req.Purpose == "reset" {
		d.Cache.Set("reset:"+req.Email, code, 10*time.Minute)
	} else {
		if user.Status != "pending" {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		d.Cache.Set("verify:"+req.Email, code, 10*time.Minute)
	}
	if err := d.Mailer.SendCode(req.Email, code, req.Purpose); err != nil {
		d.Logger.Printf("[mail] failed to send %s code to %s: %v", req.Purpose, req.Email, err)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// verifyEmailHandler activates a pending account using a 6-digit code.
func verifyEmailHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Code == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	saved, ok := d.Cache.Get("verify:" + req.Email)
	if !ok || saved != strings.TrimSpace(req.Code) {
		writeError(w, 400, errors.New("invalid or expired verification code"))
		return
	}
	d.Cache.Delete("verify:" + req.Email)

	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil || user.Status != "pending" {
		writeError(w, 400, errors.New("invalid verification request"))
		return
	}
	if err := store.SetUserStatus(r.Context(), d.DB, user.ID, "active"); err != nil {
		writeError(w, 500, err)
		return
	}
	user.Status = "active"
	finaliseSession(d, w, r, user, 0)
}

// forgotPasswordHandler sends a 6-digit reset code to the email.
// Always returns 200 to prevent email enumeration.
func forgotPasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email); err == nil && user != nil {
		code := genCode6()
		d.Cache.Set("reset:"+req.Email, code, 10*time.Minute)
		if err := d.Mailer.SendCode(req.Email, code, "reset"); err != nil {
			d.Logger.Printf("[mail] failed to send reset code to %s: %v", req.Email, err)
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// resetPasswordHandler accepts email + code + new password and updates the
// user's password hash.
func resetPasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Code == "" || req.NewPassword == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if len(req.NewPassword) < 8 {
		writeError(w, 400, errors.New("password must be at least 8 characters"))
		return
	}

	saved, ok := d.Cache.Get("reset:" + req.Email)
	if !ok || saved != strings.TrimSpace(req.Code) {
		writeError(w, 400, errors.New("invalid or expired reset code"))
		return
	}
	d.Cache.Delete("reset:" + req.Email)

	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		writeError(w, 400, errors.New("account not found"))
		return
	}

	hash, err := store.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := store.UpdateUserPassword(r.Context(), d.DB, user.ID, hash); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginHandler verifies credentials and sets the auth cookie.
func loginHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		writeError(w, 401, errors.New("invalid email or password"))
		return
	}
	if user.Status == "pending" {
		writeError(w, 403, errors.New("email not verified"))
		return
	}
	if user.Status != "active" {
		writeError(w, 403, errAccountBlocked)
		return
	}
	hash, err := store.PasswordFor(r.Context(), d.DB, user.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !store.CheckPassword(hash, req.Password) {
		writeError(w, 401, errors.New("invalid email or password"))
		return
	}
	// 2FA gate (§ 2FA login): with TOTP enabled, the password alone doesn't mint
	// a session — return a short-lived ticket the client redeems with a code via
	// /auth/login/2fa.
	if user.TotpEnabled {
		ticket := issueTwofaTicket(d, user.ID)
		if ticket == "" {
			writeError(w, 500, errors.New("could not start two-factor challenge"))
			return
		}
		writeJSON(w, 200, map[string]any{"totp_required": true, "ticket": ticket})
		return
	}
	finaliseSession(d, w, r, user, 0)
}

// logoutHandler clears the cookies. Also revokes the refresh token if present.
func logoutHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil {
		if claims, err := d.Auth.ParseRefresh(c.Value); err == nil {
			_ = store.RevokeRefreshToken(r.Context(), d.DB, claims.ID)
		}
	}
	clearCookie(w, "auth_token")
	clearCookie(w, "refresh_token")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// refreshHandler swaps a refresh token for a new access token.
func refreshHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, 401, errAuthRequired)
		return
	}
	claims, err := d.Auth.ParseRefresh(c.Value)
	if err != nil {
		writeError(w, 401, errAuthRequired)
		return
	}
	ok, err := store.IsRefreshTokenValid(r.Context(), d.DB, claims.ID, claims.UID)
	if err != nil || !ok {
		writeError(w, 401, errSessionExpired)
		return
	}
	user, err := store.FindUserByID(r.Context(), d.DB, claims.UID)
	if err != nil || user.Status != "active" {
		writeError(w, 401, errAccountBlocked)
		return
	}
	// §A2 refresh-token rotation: revoke the presented jti before minting the
	// replacement so a captured refresh token is single-use and can't be replayed
	// for its full 30-day TTL. (finaliseSession issues a fresh jti below.)
	// Carry the original sign-in time forward so the session keeps one stable
	// "signed in" timestamp across rotations instead of resetting every refresh.
	createdAt := store.RefreshTokenCreatedAt(r.Context(), d.DB, claims.ID)
	_ = store.RevokeRefreshToken(r.Context(), d.DB, claims.ID)
	finaliseSession(d, w, r, user, createdAt)
}

func finaliseSession(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, inheritCreatedAt int64) {
	access, exp, err := issueSessionCookies(d, w, r, user, inheritCreatedAt)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, authResp{User: user, AccessToken: access, ExpiresAt: exp.Unix()})
}

// issueSessionCookies mints the access + refresh tokens, persists the refresh
// jti (with the request's device/network context), and writes both cookies.
// Shared by finaliseSession (which then returns JSON) and the OAuth callback
// (which redirects). inheritCreatedAt carries the original sign-in time across
// refresh rotation (0 = a fresh sign-in). Returns the access token and its
// expiry so the JSON path can echo them.
func issueSessionCookies(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, inheritCreatedAt int64) (string, time.Time, error) {
	access, exp, err := d.Auth.IssueAccess(user.ID, user.Role, user.TokenVer)
	if err != nil {
		return "", time.Time{}, err
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	ip := clientIP(r)
	_ = store.SaveRefreshToken(context.Background(), d.DB, jti, user.ID, refreshExp, store.SessionMeta{
		IP:        ip,
		UserAgent: r.UserAgent(),
		Location:  sessionLocation(r, ip),
		CreatedAt: inheritCreatedAt,
	})

	secure := secureCookie(r)
	setCookie(w, "auth_token", access, exp, false, secure)
	setCookie(w, "refresh_token", refresh, refreshExp, true, secure)
	return access, exp, nil
}

// secureCookie reports whether session cookies should carry the Secure flag —
// true only when the browser↔edge connection is HTTPS (directly, or terminated
// by a trusted proxy that sets X-Forwarded-Proto=https). Marking cookies Secure
// on a plain-HTTP site makes browsers drop them, which silently logs the user
// out on the next page load (refresh can't find the refresh_token cookie).
func secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(firstHeader(r, "X-Forwarded-Proto")), "https")
}

// sessionLocation derives a best-effort human location for the request from the
// common reverse-proxy geo headers (Cloudflare etc.). We never call an external
// geo-IP service, so this is empty unless a proxy supplies it; loopback/private
// peers report a local label so self-hosted setups still show something.
func sessionLocation(r *http.Request, ip string) string {
	country := firstHeader(r, "CF-IPCountry", "X-Geo-Country", "X-Country-Code")
	if country == "XX" || country == "T1" { // Cloudflare sentinels: unknown / Tor
		country = ""
	}
	city := firstHeader(r, "CF-IPCity", "X-Geo-City")
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	case country != "":
		return country
	}
	if p := net.ParseIP(ip); p != nil && (p.IsLoopback() || p.IsPrivate()) {
		return "Local network"
	}
	return ""
}

func firstHeader(r *http.Request, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(r.Header.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

// externalBaseURL reconstructs the public scheme://host the browser used,
// honouring the standard reverse-proxy forwarding headers. Used to build OAuth
// redirect URIs and the post-login redirect target.
func externalBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}

func setCookie(w http.ResponseWriter, name, value string, expires time.Time, restrictPath, secure bool) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		HttpOnly: true,
		Secure:   secure,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	}
	if restrictPath {
		c.Path = "/api/auth"
	}
	http.SetCookie(w, c)
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", HttpOnly: true, Path: "/", SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if name == "refresh_token" {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", HttpOnly: true, Path: "/api/auth", SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}

// genCode6 generates a cryptographically random 6-digit code ("000000"–"999999").
// Panics if the OS entropy source is broken — a predictable fallback would be a
// security hole (every account would share the same code).
func genCode6() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic("genCode6: crypto/rand unavailable — refusing to use a predictable code: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}
