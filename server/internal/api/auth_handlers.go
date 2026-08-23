package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
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
	codeFailureCounterTTL    = securityDuration("AIVORY_API_CODE_FAILURE_COUNTER_TTL", 10*time.Minute)
	minimumPasswordLength    = securityPasswordMinimum("AIVORY_API_MINIMUM_PASSWORD_LENGTH", 8)
	emailVerificationCodeTTL = securityDuration("AIVORY_API_EMAIL_VERIFICATION_CODE_TTL", 10*time.Minute)
	passwordResetCodeTTL     = securityDuration("AIVORY_API_PASSWORD_RESET_CODE_TTL", 10*time.Minute)
)

const emailSendCooldown = 120 * time.Second

// reserveEmailSend atomically claims the recipient+purpose delivery window.
// Both public reset endpoints use purpose=reset, while registration and the
// verification resend use purpose=verify, so switching endpoints or IPs cannot
// send another email before the same 120-second window expires. Callers invoke
// this before account lookup as well, keeping existing/non-existing recipients
// indistinguishable on repeated requests.
func reserveEmailSend(d Deps, recipient, purpose string) (retryAfter int, allowed bool) {
	retryAfter = int(emailSendCooldown / time.Second)
	if d.Cache == nil {
		return retryAfter, false
	}
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	key := "mailcooldown:" + purpose + ":" + recipient
	if n := d.Cache.Incr(key, emailSendCooldown); n == 1 {
		return retryAfter, true
	}
	if ttl, ok := d.Cache.TTL(key); ok {
		seconds := int((ttl + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		if seconds < retryAfter {
			retryAfter = seconds
		}
	}
	return retryAfter, false
}

func writeEmailCooldown(w http.ResponseWriter, retryAfter int) {
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":       errEmailCooldown.Error(),
		"retry_after": retryAfter,
	})
}

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

func securityBoolSetting(d Deps, key string) (bool, error) {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return false, fmt.Errorf("read security setting %s: %w", key, err)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode security setting %s: %w", key, err)
	}
	return value, nil
}

func securityNonNegativeIntSetting(d Deps, key string) (int, error) {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return 0, fmt.Errorf("read security setting %s: %w", key, err)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("decode security setting %s: %w", key, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("security setting %s cannot be negative", key)
	}
	return value, nil
}

// signupOpenHandler reports whether new registrations are accepted, and whether
// the registration form must solve the slider-puzzle captcha. The client reads
// both up front so it can render the captcha only when needed.
func signupOpenHandler(d Deps, w http.ResponseWriter, _ *http.Request) {
	open, err := securityBoolSetting(d, "signup_open")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	captcha, err := securityBoolSetting(d, "register_captcha_required")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	loginCaptcha, err := securityBoolSetting(d, "login_captcha_required")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
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
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	email, err := store.NormalizeUserEmail(req.Email)
	if err != nil {
		writeError(w, 400, errInvalidEmail)
		return
	}
	req.Email = email
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, 400, errNameRequired)
		return
	}
	if err := validateNewPassword(req.Password, minimumPasswordLength); err != nil {
		writeError(w, 400, err)
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	user, err := store.CreateInitialAdmin(r.Context(), d.DB, req.Email, req.Name, hash)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyInitialized) {
			writeError(w, 409, errAlreadyInitialized)
			return
		}
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

// authSessionResp is used only for the browser's startup session probe. A
// missing or expired refresh cookie is an expected state on the login screen,
// so that probe reports it as a normal 200 response instead of an HTTP error.
type authSessionResp struct {
	Authenticated bool        `json:"authenticated"`
	User          *store.User `json:"user,omitempty"`
	AccessToken   string      `json:"access_token,omitempty"`
	ExpiresAt     int64       `json:"expires_at,omitempty"`
}

