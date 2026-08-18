package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func newOAuthGateTestDeps(t *testing.T) Deps {
	t.Helper()
	d := newEmptyOAuthGateTestDeps(t)
	if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
		t.Fatalf("seed initial admin: %v", err)
	}
	return d
}

func newEmptyOAuthGateTestDeps(t *testing.T) Deps {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oauth.db"))
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
	// The settings cache (store.GetSetting) is a process-global 15s-TTL cache
	// keyed by setting name only, not by *sql.DB — harmless in production (one
	// DB per process) but it would otherwise leak a prior test's cached
	// signup_open value across these tests' separate temp DBs.
	store.InvalidateConfig()
	return Deps{DB: db, Cache: cache.NewMemory()}
}

type oauthGateFailingIncrCache struct {
	cache.Cache
}

func (oauthGateFailingIncrCache) Incr(string, time.Duration) int64 { return 0 }

type oauthGateMail struct {
	sent chan string
}

func (m *oauthGateMail) SendCode(to, _ string, purpose string) error {
	m.sent <- purpose + ":" + to
	return nil
}

// TestResolveOAuthUserBlocksNewSignupWhenClosed covers the §login/register
// hardening gap: a first-time visitor with no existing account or linked
// identity must be rejected when provider-driven account creation is disabled.
func TestResolveOAuthUserBlocksNewSignupWhenClosed(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	if err := store.SetSetting(d.DB, "oauth_auto_provision_enabled", false); err != nil {
		t.Fatalf("set oauth_auto_provision_enabled: %v", err)
	}
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	info := oauth.UserInfo{Subject: "sub-new-1", Email: "brandnew@example.test", EmailVerified: true, Name: "Brand New"}

	u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{})
	if u != nil {
		t.Fatalf("expected no user to be provisioned, got %+v", u)
	}
	if !errors.Is(err, errOAuthAutoProvisionDisabled) {
		t.Fatalf("err = %v, want errOAuthAutoProvisionDisabled", err)
	}

	// And no account/identity was actually created despite the attempt.
	if got, err := store.FindUserByEmail(context.Background(), d.DB, "brandnew@example.test"); err == nil && got != nil {
		t.Fatalf("a user row was created despite the closed-signup gate: %+v", got)
	}
}

