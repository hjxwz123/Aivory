package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	appconfig "aivory/server/internal/config"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

type oauthAdminFixture struct {
	db  *sql.DB
	mux *mux
}

func newOAuthAdminFixture(t *testing.T, callbackBase string) oauthAdminFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "oauth-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	d := Deps{DB: db, Config: appconfig.Config{OAuthCallbackBaseURL: callbackBase}}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/oauth-providers", wrap(d, listOAuthProvidersAdmin))
	mx.handle(http.MethodPost, "/api/admin/oauth-providers/prepare", wrap(d, prepareOAuthProviderAdmin))
	mx.handle(http.MethodPost, "/api/admin/oauth-providers", wrap(d, createOAuthProviderAdmin))
	mx.handle(http.MethodPatch, "/api/admin/oauth-providers/:id", wrap(d, updateOAuthProviderAdmin))
	return oauthAdminFixture{db: db, mux: mx}
}

func (fx oauthAdminFixture) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s request: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func decodeOAuthAdminResponse[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("response status = %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var result T
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return result
}

func TestOAuthProviderPreparedIDPersistsWithCanonicalCallback(t *testing.T) {
	fx := newOAuthAdminFixture(t, "https://login.example.test/")
	prepared := decodeOAuthAdminResponse[preparedOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers/prepare", nil), http.StatusOK)
	if !validPreparedRecordID(prepared.ID, "oa") {
		t.Fatalf("prepared oauth provider id = %q, want oa_<12 lowercase hex>", prepared.ID)
	}
	wantRedirect := "https://login.example.test/api/auth/oauth/" + prepared.ID + "/callback"
	if prepared.RedirectURI != wantRedirect {
		t.Fatalf("prepared redirect URI = %q, want %q", prepared.RedirectURI, wantRedirect)
	}

	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"id": prepared.ID, "kind": "google", "name": "Prepared Google", "enabled": false,
		}), http.StatusCreated)
	if created.ID != prepared.ID {
		t.Fatalf("created id = %q, want prepared id %q", created.ID, prepared.ID)
	}
	if created.RedirectURI != prepared.RedirectURI {
		t.Fatalf("created redirect URI = %q, want prepared URI %q", created.RedirectURI, prepared.RedirectURI)
	}

	listed := decodeOAuthAdminResponse[[]adminOAuthProviderResponse](t,
		fx.request(t, http.MethodGet, "/api/admin/oauth-providers", nil), http.StatusOK)
	if len(listed) != 1 || listed[0].RedirectURI != prepared.RedirectURI {
		t.Fatalf("listed providers = %+v, want the canonical prepared URI", listed)
	}
}

func TestOAuthProviderCreateRejectsInvalidAndDuplicatePreparedIDs(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	invalid := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
		"id": "oa_NOT_HEX", "kind": "google", "name": "Invalid ID",
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid prepared id status = %d, want %d; body=%s", invalid.Code, http.StatusBadRequest, invalid.Body.String())
	}

	prepared := decodeOAuthAdminResponse[preparedOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers/prepare", nil), http.StatusOK)
	decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"id": prepared.ID, "kind": "github", "name": "First provider",
		}), http.StatusCreated)
	duplicate := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
		"id": prepared.ID, "kind": "github", "name": "Different provider name",
	})
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate prepared id status = %d, want %d; body=%s", duplicate.Code, http.StatusConflict, duplicate.Body.String())
	}
	if !strings.Contains(duplicate.Body.String(), store.ErrOAuthProviderIDExists.Error()) {
		t.Fatalf("duplicate prepared id body = %s, want %q", duplicate.Body.String(), store.ErrOAuthProviderIDExists)
	}
}

