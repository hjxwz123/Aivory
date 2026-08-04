package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func newAuthSecurityDeps(t *testing.T, name string) Deps {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	store.InvalidateConfig()
	c := cache.NewMemory()
	return Deps{
		DB:     db,
		Cache:  c,
		Auth:   authsvc.New("auth-security-regression-secret", time.Hour, 24*time.Hour, c),
		Mailer: newRecordingCodeMailer(),
		Logger: log.New(io.Discard, "", 0),
	}
}

func TestRegistrationCreatesPendingUserInInitialInsert(t *testing.T) {
	d := newAuthSecurityDeps(t, "pending-registration.db")
	if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := store.SetSetting(d.DB, "email_verification_required", true); err != nil {
		t.Fatalf("enable verification: %v", err)
	}
	store.InvalidateConfig()

	rec := runAuthJSONHandler(t, d, registerHandler, "/api/auth/register", `{"email":"pending@example.test","name":"Pending","password":"password123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}
	user, err := store.FindUserByEmail(t.Context(), d.DB, "pending@example.test")
	if err != nil {
		t.Fatalf("find registered user: %v", err)
	}
	if user.Status != "pending" {
		t.Fatalf("registered user status=%q, want pending", user.Status)
	}
}

func TestRegistrationFailsClosedOnMalformedVerificationSetting(t *testing.T) {
	d := newAuthSecurityDeps(t, "malformed-verification-setting.db")
	if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE settings SET value='not-json' WHERE key='email_verification_required'`); err != nil {
		t.Fatalf("corrupt setting: %v", err)
	}
	store.InvalidateConfig()

	rec := runAuthJSONHandler(t, d, registerHandler, "/api/auth/register", `{"email":"must-not-exist@example.test","name":"Nope","password":"password123"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("register status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
	if user, _ := store.FindUserByEmail(t.Context(), d.DB, "must-not-exist@example.test"); user != nil {
		t.Fatalf("registration created an account despite malformed verification policy: %+v", user)
	}
}

func TestRegistrationFailsClosedOnMalformedSecuritySettings(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "signup open", key: "signup_open", value: "not-json"},
		{name: "register captcha", key: "register_captcha_required", value: "not-json"},
		{name: "registration ip limit malformed", key: "register_ip_daily_limit", value: "not-json"},
		{name: "registration ip limit negative", key: "register_ip_daily_limit", value: "-1"},
		{name: "missing signup setting", key: "signup_open", value: "__delete__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "malformed-"+strings.ReplaceAll(tc.key, "_", "-")+".db")
			if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
				t.Fatalf("create admin: %v", err)
			}
			if tc.value == "__delete__" {
				if _, err := d.DB.Exec(`DELETE FROM settings WHERE key=?`, tc.key); err != nil {
					t.Fatalf("delete setting: %v", err)
				}
			} else if _, err := d.DB.Exec(`UPDATE settings SET value=? WHERE key=?`, tc.value, tc.key); err != nil {
				t.Fatalf("corrupt setting: %v", err)
			}
			store.InvalidateConfig()
			email := strings.ReplaceAll(tc.key, "_", "-") + "@example.test"
			rec := runAuthJSONHandler(t, d, registerHandler, "/api/auth/register", fmt.Sprintf(`{"email":%q,"name":"Nope","password":"password123"}`, email))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("register status=%d body=%s, want 500", rec.Code, rec.Body.String())
			}
			if user, _ := store.FindUserByEmail(t.Context(), d.DB, email); user != nil {
				t.Fatalf("registration created an account despite invalid %s policy: %+v", tc.key, user)
			}
		})
	}
}

func TestSignupStatusAndLoginFailClosedOnMalformedSecuritySettings(t *testing.T) {
	d := newAuthSecurityDeps(t, "malformed-public-auth-settings.db")
	if _, err := d.DB.Exec(`UPDATE settings SET value='not-json' WHERE key='login_captcha_required'`); err != nil {
		t.Fatalf("corrupt login captcha setting: %v", err)
	}
	store.InvalidateConfig()
	public := httptest.NewRecorder()
	signupOpenHandler(d, public, httptest.NewRequest(http.MethodGet, "/api/public/signup-open", nil))
	if public.Code != http.StatusInternalServerError {
		t.Fatalf("signup status=%d body=%s, want 500", public.Code, public.Body.String())
	}
	login := runAuthJSONHandler(t, d, loginHandler, "/api/auth/login", `{"email":"nobody@example.test","password":"password123"}`)
	if login.Code != http.StatusInternalServerError {
		t.Fatalf("login status=%d body=%s, want 500", login.Code, login.Body.String())
	}
}

func TestVerifyEmailCodeHasOneConcurrentWinner(t *testing.T) {
	d := newAuthSecurityDeps(t, "verify-code.db")
	user, err := store.CreateUserWithState(
		t.Context(), d.DB, "verify@example.test", "Verify", "hash", "user", "pending", true,
	)
	if err != nil {
		t.Fatalf("create pending user: %v", err)
	}
	d.Cache.Set("verify:"+user.Email, "654321", time.Minute)

	const attempts = 8
	start := make(chan struct{})
	results := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := runAuthJSONHandler(
				t, d, verifyEmailHandler, "/api/auth/verify-email",
				`{"email":"verify@example.test","code":"654321"}`,
			)
			results <- rec.Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusBadRequest {
			t.Fatalf("verify status=%d, want 200 or 400", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful verification requests=%d, want 1", successes)
	}
	verified, err := store.FindUserByID(t.Context(), d.DB, user.ID)
	if err != nil {
		t.Fatalf("reload verified user: %v", err)
	}
	if verified.Status != "active" {
		t.Fatalf("verified user status=%q, want active", verified.Status)
	}
}

func TestVerifyEmailCannotReviveConcurrentlyBannedUser(t *testing.T) {
	d := newAuthSecurityDeps(t, "verify-ban-race.db")
	for i := 0; i < 20; i++ {
		user, err := store.CreateUserWithState(
			t.Context(), d.DB, fmt.Sprintf("verify-ban-%d@example.test", i), "Verify Ban", "hash", "user", "pending", true,
		)
		if err != nil {
			t.Fatalf("create pending user %d: %v", i, err)
		}
		d.Cache.Set("verify:"+user.Email, "654321", time.Minute)

		start := make(chan struct{})
		verified := make(chan int, 1)
		banned := make(chan error, 1)
		go func(email string) {
			<-start
			rec := runAuthJSONHandler(
				t, d, verifyEmailHandler, "/api/auth/verify-email",
				fmt.Sprintf(`{"email":%q,"code":"654321"}`, email),
			)
			verified <- rec.Code
		}(user.Email)
		go func(userID string) {
			<-start
			ok, banErr := store.SetUserStatusGuarded(t.Context(), d.DB, userID, "banned")
			if banErr == nil && !ok {
				banErr = errors.New("ban did not update the user")
			}
			banned <- banErr
		}(user.ID)
		close(start)

		if err := <-banned; err != nil {
			t.Fatalf("ban user %d: %v", i, err)
		}
		status := <-verified
		if status != http.StatusOK && status != http.StatusBadRequest {
			t.Fatalf("verify status=%d, want 200 or 400", status)
		}
		stored, err := store.FindUserByID(t.Context(), d.DB, user.ID)
		if err != nil {
			t.Fatalf("reload user %d: %v", i, err)
		}
		if stored.Status != "banned" {
			t.Fatalf("user %d status=%q after verify/ban race, want banned", i, stored.Status)
		}
	}
}

func TestRefreshHandlerConsumesOneTokenOnlyOnce(t *testing.T) {
	d := newAuthSecurityDeps(t, "refresh-handler.db")
	user, err := store.CreateUser(t.Context(), d.DB, "refresh@example.test", "Refresh", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	if err := store.SaveRefreshToken(t.Context(), d.DB, jti, user.ID, refreshExp, store.SessionMeta{}); err != nil {
		t.Fatalf("save refresh: %v", err)
	}

	const attempts = 10
	start := make(chan struct{})
	results := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
			req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refresh})
			rec := httptest.NewRecorder()
			refreshHandler(d, rec, req)
			results <- rec.Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusUnauthorized {
			t.Fatalf("refresh status=%d, want 200 or 401", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful refresh requests=%d, want 1", successes)
	}
	var active int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&active); err != nil {
		t.Fatalf("count active sessions: %v", err)
	}
	if active != 1 {
		t.Fatalf("active sessions=%d, want 1", active)
	}
}

func TestResetPasswordImmediatelyInvalidatesCachedAccessAndPublishesKill(t *testing.T) {
	d := newAuthSecurityDeps(t, "reset-cache.db")
	user, err := store.CreateUser(t.Context(), d.DB, "reset@example.test", "Reset", "old-hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	oldAccess := issueBoundTestAccessToken(t, d.DB, d.Auth, user)
	if raw, err := json.Marshal(user); err != nil {
		t.Fatalf("marshal auth cache user: %v", err)
	} else {
		d.Cache.Set(authUserCacheKey(d, user.ID), string(raw), time.Minute)
	}
	d.Cache.Set("reset:"+user.Email, "123456", time.Minute)
	kill, unsubscribe := d.Cache.Subscribe("user:" + user.ID + ":kill")
	defer unsubscribe()

	rec := runAuthJSONHandler(t, d, resetPasswordHandler, "/api/auth/reset-password", `{"email":"reset@example.test","code":"123456","new_password":"new-password-123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := d.Cache.Get(authUserCacheKey(d, user.ID)); ok {
		t.Fatal("password reset left the cached auth user in place")
	}
	select {
	case payload := <-kill:
		if payload != "password_reset" {
			t.Fatalf("kill payload=%q", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("password reset did not publish a realtime kill signal")
	}

	called := false
	h := requireAuth(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+oldAccess)
	accessRec := httptest.NewRecorder()
	h.ServeHTTP(accessRec, req)
	if accessRec.Code != http.StatusUnauthorized || called {
		t.Fatalf("old access status=%d called=%v body=%s", accessRec.Code, called, accessRec.Body.String())
	}

	replay := runAuthJSONHandler(t, d, resetPasswordHandler, "/api/auth/reset-password", `{"email":"reset@example.test","code":"123456","new_password":"another-password-123"}`)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("reset-code replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}
