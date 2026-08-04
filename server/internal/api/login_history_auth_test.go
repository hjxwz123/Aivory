package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestSuccessfulLoginHistoryMethodsAndRefreshExclusion(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "login-methods.db"))
	defer db.Close()
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.InvalidateConfig()
	c := cache.NewMemory()
	d := Deps{
		DB: db, Cache: c,
		Auth:   authsvc.New("login-history-auth-test-secret", time.Hour, 24*time.Hour, c),
		Logger: log.New(io.Discard, "", 0),
	}
	const password = "correct-password"
	hash, err := store.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	createUser := func(email string, twoFactor bool) (*store.User, string) {
		t.Helper()
		user, err := store.CreateUser(t.Context(), db, email, email, hash)
		if err != nil {
			t.Fatalf("create %s: %v", email, err)
		}
		secret := ""
		if twoFactor {
			secret, err = authsvc.GenerateTotpSecret()
			if err != nil {
				t.Fatalf("generate TOTP secret: %v", err)
			}
			if err := store.SetUserTotp(t.Context(), db, user.ID, secret, true); err != nil {
				t.Fatalf("enable TOTP for %s: %v", email, err)
			}
			user, err = store.FindUserByID(t.Context(), db, user.ID)
			if err != nil {
				t.Fatalf("reload %s: %v", email, err)
			}
		}
		return user, secret
	}
	request := func(method, target, body string) *http.Request {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "History Test Browser/1.0")
		req.Header.Set("X-Forwarded-For", "203.0.113.42")
		req.Header.Set("CF-IPCity", "Paris")
		req.Header.Set("CF-IPCountry", "FR")
		req.RemoteAddr = "10.0.0.5:4321"
		return req
	}
	assertOnlyMethod := func(userID, method string) store.LoginHistory {
		t.Helper()
		rows, err := store.ListLoginHistoriesForUser(t.Context(), db, userID, 10, 0)
		if err != nil {
			t.Fatalf("list history for %s: %v", userID, err)
		}
		if len(rows) != 1 || rows[0].Method != method {
			t.Fatalf("history for %s = %+v, want one %s row", userID, rows, method)
		}
		return rows[0]
	}

	t.Run("password login records metadata but refresh does not", func(t *testing.T) {
		user, _ := createUser("password@example.test", false)
		rec := httptest.NewRecorder()
		loginHandler(d, rec, request(http.MethodPost, "/api/auth/login", `{"email":"password@example.test","password":"correct-password"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
		}
		row := assertOnlyMethod(user.ID, store.LoginMethodPassword)
		if row.IP != "203.0.113.42" || row.Location != "Paris, FR" || row.UserAgent != "History Test Browser/1.0" {
			t.Fatalf("login metadata = %+v", row)
		}
		var refreshCookie *http.Cookie
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == "refresh_token" && cookie.Value != "" {
				refreshCookie = cookie
				break
			}
		}
		if refreshCookie == nil {
			t.Fatal("login response did not set refresh_token")
		}
		refreshReq := request(http.MethodPost, "/api/auth/refresh", "")
		refreshReq.AddCookie(refreshCookie)
		refreshRec := httptest.NewRecorder()
		refreshHandler(d, refreshRec, refreshReq)
		if refreshRec.Code != http.StatusOK {
			t.Fatalf("refresh status=%d body=%s", refreshRec.Code, refreshRec.Body.String())
		}
		assertOnlyMethod(user.ID, store.LoginMethodPassword)
	})

	t.Run("password plus 2FA records final success only", func(t *testing.T) {
		user, secret := createUser("password-2fa@example.test", true)
		first := httptest.NewRecorder()
		loginHandler(d, first, request(http.MethodPost, "/api/auth/login", `{"email":"password-2fa@example.test","password":"correct-password"}`))
		if first.Code != http.StatusOK {
			t.Fatalf("password step status=%d body=%s", first.Code, first.Body.String())
		}
		if count, err := store.CountLoginHistoriesForUser(t.Context(), db, user.ID); err != nil || count != 0 {
			t.Fatalf("history before 2FA count=%d err=%v, want 0", count, err)
		}
		var handoff struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &handoff); err != nil || handoff.Ticket == "" {
			t.Fatalf("decode 2FA handoff: ticket=%q err=%v body=%s", handoff.Ticket, err, first.Body.String())
		}
		second := httptest.NewRecorder()
		login2faHandler(d, second, request(http.MethodPost, "/api/auth/login/2fa", fmt.Sprintf(`{"ticket":%q,"code":%q}`, handoff.Ticket, currentTOTPCode(t, secret))))
		if second.Code != http.StatusOK {
			t.Fatalf("2FA step status=%d body=%s", second.Code, second.Body.String())
		}
		assertOnlyMethod(user.ID, store.LoginMethodPassword2FA)
	})

	t.Run("OAuth records source with and without 2FA", func(t *testing.T) {
		provider, err := store.CreateOAuthProvider(t.Context(), db, store.OAuthProvider{
			ID: "oa_login_history", Kind: "google", Name: "Login History OAuth", ClientID: "client-id",
			SubjectNamespace: "oauth:v1:login-history:", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		guard := store.NewOAuthProviderCallbackGuard(*provider)
		plainUser, _ := createUser("oauth@example.test", false)
		plainRec := httptest.NewRecorder()
		completeOAuthLoginWithGuard(
			d, plainRec, request(http.MethodGet, "https://app.example.test/api/auth/oauth/handoff", ""),
			plainUser, "https://app.example.test", &guard,
		)
		if plainRec.Code != http.StatusFound {
			t.Fatalf("OAuth status=%d body=%s", plainRec.Code, plainRec.Body.String())
		}
		assertOnlyMethod(plainUser.ID, store.LoginMethodOAuth)

		twoFAUser, secret := createUser("oauth-2fa@example.test", true)
		first := httptest.NewRecorder()
		completeOAuthLoginWithGuard(
			d, first, request(http.MethodGet, "https://app.example.test/api/auth/oauth/handoff", ""),
			twoFAUser, "https://app.example.test", &guard,
		)
		if first.Code != http.StatusFound {
			t.Fatalf("OAuth 2FA handoff status=%d body=%s", first.Code, first.Body.String())
		}
		if count, err := store.CountLoginHistoriesForUser(t.Context(), db, twoFAUser.ID); err != nil || count != 0 {
			t.Fatalf("OAuth history before 2FA count=%d err=%v, want 0", count, err)
		}
		var twoFACookie *http.Cookie
		for _, cookie := range first.Result().Cookies() {
			if cookie.Name == "aivory_2fa" && cookie.Value != "" {
				twoFACookie = cookie
				break
			}
		}
		if twoFACookie == nil {
			t.Fatal("OAuth flow did not set 2FA handoff cookie")
		}
		secondReq := request(http.MethodPost, "https://app.example.test/api/auth/login/2fa", fmt.Sprintf(`{"code":%q}`, currentTOTPCode(t, secret)))
		secondReq.AddCookie(twoFACookie)
		second := httptest.NewRecorder()
		login2faHandler(d, second, secondReq)
		if second.Code != http.StatusOK {
			t.Fatalf("OAuth 2FA completion status=%d body=%s", second.Code, second.Body.String())
		}
		assertOnlyMethod(twoFAUser.ID, store.LoginMethodOAuth2FA)
	})
}

func currentTOTPCode(t *testing.T, secret string) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("decode TOTP secret: %v", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1000000)
}
