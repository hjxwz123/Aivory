package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func TestOAuthCallbackRejectsURLTransferredToAnotherBrowser(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-callback-browser-binding.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	const providerID = "oa_transferred"
	const stateID = "transferred-callback-state"
	cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
		ProviderID: providerID, BrowserBinding: "originating-browser-secret",
	})

	transferred := runOAuthCallbackForTest(d, providerID, stateID, "", "")
	if transferred.Code != http.StatusFound || !strings.Contains(transferred.Header().Get("Location"), "oauth_error=invalid_browser_binding") {
		t.Fatalf("transferred callback status=%d location=%q", transferred.Code, transferred.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, transferred)

	// State is atomically consumed by the failed cross-browser redemption. Even
	// presenting the right binding afterward cannot replay the authorization.
	replay := runOAuthCallbackForTest(d, providerID, stateID, stateID, "originating-browser-secret")
	if replay.Code != http.StatusFound || !strings.Contains(replay.Header().Get("Location"), "oauth_error=invalid_or_expired_state") {
		t.Fatalf("callback replay status=%d location=%q", replay.Code, replay.Header().Get("Location"))
	}
}

func TestOAuthCallbackAcceptsMatchingBrowserBeforeProviderExchange(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-callback-matching-browser.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	const providerID = "oa_matching"
	const stateID = "matching-callback-state"
	const binding = "matching-browser-secret"
	cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{ProviderID: providerID, BrowserBinding: binding})

	rec := runOAuthCallbackForTest(d, providerID, stateID, stateID, binding)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=provider_unavailable") {
		t.Fatalf("matching callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Header().Get("Location"), "invalid_browser_binding") {
		t.Fatalf("matching browser binding was rejected: %q", rec.Header().Get("Location"))
	}
	if !responseDeletesCookie(rec, oauthBrowserBindingCookieName(stateID)) {
		t.Fatal("matching callback did not clear its one-flow browser-binding cookie")
	}
}

func TestOAuthCallbackRejectsLegacyStateWithoutBrowserBinding(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-callback-legacy-state.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	const providerID = "oa_legacy"
	const stateID = "legacy-callback-state"
	cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{ProviderID: providerID})
	rec := runOAuthCallbackForTest(d, providerID, stateID, "", "")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=invalid_browser_binding") {
		t.Fatalf("legacy callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOAuthStartUsesIndependentBrowserCookiesForConcurrentFlows(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-concurrent-browser-bindings.db")
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, store.OAuthProvider{
		ID: "oa_concurrent", Kind: "github", Name: "Concurrent GitHub", ClientID: "client-id", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/auth/oauth/:id/start", wrap(d, oauthStartHandler))

	type flow struct {
		state   string
		binding string
		cookie  *http.Cookie
	}
	flows := make([]flow, 0, 2)
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "https://app.example.test/api/auth/oauth/"+provider.ID+"/start", nil)
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
		}
		authorizeURL, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		stateID := authorizeURL.Query().Get("state")
		raw, ok := d.Cache.Get("oauth:state:" + stateID)
		if stateID == "" || !ok {
			t.Fatalf("state=%q cached=%v", stateID, ok)
		}
		var state oauthFlowState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		cookie := responseCookie(rec, oauthBrowserBindingCookieName(stateID))
		if cookie == nil || cookie.Value == "" || cookie.Value != state.BrowserBinding {
			t.Fatalf("state=%+v cookie=%+v", state, cookie)
		}
		flows = append(flows, flow{state: stateID, binding: state.BrowserBinding, cookie: cookie})
	}
	if flows[0].state == flows[1].state || flows[0].cookie.Name == flows[1].cookie.Name || flows[0].binding == flows[1].binding {
		t.Fatalf("concurrent OAuth flows collided: %+v", flows)
	}
	for _, flow := range flows {
		req := httptest.NewRequest(http.MethodGet, "https://app.example.test/api/auth/oauth/callback", nil)
		for _, other := range flows {
			req.AddCookie(other.cookie)
		}
		rec := httptest.NewRecorder()
		if !consumeOAuthBrowserBinding(rec, req, flow.state, flow.binding) {
			t.Fatalf("concurrent flow %q rejected its own cookie", flow.state)
		}
	}
}

