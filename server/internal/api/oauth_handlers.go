package api

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

// OAuth timing knobs — overridable via env (see docs/config-reference.md); the
// defaults preserve the previous hardcoded behaviour.
var (
	oauth2FAHandoffCookieTTL        = envcfg.Dur("AIVORY_API_OAUTH_2FA_HANDOFF_COOKIE_TTL", 300*time.Second)
	oauthStateCacheTTL              = envcfg.Dur("AIVORY_API_OAUTH_STATE_CACHE_TTL", 10*time.Minute)
	oauthTokenExchangeCtxTimeout    = envcfg.Dur("AIVORY_API_OAUTH_TOKEN_EXCHANGE_CONTEXT_TIMEOUT", 20*time.Second)
	oauthCrossDomainHandoffTokenTTL = envcfg.Dur("AIVORY_API_OAUTH_CROSS_DOMAIN_HANDOFF_TOKEN_TTL", 60*time.Second)
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
		if strings.TrimSpace(p.ClientID) == "" {
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

// allowedReturnOrigin reports whether origin (scheme://host) is an exact match
// for one of the configured return targets. This is the open-redirect guard for
// the hand-off: a value that fails here is NEVER used as a redirect destination.
func allowedReturnOrigin(d Deps, origin string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return false
	}
	for _, o := range d.Config.OAuthReturnOrigins {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(o), "/"), origin) {
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
	reqBase := strings.TrimRight(externalBaseURL(r), "/")
	if reqBase == strings.TrimRight(callbackBase, "/") {
		return ""
	}
	if !allowedReturnOrigin(d, reqBase) {
		return ""
	}
	return reqBase
}

// completeOAuthLogin runs the shared login tail — account-status gate, TOTP
// hand-off, session minting — on whatever host `base` names, so the session (and
// 2FA) cookies always land on the domain the browser is actually on. Used by the
// callback for same-host logins and by the hand-off endpoint for cross-domain.
func completeOAuthLogin(d Deps, w http.ResponseWriter, r *http.Request, user *store.User, base string) {
	fail := func(reason string) {
		http.Redirect(w, r, base+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
	}
	if user.Status != "active" {
		fail("account_disabled")
		return
	}
	// 2FA gate (§ 2FA login): honour the user's TOTP setting on social logins too
	// — hand off to the login page's code step via a short-lived ticket instead of
	// minting a session here.
	if user.TotpEnabled {
		ticket := issueTwofaTicket(d, user.ID)
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
	if _, _, err := issueSessionCookies(d, w, r, user, 0); err != nil {
		fail("session_error")
		return
	}
	http.Redirect(w, r, base+"/", http.StatusFound)
}

// oauthHandoffHandler completes a cross-domain login on the ORIGIN host. It
// redeems the one-time token the canonical callback minted (single-use, 60s TTL,
// held in the process-shared cache), loads the resolved user, then runs the
// shared login tail so the session cookies are set on THIS domain.
func oauthHandoffHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	base := externalBaseURL(r)
	fail := func(reason string) {
		http.Redirect(w, r, base+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
	}
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok == "" {
		fail("invalid_handoff")
		return
	}
	uid, ok := d.Cache.Get("oauth:handoff:" + tok)
	if !ok {
		fail("invalid_or_expired_handoff")
		return
	}
	d.Cache.Delete("oauth:handoff:" + tok) // one-time use — a redeemed token can't be replayed
	user, err := store.FindUserByID(r.Context(), d.DB, uid)
	if err != nil || user == nil {
		fail("account_error")
		return
	}
	completeOAuthLogin(d, w, r, user, base)
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
	if cfg.ClientID == "" || cfg.AuthURL == "" {
		writeError(w, 400, errors.New("provider is not fully configured"))
		return
	}

	state := randToken(24)
	verifier := ""
	challenge := ""
	if oauth.UsesPKCE(p.Kind) {
		verifier = randToken(32)
		challenge = oauth.PKCEChallenge(verifier)
	}

	// §cross-domain: pin the callback to the canonical host the provider trusts and
	// remember the (allowlisted) origin the user began on so we can bounce back.
	callbackBase := oauthCallbackBase(d, r)
	origin := startOrigin(d, r, callbackBase)

	stash, _ := json.Marshal(map[string]string{"provider_id": id, "verifier": verifier, "origin": origin})
	d.Cache.Set("oauth:state:"+state, string(stash), oauthStateCacheTTL)

	redirectURI := callbackBase + "/api/auth/oauth/" + id + "/callback"
	http.Redirect(w, r, cfg.AuthCodeURL(redirectURI, state, challenge), http.StatusFound)
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

	raw, ok := d.Cache.Get("oauth:state:" + state)
	if !ok {
		fail("invalid_or_expired_state")
		return
	}
	d.Cache.Delete("oauth:state:" + state) // one-time use
	var st struct {
		ProviderID string `json:"provider_id"`
		Verifier   string `json:"verifier"`
		// LinkUserID marks a §identity-linking BIND flow (set by the authenticated
		// link-start). When present the callback links to this user instead of
		// logging in — it is trusted because it was stashed server-side from an
		// authenticated session, never read from the callback request.
		LinkUserID string `json:"link_user_id"`
		// Origin is the allowlisted host the flow began on (§cross-domain). Like the
		// other fields it comes from the trusted server-side stash, never the
		// callback URL, so the provider/user cannot tamper with it.
		Origin string `json:"origin"`
	}
	_ = json.Unmarshal([]byte(raw), &st)
	if st.ProviderID != id {
		fail("state_mismatch")
		return
	}
	// Send the browser back to the origin domain from here on. Re-validate against
	// the allowlist (defence in depth — config may have changed mid-flight); a
	// value that no longer passes falls back to the canonical host.
	if st.Origin != "" && allowedReturnOrigin(d, st.Origin) {
		returnBase = strings.TrimRight(st.Origin, "/")
	}

	p, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil || !p.Enabled {
		fail("provider_unavailable")
		return
	}
	cfg := oauth.Resolve(toOAuthConfig(p))
	redirectURI := callbackBase + "/api/auth/oauth/" + id + "/callback"

	ctx, cancel := context.WithTimeout(r.Context(), oauthTokenExchangeCtxTimeout)
	defer cancel()

	tokens, err := cfg.Exchange(ctx, redirectURI, code, st.Verifier)
	if err != nil {
		d.Logger.Printf("[oauth] %s exchange failed: %v", p.Kind, err)
		fail("token_exchange_failed")
		return
	}
	info, err := cfg.FetchUserInfo(ctx, tokens)
	if err != nil || info.Subject == "" {
		d.Logger.Printf("[oauth] %s userinfo failed: %v", p.Kind, err)
		fail("profile_fetch_failed")
		return
	}
	// Apple delivers the display name only on first consent, in a `user` field.
	if info.Name == "" {
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
		switch err := store.BindOAuthIdentity(ctx, d.DB, p.ID, info.Subject, st.LinkUserID, info.Email); {
		case err == nil:
			http.Redirect(w, r, acct+"?linked="+url.QueryEscape(p.Name), http.StatusFound)
		case errors.Is(err, store.ErrOAuthIdentityConflict):
			http.Redirect(w, r, acct+"?link_error=conflict", http.StatusFound)
		default:
			d.Logger.Printf("[oauth] %s identity link failed: %v", p.Kind, err)
			http.Redirect(w, r, acct+"?link_error=failed", http.StatusFound)
		}
		return
	}

	user, err := resolveOAuthUser(ctx, d, p, info)
	if err != nil {
		d.Logger.Printf("[oauth] %s account resolve failed: %v", p.Kind, err)
		fail("account_error")
		return
	}

	// §cross-domain hand-off: the flow always completes here on the canonical host,
	// but session cookies must be set on the domain the user is actually browsing.
	// When that origin differs, mint a one-time token (single-use, 60s, in the
	// process-shared cache) and bounce back — the origin's /handoff endpoint sets
	// the cookies there. The status/2FA/session tail runs on the FINAL host.
	if returnBase != callbackBase {
		tok := randToken(24)
		d.Cache.Set("oauth:handoff:"+tok, user.ID, oauthCrossDomainHandoffTokenTTL)
		http.Redirect(w, r, returnBase+"/api/auth/oauth/handoff?token="+url.QueryEscape(tok), http.StatusFound)
		return
	}
	completeOAuthLogin(d, w, r, user, callbackBase)
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
	if cfg.ClientID == "" || cfg.AuthURL == "" {
		writeError(w, 400, errors.New("provider is not fully configured"))
		return
	}
	state := randToken(24)
	verifier := ""
	challenge := ""
	if oauth.UsesPKCE(p.Kind) {
		verifier = randToken(32)
		challenge = oauth.PKCEChallenge(verifier)
	}
	callbackBase := oauthCallbackBase(d, r)
	origin := startOrigin(d, r, callbackBase)
	stash, _ := json.Marshal(map[string]string{
		"provider_id": id, "verifier": verifier, "link_user_id": u.ID, "origin": origin,
	})
	d.Cache.Set("oauth:state:"+state, string(stash), oauthStateCacheTTL)
	redirectURI := callbackBase + "/api/auth/oauth/" + id + "/callback"
	writeJSON(w, 200, map[string]string{"authorize_url": cfg.AuthCodeURL(redirectURI, state, challenge)})
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
	// Lockout guard: an account with no password must keep at least one sign-in
	// method — refuse to remove the last identity (set a password first).
	if !u.HasPassword {
		n, err := store.CountOAuthIdentitiesForUser(r.Context(), d.DB, u.ID)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if n <= 1 {
			writeError(w, 400, store.ErrOAuthLastLoginMethod)
			return
		}
	}
	ok, err := store.UnbindOAuthIdentity(r.Context(), d.DB, providerID, subject, u.ID)
	if err != nil {
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

func resolveOAuthUser(ctx context.Context, d Deps, p *store.OAuthProvider, info oauth.UserInfo) (*store.User, error) {
	if uid, err := store.FindOAuthIdentityUser(ctx, d.DB, p.ID, info.Subject); err == nil {
		u, err := store.FindUserByID(ctx, d.DB, uid)
		if err == nil {
			adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
		}
		return u, err
	}

	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email != "" && info.EmailVerified {
		if u, err := store.FindUserByEmail(ctx, d.DB, email); err == nil && u != nil {
			_ = store.LinkOAuthIdentity(ctx, d.DB, p.ID, info.Subject, u.ID, email)
			adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
			return u, nil
		}
	}

	if email == "" {
		// No email from the provider → synthesize a unique, non-colliding
		// placeholder so the account can still be provisioned.
		email = p.Kind + "-" + shortHash(p.ID+":"+info.Subject) + "@oauth.local"
	} else if !info.EmailVerified {
		// §A1 account-takeover guard: an UNVERIFIED provider email that collides
		// with an existing account must NOT auto-link — a hostile/misconfigured
		// (esp. generic oidc) provider could otherwise assert a victim's address
		// and sign in as them. Refuse; the user can link it from an authenticated
		// session instead. (Verified collisions were handled above.)
		if u, err := store.FindUserByEmail(ctx, d.DB, email); err == nil && u != nil {
			return nil, errors.New("an account with this email already exists — sign in with your password first, then link this provider")
		}
	}

	u, err := store.CreateOAuthUser(ctx, d.DB, email, info.Name)
	if err != nil {
		return nil, err
	}
	_ = store.LinkOAuthIdentity(ctx, d.DB, p.ID, info.Subject, u.ID, email)
	adoptOAuthAvatar(ctx, d, u, info.AvatarURL)
	return u, nil
}

// toOAuthConfig projects a stored provider row onto the engine's Config.
func toOAuthConfig(p *store.OAuthProvider) oauth.Config {
	return oauth.Config{
		Kind:         p.Kind,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		AuthURL:      p.AuthURL,
		TokenURL:     p.TokenURL,
		UserInfoURL:  p.UserInfoURL,
		Scopes:       p.Scopes,
		TeamID:       p.TeamID,
		KeyID:        p.KeyID,
	}
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
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// ===== Admin CRUD =====

func listOAuthProvidersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListOAuthProviders(r.Context(), d.DB)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, rows)
}

func createOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var p store.OAuthProvider
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, 400, errInvalidInput)
		return
	}
	if err := validateOAuthKind(p.Kind); err != nil {
		writeError(w, 400, err)
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	if existing, err := store.GetOAuthProviderByName(r.Context(), d.DB, p.Name); err == nil && existing != nil {
		writeError(w, 409, store.ErrOAuthProviderNameExists)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, err)
		return
	}
	created, err := store.CreateOAuthProvider(r.Context(), d.DB, p)
	if err != nil {
		if errors.Is(err, store.ErrOAuthProviderNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, created)
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
	upd, err := store.UpdateOAuthProvider(r.Context(), d.DB, id, patch)
	if err != nil {
		if errors.Is(err, store.ErrOAuthProviderNameExists) {
			writeError(w, 409, err)
			return
		}
		writeError(w, 404, errNotFound)
		return
	}
	writeJSON(w, 200, upd)
}

func deleteOAuthProviderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := store.DeleteOAuthProvider(r.Context(), d.DB, id); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func validateOAuthKind(kind string) error {
	switch kind {
	case "google", "github", "apple", "oidc":
		return nil
	default:
		return errors.New("kind must be one of google, github, apple, oidc")
	}
}
