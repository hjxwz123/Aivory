package api

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/mail"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

// OAuth timing knobs — overridable via env (see docs/config-reference.md); the
// defaults preserve the previous hardcoded behaviour.
var (
	oauth2FAHandoffCookieTTL        = securityDuration("AIVORY_API_OAUTH_2FA_HANDOFF_COOKIE_TTL", 300*time.Second)
	oauthStateCacheTTL              = securityDuration("AIVORY_API_OAUTH_STATE_CACHE_TTL", 10*time.Minute)
	oauthTokenExchangeCtxTimeout    = securityDuration("AIVORY_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT", 40*time.Second)
	oauthCrossDomainHandoffTokenTTL = securityDuration("AIVORY_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL", 60*time.Second)
)

var (
	errOAuthClientSecretReentryRequired = errors.New("oauth_client_secret_reentry_required")
	errOAuthExplicitLinkRequired        = errors.New("oauth_email_link_required")

	exchangeOAuthAuthorizationCode = func(ctx context.Context, cfg oauth.Config, redirectURI, code, verifier string) (oauth.Tokens, error) {
		return cfg.Exchange(ctx, redirectURI, code, verifier)
	}
	fetchOAuthCallbackUserInfo = func(ctx context.Context, cfg oauth.Config, tokens oauth.Tokens, nonce string) (oauth.UserInfo, error) {
		return cfg.FetchUserInfo(ctx, tokens, nonce)
	}
)

// ===== Public: provider list for the login page =====

type publicOAuthProvider struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// oauthProvidersPublicHandler lists the enabled providers (no secrets) so the
// login page can render a button per configured method. Returns [] when none
// are configured, which the frontend uses to hide the whole OAuth section.
func oauthProvidersPublicHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListEnabledOAuthProviders(r.Context(), d.DB)
	if err != nil {
		writeJSON(w, 200, []publicOAuthProvider{})
		return
	}
	out := make([]publicOAuthProvider, 0, len(rows))
	for _, p := range rows {
		// A provider with no client_id can't actually start a flow — hide it so
		// the login screen never shows a button that errors on click.
		if !oauthProviderReady(p) {
			continue
		}
		out = append(out, publicOAuthProvider{ID: p.ID, Kind: p.Kind, Name: p.Name, Icon: p.Icon})
	}
	writeJSON(w, 200, out)
}

// ===== Multi-domain OAuth (§ cross-domain hand-off) =====
//
// The site answers on several domains but a provider typically registers a
// single redirect_uri, so every flow must send the provider the ONE callback it
// trusts (the canonical host, domain A) regardless of which domain the user
// started on. The flow therefore always lands on A; when the user began on a
// different — allowlisted — origin we mint a one-time hand-off token and bounce
// the browser back there, where the session cookies get set on the right host.

// oauthCallbackBase returns the canonical scheme://host whose callback path is
// registered with the providers. OAUTH_CALLBACK_BASE_URL wins when set; else we
// fall back to the request host (single-domain deployments — unchanged).
func oauthCallbackBase(d Deps, r *http.Request) string {
	if b := strings.TrimRight(strings.TrimSpace(d.Config.OAuthCallbackBaseURL), "/"); b != "" {
		return b
	}
	return externalBaseURL(r)
}

func oauthRedirectURI(callbackBase, providerID string) string {
	return strings.TrimRight(callbackBase, "/") + "/api/auth/oauth/" + url.PathEscape(providerID) + "/callback"
}

// normalizeOAuthOrigin converts a scheme://host value to the form used for
// hand-off binding. Paths, credentials, query strings and fragments are never
// valid origins. Default ports are removed to match browser origin semantics.
func normalizeOAuthOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	hostname := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if hostname == "" {
		return "", false
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return scheme + "://" + host, true
}

// allowedReturnOrigin reports whether origin (scheme://host) is an exact match
// for one of the configured return targets. This is the open-redirect guard for
// the hand-off: a value that fails here is NEVER used as a redirect destination.
func allowedReturnOrigin(d Deps, origin string) bool {
	normalized, ok := normalizeOAuthOrigin(origin)
	if !ok {
		return false
	}
	for _, o := range d.Config.OAuthReturnOrigins {
		if allowed, valid := normalizeOAuthOrigin(o); valid && allowed == normalized {
			return true
		}
	}
	return false
}

// startOrigin decides where a flow that begins on this request should return to.
// It is the request host when that differs from the canonical callback host AND
// is allowlisted; otherwise "" (a same-canonical-host flow, no hand-off). Derived
// from the (trusted-peer-gated) Host — never a client-supplied query param.
func startOrigin(d Deps, r *http.Request, callbackBase string) string {
	reqBase, ok := normalizeOAuthOrigin(externalBaseURL(r))
	if !ok {
		return ""
	}
	callbackOrigin, callbackOK := normalizeOAuthOrigin(callbackBase)
	if callbackOK && reqBase == callbackOrigin {
		return ""
	}
	if !allowedReturnOrigin(d, reqBase) {
		return ""
	}
	return reqBase
}

type oauthHandoff struct {
	UID            string                            `json:"uid"`
	Origin         string                            `json:"origin"`
	FlowState      string                            `json:"flow_state"`
	BrowserBinding string                            `json:"browser_binding"`
	TokenVer       *int                              `json:"token_ver"`
	ProviderGuard  *store.OAuthProviderCallbackGuard `json:"provider_guard"`
}

type oauthFlowState struct {
	ProviderID    string `json:"provider_id"`
	Verifier      string `json:"verifier"`
	Nonce         string `json:"nonce"`
	SignupIP      string `json:"signup_ip"`
	Captcha       string `json:"captcha_token,omitempty"` // rolling-deploy fallback for already-started flows
	CaptchaPassed bool   `json:"captcha_passed"`
	// LinkUserID marks an authenticated identity-linking flow. The token version
	// and session family bind that delayed callback to the exact authorization
	// context that started it, so password reset or session revocation also
	// invalidates an in-flight link.
	LinkUserID     string `json:"link_user_id"`
	LinkTokenVer   string `json:"link_token_ver"`
	LinkSessionID  string `json:"link_session_id"`
	Origin         string `json:"origin"`
	BrowserBinding string `json:"browser_binding"`
}

