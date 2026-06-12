package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
// access-token cookie.
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

	// §8.2 防滥用: 邮箱验证（管理员开关 email_verification_required，默认关）。
	// 开启时新用户置 pending 并发出验证链接；无 SMTP 的开发环境把链接打到服务端
	// 日志（接入真实邮件服务的唯一替换点是 sendVerificationMail）。
	verifyRequired := false
	if raw, _ := store.GetSetting(d.DB, "email_verification_required"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &verifyRequired)
	}
	if verifyRequired {
		token := store.GenID("vt") + store.GenID("vt")
		_ = store.SetUserStatus(r.Context(), d.DB, user.ID, "pending")
		_, _ = store.UpdateUserSettings(r.Context(), d.DB, user.ID, map[string]any{"verify_token": token})
		sendVerificationMail(d, user.Email, token)
		writeJSON(w, 200, map[string]any{"verification_required": true})
		return
	}
	finaliseSession(d, w, user)
}

// sendVerificationMail delivers the verification link. Replace this function
// to integrate a real SMTP/ESP provider; the dev default logs the link.
func sendVerificationMail(d Deps, email, token string) {
	d.Logger.Printf("[mail] email verification for %s: POST /api/auth/verify-email {\"email\":%q,\"token\":%q}", email, email, token)
}

// verifyEmailHandler activates a pending account (§8.2 邮箱验证).
func verifyEmailHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Token == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	user, err := store.FindUserByEmail(r.Context(), d.DB, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || user.Status != "pending" {
		writeError(w, 400, errors.New("invalid verification request"))
		return
	}
	var saved string
	if raw, err := store.GetUserSettingKey(r.Context(), d.DB, user.ID, "verify_token"); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &saved)
	}
	if saved == "" || saved != req.Token {
		writeError(w, 400, errors.New("invalid or expired verification token"))
		return
	}
	if err := store.SetUserStatus(r.Context(), d.DB, user.ID, "active"); err != nil {
		writeError(w, 500, err)
		return
	}
	_, _ = store.UpdateUserSettings(r.Context(), d.DB, user.ID, map[string]any{"verify_token": ""})
	user.Status = "active"
	finaliseSession(d, w, user)
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
	finaliseSession(d, w, user)
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
	finaliseSession(d, w, user)
}

func finaliseSession(d Deps, w http.ResponseWriter, user *store.User) {
	access, exp, err := d.Auth.IssueAccess(user.ID, user.Role, user.TokenVer)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	_ = store.SaveRefreshToken(context.Background(), d.DB, jti, user.ID, refreshExp)

	setCookie(w, "auth_token", access, exp, false)
	setCookie(w, "refresh_token", refresh, refreshExp, true)

	writeJSON(w, 200, authResp{User: user, AccessToken: access, ExpiresAt: exp.Unix()})
}

func setCookie(w http.ResponseWriter, name, value string, expires time.Time, restrictPath bool) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		HttpOnly: true,
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
