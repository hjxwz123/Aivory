package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/mail"
	"aivory/server/internal/store"
)

// maxCodeAttempts is the number of wrong guesses allowed against a single
// emailed verify/reset code before it is burned (§ brute-force). With the code
// burned after 5 misses, the 6-digit space can't be swept across rotating IPs.
var maxCodeAttempts = envcfg.Int("AIVORY_API_MAX_CODE_ATTEMPTS", 5)

// Tunable knobs — envcfg overrides; defaults preserve original behaviour.
var (
	codeFailureCounterTTL    = envcfg.Dur("AIVORY_API_CODE_FAILURE_COUNTER_TTL", 10*time.Minute)
	minimumPasswordLength    = envcfg.Int("AIVORY_API_MINIMUM_PASSWORD_LENGTH", 8)
	emailVerificationCodeTTL = envcfg.Dur("AIVORY_API_EMAIL_VERIFICATION_CODE_TTL", 10*time.Minute)
	passwordResetCodeTTL     = envcfg.Dur("AIVORY_API_PASSWORD_RESET_CODE_TTL", 10*time.Minute)
)

// registerCodeFailure counts wrong guesses of a verify/reset code per email and,
// once maxCodeAttempts is hit, deletes (burns) the code so it can no longer be
// guessed — the user must request a fresh one. Mirrors the 2FA ticket-burn.
// purpose is "verify" | "reset". The counter shares the code's 10-minute TTL.
func registerCodeFailure(d Deps, purpose, email string) {
	if n := d.Cache.Incr("codefail:"+purpose+":"+email, codeFailureCounterTTL); n >= int64(maxCodeAttempts) {
		d.Cache.Delete(purpose + ":" + email)
	}
}

// codeMatches compares an emailed code to user input in constant time, so a
// wrong guess leaks no timing signal about how many leading digits were right.
func codeMatches(saved, input string) bool {
	return subtle.ConstantTimeCompare([]byte(saved), []byte(strings.TrimSpace(input))) == 1
}

// dummyPasswordHash is a real (cost-10) bcrypt hash used to run a constant-time
// verify on the login-with-nonexistent-email path, so timing doesn't reveal
// whether an account exists. The plaintext is irrelevant — the compare always
// fails; only its CPU cost matters.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// signupOpenHandler reports whether new registrations are accepted, and whether
// the registration form must solve the slider-puzzle captcha. The client reads
// both up front so it can render the captcha only when needed.
func signupOpenHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	open := true
	if raw, err := store.GetSetting(d.DB, "signup_open"); err == nil {
		_ = json.Unmarshal(raw, &open)
	}
	captcha := false
	if raw, err := store.GetSetting(d.DB, "register_captcha_required"); err == nil {
		_ = json.Unmarshal(raw, &captcha)
	}
	loginCaptcha := false
	if raw, err := store.GetSetting(d.DB, "login_captcha_required"); err == nil {
		_ = json.Unmarshal(raw, &loginCaptcha)
	}
	writeJSON(w, 200, map[string]bool{"open": open, "captcha_required": captcha, "login_captcha_required": loginCaptcha})
}

// The slider-puzzle captcha (generation + verification) lives in captcha.go.

// needsSetupHandler reports whether the deployment still needs its first-run
// setup — i.e. there are zero user accounts. The client routes to the setup
// screen (create the first admin) when this is true (§ first-run setup).
func needsSetupHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	n, err := store.CountUsers(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"needs_setup": n == 0})
}