// completeOAuthLogin runs the shared login tail — account-status gate, TOTP
// hand-off, session minting — on whatever host `base` names, so the session (and
// 2FA) cookies always land on the domain the browser is actually on. Used by the
// callback for same-host logins and by the hand-off endpoint for cross-domain.
func completeOAuthLoginWithGuard(
	d Deps,
	w http.ResponseWriter,
	r *http.Request,
	user *store.User,
	base string,
	guard *store.OAuthProviderCallbackGuard,
) {
	fail := func(reason string) {
		http.Redirect(w, r, base+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
	}
	if user.Status != "active" {
		fail("account_disabled")
		return
	}
	if guard == nil {
		fail("provider_unavailable")
		return
	}
	if err := store.ValidateOAuthProviderCallbackGuard(r.Context(), d.DB, *guard); err != nil {
		fail("provider_unavailable")
		return
	}
	// 2FA gate (§ 2FA login): honour the user's TOTP setting on social logins too
	// — hand off to the login page's code step via a short-lived ticket instead of
	// minting a session here.
	if user.TotpEnabled {
		ticket := issueOAuthTwofaTicket(d, user.ID, user.TokenVer, *guard)
		if ticket == "" {
			fail("session_error")
			return
		}
		// §A10: hand the ticket to the SPA via a short-lived HttpOnly cookie (Path
		// /api/auth, so it rides only the /auth/login/2fa request) rather than the
		// URL — keeps the bearer secret out of history, Referer and access logs.
		http.SetCookie(w, &http.Cookie{
			Name: "aivory_2fa", Value: ticket, Path: "/api/auth",
			HttpOnly: true, Secure: secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: int(oauth2FAHandoffCookieTTL.Seconds()),
		})
		http.Redirect(w, r, base+"/login?twofa=1", http.StatusFound)
		return
	}
	if _, _, err := issueSessionCookiesWithOAuthGuard(
		d, w, r, user, 0, guard, store.LoginSessionWithout2FA,
	); err != nil {
		fail("session_error")
		return
	}
	recordSuccessfulLogin(d, r, user.ID, store.LoginMethodOAuth)
	http.Redirect(w, r, base+"/", http.StatusFound)
}

// oauthHandoffHandler completes a cross-domain login on the ORIGIN host. It
// redeems the one-time token the canonical callback minted (single-use, 60s TTL,
// held in the process-shared cache), loads the resolved user, then runs the
// shared login tail so the session cookies are set on THIS domain.
func oauthHandoffHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	base, baseOK := normalizeOAuthOrigin(externalBaseURL(r))
	if !baseOK {
		// Keep failure redirects relative when the request origin itself cannot be
		// represented safely; never reflect a malformed Host into Location.
		base = ""
	}
	fail := func(reason string) {
		http.Redirect(w, r, base+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
	}
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok == "" {
		fail("invalid_handoff")
		return
	}
	raw, ok := d.Cache.Take("oauth:handoff:" + tok)
	if !ok {
		fail("invalid_or_expired_handoff")
		return
	}
	var handoff oauthHandoff
	if !baseOK || json.Unmarshal([]byte(raw), &handoff) != nil ||
		strings.TrimSpace(handoff.UID) == "" || handoff.Origin != base ||
		!allowedReturnOrigin(d, base) {
		fail("invalid_handoff_origin")
		return
	}
	if strings.TrimSpace(handoff.FlowState) == "" || strings.TrimSpace(handoff.BrowserBinding) == "" ||
		!consumeOAuthBrowserBinding(w, r, handoff.FlowState, handoff.BrowserBinding) {
		fail("invalid_browser_binding")
		return
	}
	user, err := store.FindUserByID(r.Context(), d.DB, handoff.UID)
	if err != nil || user == nil || user.Status != "active" || handoff.TokenVer == nil || user.TokenVer != *handoff.TokenVer {
		fail("account_error")
		return
	}
	if handoff.ProviderGuard == nil {
		fail("provider_unavailable")
		return
	}
	completeOAuthLoginWithGuard(d, w, r, user, base, handoff.ProviderGuard)
}

const oauthBrowserBindingCookiePrefix = "aivory_oauth_"

func oauthBrowserBindingCookieName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return oauthBrowserBindingCookiePrefix + hex.EncodeToString(sum[:16])
}

func setOAuthBrowserBindingCookie(w http.ResponseWriter, r *http.Request, state, binding string, formPost bool) {
	maxAge := int(oauthStateCacheTTL / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	sameSite := http.SameSiteLaxMode
	secure := secureCookie(r)
	if formPost {
		// response_mode=form_post is a cross-site POST, on which Lax cookies are
		// intentionally withheld. Providers require HTTPS callbacks, so a Secure
		// SameSite=None state-specific cookie is appropriate here.
		sameSite = http.SameSiteNoneMode
		secure = true
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthBrowserBindingCookieName(state), Value: binding,
		Path: "/api/auth/oauth", HttpOnly: true, Secure: secure,
		SameSite: sameSite, MaxAge: maxAge, Expires: time.Now().Add(oauthStateCacheTTL),
	})
}

func consumeOAuthBrowserBinding(w http.ResponseWriter, r *http.Request, state, expected string) bool {
	name := oauthBrowserBindingCookieName(state)
	cookie, err := r.Cookie(name)
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/api/auth/oauth", HttpOnly: true,
		Secure: secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	if err != nil || expected == "" || len(cookie.Value) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expected)) == 1
}

// ===== OAuth flow =====