// registerHandler creates a new account (default role=user) and sets the
// access-token cookie. When email_verification_required is on, the account
// starts as "pending" and a 6-digit code is sent via the configured mailer.
func registerHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if !requirePasswordLoginEnabled(d, w) {
		return
	}
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	email, err := store.NormalizeUserEmail(req.Email)
	if err != nil {
		writeError(w, 400, errInvalidEmail)
		return
	}
	req.Email = email
	if err := validateNewPassword(req.Password, minimumPasswordLength); err != nil {
		writeError(w, 400, err)
		return
	}

	// First-run guard: a brand-new deployment has zero users and must create its
	// first account through the setup flow (which makes it the admin), never via
	// open registration — otherwise the first signup would be a plain user and the
	// system would have no admin.
	n, err := store.CountUsers(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n == 0 {
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
	open, err := securityBoolSetting(d, "signup_open")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !open {
		writeError(w, 403, errSignupClosed)
		return
	}

	// Resolve the account's initial lifecycle state before INSERT. A malformed or
	// unreadable security setting fails closed instead of creating an active user.
	verifyRequired, err := securityBoolSetting(d, "email_verification_required")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Slider-captcha gate. The client solves the puzzle via /captcha/verify, which
	// returns a single-use pass token; we consume it here (single-use whether or
	// not it was valid, so a guessed token can't be hammered).
	captchaRequired, err := securityBoolSetting(d, "register_captcha_required")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
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
	ipLimit, err := securityNonNegativeIntSetting(d, "register_ip_daily_limit")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ip := clientIP(r)
	regKey := "regquota:" + ip + ":" + time.Now().Format("2006-01-02")
	quotaReserved := false
	if ipLimit > 0 {
		if ip == "" || d.Cache == nil {
			writeError(w, http.StatusInternalServerError, errors.New("registration quota enforcement unavailable"))
			return
		}
		n := d.Cache.Incr(regKey, 25*time.Hour)
		if n <= 0 {
			writeError(w, http.StatusInternalServerError, errors.New("registration quota enforcement unavailable"))
			return
		}
		if int(n) > ipLimit {
			d.Cache.Decr(regKey)
			writeError(w, 429, errRegisterIPLimit)
			return
		}
		quotaReserved = true
	}
	releaseQuota := func() {
		if quotaReserved {
			d.Cache.Decr(regKey)
		}
	}

	hash, err := store.HashPassword(req.Password)
	if err != nil {
		releaseQuota()
		writeError(w, 500, err)
		return
	}
	initialStatus := "active"
	if verifyRequired {
		initialStatus = "pending"
	}
	user, err := store.CreateUserWithState(r.Context(), d.DB, req.Email, req.Name, hash, "user", initialStatus, true)
	if err != nil {
		releaseQuota()
		writeError(w, 500, err)
		return
	}

	if verifyRequired {
		retryAfter, allowed := reserveEmailSend(d, req.Email, "verify")
		if allowed {
			code := genCode6()
			d.Cache.Set("verify:"+req.Email, code, emailVerificationCodeTTL)
			// Send off the request path: even with timeouts a slow SMTP server would
			// otherwise make "Create account" spin for seconds. The code is already
			// cached, so the client can move to the code screen immediately; a failed
			// send is logged and the user can retry after the cooldown.
			email := req.Email
			go func() {
				if err := d.Mailer.SendCode(email, code, "verify"); err != nil {
					d.Logger.Printf("[mail] failed to send verification to %s: %v", email, err)
				}
			}()
		}
		writeJSON(w, 200, map[string]any{
			"verification_required": true,
			"email":                 req.Email,
			"retry_after":           retryAfter,
		})
		return
	}
	finaliseSession(d, w, r, user, 0)
}

// sendCodeHandler resends a 6-digit verification or password-reset code.
// The router's IP budget is supplemented by the authoritative recipient and
// purpose cooldown shared with registration and forgotPasswordHandler.
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
	req.Purpose = strings.ToLower(strings.TrimSpace(req.Purpose))
	if req.Purpose == "" {
		req.Purpose = "verify"
	}
	if req.Purpose != "verify" && req.Purpose != "reset" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if req.Purpose == "reset" && !requirePasswordLoginEnabled(d, w) {
		return
	}
	retryAfter, allowed := reserveEmailSend(d, req.Email, req.Purpose)
	if !allowed {
		writeEmailCooldown(w, retryAfter)
		return
	}

	// For an accepted cooldown reservation, return the same success response
	// whether or not the account exists. Nonexistent/invalid-state recipients
	// consume the same cooldown too, avoiding email-enumeration leaks.
	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "retry_after": retryAfter})
		return
	}

	code := genCode6()
	if req.Purpose == "reset" {
		d.Cache.Set("reset:"+req.Email, code, passwordResetCodeTTL)
	} else {
		if user.Status != "pending" {
			writeJSON(w, 200, map[string]any{"ok": true, "retry_after": retryAfter})
			return
		}
		d.Cache.Set("verify:"+req.Email, code, emailVerificationCodeTTL)
	}
	if err := d.Mailer.SendCode(req.Email, code, req.Purpose); err != nil {
		d.Logger.Printf("[mail] failed to send %s code to %s: %v", req.Purpose, req.Email, err)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "retry_after": retryAfter})
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
	user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email)
	if err != nil || user.Status != "pending" {
		writeError(w, 400, errInvalidVerificationReq)
		return
	}
	if !d.Cache.CompareAndDelete("verify:"+req.Email, saved) {
		writeError(w, 400, errInvalidOrExpiredCode)
		return
	}
	d.Cache.Delete("codefail:verify:" + req.Email)
	activated, err := store.ActivatePendingUser(r.Context(), d.DB, user.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if !activated {
		writeError(w, 400, errInvalidVerificationReq)
		return
	}
	user.Status = "active"
	finaliseSession(d, w, r, user, 0)
}