func TestResolveOAuthUserRequiresInitialSetupBeforeNewSignup(t *testing.T) {
	d := newEmptyOAuthGateTestDeps(t)
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	info := oauth.UserInfo{Subject: "pre-setup-subject", Email: "first-user@example.test", EmailVerified: true}

	u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{IP: "192.0.2.10"})
	if u != nil || !errors.Is(err, errSetupRequired) {
		t.Fatalf("pre-setup OAuth signup = user %+v err %v, want nil/errSetupRequired", u, err)
	}
	var users, identities int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM oauth_identities`).Scan(&identities); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if users != 0 || identities != 0 {
		t.Fatalf("pre-setup OAuth signup persisted users=%d identities=%d", users, identities)
	}
}

func TestResolveOAuthUserRegistrationQuotaFailsClosedWhenUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ip    string
		cache func() cache.Cache
	}{
		{name: "missing source IP", ip: "", cache: cache.NewMemory},
		{name: "missing cache", ip: "192.0.2.20", cache: func() cache.Cache { return nil }},
		{name: "counter operation failure", ip: "192.0.2.21", cache: func() cache.Cache {
			return oauthGateFailingIncrCache{Cache: cache.NewMemory()}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newOAuthGateTestDeps(t)
			d.Cache = tc.cache()
			if err := store.SetSetting(d.DB, "register_ip_daily_limit", 1); err != nil {
				t.Fatalf("set registration limit: %v", err)
			}
			p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
			email := strings.ReplaceAll(tc.name, " ", "-") + "@example.test"
			info := oauth.UserInfo{Subject: tc.name, Email: email, EmailVerified: true}

			u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{IP: tc.ip})
			if u != nil || err == nil {
				t.Fatalf("quota enforcement failure created user=%+v err=%v", u, err)
			}
			if stored, findErr := store.FindUserByEmail(context.Background(), d.DB, email); findErr == nil || stored != nil {
				t.Fatalf("quota enforcement failure persisted user=%+v err=%v", stored, findErr)
			}
		})
	}
}

// TestResolveOAuthUserAllowsExistingUserLoginWhenClosed: disabled provisioning
// must only block NEW accounts — an OAuth identity already linked to an
// existing user must still be able to sign IN (that's a login, not a signup).
func TestResolveOAuthUserAllowsExistingUserLoginWhenClosed(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})

	// First contact while signups are open: provisions + links the identity.
	info := oauth.UserInfo{Subject: "sub-existing-1", Email: "existing@example.test", EmailVerified: true, Name: "Existing User"}
	first, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{})
	if err != nil || first == nil {
		t.Fatalf("initial provisioning failed: %v", err)
	}

	// Admin closes registration afterward.
	if err := store.SetSetting(d.DB, "oauth_auto_provision_enabled", false); err != nil {
		t.Fatalf("set oauth_auto_provision_enabled: %v", err)
	}

	// Same identity signing back in must still succeed (linked-identity path).
	second, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{})
	if err != nil {
		t.Fatalf("existing linked identity was blocked by the signup-closed gate: %v", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("expected the same existing user, got %+v", second)
	}

	// A second, already-registered (verified) email that hasn't linked THIS
	// provider yet must also still be able to auto-link and log in — that's
	// resolving an existing account, not minting a new one.
	if _, err := store.CreateUser(context.Background(), d.DB, "verified-elsewhere@example.test", "Someone", "hash"); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	info2 := oauth.UserInfo{Subject: "sub-existing-2", Email: "verified-elsewhere@example.test", EmailVerified: true, Name: "Someone"}
	third, err := resolveOAuthUser(context.Background(), d, p, info2, oauthSignupContext{})
	if err != nil {
		t.Fatalf("existing account auto-link was blocked by the signup-closed gate: %v", err)
	}
	if third == nil || third.Email != "verified-elsewhere@example.test" {
		t.Fatalf("expected the existing account to be resolved, got %+v", third)
	}
}

func TestGenericOAuth2VerifiedEmailCannotAutoMergeExistingAccounts(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	normal, err := store.CreateUser(context.Background(), d.DB, "member@example.test", "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.FindUserByEmail(context.Background(), d.DB, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	provider := namespacedOAuthProviderForTest(&store.OAuthProvider{
		ID: "oa_generic_no_email_merge", Kind: "oauth2", ClientID: "client-id",
		AuthURL: "https://identity.example.test/authorize", TokenURL: "https://identity.example.test/token",
		UserInfoURL: "https://identity.example.test/me",
	})
	for _, existing := range []*store.User{admin, normal} {
		rawSubject := "subject-for-" + existing.ID
		user, err := resolveOAuthUser(context.Background(), d, provider, oauth.UserInfo{
			Subject: rawSubject, Email: existing.Email, EmailVerified: true,
		}, oauthSignupContext{})
		if user != nil || !errors.Is(err, errOAuthExplicitLinkRequired) {
			t.Fatalf("OAuth2 verified email %q resolved user=%+v err=%v", existing.Email, user, err)
		}
		namespaced := mustOAuthSubjectForTest(t, provider, rawSubject)
		if owner, findErr := store.FindOAuthIdentityUser(context.Background(), d.DB, provider.ID, namespaced); owner != "" ||
			!errors.Is(findErr, store.ErrNotFound) {
			t.Fatalf("OAuth2 email collision bound owner=%q err=%v", owner, findErr)
		}
	}
}

func TestVerifiedEmailAutoMergeRemainsForGitHubAndOIDC(t *testing.T) {
	for _, kind := range []string{"github", "oidc"} {
		t.Run(kind, func(t *testing.T) {
			d := newOAuthGateTestDeps(t)
			email := kind + "-merge@example.test"
			existing, err := store.CreateUser(context.Background(), d.DB, email, "Existing", "hash")
			if err != nil {
				t.Fatal(err)
			}
			provider := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "oa_" + kind + "_merge", Kind: kind})
			info := oauth.UserInfo{Subject: kind + "-subject", Email: email, EmailVerified: true}
			resolved, err := resolveOAuthUser(context.Background(), d, provider, info, oauthSignupContext{})
			if err != nil || resolved == nil || resolved.ID != existing.ID {
				t.Fatalf("%s verified email merge user=%+v err=%v", kind, resolved, err)
			}
			subject := mustOAuthSubjectForTest(t, provider, info.Subject)
			if owner, err := store.FindOAuthIdentityUser(context.Background(), d.DB, provider.ID, subject); err != nil || owner != existing.ID {
				t.Fatalf("%s identity owner=%q err=%v", kind, owner, err)
			}
		})
	}
}

// TestResolveOAuthUserAllowsNewSignupWhenOpen: the default (open) behaviour
// must be completely unaffected by the new gate.
func TestResolveOAuthUserAllowsNewSignupWhenOpen(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	info := oauth.UserInfo{Subject: "sub-open-1", Email: "open@example.test", EmailVerified: true, Name: "Open Signup"}

	u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{})
	if err != nil {
		t.Fatalf("resolveOAuthUser with open signups: %v", err)
	}
	if u == nil || u.Email != "open@example.test" {
		t.Fatalf("expected a provisioned user, got %+v", u)
	}
}

func TestResolveOAuthUserAppliesDomainCaptchaAndIPPoliciesOnlyToNewAccounts(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	ctx := context.Background()

	if err := store.SetSetting(d.DB, "email_domain_whitelist", "allowed.test"); err != nil {
		t.Fatal(err)
	}
	blocked := oauth.UserInfo{Subject: "blocked-domain", Email: "new@blocked.test", EmailVerified: true}
	if _, err := resolveOAuthUser(ctx, d, p, blocked, oauthSignupContext{IP: "192.0.2.1", CaptchaPassed: true}); !errors.Is(err, errEmailDomainNotAllowed) {
		t.Fatalf("domain policy error = %v, want %v", err, errEmailDomainNotAllowed)
	}

	if err := store.SetSetting(d.DB, "register_captcha_required", true); err != nil {
		t.Fatal(err)
	}
	needsCaptcha := oauth.UserInfo{Subject: "needs-captcha", Email: "one@allowed.test", EmailVerified: true}
	if _, err := resolveOAuthUser(ctx, d, p, needsCaptcha, oauthSignupContext{IP: "192.0.2.1"}); !errors.Is(err, errCaptcha) {
		t.Fatalf("captcha policy error = %v, want %v", err, errCaptcha)
	}

	if err := store.SetSetting(d.DB, "register_ip_daily_limit", 1); err != nil {
		t.Fatal(err)
	}
	first, err := resolveOAuthUser(ctx, d, p, needsCaptcha, oauthSignupContext{IP: "192.0.2.1", CaptchaPassed: true})
	if err != nil || first == nil {
		t.Fatalf("first policy-compliant signup: user=%+v err=%v", first, err)
	}
	secondInfo := oauth.UserInfo{Subject: "over-quota", Email: "two@allowed.test", EmailVerified: true}
	if _, err := resolveOAuthUser(ctx, d, p, secondInfo, oauthSignupContext{IP: "192.0.2.1", CaptchaPassed: true}); !errors.Is(err, errRegisterIPLimit) {
		t.Fatalf("IP quota error = %v, want %v", err, errRegisterIPLimit)
	}

	// Signing in through an already-linked identity is a login: it must remain
	// available even after the source IP has exhausted its registration quota.
	again, err := resolveOAuthUser(ctx, d, p, needsCaptcha, oauthSignupContext{IP: "192.0.2.1"})
	if err != nil || again == nil || again.ID != first.ID {
		t.Fatalf("existing identity was subjected to signup policy: user=%+v err=%v", again, err)
	}
}

func TestResolveOAuthUserCreatesPendingAccountWhenEmailVerificationRequired(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	mailer := &oauthGateMail{sent: make(chan string, 1)}
	d.Mailer = mailer
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
	if err := store.SetSetting(d.DB, "email_verification_required", true); err != nil {
		t.Fatal(err)
	}
	info := oauth.UserInfo{Subject: "pending-subject", Email: "pending@example.test", EmailVerified: true}
	u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{})
	if err != nil {
		t.Fatalf("pending OAuth signup: %v", err)
	}
	if u.Status != "pending" || u.HasPassword {
		t.Fatalf("OAuth user state = status %q has_password=%v", u.Status, u.HasPassword)
	}
	if _, ok := d.Cache.Get("verify:pending@example.test"); !ok {
		t.Fatal("verification code was not stored")
	}
	select {
	case got := <-mailer.sent:
		if got != "verify:pending@example.test" {
			t.Fatalf("mail delivery = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("verification email was not sent")
	}
	stored, err := store.FindUserByEmail(context.Background(), d.DB, "pending@example.test")
	if err != nil || stored.Status != "pending" || stored.HasPassword {
		t.Fatalf("persisted OAuth user = %+v err=%v", stored, err)
	}
}

func TestResolveOAuthUserRejectsUnreachableVerificationAddress(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "apple", Kind: "apple", Name: "Apple"})
	if err := store.SetSetting(d.DB, "email_verification_required", true); err != nil {
		t.Fatal(err)
	}
	info := oauth.UserInfo{Subject: "no-email", Email: "", EmailVerified: false}
	if _, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{}); !errors.Is(err, errInvalidEmail) {
		t.Fatalf("missing-email policy error = %v, want %v", err, errInvalidEmail)
	}
}

func TestResolveOAuthUserFailsClosedOnMalformedRegistrationSettings(t *testing.T) {
	for _, key := range []string{
		"oauth_auto_provision_enabled",
		"email_domain_whitelist",
		"register_captcha_required",
		"register_ip_daily_limit",
		"email_verification_required",
	} {
		t.Run(key, func(t *testing.T) {
			d := newOAuthGateTestDeps(t)
			if _, err := d.DB.Exec(`UPDATE settings SET value='{' WHERE key=?`, key); err != nil {
				t.Fatal(err)
			}
			store.InvalidateConfig()
			p := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"})
			email := key + "@example.test"
			info := oauth.UserInfo{Subject: key, Email: email, EmailVerified: true}
			if u, err := resolveOAuthUser(context.Background(), d, p, info, oauthSignupContext{CaptchaPassed: true}); err == nil || u != nil {
				t.Fatalf("malformed %s failed open: user=%+v err=%v", key, u, err)
			}
			if u, err := store.FindUserByEmail(context.Background(), d.DB, email); err == nil && u != nil {
				t.Fatalf("malformed %s created user %+v", key, u)
			}
		})
	}
}