// oauthStartHandler kicks off the Authorization Code flow: it generates state
// (+ a PKCE verifier where supported), stashes them in the cache keyed by the
// random state, and 302-redirects the browser to the provider.
func oauthStartHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	p, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil || !p.Enabled {
		writeError(w, 404, errNotFound)
		return
	}
	cfg := oauth.Resolve(toOAuthConfig(p))
	if !oauthConfigReady(cfg) {
		writeError(w, 400, errors.New("provider is not fully configured"))
		return
	}

	state := randToken(24)
	browserBinding := randToken(24)
	nonce := ""
	if oauth.UsesIDToken(p.Kind) {
		nonce = randToken(24)
	}
	verifier := ""
	challenge := ""
	if oauth.UsesPKCE(p.Kind) {
		verifier = randToken(32)
		challenge = oauth.PKCEChallenge(verifier)
	}
	if state == "" || browserBinding == "" || (oauth.UsesIDToken(p.Kind) && nonce == "") || (oauth.UsesPKCE(p.Kind) && verifier == "") {
		writeError(w, http.StatusInternalServerError, errors.New("secure random source unavailable"))
		return
	}

	// §cross-domain: pin the callback to the canonical host the provider trusts and
	// remember the (allowlisted) origin the user began on so we can bounce back.
	callbackBase := oauthCallbackBase(d, r)
	origin := startOrigin(d, r, callbackBase)
	captchaToken := strings.TrimSpace(r.URL.Query().Get("captcha_token"))
	if len(captchaToken) > 2048 {
		captchaToken = ""
	}
	captchaPassed := captchaToken != "" && consumeCaptchaPass(d, r, captchaToken, captchaPurposeRegister)

	stash, _ := json.Marshal(oauthFlowState{
		ProviderID: id, Verifier: verifier, Nonce: nonce, Origin: origin,
		SignupIP: clientIP(r), BrowserBinding: browserBinding, CaptchaPassed: captchaPassed,
	})
	d.Cache.Set("oauth:state:"+state, string(stash), oauthStateCacheTTL)
	setOAuthBrowserBindingCookie(w, r, state, browserBinding, oauth.UsesFormPost(cfg))

	redirectURI := oauthRedirectURI(callbackBase, id)
	http.Redirect(w, r, cfg.AuthCodeURL(redirectURI, state, challenge, nonce), http.StatusFound)
}