// forgotPasswordHandler sends a 6-digit reset code to the email. Existing and
// nonexistent recipients receive the same success or cooldown response so the
// endpoint cannot be used to enumerate accounts.
func forgotPasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if !requirePasswordLoginEnabled(d, w) {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	retryAfter, allowed := reserveEmailSend(d, req.Email, "reset")
	if !allowed {
		writeEmailCooldown(w, retryAfter)
		return
	}

	if user, err := store.FindUserByEmail(r.Context(), d.DB, req.Email); err == nil && user != nil {
		code := genCode6()
		d.Cache.Set("reset:"+req.Email, code, passwordResetCodeTTL)
		if err := d.Mailer.SendCode(req.Email, code, "reset"); err != nil {
			d.Logger.Printf("[mail] failed to send reset code to %s: %v", req.Email, err)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "retry_after": retryAfter})
}

// resetPasswordHandler accepts email + code + new password and updates the
// user's password hash.
func resetPasswordHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if !requirePasswordLoginEnabled(d, w) {
		return
	}
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
	if err := validateNewPassword(req.NewPassword, minimumPasswordLength); err != nil {
		writeError(w, 400, err)
		return
	}

	saved, ok := d.Cache.Get("reset:" + req.Email)
	if !ok || !codeMatches(saved, req.Code) {
		registerCodeFailure(d, "reset", req.Email)
		writeError(w, 400, errInvalidOrExpiredCode)
		return
	}
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
	if !d.Cache.CompareAndDelete("reset:"+req.Email, saved) {
		writeError(w, 400, errInvalidOrExpiredCode)
		return
	}
	d.Cache.Delete("codefail:reset:" + req.Email)
	if err := store.UpdateUserPassword(r.Context(), d.DB, user.ID, hash); err != nil {
		writeError(w, 500, err)
		return
	}
	invalidateAuthUser(d, user.ID)
	if d.Cache != nil {
		d.Cache.Publish("user:"+user.ID+":kill", "password_reset")
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
	if !requirePasswordLoginEnabled(d, w) {
		return
	}
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}

	// Slider-captcha gate (admin-toggleable, off by default). Checked BEFORE any
	// account lookup so a captcha-less credential-stuffing run never reaches the
	// bcrypt-timed compare below — it's the cheapest possible reject.
	loginCaptchaRequired, err := securityBoolSetting(d, "login_captcha_required")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
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
		ticket := issueTwofaTicket(d, user.ID, user.TokenVer, store.LoginMethodPassword)
		if ticket == "" {
			writeError(w, 500, errTwofaStartFailed)
			return
		}
		writeJSON(w, 200, map[string]any{"totp_required": true, "ticket": ticket})
		return
	}
	finaliseLoginSession(d, w, r, user, store.LoginMethodPassword)
}

// logoutHandler clears the cookies. Also revokes the refresh token if present.
func logoutHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil {
		if claims, err := d.Auth.ParseRefresh(c.Value); err == nil {
			_, _ = store.RevokeUserSession(r.Context(), d.DB, claims.UID, claims.ID)
		}
	}
	clearCookie(w, "auth_token")
	clearCookie(w, "refresh_token")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type refreshAuthFailure struct {
	reason error
}

func (e *refreshAuthFailure) Error() string { return e.reason.Error() }
func (e *refreshAuthFailure) Unwrap() error { return e.reason }

type refreshedSession struct {
	user       *store.User
	access     string
	accessExp  time.Time
	refresh    string
	refreshExp time.Time
}

