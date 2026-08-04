package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

// TestAllowedReturnOrigin is the open-redirect guard for the cross-domain OAuth
// hand-off: only exact scheme://host matches from the configured allowlist may
// ever be a redirect target. A miss here means the flow refuses to bounce back.
func TestAllowedReturnOrigin(t *testing.T) {
	d := Deps{Config: config.Config{OAuthReturnOrigins: []string{
		"https://a.example.com", "https://b.example.com/",
	}}}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://a.example.com", true},
		{"https://b.example.com", true},           // trailing slash in config is normalised
		{"https://a.example.com/", true},          // trailing slash in input is normalised
		{"https://A.EXAMPLE.COM", true},           // host compare is case-insensitive
		{"https://evil.example.com", false},       // not listed
		{"http://a.example.com", false},           // scheme must match (no downgrade)
		{"https://a.example.com.evil.com", false}, // suffix attack
		{"https://a.example.com@evil.com", false}, // userinfo trick
		{"", false},
	}
	for _, c := range cases {
		if got := allowedReturnOrigin(d, c.origin); got != c.want {
			t.Errorf("allowedReturnOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

func TestRandTokenFailsClosedWhenSecureRandomUnavailable(t *testing.T) {
	original := secureRandomRead
	secureRandomRead = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	t.Cleanup(func() { secureRandomRead = original })
	if token := randToken(24); token != "" {
		t.Fatalf("randToken degraded to predictable value %q", token)
	}
}

// TestStartOrigin covers the decision made when a flow begins: return the request
// host only when it differs from the canonical callback host AND is allowlisted;
// otherwise "" (a same-host flow, no hand-off).
func TestStartOrigin(t *testing.T) {
	d := Deps{Config: config.Config{
		OAuthCallbackBaseURL: "https://a.example.com",
		OAuthReturnOrigins:   []string{"https://a.example.com", "https://b.example.com"},
	}}
	callbackBase := oauthCallbackBase(d, httptest.NewRequest("GET", "https://a.example.com/x", nil))
	if callbackBase != "https://a.example.com" {
		t.Fatalf("callbackBase = %q, want canonical", callbackBase)
	}

	// Started on B (allowlisted, != canonical) → hand-off back to B.
	req := httptest.NewRequest("GET", "https://b.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "https://b.example.com" {
		t.Errorf("startOrigin(B) = %q, want https://b.example.com", got)
	}
	// Started on the canonical host itself → no hand-off.
	req = httptest.NewRequest("GET", "https://a.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "" {
		t.Errorf("startOrigin(A) = %q, want empty", got)
	}
	// Started on an UN-allowlisted domain → no hand-off (falls back to canonical,
	// never trusts an arbitrary host as a redirect target).
	req = httptest.NewRequest("GET", "https://evil.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "" {
		t.Errorf("startOrigin(evil) = %q, want empty", got)
	}
}

// TestOAuthCallbackBaseFallback: with no canonical host configured the callback
// base derives from the request host (single-domain deployments — unchanged).
func TestOAuthCallbackBaseFallback(t *testing.T) {
	d := Deps{Config: config.Config{}}
	req := httptest.NewRequest("GET", "https://only.example.com/x", nil)
	if got := oauthCallbackBase(d, req); got != "https://only.example.com" {
		t.Errorf("oauthCallbackBase fallback = %q, want request host", got)
	}
}

func TestOAuthHandoffRejectsWrongAllowedOriginAndConsumesToken(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-handoff-wrong-origin.db")
	d.Config.OAuthReturnOrigins = []string{"https://a.example.com", "https://b.example.com"}
	user, err := store.CreateUser(t.Context(), d.DB, "handoff@example.test", "Handoff", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const token = "wrong-origin-token"
	cacheOAuthHandoffForTest(t, d, token, oauthHandoff{UID: user.ID, Origin: "https://b.example.com"})

	wrong := runOAuthHandoffForTest(d, "https://a.example.com", token)
	if wrong.Code != http.StatusFound || !strings.Contains(wrong.Header().Get("Location"), "oauth_error=invalid_handoff_origin") {
		t.Fatalf("wrong-origin response status=%d location=%q", wrong.Code, wrong.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, wrong)

	// The first redemption attempt consumes the bearer even when its origin is
	// wrong, so possession cannot be probed repeatedly or replayed on the target.
	replay := runOAuthHandoffForTest(d, "https://b.example.com", token)
	if replay.Code != http.StatusFound || !strings.Contains(replay.Header().Get("Location"), "oauth_error=invalid_or_expired_handoff") {
		t.Fatalf("replay response status=%d location=%q", replay.Code, replay.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, replay)

	var sessions int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=?`, user.ID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("wrong-origin handoff created %d sessions", sessions)
	}
}

func TestOAuthHandoffIsOneShotOnBoundOrigin(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-handoff-one-shot.db")
	d.Config.OAuthReturnOrigins = []string{"https://b.example.com"}
	user, err := store.CreateUser(t.Context(), d.DB, "one-shot@example.test", "One Shot", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const token = "one-shot-token"
	cacheOAuthHandoffForTest(t, d, token, oauthHandoff{UID: user.ID, Origin: "https://b.example.com"})

	first := runOAuthHandoffForTest(d, "https://b.example.com", token)
	if first.Code != http.StatusFound || first.Header().Get("Location") != "https://b.example.com/" {
		t.Fatalf("first response status=%d location=%q", first.Code, first.Header().Get("Location"))
	}
	access := oauthResponseCookieValue(first, "auth_token")
	refresh := oauthResponseCookieValue(first, "refresh_token")
	if access == "" || refresh == "" {
		t.Fatalf("successful handoff cookies access=%t refresh=%t", access != "", refresh != "")
	}
	claims, err := d.Auth.ParseAccess(access)
	if err != nil || claims.UID != user.ID || strings.TrimSpace(claims.SessionID) == "" {
		t.Fatalf("parse handoff access claims=%+v err=%v", claims, err)
	}

	second := runOAuthHandoffForTest(d, "https://b.example.com", token)
	if second.Code != http.StatusFound || !strings.Contains(second.Header().Get("Location"), "oauth_error=invalid_or_expired_handoff") {
		t.Fatalf("second response status=%d location=%q", second.Code, second.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, second)

	var sessions int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=? AND revoked=0`, user.ID).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("successful one-shot handoff created %d active sessions, want 1", sessions)
	}
}

func TestOAuthHandoffRejectsCallbackTransferredToAnotherBrowser(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-handoff-browser-binding.db")
	d.Config.OAuthReturnOrigins = []string{"https://b.example.com"}
	user, err := store.CreateUser(t.Context(), d.DB, "browser-bound@example.test", "Browser Bound", "hash")
	if err != nil {
		t.Fatal(err)
	}
	const token = "transferred-handoff-token"
	cacheOAuthHandoffForTest(t, d, token, oauthHandoff{UID: user.ID, Origin: "https://b.example.com"})

	transferred := runOAuthHandoffWithoutBindingForTest(d, "https://b.example.com", token)
	if transferred.Code != http.StatusFound || !strings.Contains(transferred.Header().Get("Location"), "oauth_error=invalid_browser_binding") {
		t.Fatalf("transferred handoff status=%d location=%q", transferred.Code, transferred.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, transferred)

	// The bearer handoff is consumed even on a browser-binding failure, so the
	// attacker cannot probe/replay it and the intended browser must restart.
	replay := runOAuthHandoffForTest(d, "https://b.example.com", token)
	if replay.Code != http.StatusFound || !strings.Contains(replay.Header().Get("Location"), "oauth_error=invalid_or_expired_handoff") {
		t.Fatalf("replayed transferred handoff status=%d location=%q", replay.Code, replay.Header().Get("Location"))
	}
	var sessions int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=?`, user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("transferred handoff created %d sessions", sessions)
	}
}

func TestOAuthHandoffRejectsRevokedAuthenticationContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(testing.TB, Deps, *store.User)
	}{
		{
			name: "password reset",
			mutate: func(t testing.TB, d Deps, user *store.User) {
				t.Helper()
				if err := store.UpdateUserPassword(context.Background(), d.DB, user.ID, "new-hash"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "account banned",
			mutate: func(t testing.TB, d Deps, user *store.User) {
				t.Helper()
				ok, err := store.SetUserStatusGuarded(context.Background(), d.DB, user.ID, "banned")
				if err != nil || !ok {
					t.Fatalf("ban user=(%v,%v), want true,nil", ok, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "oauth-handoff-revoked-context.db")
			d.Config.OAuthReturnOrigins = []string{"https://b.example.com"}
			user, err := store.CreateUser(t.Context(), d.DB, "revoked-context@example.test", "Revoked Context", "hash")
			if err != nil {
				t.Fatal(err)
			}
			token := "revoked-context-" + strings.ReplaceAll(tc.name, " ", "-")
			cacheOAuthHandoffForTest(t, d, token, oauthHandoff{UID: user.ID, Origin: "https://b.example.com"})
			tc.mutate(t, d, user)

			rec := runOAuthHandoffForTest(d, "https://b.example.com", token)
			if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=account_error") {
				t.Fatalf("revoked-context handoff status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
			assertNoOAuthSessionCookies(t, rec)
			replay := runOAuthHandoffForTest(d, "https://b.example.com", token)
			if replay.Code != http.StatusFound || !strings.Contains(replay.Header().Get("Location"), "oauth_error=invalid_or_expired_handoff") {
				t.Fatalf("revoked-context replay status=%d location=%q", replay.Code, replay.Header().Get("Location"))
			}
			var sessions int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=?`, user.ID).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if sessions != 0 {
				t.Fatalf("revoked context created %d sessions", sessions)
			}
		})
	}
}

func TestOAuthHandoffRejectsLegacyPayloadWithoutTokenVersion(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-handoff-legacy-token-version.db")
	d.Config.OAuthReturnOrigins = []string{"https://b.example.com"}
	user, err := store.CreateUser(t.Context(), d.DB, "legacy-context@example.test", "Legacy Context", "hash")
	if err != nil {
		t.Fatal(err)
	}
	const token = "legacy-token-version"
	raw, err := json.Marshal(oauthHandoff{
		UID: user.ID, Origin: "https://b.example.com",
		FlowState: "flow-" + token, BrowserBinding: "binding-" + token,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Cache.Set("oauth:handoff:"+token, string(raw), time.Minute)
	rec := runOAuthHandoffForTest(d, "https://b.example.com", token)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=account_error") {
		t.Fatalf("legacy handoff status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, rec)
}

func TestOAuthHandoffRejectsLegacyAndMalformedPayloads(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-handoff-invalid-payload.db")
	d.Config.OAuthReturnOrigins = []string{"https://b.example.com"}
	user, err := store.CreateUser(t.Context(), d.DB, "legacy-handoff@example.test", "Legacy", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "legacy uid-only value", value: user.ID},
		{name: "malformed json", value: `{"uid":`},
		{name: "missing origin", value: `{"uid":"` + user.ID + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := strings.ReplaceAll(tc.name, " ", "-")
			d.Cache.Set("oauth:handoff:"+token, tc.value, time.Minute)
			rec := runOAuthHandoffForTest(d, "https://b.example.com", token)
			if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=invalid_handoff_origin") {
				t.Fatalf("response status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
			assertNoOAuthSessionCookies(t, rec)
		})
	}
}

func cacheOAuthHandoffForTest(t testing.TB, d Deps, token string, handoff oauthHandoff) {
	t.Helper()
	if handoff.FlowState == "" {
		handoff.FlowState = "flow-" + token
	}
	if handoff.BrowserBinding == "" {
		handoff.BrowserBinding = "binding-" + token
	}
	if handoff.TokenVer == nil {
		user, err := store.FindUserByID(context.Background(), d.DB, handoff.UID)
		if err != nil {
			t.Fatalf("load handoff user: %v", err)
		}
		tokenVer := user.TokenVer
		handoff.TokenVer = &tokenVer
	}
	if handoff.ProviderGuard == nil {
		provider, err := store.GetOAuthProvider(context.Background(), d.DB, "oa_handoff_test")
		if errors.Is(err, store.ErrNotFound) {
			provider, err = store.CreateOAuthProvider(context.Background(), d.DB, store.OAuthProvider{
				ID: "oa_handoff_test", Kind: "google", Name: "Handoff Test", ClientID: "client-id",
				SubjectNamespace: "oauth:v1:handoff-test:", Enabled: true,
			})
		}
		if err != nil {
			t.Fatalf("load handoff provider: %v", err)
		}
		guard := store.NewOAuthProviderCallbackGuard(*provider)
		handoff.ProviderGuard = &guard
	}
	raw, err := json.Marshal(handoff)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	d.Cache.Set("oauth:handoff:"+token, string(raw), time.Minute)
}

func runOAuthHandoffForTest(d Deps, origin, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, origin+"/api/auth/oauth/handoff?token="+token, nil)
	req.AddCookie(&http.Cookie{
		Name: oauthBrowserBindingCookieName("flow-" + token), Value: "binding-" + token,
		Path: "/api/auth/oauth", HttpOnly: true,
	})
	rec := httptest.NewRecorder()
	oauthHandoffHandler(d, rec, req)
	return rec
}

func runOAuthHandoffWithoutBindingForTest(d Deps, origin, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, origin+"/api/auth/oauth/handoff?token="+token, nil)
	rec := httptest.NewRecorder()
	oauthHandoffHandler(d, rec, req)
	return rec
}

func oauthResponseCookieValue(rec *httptest.ResponseRecorder, name string) string {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

func assertNoOAuthSessionCookies(t testing.TB, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, name := range []string{"auth_token", "refresh_token"} {
		if value := oauthResponseCookieValue(rec, name); value != "" {
			t.Fatalf("failure response set %s=%q", name, value)
		}
	}
}