// oauthCallbackHandler completes the flow: validates state, exchanges the code,
// resolves/creates the local user, sets the session cookies, and redirects back
// into the app. Apple posts the callback (form_post) so we accept GET and POST;
// r.ParseForm merges query + body, so FormValue works for both.
func oauthCallbackHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	// The provider redirected here using the canonical registered callback, so
	// redirect_uri for the exchange must be rebuilt from the SAME canonical host,
	// not the request host. returnBase (where the browser is finally sent) is
	// overwritten below once we read the origin out of the server-side state.
	callbackBase := oauthCallbackBase(d, r)
	returnBase := callbackBase
	fail := func(reason string) {
		http.Redirect(w, r, returnBase+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
	}

	id := pathParam(r, "id")
	if e := r.FormValue("error"); e != "" {
		fail(e)
		return
	}
	code := r.FormValue("code")
	state := r.FormValue("state")
	if code == "" || state == "" {
		fail("missing_code_or_state")
		return
	}

	raw, ok := d.Cache.Take("oauth:state:" + state)
	if !ok {
		fail("invalid_or_expired_state")
		return
	}
	var st oauthFlowState
	if json.Unmarshal([]byte(raw), &st) != nil {
		fail("invalid_state")
		return
	}
	if st.ProviderID != id {
		fail("state_mismatch")
		return
	}
	crossDomainFlow := false
	if st.Origin != "" {
		origin, originOK := normalizeOAuthOrigin(st.Origin)
		callbackOrigin, callbackOK := normalizeOAuthOrigin(callbackBase)
		if !originOK || !callbackOK || origin == callbackOrigin || !allowedReturnOrigin(d, origin) {
			fail("invalid_return_origin")
			return
		}
		returnBase = origin
		crossDomainFlow = true
	}
	// Login state must be tied to the browser that initiated it. Identity-link
	// state has a stronger authenticated token-version/session-family binding and
	// deliberately remains cookie-independent, including on cross-domain sites.
	if st.LinkUserID == "" {
		if strings.TrimSpace(st.BrowserBinding) == "" {
			fail("invalid_browser_binding")
			return
		}
		if !crossDomainFlow && !consumeOAuthBrowserBinding(w, r, state, st.BrowserBinding) {
			fail("invalid_browser_binding")
			return
		}
	}
	captchaPassed := st.CaptchaPassed
	if !captchaPassed && st.Captcha != "" {
		captchaPassed = consumeCaptchaPass(d, r, st.Captcha, captchaPurposeRegister)
	}
	p, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil || !p.Enabled {
		fail("provider_unavailable")
		return
	}
	cfg := oauth.Resolve(toOAuthConfig(p))
	if !oauthConfigReady(cfg) {
		fail("provider_unavailable")
		return
	}
	providerKind := p.Kind
	p, err = store.InitializeOAuthProviderSubjectNamespace(
		r.Context(), d.DB, *p, cfg.SubjectNamespace(),
	)
	if err != nil {
		d.Logger.Printf("[oauth] %s subject namespace initialization failed: %v", providerKind, err)
		fail("provider_unavailable")
		return
	}
	cfg = oauth.Resolve(toOAuthConfig(p))
	if !oauthConfigReady(cfg) || p.SubjectNamespace != cfg.SubjectNamespace() {
		fail("provider_unavailable")
		return
	}
	redirectURI := oauthRedirectURI(callbackBase, id)

	ctx, cancel := context.WithTimeout(r.Context(), oauthTokenExchangeCtxTimeout)
	defer cancel()

	tokens, err := exchangeOAuthAuthorizationCode(ctx, cfg, redirectURI, code, st.Verifier)
	if err != nil {
		reason := oauth.TokenExchangeFailureReason(err)
		d.Logger.Printf("[oauth] %s exchange failed (%s): %v", p.Kind, reason, err)
		fail(reason)
		return
	}
	info, err := fetchOAuthCallbackUserInfo(ctx, cfg, tokens, st.Nonce)
	if err != nil || info.Subject == "" {
		d.Logger.Printf("[oauth] %s userinfo failed: %v", p.Kind, err)
		fail("profile_fetch_failed")
		return
	}
	providerGuard := store.NewOAuthProviderCallbackGuard(*p)
	if err := store.ValidateOAuthProviderCallbackGuard(ctx, d.DB, providerGuard); err != nil {
		d.Logger.Printf("[oauth] %s provider changed during callback: %v", p.Kind, err)
		fail("provider_unavailable")
		return
	}
	// Apple delivers the display name only on first consent, in a `user` field.
	if p.Kind == "apple" && info.Name == "" {
		if n := appleUserName(r.FormValue("user")); n != "" {
			info.Name = n
		}
	}

	// §identity linking BIND flow: link this provider identity to the already-
	// authenticated user (conflict-checked) instead of logging in. No session is
	// minted, no account provisioned, 2FA is not re-challenged. Redirect back to
	// the account page with a status the SPA turns into a toast.
	if st.LinkUserID != "" {
		// The linking user's session lives on the origin domain; binding sets no
		// cookies, so a plain redirect back to returnBase is enough (no hand-off).
		acct := returnBase + "/settings/account"
		switch err := bindOAuthIdentityFromStateWithGuard(ctx, d, st, p, info, &providerGuard); {
		case err == nil:
			http.Redirect(w, r, acct+"?linked="+url.QueryEscape(p.Name), http.StatusFound)
		case errors.Is(err, store.ErrOAuthIdentityConflict):
			http.Redirect(w, r, acct+"?link_error=conflict", http.StatusFound)
		case errors.Is(err, store.ErrOAuthLinkSessionExpired):
			http.Redirect(w, r, acct+"?link_error=session_expired", http.StatusFound)
		default:
			d.Logger.Printf("[oauth] %s identity link failed: %v", p.Kind, err)
			http.Redirect(w, r, acct+"?link_error=failed", http.StatusFound)
		}
		return
	}

	user, err := resolveOAuthUser(ctx, d, p, info, oauthSignupContext{
		IP: st.SignupIP, CaptchaPassed: captchaPassed, ProviderGuard: &providerGuard,
	})
	if err != nil {
		switch {
		case errors.Is(err, errSetupRequired):
			fail("setup_required")
			return
		case errors.Is(err, errOAuthAutoProvisionDisabled):
			fail("oauth_auto_provision_disabled")
			return
		case errors.Is(err, errEmailDomainNotAllowed):
			fail("email_domain_not_allowed")
			return
		case errors.Is(err, errCaptcha):
			fail("captcha_failed")
			return
		case errors.Is(err, errRegisterIPLimit):
			fail("register_ip_limit")
			return
		case errors.Is(err, errInvalidEmail):
			fail("email_required")
			return
		}
		d.Logger.Printf("[oauth] %s account resolve failed: %v", p.Kind, err)
		fail("account_error")
		return
	}
	if user.Status == "pending" {
		verifyURL := returnBase + "/register?verify_email=" + url.QueryEscape(user.Email) +
			"&retry_after=" + strconv.Itoa(int(emailSendCooldown/time.Second))
		http.Redirect(w, r, verifyURL, http.StatusFound)
		return
	}

	// §cross-domain hand-off: the flow always completes here on the canonical host,
	// but session cookies must be set on the domain the user is actually browsing.
	// When that origin differs, mint a one-time token (single-use, 60s, in the
	// process-shared cache) and bounce back — the origin's /handoff endpoint sets
	// the cookies there. The status/2FA/session tail runs on the FINAL host.
	if crossDomainFlow {
		tok := randToken(24)
		if tok == "" {
			fail("session_error")
			return
		}
		origin, ok := normalizeOAuthOrigin(returnBase)
		if !ok || !allowedReturnOrigin(d, origin) {
			fail("session_error")
			return
		}
		tokenVer := user.TokenVer
		handoff, err := json.Marshal(oauthHandoff{
			UID: user.ID, Origin: origin, FlowState: state, BrowserBinding: st.BrowserBinding, TokenVer: &tokenVer,
			ProviderGuard: &providerGuard,
		})
		if err != nil {
			fail("session_error")
			return
		}
		d.Cache.Set("oauth:handoff:"+tok, string(handoff), oauthCrossDomainHandoffTokenTTL)
		http.Redirect(w, r, returnBase+"/api/auth/oauth/handoff?token="+url.QueryEscape(tok), http.StatusFound)
		return
	}
	completeOAuthLoginWithGuard(d, w, r, user, callbackBase, &providerGuard)
}

// ===== Identity linking (authenticated: §account → identity sources) =====

// oauthLinkStartHandler begins a BIND flow for the logged-in user. It mirrors
// oauthStartHandler but (a) stashes the caller's user id in the state so the
// shared callback links instead of logging in, and (b) returns the authorize
// URL as JSON for the SPA to navigate to. The SPA calls this with its bearer
// token (a plain browser navigation to a /start URL wouldn't carry it), then
// does a full-page redirect to the returned URL.
func oauthLinkStartHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	id := pathParam(r, "id")
	p, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil || !p.Enabled {
		writeError(w, 404, errNotFound)
		return
	}
	cfg := oauth.Resolve(toOAuthConfig(p))
	if !oauthConfigReady(cfg) {
		writeError(w, 400, errors.New("provider is not fully configured"))
		return
	}
	state := randToken(24)
	nonce := ""
	if oauth.UsesIDToken(p.Kind) {
		nonce = randToken(24)
	}
	verifier := ""
	challenge := ""
	if oauth.UsesPKCE(p.Kind) {
		verifier = randToken(32)
		challenge = oauth.PKCEChallenge(verifier)
	}
	if state == "" || (oauth.UsesIDToken(p.Kind) && nonce == "") || (oauth.UsesPKCE(p.Kind) && verifier == "") {
		writeError(w, http.StatusInternalServerError, errors.New("secure random source unavailable"))
		return
	}
	callbackBase := oauthCallbackBase(d, r)
	origin := startOrigin(d, r, callbackBase)
	claims, err := d.Auth.ParseAccess(readAccessToken(r))
	if err != nil || claims.UID != u.ID || strings.TrimSpace(claims.SessionID) == "" {
		writeError(w, http.StatusUnauthorized, errAuthRequired)
		return
	}
	stash, _ := json.Marshal(map[string]string{
		"provider_id": id, "verifier": verifier, "nonce": nonce,
		"link_user_id": u.ID, "link_token_ver": strconv.Itoa(u.TokenVer), "link_session_id": claims.SessionID,
		"origin": origin,
	})
	d.Cache.Set("oauth:state:"+state, string(stash), oauthStateCacheTTL)
	redirectURI := oauthRedirectURI(callbackBase, id)
	writeJSON(w, 200, map[string]string{"authorize_url": cfg.AuthCodeURL(redirectURI, state, challenge, nonce)})
}