// refreshSession rotates the presented refresh token and returns the new token
// pair. Authentication failures are marked separately from operational errors
// so the startup session probe can report "not signed in" without weakening the
// existing /auth/refresh API contract.
func refreshSession(d Deps, r *http.Request) (*refreshedSession, error) {
	c, err := r.Cookie("refresh_token")
	if err != nil {
		return nil, &refreshAuthFailure{reason: errAuthRequired}
	}
	claims, err := d.Auth.ParseRefresh(c.Value)
	if err != nil {
		return nil, &refreshAuthFailure{reason: errAuthRequired}
	}
	user, err := store.FindUserByID(r.Context(), d.DB, claims.UID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &refreshAuthFailure{reason: errAccountBlocked}
		}
		return nil, err
	}
	if user.Status != "active" {
		return nil, &refreshAuthFailure{reason: errAccountBlocked}
	}
	if user.TokenVer != claims.TV {
		return nil, &refreshAuthFailure{reason: errSessionExpired}
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		return nil, err
	}
	sessionID, err := store.RotateRefreshToken(
		r.Context(), d.DB, claims.ID, claims.UID, claims.TV,
		jti, refreshExp, sessionMeta(r, 0),
	)
	if err != nil {
		if errors.Is(err, store.ErrInvalidRefreshToken) {
			return nil, &refreshAuthFailure{reason: errSessionExpired}
		}
		return nil, err
	}
	access, exp, err := d.Auth.IssueAccessForSession(user.ID, user.Role, user.TokenVer, sessionID)
	if err != nil {
		_, _ = store.RevokeUserSession(r.Context(), d.DB, user.ID, jti)
		return nil, err
	}
	return &refreshedSession{
		user:       user,
		access:     access,
		accessExp:  exp,
		refresh:    refresh,
		refreshExp: refreshExp,
	}, nil
}

func writeRefreshedSession(d Deps, w http.ResponseWriter, r *http.Request, session *refreshedSession, response any) {
	invalidateAuthUser(d, session.user.ID)
	setSessionCookies(w, r, session.access, session.accessExp, session.refresh, session.refreshExp)
	attachGroupInfo(d, r, session.user)
	writeJSON(w, http.StatusOK, response)
}