func TestOAuthProviderCreatePersistsWriteOnlyClientSecret(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind":          "oidc",
			"name":          "Example ID",
			"client_id":     "client-id",
			"client_secret": "super-secret",
			"issuer_url":    "https://id.example.test",
			"jwks_url":      "https://id.example.test/keys",
			"auth_url":      "https://id.example.test/authorize",
			"token_url":     "https://id.example.test/token",
		}), http.StatusCreated)
	if !created.HasSecret {
		t.Fatalf("created provider has_secret=false: %+v", created)
	}
	if created.ClientSecret != "" {
		t.Fatalf("create response leaked client_secret %q", created.ClientSecret)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
	if err != nil {
		t.Fatalf("load provider: %v", err)
	}
	if stored.ClientSecret != "super-secret" {
		t.Fatalf("stored client_secret = %q, want submitted secret", stored.ClientSecret)
	}
	if stored.IssuerURL != "https://id.example.test" || stored.JWKSURL != "https://id.example.test/keys" {
		t.Fatalf("stored OIDC trust config = issuer %q jwks %q", stored.IssuerURL, stored.JWKSURL)
	}
	wantNamespace := oauth.Resolve(toOAuthConfig(stored)).SubjectNamespace()
	if stored.SubjectNamespace != wantNamespace || stored.SubjectNamespace == "" {
		t.Fatalf("stored subject namespace=%q, want %q", stored.SubjectNamespace, wantNamespace)
	}
	listed := fx.request(t, http.MethodGet, "/api/admin/oauth-providers", nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "subject_namespace") ||
		strings.Contains(listed.Body.String(), stored.SubjectNamespace) {
		t.Fatalf("admin response exposed internal subject namespace: status=%d body=%s", listed.Code, listed.Body.String())
	}
}

func TestOAuthProviderAdminRejectsStaleCredentialSnapshot(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oauth2", "name": "Concurrent OAuth", "client_id": "client-id",
			"client_secret": "original-secret", "auth_url": "https://login.example.test/authorize",
			"token_url": "https://login.example.test/token", "userinfo_url": "https://login.example.test/me",
			"enabled": true,
		}), http.StatusCreated)

	previous := updateOAuthProviderCAS
	updateOAuthProviderCAS = func(
		ctx context.Context,
		db *sql.DB,
		id string,
		patch store.OAuthProviderPatch,
		expected store.OAuthProvider,
		currentNamespace string,
		nextNamespace string,
	) (*store.OAuthProvider, error) {
		if _, err := db.ExecContext(ctx, `UPDATE oauth_providers SET client_secret='concurrent-secret' WHERE id=?`, id); err != nil {
			return nil, err
		}
		return store.UpdateOAuthProviderCAS(ctx, db, id, patch, expected, currentNamespace, nextNamespace)
	}
	t.Cleanup(func() { updateOAuthProviderCAS = previous })

	rec := fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, map[string]any{"name": "Stale Rename"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.ErrOAuthProviderChanged.Error()) {
		t.Fatalf("stale provider update status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := store.GetOAuthProvider(t.Context(), fx.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != created.Name || stored.ClientSecret != "concurrent-secret" {
		t.Fatalf("stale patch merged into provider name=%q secret=%q", stored.Name, stored.ClientSecret)
	}
}

func TestEnabledOIDCProviderRequiresExplicitTrustConfiguration(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	rejected := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
		"kind": "oidc", "name": "Unsafe OIDC", "client_id": "cid", "enabled": true,
	})
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("enabled OIDC without issuer/JWKS status=%d body=%s", rejected.Code, rejected.Body.String())
	}

	disabled := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oidc", "name": "OIDC Draft", "client_id": "cid", "enabled": false,
		}), http.StatusCreated)
	enableWithoutTrust := fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+disabled.ID, map[string]any{"enabled": true})
	if enableWithoutTrust.Code != http.StatusBadRequest {
		t.Fatalf("enable OIDC draft without issuer/JWKS status=%d body=%s", enableWithoutTrust.Code, enableWithoutTrust.Body.String())
	}
	enabled := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+disabled.ID, map[string]any{
			"issuer_url": "https://id.example.test",
			"jwks_url":   "https://id.example.test/keys",
			"auth_url":   "https://id.example.test/authorize",
			"token_url":  "https://id.example.test/token",
			"enabled":    true,
		}), http.StatusOK)
	if !enabled.Enabled {
		t.Fatalf("provider remained disabled: %+v", enabled)
	}
}