func bindOAuthIdentityFromState(ctx context.Context, d Deps, st oauthFlowState, p *store.OAuthProvider, info oauth.UserInfo) error {
	return bindOAuthIdentityFromStateWithGuard(ctx, d, st, p, info, nil)
}

func bindOAuthIdentityFromStateWithGuard(
	ctx context.Context,
	d Deps,
	st oauthFlowState,
	p *store.OAuthProvider,
	info oauth.UserInfo,
	guard *store.OAuthProviderCallbackGuard,
) error {
	if strings.TrimSpace(st.LinkUserID) == "" || strings.TrimSpace(st.LinkSessionID) == "" || strings.TrimSpace(st.LinkTokenVer) == "" {
		return store.ErrOAuthLinkSessionExpired
	}
	if p == nil || strings.TrimSpace(p.ID) == "" {
		return store.ErrNotFound
	}
	tokenVer, err := strconv.Atoi(st.LinkTokenVer)
	if err != nil || tokenVer < 0 {
		return store.ErrOAuthLinkSessionExpired
	}
	info.Subject, err = oauthSubjectForProvider(p, info.Subject)
	if err != nil {
		return err
	}
	if guard != nil {
		return store.BindOAuthIdentityForCallbackSession(
			ctx, d.DB, *guard, p.ID, info.Subject, st.LinkUserID, info.Email,
			tokenVer, st.LinkSessionID,
		)
	}
	return store.BindOAuthIdentityForSession(
		ctx, d.DB, p.ID, info.Subject, st.LinkUserID, info.Email,
		tokenVer, st.LinkSessionID,
	)
}

