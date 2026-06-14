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

	"aurelia/server/internal/oauth"
	"aurelia/server/internal/store"
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

	stash, _ := json.Marshal(map[string]string{"provider_id": id, "verifier": verifier})
	d.Cache.Set("oauth:state:"+state, string(stash), 10*time.Minute)

	redirectURI := externalBaseURL(r) + "/api/auth/oauth/" + id + "/callback"
	http.Redirect(w, r, cfg.AuthCodeURL(redirectURI, state, challenge), http.StatusFound)
}

// oauthCallbackHandler completes the flow: validates state, exchanges the code,
// resolves/creates the local user, sets the session cookies, and redirects back
// into the app. Apple posts the callback (form_post) so we accept GET and POST;
// r.ParseForm merges query + body, so FormValue works for both.
func oauthCallbackHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	base := externalBaseURL(r)
	fail := func(reason string) {
		http.Redirect(w, r, base+"/login?oauth_error="+url.QueryEscape(reason), http.StatusFound)
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
	}
	_ = json.Unmarshal([]byte(raw), &st)
	if st.ProviderID != id {
		fail("state_mismatch")
		return
	}

	p, err := store.GetOAuthProvider(r.Context(), d.DB, id)
	if err != nil || !p.Enabled {
		fail("provider_unavailable")
		return
	}
	cfg := oauth.Resolve(toOAuthConfig(p))
	redirectURI := base + "/api/auth/oauth/" + id + "/callback"

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
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

	user, err := resolveOAuthUser(ctx, d, p, info)
	if err != nil {
		d.Logger.Printf("[oauth] %s account resolve failed: %v", p.Kind, err)
		fail("account_error")
		return
	}
	if user.Status != "active" {
		fail("account_disabled")
		return
	}
	// 2FA gate (§ 2FA login): honour the user's TOTP setting on social logins too
	// — hand off to the login page's code step via a short-lived ticket instead
	// of minting a session here.
	if user.TotpEnabled {
		ticket := issueTwofaTicket(d, user.ID)
		if ticket == "" {
			fail("session_error")
			return
		}
		// §A10: hand the ticket to the SPA via a short-lived HttpOnly cookie
		// (Path /api/auth, so it rides only the /auth/login/2fa request) rather
		// than the URL query string — keeps the bearer secret out of browser
		// history, Referer headers and access logs. The SPA only sees ?twofa=1.
		http.SetCookie(w, &http.Cookie{
			Name: "aurelia_2fa", Value: ticket, Path: "/api/auth",
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300,
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

// resolveOAuthUser maps a provider identity to a local account:
//  1. an existing (provider, subject) link wins — survives email changes;
//  2. otherwise a *verified* provider email links to the matching account;
//  3. otherwise a fresh account is provisioned (synthesising a placeholder
//     email when the provider returns none, e.g. Apple "Hide My Email" opt-out).
func resolveOAuthUser(ctx context.Context, d Deps, p *store.OAuthProvider, info oauth.UserInfo) (*store.User, error) {
	if uid, err := store.FindOAuthIdentityUser(ctx, d.DB, p.ID, info.Subject); err == nil {
		return store.FindUserByID(ctx, d.DB, uid)
	}

	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email != "" && info.EmailVerified {
		if u, err := store.FindUserByEmail(ctx, d.DB, email); err == nil && u != nil {
			_ = store.LinkOAuthIdentity(ctx, d.DB, p.ID, info.Subject, u.ID, email)
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
	if strings.TrimSpace(p.Name) == "" {
		writeError(w, 400, errors.New("name required"))
		return
	}
	created, err := store.CreateOAuthProvider(r.Context(), d.DB, p)
	if err != nil {
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
	upd, err := store.UpdateOAuthProvider(r.Context(), d.DB, id, patch)
	if err != nil {
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
