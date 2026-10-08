package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func desktopTestRequest(d Deps, h http.HandlerFunc, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func desktopTestStart(t *testing.T, d Deps, verifier string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	w := desktopTestRequest(d, wrap(d, desktopStartHandler), "POST", "/api/auth/desktop/start", fmt.Sprintf(`{"challenge":%q}`, challenge), "")
	var response struct {
		RequestID string `json:"request_id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.RequestID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	return response.RequestID
}

func desktopTestUser(t *testing.T, d Deps) (*store.User, string) {
	t.Helper()
	u, err := store.CreateUserWithRole(t.Context(), d.DB, "desktop@example.test", "Desktop", "hash", "user")
	if err != nil {
		t.Fatal(err)
	}
	return u, issueBoundTestAccessToken(t, d.DB, d.Auth, u)
}

func desktopTestApprove(d Deps, id, token string, approve bool) *httptest.ResponseRecorder {
	return desktopTestRequest(d, requireAuth(d, desktopAuthorizeHandler), "POST", "/api/auth/desktop/authorize", fmt.Sprintf(`{"request_id":%q,"approve":%t}`, id, approve), token)
}

func desktopTestExchange(d Deps, id, verifier string) *httptest.ResponseRecorder {
	return desktopTestRequest(d, wrap(d, desktopTokenHandler), "POST", "/api/auth/desktop/token", fmt.Sprintf(`{"request_id":%q,"verifier":%q}`, id, verifier), "")
}

func TestDesktopAuthorizationPKCEAndSingleUse(t *testing.T) {
	d := newAuthSecurityDeps(t, "desktop-pkce.db")
	_, token := desktopTestUser(t, d)
	v := strings.Repeat("v", 43)
	id := desktopTestStart(t, d, v)
	if w := desktopTestExchange(d, id, v); w.Code != 200 || !strings.Contains(w.Body.String(), "pending") {
		t.Fatalf("pending: %d %s", w.Code, w.Body.String())
	}
	if w := desktopTestApprove(d, id, "", true); w.Code != 401 {
		t.Fatalf("unauthorized grant: %d", w.Code)
	}
	if w := desktopTestApprove(d, id, token, true); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if w := desktopTestExchange(d, id, strings.Repeat("x", 43)); w.Code != 403 {
		t.Fatalf("invalid PKCE: %d", w.Code)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Go(func() { results <- desktopTestExchange(d, id, v) })
	}
	wg.Wait()
	close(results)
	winners := 0
	for w := range results {
		if strings.Contains(w.Body.String(), "authorized") && w.Code == 200 {
			winners++
			if len(w.Result().Cookies()) < 2 {
				t.Fatal("missing desktop cookies")
			}
			var refresh string
			for _, c := range w.Result().Cookies() {
				if c.Name == "refresh_token" {
					refresh = c.Value
				}
			}
			claims, err := d.Auth.ParseRefresh(refresh)
			if err != nil {
				t.Fatal(err)
			}
			browser, _ := d.Auth.ParseAccess(token)
			if claims.ID == browser.SessionID {
				t.Fatal("desktop must have its own session")
			}
		}
	}
	if winners != 1 {
		t.Fatalf("exchange winners=%d", winners)
	}
	if w := desktopTestExchange(d, id, v); w.Code != 404 {
		t.Fatalf("replay status=%d", w.Code)
	}
}

func TestDesktopAuthorizationDenialExpiryAndRevocation(t *testing.T) {
	for _, reason := range []string{"denied", "expired", "password", "session", "banned"} {
		t.Run(reason, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "desktop-"+reason+".db")
			u, token := desktopTestUser(t, d)
			v := strings.Repeat("v", 43)
			id := desktopTestStart(t, d, v)
			if w := desktopTestApprove(d, id, token, reason != "denied"); w.Code != 200 {
				t.Fatalf("approve: %d %s", w.Code, w.Body.String())
			}
			switch reason {
			case "expired":
				d.Cache.Set("desktop:request:"+id, `{"expires_at":1}`, time.Minute)
			case "password":
				_, err := d.DB.Exec(`UPDATE users SET token_ver=token_ver+1 WHERE id=?`, u.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "session":
				claims, _ := d.Auth.ParseAccess(token)
				if _, err := store.RevokeUserSession(t.Context(), d.DB, u.ID, claims.SessionID); err != nil {
					t.Fatal(err)
				}
			case "banned":
				if _, err := d.DB.Exec(`UPDATE users SET status='banned' WHERE id=?`, u.ID); err != nil {
					t.Fatal(err)
				}
			}
			w := desktopTestExchange(d, id, v)
			want := 401
			if reason == "expired" {
				want = 404
			}
			if reason == "denied" {
				want = 200
				if !strings.Contains(w.Body.String(), "denied") {
					t.Fatalf("denial: %s", w.Body.String())
				}
			}
			if w.Code != want {
				t.Fatalf("exchange: %d %s, want %d", w.Code, w.Body.String(), want)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("failed authorization wrote session cookies")
			}
		})
	}
}

func TestDesktopAuthorizationRoutesAreRegisteredAndKeepRequestProtection(t *testing.T) {
	d := newAuthSecurityDeps(t, "desktop-routes.db")
	_, token := desktopTestUser(t, d)
	router := NewRouter(d)
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/api/auth/desktop/start"}, {"POST", "/api/auth/desktop/token"},
		{"GET", "/api/auth/desktop/authorize"}, {"POST", "/api/auth/desktop/authorize"},
	} {
		w := desktopTestRequest(d, router.ServeHTTP, endpoint.method, endpoint.path, `{}`, "")
		if w.Code == 404 || w.Code == 405 {
			t.Fatalf("route missing: %s %s: %d", endpoint.method, endpoint.path, w.Code)
		}
	}
	d.Config.RequestSignaturesRequired = true
	id := desktopTestStart(t, d, strings.Repeat("v", 43))
	if w := desktopTestApprove(d, id, token, true); w.Code != 403 {
		t.Fatalf("unsigned grant: %d", w.Code)
	}
	d.Config.RequestSignaturesRequired = false
	r := httptest.NewRequest("POST", "/api/auth/desktop/authorize", strings.NewReader(fmt.Sprintf(`{"request_id":%q,"approve":true}`, id)))
	r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	requireAuth(d, desktopAuthorizeHandler)(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-site grant: %d", w.Code)
	}
}

func TestDesktopAuthorizationConsentDescribesInitiatingClient(t *testing.T) {
	d := newAuthSecurityDeps(t, "desktop-consent-client.db")
	_, token := desktopTestUser(t, d)
	verifier := strings.Repeat("v", 43)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	start := httptest.NewRequest("POST", "/api/auth/desktop/start", strings.NewReader(fmt.Sprintf(`{"challenge":%q}`, challenge)))
	start.RemoteAddr = "203.0.113.10:5678"
	start.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) AivoryDesktop/2.5.1-beta.7")
	start.Header.Set("X-Forwarded-For", "198.51.100.99") // Public peers cannot spoof the source IP.
	start.Header.Set("X-Geo-Country", "SG")
	w := httptest.NewRecorder()
	wrap(d, desktopStartHandler)(w, start)
	var started struct {
		RequestID string `json:"request_id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.RequestID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	info := httptest.NewRequest("GET", "/api/auth/desktop/authorize?request_id="+started.RequestID, nil)
	info.Header.Set("Authorization", "Bearer "+token)
	info.Header.Set("User-Agent", "Browser that approves the desktop")
	info.RemoteAddr = "198.51.100.20:1234"
	w = httptest.NewRecorder()
	requireAuth(d, desktopAuthorizationInfoHandler)(w, info)
	var response struct {
		ExpiresAt int64                       `json:"expires_at"`
		Client    *desktopAuthorizationClient `json:"client"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Client == nil {
		t.Fatalf("info: %d %s", w.Code, w.Body.String())
	}
	if response.Client.UserAgent != start.UserAgent() || response.Client.IP != "203.0.113.10" || response.Client.Location != "SG" {
		t.Fatalf("consent describes approving browser instead of initiating client: %+v", response.Client)
	}
	if response.ExpiresAt <= time.Now().Unix() || strings.Contains(w.Body.String(), challenge) || strings.Contains(w.Body.String(), verifier) || strings.Contains(w.Body.String(), "challenge") {
		t.Fatalf("invalid expiry or exposed proof: %s", w.Body.String())
	}
	// Cached requests started on the old version still authorize after an upgrade.
	d.Cache.Set("desktop:request:"+started.RequestID, fmt.Sprintf(`{"challenge":%q,"expires_at":%d}`, challenge, response.ExpiresAt), time.Minute)
	w = desktopTestRequest(d, requireAuth(d, desktopAuthorizationInfoHandler), "GET", info.URL.String(), "", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "client") {
		t.Fatalf("legacy request metadata: %d %s", w.Code, w.Body.String())
	}
	if w := desktopTestApprove(d, started.RequestID, token, true); w.Code != 200 {
		t.Fatalf("legacy approve: %d %s", w.Code, w.Body.String())
	}
	if w := desktopTestExchange(d, started.RequestID, verifier); w.Code != 200 || !strings.Contains(w.Body.String(), "authorized") {
		t.Fatalf("legacy exchange: %d %s", w.Code, w.Body.String())
	}
}

func TestDesktopAuthorizationNativeSessionRecordsAndIndependentRevocation(t *testing.T) {
	d := newAuthSecurityDeps(t, "desktop-native-records.db")
	u, browserToken := desktopTestUser(t, d)
	verifier := strings.Repeat("v", 43)
	id := desktopTestStart(t, d, verifier)
	if w := desktopTestApprove(d, id, browserToken, true); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/api/auth/desktop/token", strings.NewReader(fmt.Sprintf(`{"request_id":%q,"verifier":%q}`, id, verifier)))
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0) AivoryDesktop/2.5.1-beta.7")
	r.Header.Set("X-Geo-Country", "SG")
	r.RemoteAddr = "203.0.113.11:5678"
	w := httptest.NewRecorder()
	wrap(d, desktopTokenHandler)(w, r)
	if w.Code != 200 {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	history, err := store.ListLoginHistoriesForUser(t.Context(), d.DB, u.ID, 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %+v %v", history, err)
	}
	if h := history[0]; h.Method != store.LoginMethodDesktopBrowser || h.UserAgent != r.UserAgent() || h.IP != "203.0.113.11" || h.Location != "SG" {
		t.Fatalf("wrong native login record: %+v", h)
	}
	var refresh string
	for _, c := range w.Result().Cookies() {
		if c.Name == "refresh_token" {
			refresh = c.Value
		}
	}
	claims, err := d.Auth.ParseRefresh(refresh)
	if err != nil {
		t.Fatal(err)
	}
	browserClaims, _ := d.Auth.ParseAccess(browserToken)
	if _, err := store.RevokeUserSession(t.Context(), d.DB, u.ID, browserClaims.SessionID); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ListUserSessions(t.Context(), d.DB, u.ID)
	if err != nil || len(sessions) != 1 || sessions[0].ID != claims.ID || sessions[0].UserAgent != r.UserAgent() || sessions[0].IP != "203.0.113.11" {
		t.Fatalf("desktop must remain independently active with native metadata: %+v %v", sessions, err)
	}
	if _, err := store.RevokeUserSession(t.Context(), d.DB, u.ID, claims.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err = store.ListUserSessions(t.Context(), d.DB, u.ID)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("native session revocation: %+v %v", sessions, err)
	}
	count, err := store.CountLoginHistoriesForUser(t.Context(), d.DB, u.ID)
	if err != nil || count != 1 {
		t.Fatalf("revocation must preserve history: %d %v", count, err)
	}
}