// listIdentitiesHandler returns the current user's bound identities.
func listIdentitiesHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	rows, err := store.ListOAuthIdentitiesForUser(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

// unlinkIdentityHandler removes one bound identity (provider_id + subject in the
// query). Guards against locking out an account that has no password of its own
// and only this one identity.
func unlinkIdentityHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	providerID := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	if providerID == "" || subject == "" {
		writeError(w, 400, errInvalidInput)
		return
	}
	ok, err := store.UnbindOAuthIdentity(r.Context(), d.DB, providerID, subject, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrOAuthLastLoginMethod) {
			writeError(w, 400, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	if !ok {
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// resolveOAuthUser maps a provider identity to a local account:
//  1. an existing (provider, subject) link wins — survives email changes;
//  2. otherwise a *verified* provider email links to the matching account;
//  3. otherwise a fresh account is provisioned (synthesising a placeholder
//     email when the provider returns none, e.g. Apple "Hide My Email" opt-out).
//
// adoptOAuthAvatar copies the provider's profile picture into the user's
// settings.avatar_url — the same field the account page writes and every
// avatar render site (sidebar, workspace members, …) already reads — but ONLY
// when the user has no avatar yet: a picture the user chose themselves must
// never be overwritten by a login. Best-effort; failures never block login.
func adoptOAuthAvatar(ctx context.Context, d Deps, u *store.User, avatarURL string) {
	avatarURL = strings.TrimSpace(avatarURL)
	if u == nil || avatarURL == "" || len(avatarURL) > 2048 ||
		(!strings.HasPrefix(avatarURL, "https://") && !strings.HasPrefix(avatarURL, "http://")) {
		return
	}
	if raw, err := store.GetUserSettingKey(ctx, d.DB, u.ID, "avatar_url"); err == nil && len(raw) > 0 {
		var cur string
		if json.Unmarshal(raw, &cur) == nil && strings.TrimSpace(cur) != "" {
			return // user already has an avatar — keep their choice
		}
	}
	_, _ = store.UpdateUserSettings(ctx, d.DB, u.ID, map[string]any{"avatar_url": avatarURL})
}

type oauthSignupContext struct {
	IP            string
	CaptchaPassed bool
	ProviderGuard *store.OAuthProviderCallbackGuard
}

func resolveOAuthUser(ctx context.Context, d Deps, p *store.OAuthProvider, info oauth.UserInfo, signup oauthSignupContext) (*store.User, error) {
	if p == nil || strings.TrimSpace(p.ID) == "" {
		return nil, store.ErrNotFound
	}
	var err error
	info.Subject, err = oauthSubjectForProvider(p, info.Subject)
	if err != nil {
		return nil, err
	}
	uid, err := store.FindOAuthIdentityUser(ctx, d.DB, p.ID, info.Subject)
	if err == nil {
		u, err := store.FindUserByID(ctx, d.DB, uid)
		if err == nil {
			adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
		}
		return u, err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email != "" {
		var normalizeErr error
		email, normalizeErr = store.NormalizeUserEmail(email)
		if normalizeErr != nil {
			return nil, errInvalidEmail
		}
	}
	if email != "" && info.EmailVerified && p.Kind != "oauth2" {
		u, findErr := store.FindUserByEmail(ctx, d.DB, email)
		if findErr == nil && u != nil {
			var linkErr error
			if signup.ProviderGuard != nil {
				linkErr = store.LinkOAuthIdentityForCallback(
					ctx, d.DB, *signup.ProviderGuard, p.ID, info.Subject, u.ID, email,
				)
			} else {
				linkErr = store.LinkOAuthIdentity(ctx, d.DB, p.ID, info.Subject, u.ID, email)
			}
			if linkErr != nil {
				if errors.Is(linkErr, store.ErrOAuthIdentityConflict) {
					return findOAuthIdentityOwner(ctx, d, p.ID, info.Subject)
				}
				return nil, linkErr
			}
			adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
			return u, nil
		}
		if findErr != nil && !errors.Is(findErr, store.ErrNotFound) {
			return nil, findErr
		}
	}

	if email == "" {
		// No email from the provider → synthesize a unique, non-colliding
		// placeholder so the account can still be provisioned.
		email = p.Kind + "-" + shortHash(p.ID+":"+info.Subject) + "@oauth.local"
	} else if !info.EmailVerified || p.Kind == "oauth2" {
		// §A1 account-takeover guard: an UNVERIFIED provider email that collides
		// with an existing account must NOT auto-link — a hostile/misconfigured
		// (esp. generic oidc) provider could otherwise assert a victim's address
		// and sign in as them. Refuse; the user can link it from an authenticated
		// session instead. (Verified collisions were handled above.)
		u, findErr := store.FindUserByEmail(ctx, d.DB, email)
		if findErr == nil && u != nil {
			return nil, errOAuthExplicitLinkRequired
		}
		if findErr != nil && !errors.Is(findErr, store.ErrNotFound) {
			return nil, findErr
		}
	}

	// §OAuth auto-provision gate: everything above this point either logs in an
	// existing identity/account or falls through here because NO account
	// matched — i.e. every remaining path is a genuine new-account signup.
	// Password registration and enterprise SSO provisioning are separate policy
	// decisions. Existing linked/matching accounts remain logins and are
	// unaffected by either new-account switch.
	userCount, err := store.CountUsers(ctx, d.DB)
	if err != nil {
		return nil, err
	}
	if userCount == 0 {
		return nil, errSetupRequired
	}
	open, err := oauthBoolSetting(d, "oauth_auto_provision_enabled")
	if err != nil {
		return nil, err
	}
	if !open {
		return nil, errOAuthAutoProvisionDisabled
	}

	// OAuth is another registration transport, not an exemption from the global
	// registration policy. Existing linked/matching accounts returned above do
	// not consume a captcha or registration quota.
	if err := mail.CheckDomainWhitelist(d.DB, email); err != nil {
		return nil, errEmailDomainNotAllowed
	}
	captchaRequired, err := oauthBoolSetting(d, "register_captcha_required")
	if err != nil {
		return nil, err
	}
	if captchaRequired && !signup.CaptchaPassed {
		return nil, errCaptcha
	}

	ipLimit, err := oauthIntSetting(d, "register_ip_daily_limit")
	if err != nil || ipLimit < 0 {
		if err == nil {
			err = errors.New("negative registration IP limit")
		}
		return nil, err
	}
	ip := strings.TrimSpace(signup.IP)
	regKey := "regquota:" + ip + ":" + time.Now().Format("2006-01-02")
	quotaReserved := false
	if ipLimit > 0 {
		if ip == "" || d.Cache == nil {
			return nil, errors.New("registration quota enforcement unavailable")
		}
		n := d.Cache.Incr(regKey, 25*time.Hour)
		if n <= 0 {
			return nil, errors.New("registration quota enforcement unavailable")
		}
		if int(n) > ipLimit {
			d.Cache.Decr(regKey)
			return nil, errRegisterIPLimit
		}
		quotaReserved = true
	}
	releaseQuota := func() {
		if quotaReserved {
			d.Cache.Decr(regKey)
		}
	}

	verifyRequired, err := oauthBoolSetting(d, "email_verification_required")
	if err != nil {
		releaseQuota()
		return nil, err
	}
	// A placeholder address cannot receive the required code. Fail before
	// creating an account that could never complete activation.
	if verifyRequired && strings.TrimSpace(info.Email) == "" {
		releaseQuota()
		return nil, errInvalidEmail
	}
	status := "active"
	if verifyRequired {
		status = "pending"
	}

	var u *store.User
	if signup.ProviderGuard != nil {
		u, err = store.CreateOAuthUserForCallback(
			ctx, d.DB, *signup.ProviderGuard, p.ID, info.Subject, email, info.Name, status,
		)
	} else {
		u, err = store.CreateOAuthUser(ctx, d.DB, p.ID, info.Subject, email, info.Name, status)
	}
	if err != nil {
		releaseQuota()
		if errors.Is(err, store.ErrOAuthIdentityConflict) {
			return findOAuthIdentityOwner(ctx, d, p.ID, info.Subject)
		}
		return nil, err
	}
	adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
	if verifyRequired {
		_, allowed := reserveEmailSend(d, email, "verify")
		if allowed {
			code := genCode6()
			d.Cache.Set("verify:"+email, code, emailVerificationCodeTTL)
			if d.Mailer != nil {
				go func() {
					if err := d.Mailer.SendCode(email, code, "verify"); err != nil && d.Logger != nil {
						d.Logger.Printf("[mail] failed to send verification to %s: %v", email, err)
					}
				}()
			}
		}
	}
	return u, nil
}

func findOAuthIdentityOwner(ctx context.Context, d Deps, providerID, subject string) (*store.User, error) {
	userID, err := store.FindOAuthIdentityUser(ctx, d.DB, providerID, subject)
	if err != nil {
		return nil, err
	}
	return store.FindUserByID(ctx, d.DB, userID)
}

func oauthBoolSetting(d Deps, key string) (bool, error) {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", key, err)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return value, nil
}

func oauthIntSetting(d Deps, key string) (int, error) {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", key, err)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("decode %s: %w", key, err)
	}
	return value, nil
}

// toOAuthConfig projects a stored provider row onto the engine's Config.
func toOAuthConfig(p *store.OAuthProvider) oauth.Config {
	return oauth.Config{
		Kind:         p.Kind,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		IssuerURL:    p.IssuerURL,
		JWKSURL:      p.JWKSURL,
		AuthURL:      p.AuthURL,
		TokenURL:     p.TokenURL,
		UserInfoURL:  p.UserInfoURL,
		Scopes:       p.Scopes,
		TeamID:       p.TeamID,
		KeyID:        p.KeyID,
	}
}

func oauthSubjectForProvider(p *store.OAuthProvider, raw string) (string, error) {
	if p == nil || strings.TrimSpace(p.ID) == "" || raw == "" || p.SubjectNamespace == "" {
		return "", store.ErrOAuthProviderChanged
	}
	expected := oauth.Resolve(toOAuthConfig(p)).SubjectNamespace()
	if p.SubjectNamespace != expected {
		return "", store.ErrOAuthProviderChanged
	}
	return p.SubjectNamespace + raw, nil
}

func oauthProviderReady(p store.OAuthProvider) bool {
	return oauthConfigReady(oauth.Resolve(toOAuthConfig(&p)))
}

func effectiveOAuthProvider(p store.OAuthProvider) store.OAuthProvider {
	if p.Kind == "oauth2" {
		// OAuth 2.0 UserInfo has no issuer/JWKS trust relationship. Clear stale
		// OIDC fields when a provider is converted instead of leaving misleading
		// trust configuration attached to the row.
		p.IssuerURL = ""
		p.JWKSURL = ""
		return p
	}
	if p.Kind == "oidc" {
		p.UserInfoURL = ""
		return p
	}
	cfg := oauth.Resolve(toOAuthConfig(&p))
	p.IssuerURL = cfg.IssuerURL
	p.JWKSURL = cfg.JWKSURL
	p.AuthURL = cfg.AuthURL
	p.TokenURL = cfg.TokenURL
	p.UserInfoURL = cfg.UserInfoURL
	return p
}

func customOAuthProviderKind(kind string) bool {
	return kind == "oidc" || kind == "oauth2"
}

func oauthConfigReady(cfg oauth.Config) bool {
	if !store.ValidOAuthProviderKind(cfg.Kind) || strings.TrimSpace(cfg.ClientID) == "" ||
		strings.TrimSpace(cfg.AuthURL) == "" || strings.TrimSpace(cfg.TokenURL) == "" {
		return false
	}
	switch cfg.Kind {
	case "oidc":
		if strings.TrimSpace(cfg.IssuerURL) == "" || strings.TrimSpace(cfg.JWKSURL) == "" {
			return false
		}
		for _, endpoint := range []string{cfg.IssuerURL, cfg.JWKSURL, cfg.AuthURL, cfg.TokenURL} {
			if oauth.ValidateHTTPSProviderEndpoint(endpoint) != nil {
				return false
			}
		}
	case "oauth2":
		if strings.TrimSpace(cfg.UserInfoURL) == "" {
			return false
		}
		for _, endpoint := range []string{cfg.AuthURL, cfg.TokenURL, cfg.UserInfoURL} {
			if oauth.ValidateHTTPSProviderEndpoint(endpoint) != nil {
				return false
			}
		}
	}
	return true
}

// appleUserName parses Apple's first-consent `user` JSON payload into a display
// name. Empty string when absent or malformed.
func appleUserName(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return ""
	}
	return strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := secureRandomRead(b); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

var secureRandomRead = rand.Read

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// ===== Admin CRUD =====

type adminOAuthProviderResponse struct {
	store.OAuthProvider
	RedirectURI string `json:"redirect_uri"`
}

type preparedOAuthProviderResponse struct {
	ID          string `json:"id"`
	RedirectURI string `json:"redirect_uri"`
}

func adminOAuthProviderJSON(p store.OAuthProvider, d Deps, r *http.Request) adminOAuthProviderResponse {
	p = effectiveOAuthProvider(p)
	return adminOAuthProviderResponse{
		OAuthProvider: p,
		RedirectURI:   oauthRedirectURI(oauthCallbackBase(d, r), p.ID),
	}
}

func prepareOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	for range 4 {
		id := store.GenID("oa")
		if _, err := store.GetOAuthProvider(r.Context(), d.DB, id); errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, preparedOAuthProviderResponse{
				ID:          id,
				RedirectURI: oauthRedirectURI(oauthCallbackBase(d, r), id),
			})
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, errors.New("could not allocate oauth provider id"))
}

func listOAuthProvidersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListOAuthProviders(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	out := make([]adminOAuthProviderResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, adminOAuthProviderJSON(p, d, r))
	}
	writeJSON(w, 200, out)
}

func createOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.OAuthProvider
		ClientSecret string `json:"client_secret"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	p := req.OAuthProvider
	p.ClientSecret = req.ClientSecret
	if err := validateOAuthKind(p.Kind); err != nil {
		writeError(w, 400, err)
		return
	}
	p.ID = strings.TrimSpace(p.ID)
	if p.ID != "" && !validPreparedRecordID(p.ID, "oa") {
		writeError(w, http.StatusBadRequest, errors.New("invalid_oauth_provider_id"))
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	p = effectiveOAuthProvider(p)
	if err := validateOAuthProviderTrust(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p.SubjectNamespace = oauth.Resolve(toOAuthConfig(&p)).SubjectNamespace()
	if existing, err := store.GetOAuthProviderByName(r.Context(), d.DB, p.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrOAuthProviderNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	created, err := store.CreateOAuthProvider(r.Context(), d.DB, p)
	if err != nil {
		if errors.Is(err, store.ErrOAuthProviderNameExists) || errors.Is(err, store.ErrOAuthProviderIDExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, adminOAuthProviderJSON(*created, d, r))
}

func reorderOAuthProvidersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if err := store.ReorderOAuthProviders(r.Context(), d.DB, body.IDs); err != nil {
		if errors.Is(err, store.ErrInvalidReorder) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func updateOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var patch store.OAuthProviderPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if patch.Kind != nil {
		if err := validateOAuthKind(*patch.Kind); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	current, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	currentEffective := effectiveOAuthProvider(*current)
	effective := *current
	if patch.Kind != nil {
		effective.Kind = *patch.Kind
	}
	if patch.ClientID != nil {
		effective.ClientID = *patch.ClientID
	}
	if patch.IssuerURL != nil {
		effective.IssuerURL = *patch.IssuerURL
	}
	if patch.JWKSURL != nil {
		effective.JWKSURL = *patch.JWKSURL
	}
	if patch.AuthURL != nil {
		effective.AuthURL = *patch.AuthURL
	}
	if patch.TokenURL != nil {
		effective.TokenURL = *patch.TokenURL
	}
	if patch.UserInfoURL != nil {
		effective.UserInfoURL = *patch.UserInfoURL
	}
	if patch.Enabled != nil {
		effective.Enabled = *patch.Enabled
	}
	effective = effectiveOAuthProvider(effective)
	adminID := ""
	if admin := authUser(r); admin != nil {
		adminID = admin.ID
	}
	if oauthClientSecretReentryRequired(currentEffective, effective, patch.ClientSecret) {
		writeError(w, http.StatusBadRequest, errOAuthClientSecretReentryRequired)
		return
	}
	if err := validateOAuthProviderTrust(effective); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if effective.Kind == "oauth2" {
		issuerURL, jwksURL := "", ""
		patch.IssuerURL, patch.JWKSURL = &issuerURL, &jwksURL
	} else if effective.Kind == "oidc" {
		userinfoURL := ""
		patch.UserInfoURL = &userinfoURL
	} else if !customOAuthProviderKind(effective.Kind) {
		issuerURL, jwksURL := effective.IssuerURL, effective.JWKSURL
		authURL, tokenURL, userinfoURL := effective.AuthURL, effective.TokenURL, effective.UserInfoURL
		patch.IssuerURL, patch.JWKSURL = &issuerURL, &jwksURL
		patch.AuthURL, patch.TokenURL, patch.UserInfoURL = &authURL, &tokenURL, &userinfoURL
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		patch.Name = &name
		if name == "" {
			writeError(w, 400, errors.New("name required"))
			return
		}
		if existing, err := store.GetOAuthProviderByName(r.Context(), d.DB, name); err == nil && existing != nil && existing.ID != id {
			writeError(w, 409, store.ErrOAuthProviderNameExists)
			return
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, 500, err)
			return
		}
	}
	currentNamespace := oauth.Resolve(toOAuthConfig(&currentEffective)).SubjectNamespace()
	nextNamespace := oauth.Resolve(toOAuthConfig(&effective)).SubjectNamespace()
	tx, err := d.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.LockAuthConfigurationTx(r.Context(), tx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if adminID != "" {
		if err := lockActiveAuthAdminTx(r.Context(), tx, adminID); err != nil {
			if errors.Is(err, errAuthPolicyAdminRequired) {
				writeError(w, http.StatusForbidden, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := ensureOAuthProviderMutationAllowedTx(r.Context(), tx, adminID, &effective, ""); err != nil {
		if errors.Is(err, errAuthPolicyProviderRequired) || errors.Is(err, errAuthPolicyAdminLinkRequired) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	upd, err := updateOAuthProviderCASTx(
		r.Context(), tx, id, patch, *current, currentNamespace, nextNamespace,
	)
	if err != nil {
		if errors.Is(err, store.ErrOAuthProviderNameExists) || errors.Is(err, store.ErrOAuthProviderChanged) {
			writeError(w, 409, err)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, errNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, adminOAuthProviderJSON(*upd, d, r))
}

var updateOAuthProviderCASTx = store.UpdateOAuthProviderCASTx

func deleteOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	adminID := ""
	if admin := authUser(r); admin != nil {
		adminID = admin.ID
	}
	tx, err := d.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.LockAuthConfigurationTx(r.Context(), tx); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if adminID != "" {
		if err := lockActiveAuthAdminTx(r.Context(), tx, adminID); err != nil {
			if errors.Is(err, errAuthPolicyAdminRequired) {
				writeError(w, http.StatusForbidden, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := ensureOAuthProviderMutationAllowedTx(r.Context(), tx, adminID, nil, id); err != nil {
		if errors.Is(err, errAuthPolicyProviderRequired) || errors.Is(err, errAuthPolicyAdminLinkRequired) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := store.DeleteOAuthProviderTx(r.Context(), tx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		writeError(w, 500, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func validateOAuthKind(kind string) error {
	if store.ValidOAuthProviderKind(kind) {
		return nil
	}
	return errors.New("kind must be one of google, github, apple, oidc, oauth2")
}

func oauthClientSecretReentryRequired(current, next store.OAuthProvider, submitted *string) bool {
	if current.ClientSecret == "" || (submitted != nil && strings.TrimSpace(*submitted) != "") {
		return false
	}
	return current.Kind != next.Kind ||
		strings.TrimSpace(current.ClientID) != strings.TrimSpace(next.ClientID) ||
		oauthTokenOrigin(current.TokenURL) != oauthTokenOrigin(next.TokenURL)
}

func oauthTokenOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return strings.TrimSpace(raw)
	}
	scheme := strings.ToLower(u.Scheme)
	hostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return scheme + "://" + host
}

func validateOAuthProviderTrust(p store.OAuthProvider) error {
	if !p.Enabled {
		return nil
	}
	required := map[string]string{}
	switch p.Kind {
	case "oidc":
		required = map[string]string{
			"client_id": p.ClientID, "issuer_url": p.IssuerURL, "jwks_url": p.JWKSURL,
			"auth_url": p.AuthURL, "token_url": p.TokenURL,
		}
	case "oauth2":
		required = map[string]string{
			"client_id": p.ClientID, "auth_url": p.AuthURL, "token_url": p.TokenURL,
			"userinfo_url": p.UserInfoURL,
		}
	default:
		return nil
	}
	for name, raw := range required {
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("enabled %s providers require %s", strings.ToUpper(p.Kind), name)
		}
		if name == "client_id" {
			continue
		}
		if err := oauth.ValidateHTTPSProviderEndpoint(raw); err != nil {
			return fmt.Errorf("%s %w", name, err)
		}
	}
	return nil
}