// setupHandler creates the FIRST account of a fresh deployment and makes it the
// admin (§ first-run setup). It only works while there are zero users; once any
// account exists it 409s, so it can't be used to mint extra admins. The new
// admin is active immediately (no email-verification gate) and is signed in.
func setupHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	n, err := store.CountUsers(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if n > 0 {
		writeError(w, 409, errAlreadyInitialized)
		return
	}
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, 400, errInvalidEmail)
		return
	}
	if req.Name == "" {
		writeError(w, 400, errNameRequired)
		return
	}
	if len(req.Password) < minimumPasswordLength {
		writeError(w, 400, errPasswordTooShort)
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	user, err := store.CreateUserWithRole(r.Context(), d.DB, req.Email, req.Name, hash, "admin")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	finaliseSession(d, w, r, user, 0)
}

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	// Single-use slider-captcha PASS token from POST /api/public/captcha/verify
	// (only checked when register_captcha_required is on).
	CaptchaToken string `json:"captcha_token"`
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
		writeError(w, 400, errInvalidEmail)
		return
	}
	if len(req.Password) < minimumPasswordLength {
		writeError(w, 400, errPasswordTooShort)
		return
	}

	// First-run guard: a brand-new deployment has zero users and must create its
	// first account through the setup flow (which makes it the admin), never via
	// open registration — otherwise the first signup would be a plain user and the
	// system would have no admin.
	if n, err := store.CountUsers(r.Context(), d.DB); err == nil && n == 0 {
		writeError(w, 409, errSetupRequired)
		return
	}

	// Domain whitelist check. The exact reason (malformed email / domain not
	// listed) is logged-worthy detail only, never shown raw to the client — map
	// to one stable code so every locale gets a real translation.
	if err := mail.CheckDomainWhitelist(d.DB, req.Email); err != nil {
		writeError(w, 403, errEmailDomainNotAllowed)
		return
	}

	// Check signup open.
	raw, _ := store.GetSetting(d.DB, "signup_open")
	open := true
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &open)
	}
	if !open {
		writeError(w, 403, errSignupClosed)
		return
	}

	// Slider-captcha gate. The client solves the puzzle via /captcha/verify, which
	// returns a single-use pass token; we consume it here (single-use whether or
	// not it was valid, so a guessed token can't be hammered).
	captchaRequired := false
	if raw, _ := store.GetSetting(d.DB, "register_captcha_required"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &captchaRequired)
	}
	if captchaRequired {
		if !consumeCaptchaPass(d, req.CaptchaToken) {
			writeError(w, 400, errCaptcha)
			return
		}
	}

	if u, _ := store.FindUserByEmail(r.Context(), d.DB, req.Email); u != nil {
		writeError(w, 409, errEmailAlreadyRegistered)
		return
	}

	// Per-IP daily registration cap (anti-abuse). 0 = unlimited. Reserve a slot
	// by incrementing the day-keyed counter up front; if the account isn't
	// actually created below, the increment is rolled back so failed attempts
	// don't burn the quota.
	ipLimit := 0
	if raw, _ := store.GetSetting(d.DB, "register_ip_daily_limit"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &ipLimit)
	}
	ip := clientIP(r)
	regKey := "regquota:" + ip + ":" + time.Now().Format("2006-01-02")
	if ipLimit > 0 && ip != "" {
		if n := d.Cache.Incr(regKey, 25*time.Hour); int(n) > ipLimit {
			d.Cache.Decr(regKey)
			writeError(w, 429, errRegisterIPLimit)
			return
		}
	}
	releaseQuota := func() {
		if ipLimit > 0 && ip != "" {
			d.Cache.Decr(regKey)
		}
	}

	hash, err := store.HashPassword(req.Password)
	if err != nil {
		releaseQuota()
		writeError(w, 500, err)
		return
	}
	user, err := store.CreateUser(r.Context(), d.DB, req.Email, req.Name, hash)
	if err != nil {
		releaseQuota()
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
		d.Cache.Set("verify:"+req.Email, code, emailVerificationCodeTTL)
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
		d.Cache.Set("reset:"+req.Email, code, passwordResetCodeTTL)
	} else {
		if user.Status != "pending" {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		d.Cache.Set("verify:"+req.Email, code, emailVerificationCodeTTL)
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
	if !ok || !codeMatches(saved, req.Code) {
		registerCodeFailure(d, "verify", req.Email)
		writeError(w, 400, errInvalidOrExpiredCode)
		return
	}
	d.Cache.Delete("verify:" + req.Email)
	d.Cache.Delete("codefail:verify:" + req.Email)

	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil || user.Status != "pending" {
		writeError(w, 400, errInvalidVerificationReq)
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
		d.Cache.Set("reset:"+req.Email, code, passwordResetCodeTTL)
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
	if len(req.NewPassword) < minimumPasswordLength {
		writeError(w, 400, errPasswordTooShort)
		return
	}

	saved, ok := d.Cache.Get("reset:" + req.Email)
	if !ok || !codeMatches(saved, req.Code) {
		registerCodeFailure(d, "reset", req.Email)
		writeError(w, 400, errInvalidOrExpiredCode)
		return
	}
	d.Cache.Delete("reset:" + req.Email)
	d.Cache.Delete("codefail:reset:" + req.Email)

	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		writeError(w, 400, errAccountNotFound)
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
	// Single-use slider-captcha PASS token from POST /api/public/captcha/verify
	// (only checked when login_captcha_required is on — § anti credential-
	// stuffing). Same token shape/verification as registration's captcha_token.
	CaptchaToken string `json:"captcha_token"`
}

// loginHandler verifies credentials and sets the auth cookie.
func loginHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}

	// Slider-captcha gate (admin-toggleable, off by default). Checked BEFORE any
	// account lookup so a captcha-less credential-stuffing run never reaches the
	// bcrypt-timed compare below — it's the cheapest possible reject.
	loginCaptchaRequired := false
	if raw, _ := store.GetSetting(d.DB, "login_captcha_required"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &loginCaptchaRequired)
	}
	if loginCaptchaRequired {
		if !consumeCaptchaPass(d, req.CaptchaToken) {
			writeError(w, 400, errCaptcha)
			return
		}
	}

	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		// Run a dummy verify so a nonexistent account takes the same time as a
		// real one, and return the SAME message — don't let an unauthenticated
		// caller distinguish "no such account" from "wrong password" (account
		// enumeration). State-specific messages (unverified/blocked) are only
		// surfaced AFTER the password is proven correct, below.
		store.CheckPassword(dummyPasswordHash, req.Password)
		writeError(w, 401, errInvalidCredentials)
		return
	}
	hash, err := store.PasswordFor(r.Context(), d.DB, user.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !store.CheckPassword(hash, req.Password) {
		writeError(w, 401, errInvalidCredentials)
		return
	}
	// Password is correct — now it's safe to reveal account state to the holder.
	if user.Status == "pending" {
		writeError(w, 403, errEmailNotVerified)
		return
	}
	if user.Status != "active" {
		writeError(w, 403, errAccountBlocked)
		return
	}
	// 2FA gate (§ 2FA login): with TOTP enabled, the password alone doesn't mint
	// a session — return a short-lived ticket the client redeems with a code via
	// /auth/login/2fa.
	if user.TotpEnabled {
		ticket := issueTwofaTicket(d, user.ID)
		if ticket == "" {
			writeError(w, 500, errTwofaStartFailed)
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
	// A login/refresh is the moment that matters most for token_ver correctness:
	// clear any stale hot auth entry before the browser starts its first burst of
	// authenticated data requests with the newly minted access token.
	invalidateAuthUser(d, user.ID)
	access, exp, err := issueSessionCookies(d, w, r, user, inheritCreatedAt)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	// Carry the tier label + feature flags so the client renders the sidebar
	// group name immediately after login, without waiting for the next /api/me.
	attachGroupInfo(d, r, user)
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
	clearCookie(w, "auth_token")
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

// externalBaseURL reconstructs the public scheme://host the browser used.
// Forwarding headers (X-Forwarded-Host, X-Forwarded-Proto) are only trusted
// when the direct peer is a loopback or private-network address — an upstream
// reverse proxy we control. Public peers must not be allowed to override the
// host, which would enable open-redirect / SSRF attacks via the OAuth callback.
func externalBaseURL(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	if isTrustedPeer(remoteHost) {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
			if proto == "" {
				proto = "https"
			}
			host := strings.TrimSpace(strings.Split(fh, ",")[0])
			return proto + "://" + host
		}
	}
	// Fallback: derive from Host header (safe — set by TLS terminator or listen).
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
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
	switch name {
	case "auth_token":
		// Older builds used narrower paths during auth experiments; clear every
		// variant so a stale /api cookie cannot shadow the fresh "/" cookie.
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", HttpOnly: true, Path: "/api", SameSite: http.SameSiteLaxMode, MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", HttpOnly: true, Path: "/api/auth", SameSite: http.SameSiteLaxMode, MaxAge: -1})
	case "refresh_token":
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