func TestBuiltInOAuthProviderAdminUsesPinnedOfficialEndpoints(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "google", "name": "Pinned Google", "client_id": "cid", "enabled": true,
			"issuer_url": "https://attacker.example/issuer", "jwks_url": "https://attacker.example/keys",
			"auth_url": "https://attacker.example/authorize", "token_url": "https://attacker.example/token",
			"userinfo_url": "https://attacker.example/userinfo",
		}), http.StatusCreated)
	if created.IssuerURL != "https://accounts.google.com" || created.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" ||
		created.AuthURL != "https://accounts.google.com/o/oauth2/v2/auth" || created.TokenURL != "https://oauth2.googleapis.com/token" ||
		created.UserInfoURL != "https://openidconnect.googleapis.com/v1/userinfo" {
		t.Fatalf("create response exposed non-effective Google endpoints: %+v", created.OAuthProvider)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.IssuerURL != created.IssuerURL || stored.JWKSURL != created.JWKSURL || stored.AuthURL != created.AuthURL ||
		stored.TokenURL != created.TokenURL || stored.UserInfoURL != created.UserInfoURL || !oauthProviderReady(*stored) {
		t.Fatalf("stored/effective Google provider mismatch: stored=%+v response=%+v", stored, created.OAuthProvider)
	}

	updated := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, map[string]any{
			"issuer_url": "https://attacker.example/issuer-2", "jwks_url": "https://attacker.example/keys-2",
			"auth_url": "https://attacker.example/authorize-2", "token_url": "https://attacker.example/token-2",
			"userinfo_url": "https://attacker.example/userinfo-2",
		}), http.StatusOK)
	if updated.IssuerURL != created.IssuerURL || updated.JWKSURL != created.JWKSURL || updated.AuthURL != created.AuthURL ||
		updated.TokenURL != created.TokenURL || updated.UserInfoURL != created.UserInfoURL {
		t.Fatalf("built-in endpoint patch changed effective Google trust: %+v", updated.OAuthProvider)
	}
}

func TestGenericOAuth2ProviderPreservesCustomEndpointsAndBecomesReady(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oauth2", "name": "Example OAuth", "client_id": "client-id", "client_secret": "client-secret",
			"auth_url": "https://login.example.test/oauth/authorize", "token_url": "https://login.example.test/oauth/token",
			"userinfo_url": "https://api.example.test/v1/me", "scopes": "profile email", "enabled": true,
		}), http.StatusCreated)
	if created.Kind != "oauth2" || created.AuthURL != "https://login.example.test/oauth/authorize" ||
		created.TokenURL != "https://login.example.test/oauth/token" || created.UserInfoURL != "https://api.example.test/v1/me" ||
		created.Scopes != "profile email" || created.IssuerURL != "" || created.JWKSURL != "" || !created.HasSecret {
		t.Fatalf("generic OAuth2 provider = %+v", created.OAuthProvider)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !oauthProviderReady(*stored) {
		t.Fatalf("complete generic OAuth2 provider is not ready: %+v", stored)
	}
	if stored.ClientSecret != "client-secret" {
		t.Fatalf("stored generic OAuth2 client_secret = %q", stored.ClientSecret)
	}
	if oauthProviderReady(store.OAuthProvider{
		Kind: "unknown", ClientID: "client-id", AuthURL: "https://login.example.test/authorize",
		TokenURL: "https://login.example.test/token",
	}) {
		t.Fatal("unknown OAuth provider kind was considered ready")
	}
}

func TestGenericOAuth2DraftCanSetEndpointsAndEnableAtomically(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	draft := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oauth2", "name": "OAuth Draft", "enabled": false,
		}), http.StatusCreated)
	if oauthProviderReady(draft.OAuthProvider) {
		t.Fatalf("incomplete generic OAuth2 draft is ready: %+v", draft.OAuthProvider)
	}

	enabled := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+draft.ID, map[string]any{
			"client_id": "client-id", "auth_url": "https://login.example.test/authorize",
			"token_url": "https://login.example.test/token", "userinfo_url": "https://login.example.test/me",
			"enabled": true,
		}), http.StatusOK)
	if !enabled.Enabled || !oauthProviderReady(enabled.OAuthProvider) ||
		enabled.AuthURL != "https://login.example.test/authorize" || enabled.TokenURL != "https://login.example.test/token" ||
		enabled.UserInfoURL != "https://login.example.test/me" {
		t.Fatalf("enabled generic OAuth2 provider = %+v", enabled.OAuthProvider)
	}
}

