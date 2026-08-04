package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func authInputErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return payload.Error
}

func TestAccountCreationHandlersRejectMalformedMailbox(t *testing.T) {
	const malformed = "victim@example.test@evil.test"

	t.Run("setup", func(t *testing.T) {
		d := newAuthSecurityDeps(t, "setup-invalid-email.db")
		rec := runAuthJSONHandler(t, d, setupHandler, "/api/auth/setup",
			fmt.Sprintf(`{"email":%q,"name":"Admin","password":"password123"}`, malformed))
		if rec.Code != http.StatusBadRequest || authInputErrorCode(t, rec) != errInvalidEmail.Error() {
			t.Fatalf("setup status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	for _, tc := range []struct {
		name string
		run  func(*testing.T, Deps) *httptest.ResponseRecorder
	}{
		{name: "register", run: func(t *testing.T, d Deps) *httptest.ResponseRecorder {
			return runAuthJSONHandler(t, d, registerHandler, "/api/auth/register",
				fmt.Sprintf(`{"email":%q,"name":"User","password":"password123"}`, malformed))
		}},
		{name: "admin create", run: func(t *testing.T, d Deps) *httptest.ResponseRecorder {
			return runAuthJSONHandler(t, d, createUserAdmin, "/api/admin/users",
				fmt.Sprintf(`{"email":%q,"name":"User","password":"password123"}`, malformed))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthSecurityDeps(t, strings.ReplaceAll(tc.name, " ", "-")+"-invalid-email.db")
			if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
				t.Fatalf("create admin: %v", err)
			}
			rec := tc.run(t, d)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var count int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("users after rejected creation = %d, err=%v", count, err)
			}
		})
	}
}

func TestOAuthSignupRejectsMalformedProviderMailbox(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	provider := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	info := oauth.UserInfo{
		Subject:       "malformed-email-subject",
		Email:         "victim@example.test@evil.test",
		EmailVerified: true,
	}
	user, err := resolveOAuthUser(context.Background(), d, provider, info, oauthSignupContext{IP: "192.0.2.40"})
	if user != nil || !errors.Is(err, errInvalidEmail) {
		t.Fatalf("OAuth malformed email = user %+v err %v, want nil/errInvalidEmail", user, err)
	}
	var identities int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM oauth_identities`).Scan(&identities); err != nil || identities != 0 {
		t.Fatalf("OAuth malformed email identities=%d err=%v", identities, err)
	}
}

func TestEveryPasswordSettingHandlerUsesCharacterAndBcryptLimits(t *testing.T) {
	oldMinimum := minimumPasswordLength
	oldCreatedMinimum := adminCreatedUserMinPasswordLength
	oldResetMinimum := adminPasswordResetMinLength
	minimumPasswordLength = 8
	adminCreatedUserMinPasswordLength = 8
	adminPasswordResetMinLength = 8
	t.Cleanup(func() {
		minimumPasswordLength = oldMinimum
		adminCreatedUserMinPasswordLength = oldCreatedMinimum
		adminPasswordResetMinLength = oldResetMinimum
	})

	badPasswords := []struct {
		name     string
		password string
		wantCode string
	}{
		{name: "seven Unicode characters", password: strings.Repeat("密", 7), wantCode: errPasswordTooShort.Error()},
		{name: "bcrypt byte overflow", password: strings.Repeat("a", bcryptMaxPasswordBytes+1), wantCode: errPasswordTooLong.Error()},
	}

	endpoints := []struct {
		name string
		run  func(*testing.T, string) *httptest.ResponseRecorder
	}{
		{name: "setup", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-setup.db")
			return runAuthJSONHandler(t, d, setupHandler, "/api/auth/setup",
				fmt.Sprintf(`{"email":"admin@example.test","name":"Admin","password":%q}`, password))
		}},
		{name: "register", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-register.db")
			if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
				t.Fatalf("create admin: %v", err)
			}
			return runAuthJSONHandler(t, d, registerHandler, "/api/auth/register",
				fmt.Sprintf(`{"email":"user@example.test","name":"User","password":%q}`, password))
		}},
		{name: "admin create", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-admin-create.db")
			return runAuthJSONHandler(t, d, createUserAdmin, "/api/admin/users",
				fmt.Sprintf(`{"email":"user@example.test","name":"User","password":%q}`, password))
		}},
		{name: "reset", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-reset.db")
			user, err := store.CreateUser(t.Context(), d.DB, "user@example.test", "User", "hash")
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			d.Cache.Set("reset:"+user.Email, "123456", time.Minute)
			return runAuthJSONHandler(t, d, resetPasswordHandler, "/api/auth/reset-password",
				fmt.Sprintf(`{"email":"user@example.test","code":"123456","new_password":%q}`, password))
		}},
		{name: "change", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-change.db")
			user, err := store.CreateUser(t.Context(), d.DB, "user@example.test", "User", "hash")
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/me/change-password", strings.NewReader(
				fmt.Sprintf(`{"current_password":"old-password","new_password":%q}`, password)))
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
			rec := httptest.NewRecorder()
			changePasswordHandler(d, rec, req)
			return rec
		}},
		{name: "initial set", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-initial-set.db")
			user, err := store.CreateUserWithState(t.Context(), d.DB, "user@example.test", "User", "hash", "user", "active", false)
			if err != nil {
				t.Fatalf("create OAuth-only user: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/me/set-password", strings.NewReader(
				fmt.Sprintf(`{"new_password":%q}`, password)))
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
			rec := httptest.NewRecorder()
			setPasswordHandler(d, rec, req)
			return rec
		}},
		{name: "admin reset", run: func(t *testing.T, password string) *httptest.ResponseRecorder {
			d := newAuthSecurityDeps(t, "password-admin-reset.db")
			user, err := store.CreateUser(t.Context(), d.DB, "user@example.test", "User", "hash")
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/admin/users/"+user.ID+"/password", strings.NewReader(
				fmt.Sprintf(`{"new_password":%q}`, password)))
			req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": user.ID}))
			rec := httptest.NewRecorder()
			setUserPasswordAdmin(d, rec, req)
			return rec
		}},
	}

	for _, endpoint := range endpoints {
		for _, bad := range badPasswords {
			t.Run(endpoint.name+"/"+bad.name, func(t *testing.T) {
				rec := endpoint.run(t, bad.password)
				if rec.Code != http.StatusBadRequest || authInputErrorCode(t, rec) != bad.wantCode {
					t.Fatalf("status=%d body=%s, want 400/%s", rec.Code, rec.Body.String(), bad.wantCode)
				}
			})
		}
	}
}

func TestAuthJSONRejectsInvalidUTF8BeforePasswordValidation(t *testing.T) {
	d := newAuthSecurityDeps(t, "password-invalid-utf8.db")
	body := append([]byte(`{"email":"admin@example.test","name":"Admin","password":"`), bytes.Repeat([]byte{0xff}, 8)...)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	setupHandler(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	var users int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("invalid UTF-8 persisted users=%d err=%v", users, err)
	}
}

func TestLoginTreatsBcryptOverflowAsInvalidCredentials(t *testing.T) {
	d := newAuthSecurityDeps(t, "password-login-overflow.db")
	hash, err := store.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := store.CreateUser(t.Context(), d.DB, "user@example.test", "User", hash); err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := runAuthJSONHandler(t, d, loginHandler, "/api/auth/login",
		fmt.Sprintf(`{"email":"user@example.test","password":%q}`, strings.Repeat("a", bcryptMaxPasswordBytes+1)))
	if rec.Code != http.StatusUnauthorized || authInputErrorCode(t, rec) != errInvalidCredentials.Error() {
		t.Fatalf("overlong login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSecurityCredentialTTLsAlwaysUsePositiveValues(t *testing.T) {
	for name, ttl := range map[string]time.Duration{
		"code failure counter": codeFailureCounterTTL,
		"email verification":   emailVerificationCodeTTL,
		"password reset":       passwordResetCodeTTL,
		"captcha challenge":    captchaChallengeCacheTTL,
		"captcha pass":         captchaPassTTL,
		"OAuth 2FA cookie":     oauth2FAHandoffCookieTTL,
		"OAuth state":          oauthStateCacheTTL,
		"OAuth exchange":       oauthTokenExchangeCtxTimeout,
		"OAuth handoff":        oauthCrossDomainHandoffTokenTTL,
		"2FA ticket":           issueTwofaTicketTTL,
	} {
		if ttl <= 0 {
			t.Errorf("%s TTL = %s, want positive", name, ttl)
		}
	}

	const durationKey = "AIVORY_TEST_SECURITY_DURATION"
	t.Setenv(durationKey, "0s")
	if got := securityDuration(durationKey, time.Minute); got != time.Minute {
		t.Fatalf("zero security duration = %s, want fallback", got)
	}
	t.Setenv(durationKey, "-1m")
	if got := securityDuration(durationKey, time.Minute); got != time.Minute {
		t.Fatalf("negative security duration = %s, want fallback", got)
	}

	const intKey = "AIVORY_TEST_PASSWORD_MINIMUM"
	t.Setenv(intKey, "0")
	if got := securityPasswordMinimum(intKey, 8); got != 8 {
		t.Fatalf("zero security integer = %d, want fallback", got)
	}
	t.Setenv(intKey, "100")
	if got := securityPasswordMinimum(intKey, 8); got != 8 {
		t.Fatalf("impossible password minimum = %d, want fallback", got)
	}
	t.Setenv(intKey, "16")
	if got := securityPasswordMinimum(intKey, 8); got != 16 {
		t.Fatalf("valid password minimum = %d, want 16", got)
	}
}