func TestOAuthAppleStartUsesFormPostCompatibleBindingCookie(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-apple-browser-binding.db")
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, store.OAuthProvider{
		ID: "oa_apple_binding", Kind: "apple", Name: "Apple", ClientID: "client-id", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/auth/oauth/:id/start", wrap(d, oauthStartHandler))
	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/api/auth/oauth/"+provider.ID+"/start", nil)
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("Apple start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var bindingCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, oauthBrowserBindingCookiePrefix) && cookie.Value != "" {
			bindingCookie = cookie
			break
		}
	}
	if bindingCookie == nil || bindingCookie.SameSite != http.SameSiteNoneMode || !bindingCookie.Secure || !bindingCookie.HttpOnly {
		t.Fatalf("Apple browser-binding cookie=%+v", bindingCookie)
	}
}

func TestCustomOAuthStartUsesFormPostCompatibleBindingCookie(t *testing.T) {
	for _, kind := range []string{"oauth2", "oidc"} {
		t.Run(kind, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "oauth-"+kind+"-form-post-binding.db")
			providerInput := store.OAuthProvider{
				ID: "oa_" + kind + "_form_post", Kind: kind, Name: "Form Post " + kind,
				ClientID: "client-id", AuthURL: "https://identity.example.test/authorize?response_mode=form_post",
				TokenURL: "https://identity.example.test/token", Enabled: true,
			}
			if kind == "oauth2" {
				providerInput.UserInfoURL = "https://identity.example.test/me"
			} else {
				providerInput.IssuerURL = "https://identity.example.test"
				providerInput.JWKSURL = "https://identity.example.test/keys"
			}
			provider, err := store.CreateOAuthProvider(t.Context(), d.DB, providerInput)
			if err != nil {
				t.Fatal(err)
			}
			mx := newMux()
			mx.handle(http.MethodGet, "/api/auth/oauth/:id/start", wrap(d, oauthStartHandler))
			req := httptest.NewRequest(http.MethodGet, "https://app.example.test/api/auth/oauth/"+provider.ID+"/start", nil)
			rec := httptest.NewRecorder()
			mx.ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("%s form-post start status=%d body=%s", kind, rec.Code, rec.Body.String())
			}
			location, err := url.Parse(rec.Header().Get("Location"))
			if err != nil || location.Query().Get("response_mode") != "form_post" {
				t.Fatalf("%s authorization location=%q err=%v", kind, rec.Header().Get("Location"), err)
			}
			var bindingCookie *http.Cookie
			for _, cookie := range rec.Result().Cookies() {
				if strings.HasPrefix(cookie.Name, oauthBrowserBindingCookiePrefix) && cookie.Value != "" {
					bindingCookie = cookie
					break
				}
			}
			if bindingCookie == nil || bindingCookie.SameSite != http.SameSiteNoneMode ||
				!bindingCookie.Secure || !bindingCookie.HttpOnly {
				t.Fatalf("%s form-post browser-binding cookie=%+v", kind, bindingCookie)
			}
		})
	}
}

func cacheOAuthFlowStateForTest(t testing.TB, d Deps, stateID string, state oauthFlowState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	d.Cache.Set("oauth:state:"+stateID, string(raw), time.Minute)
}

func runOAuthCallbackForTest(d Deps, providerID, stateID, cookieState, binding string) *httptest.ResponseRecorder {
	mx := newMux()
	mx.handle(http.MethodGet, "/api/auth/oauth/:id/callback", wrap(d, oauthCallbackHandler))
	req := httptest.NewRequest(
		http.MethodGet,
		"https://app.example.test/api/auth/oauth/"+providerID+"/callback?code=authorization-code&state="+url.QueryEscape(stateID),
		nil,
	)
	if cookieState != "" {
		req.AddCookie(&http.Cookie{Name: oauthBrowserBindingCookieName(cookieState), Value: binding, Path: "/api/auth/oauth"})
	}
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	return rec
}

func responseCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return cookie
		}
	}
	return nil
}

func responseDeletesCookie(rec *httptest.ResponseRecorder, name string) bool {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}