func TestConvertingOIDCToGenericOAuth2ClearsOIDCTrustFields(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	oidc := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oidc", "name": "Convertible", "client_id": "client-id", "enabled": true,
			"issuer_url": "https://id.example.test", "jwks_url": "https://id.example.test/keys",
			"auth_url": "https://id.example.test/authorize", "token_url": "https://id.example.test/token",
		}), http.StatusCreated)

	converted := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+oidc.ID, map[string]any{
			"kind": "oauth2", "auth_url": "https://login.example.test/authorize",
			"token_url": "https://login.example.test/token", "userinfo_url": "https://login.example.test/me",
		}), http.StatusOK)
	if converted.Kind != "oauth2" || converted.IssuerURL != "" || converted.JWKSURL != "" || !oauthProviderReady(converted.OAuthProvider) {
		t.Fatalf("converted provider retained OIDC trust: %+v", converted.OAuthProvider)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, oidc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.IssuerURL != "" || stored.JWKSURL != "" {
		t.Fatalf("stored converted provider retained issuer=%q jwks=%q", stored.IssuerURL, stored.JWKSURL)
	}
}

func TestUpdatingOIDCProviderClearsLegacyUserInfoURL(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oidc", "name": "Strict OIDC", "client_id": "client-id", "enabled": true,
			"issuer_url": "https://id.example.test", "jwks_url": "https://id.example.test/keys",
			"auth_url": "https://id.example.test/authorize", "token_url": "https://id.example.test/token",
		}), http.StatusCreated)

	// Simulate a row created before strict OIDC stopped using UserInfo. The stale
	// field is outside the OIDC trust namespace, but should not survive a save.
	if _, err := fx.db.ExecContext(context.Background(),
		`UPDATE oauth_providers SET userinfo_url=? WHERE id=?`,
		"https://legacy.example.test/userinfo", created.ID); err != nil {
		t.Fatal(err)
	}
	updated := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, map[string]any{
			"name": "Strict OIDC Updated",
		}), http.StatusOK)
	if updated.UserInfoURL != "" {
		t.Fatalf("updated OIDC response retained userinfo_url=%q", updated.UserInfoURL)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UserInfoURL != "" {
		t.Fatalf("stored OIDC provider retained userinfo_url=%q", stored.UserInfoURL)
	}
}

func TestOAuthProviderTrustBoundaryChangeRequiresClientSecretReentry(t *testing.T) {
	for _, kind := range []string{"oidc", "apple"} {
		t.Run(kind+" to oauth2", func(t *testing.T) {
			fx := newOAuthAdminFixture(t, "")
			oldSecret := "old-secret"
			if kind == "apple" {
				oldSecret = "-----BEGIN PRIVATE KEY-----\nold-apple-p8-material\n-----END PRIVATE KEY-----"
			}
			body := map[string]any{
				"kind": kind, "name": "Convertible " + kind, "client_id": "old-client",
				"client_secret": oldSecret, "enabled": true,
			}
			if kind == "oidc" {
				body["issuer_url"] = "https://id.example.test"
				body["jwks_url"] = "https://id.example.test/keys"
				body["auth_url"] = "https://id.example.test/authorize"
				body["token_url"] = "https://id.example.test/token"
			}
			created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
				fx.request(t, http.MethodPost, "/api/admin/oauth-providers", body), http.StatusCreated)
			conversion := map[string]any{
				"kind": "oauth2", "client_id": "new-client", "auth_url": "https://oauth.example.test/authorize",
				"token_url": "https://oauth.example.test/token", "userinfo_url": "https://oauth.example.test/me",
			}
			rejected := fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, conversion)
			if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), errOAuthClientSecretReentryRequired.Error()) {
				t.Fatalf("conversion without secret status=%d body=%s", rejected.Code, rejected.Body.String())
			}
			if strings.Contains(rejected.Body.String(), oldSecret) {
				t.Fatal("secret reentry rejection leaked the existing client secret")
			}
			unchanged, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
			if err != nil || unchanged.ClientSecret != oldSecret {
				t.Fatalf("rejected conversion changed existing secret=%q err=%v", unchanged.ClientSecret, err)
			}

			conversion["client_secret"] = "new-secret"
			updated := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
				fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, conversion), http.StatusOK)
			if updated.Kind != "oauth2" || updated.IssuerURL != "" || updated.JWKSURL != "" {
				t.Fatalf("converted provider = %+v", updated.OAuthProvider)
			}
			stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
			if err != nil || stored.ClientSecret != "new-secret" {
				t.Fatalf("stored replacement secret=%q err=%v", stored.ClientSecret, err)
			}
		})
	}
}