// refreshHandler swaps a refresh token for a new access token. This endpoint
// retains HTTP 401 for callers that explicitly request a token refresh.
func refreshHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	session, err := refreshSession(d, r)
	if err != nil {
		var authFailure *refreshAuthFailure
		if errors.As(err, &authFailure) {
			writeError(w, http.StatusUnauthorized, authFailure.reason)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeRefreshedSession(d, w, r, session, authResp{User: session.user, AccessToken: session.access, ExpiresAt: session.accessExp.Unix()})
}

// sessionHandler is the browser-only startup/renewal probe. It keeps a normal
// logged-out page from generating an expected 401 in DevTools, while protected
// endpoints and the explicit /auth/refresh contract continue to use 401.
func sessionHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	session, err := refreshSession(d, r)
	if err != nil {
		var authFailure *refreshAuthFailure
		if errors.As(err, &authFailure) {
			clearCookie(w, "auth_token")
			clearCookie(w, "refresh_token")
			writeJSON(w, http.StatusOK, authSessionResp{Authenticated: false})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeRefreshedSession(d, w, r, session, authSessionResp{
		Authenticated: true,
		User:          session.user,
		AccessToken:   session.access,
		ExpiresAt:     session.accessExp.Unix(),
	})
}

func finaliseSession(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, inheritCreatedAt int64) {
	finaliseSessionResponse(d, w, r, user, inheritCreatedAt, "")
}

// finaliseLoginSession is the fresh-login counterpart to finaliseSession.
// Keeping the method explicit prevents refresh-token rotation (which calls
// finaliseSession above) from creating duplicate login-history rows.
func finaliseLoginSession(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, method string) {
	finaliseSessionResponseWithOAuthGuard(
		d, w, r, user, 0, method, nil, store.OAuthCallbackSessionWithout2FA,
	)
}

func finaliseOAuthLoginSession(
	d Deps,
	w http.ResponseWriter,
	r *http.Request,
	user *store.User,
	method string,
	guard store.OAuthProviderCallbackGuard,
) {
	finaliseSessionResponseWithOAuthGuard(
		d, w, r, user, 0, method, &guard, store.OAuthCallbackSessionWithVerified2FA,
	)
}

func finaliseSessionResponse(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, inheritCreatedAt int64, loginMethod string) {
	finaliseSessionResponseWithOAuthGuard(
		d, w, r, user, inheritCreatedAt, loginMethod, nil, store.OAuthCallbackSessionWithout2FA,
	)
}

func finaliseSessionResponseWithOAuthGuard(
	d Deps,
	w http.ResponseWriter,
	r *http.Request,
	user *store.User,
	inheritCreatedAt int64,
	loginMethod string,
	guard *store.OAuthProviderCallbackGuard,
	authMode store.OAuthCallbackSessionAuthMode,
) {
	// A login/refresh is the moment that matters most for token_ver correctness:
	// clear any stale hot auth entry before the browser starts its first burst of
	// authenticated data requests with the newly minted access token.
	invalidateAuthUser(d, user.ID)
	access, exp, err := issueSessionCookiesWithOAuthGuard(d, w, r, user, inheritCreatedAt, guard, authMode)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if loginMethod != "" {
		recordSuccessfulLogin(d, r, user.ID, loginMethod)
	}
	// Carry the tier label + feature flags so the client renders the sidebar
	// group name immediately after login, without waiting for the next /api/me.
	attachGroupInfo(d, r, user)
	writeJSON(w, 200, authResp{User: user, AccessToken: access, ExpiresAt: exp.Unix()})
}

// recordSuccessfulLogin is deliberately best-effort after the session has
// already been minted. A transient audit-write failure must not return a 500
// alongside valid cookies; it is logged for operators instead.
func recordSuccessfulLogin(d Deps, r *http.Request, userID, method string) {
	if _, err := store.RecordLoginHistory(context.Background(), d.DB, userID, method, sessionMeta(r, 0)); err != nil && d.Logger != nil {
		d.Logger.Printf("[auth] record login history for user=%s method=%s: %v", userID, method, err)
	}
}

// issueSessionCookies mints the access + refresh tokens, persists the refresh
// jti (with the request's device/network context), and writes both cookies.
// Shared by finaliseSession (which then returns JSON) and the OAuth callback
// (which redirects). inheritCreatedAt carries the original sign-in time across
// refresh rotation (0 = a fresh sign-in). Returns the access token and its
// expiry so the JSON path can echo them.
func issueSessionCookies(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, inheritCreatedAt int64) (string, time.Time, error) {
	return issueSessionCookiesWithOAuthGuard(
		d, w, r, user, inheritCreatedAt, nil, store.OAuthCallbackSessionWithout2FA,
	)
}

func issueSessionCookiesWithOAuthGuard(
	d Deps,
	w http.ResponseWriter,
	r *http.Request,
	user *store.User,
	inheritCreatedAt int64,
	guard *store.OAuthProviderCallbackGuard,
	authMode store.OAuthCallbackSessionAuthMode,
) (string, time.Time, error) {
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		return "", time.Time{}, err
	}
	meta := sessionMeta(r, inheritCreatedAt)
	if guard != nil {
		// Mint both signed values before the guarded INSERT. The INSERT is then the
		// sole session-issuance linearization point: if an administrator provider
		// write commits first it fails, while a write which waits for this transaction
		// necessarily happens after the complete token pair was authorized.
		access, exp, err := d.Auth.IssueAccessForSession(user.ID, user.Role, user.TokenVer, jti)
		if err != nil {
			return "", time.Time{}, err
		}
		expectedTotpSecret := ""
		if authMode == store.OAuthCallbackSessionWithVerified2FA {
			expectedTotpSecret = user.TotpSecret
		}
		if err := store.SaveRefreshTokenForOAuthCallback(
			r.Context(), d.DB, *guard, jti, user.ID, user.TokenVer,
			authMode, expectedTotpSecret, refreshExp, meta,
		); err != nil {
			return "", time.Time{}, err
		}
		setSessionCookies(w, r, access, exp, refresh, refreshExp)
		return access, exp, nil
	}
	if err := store.SaveRefreshToken(r.Context(), d.DB, jti, user.ID, refreshExp, meta); err != nil {
		return "", time.Time{}, err
	}
	access, exp, err := d.Auth.IssueAccessForSession(user.ID, user.Role, user.TokenVer, jti)
	if err != nil {
		_, _ = store.RevokeUserSession(r.Context(), d.DB, user.ID, jti)
		return "", time.Time{}, err
	}

	setSessionCookies(w, r, access, exp, refresh, refreshExp)
	return access, exp, nil
}

func setSessionCookies(w http.ResponseWriter, r *http.Request, access string, accessExp time.Time, refresh string, refreshExp time.Time) {
	secure := secureCookie(r)
	clearCookie(w, "auth_token")
	setCookie(w, "auth_token", access, accessExp, false, secure)
	setCookie(w, "refresh_token", refresh, refreshExp, true, secure)
}

func sessionMeta(r *http.Request, createdAt int64) store.SessionMeta {
	ip := clientIP(r)
	return store.SessionMeta{
		IP:        ip,
		UserAgent: r.UserAgent(),
		Location:  sessionLocation(r, ip),
		CreatedAt: createdAt,
	}
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