func TestGenericOAuth2TokenOriginAndClientChangeRequireSecretReentry(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"kind": "oauth2", "name": "Secret Boundary", "client_id": "client-id", "client_secret": "old-secret",
			"auth_url": "https://login.example.test/authorize", "token_url": "https://login.example.test/oauth/token",
			"userinfo_url": "https://login.example.test/me", "enabled": true,
		}), http.StatusCreated)

	// A path-only change stays on the same credential origin and may retain the
	// existing secret.
	pathOnly := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, map[string]any{
			"token_url": "https://login.example.test/oauth/v2/token",
		}), http.StatusOK)
	if pathOnly.TokenURL != "https://login.example.test/oauth/v2/token" {
		t.Fatalf("path-only token URL update = %q", pathOnly.TokenURL)
	}

	for name, patch := range map[string]map[string]any{
		"token origin": {"token_url": "https://tokens.example.test/oauth/token"},
		"client id":    {"client_id": "different-client"},
	} {
		t.Run(name, func(t *testing.T) {
			rejected := fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, patch)
			if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), errOAuthClientSecretReentryRequired.Error()) {
				t.Fatalf("credential boundary update status=%d body=%s", rejected.Code, rejected.Body.String())
			}
		})
	}

	updated := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPatch, "/api/admin/oauth-providers/"+created.ID, map[string]any{
			"token_url": "https://tokens.example.test/oauth/token", "client_secret": "replacement-secret",
		}), http.StatusOK)
	if updated.TokenURL != "https://tokens.example.test/oauth/token" {
		t.Fatalf("token origin update = %q", updated.TokenURL)
	}
	stored, err := store.GetOAuthProvider(context.Background(), fx.db, created.ID)
	if err != nil || stored.ClientSecret != "replacement-secret" {
		t.Fatalf("stored replacement secret=%q err=%v", stored.ClientSecret, err)
	}
}

func TestEnabledGenericOAuth2ProviderRequiresSafeFixedEndpoints(t *testing.T) {
	tests := []struct {
		name  string
		patch map[string]any
	}{
		{name: "missing client id", patch: map[string]any{"client_id": ""}},
		{name: "missing userinfo", patch: map[string]any{"userinfo_url": ""}},
		{name: "plaintext authorization", patch: map[string]any{"auth_url": "http://login.example.test/authorize"}},
		{name: "plaintext token", patch: map[string]any{"token_url": "http://login.example.test/token"}},
		{name: "userinfo credentials", patch: map[string]any{"userinfo_url": "https://user:password@login.example.test/me"}},
		{name: "userinfo fragment", patch: map[string]any{"userinfo_url": "https://login.example.test/me#private"}},
		{name: "loopback token", patch: map[string]any{"token_url": "https://127.0.0.1/token"}},
		{name: "metadata userinfo", patch: map[string]any{"userinfo_url": "https://169.254.169.254/latest/meta-data"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newOAuthAdminFixture(t, "")
			body := map[string]any{
				"kind": "oauth2", "name": "Unsafe OAuth", "client_id": "client-id", "enabled": true,
				"auth_url": "https://login.example.test/authorize", "token_url": "https://login.example.test/token",
				"userinfo_url": "https://login.example.test/me",
			}
			for key, value := range tc.patch {
				body[key] = value
			}
			rec := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("unsafe generic OAuth2 status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestOAuthProviderPrepareRequiresAdmin(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	router := NewRouter(Deps{DB: fx.db})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/oauth-providers/prepare", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("oauth prepare status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
