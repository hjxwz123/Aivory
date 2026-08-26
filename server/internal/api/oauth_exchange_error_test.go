package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func TestOAuthCallbackSurfacesTokenExchangeTimeout(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-exchange-timeout.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, store.OAuthProvider{
		ID: "oa_timeout", Kind: "github", Name: "GitHub", ClientID: "client-id",
		ClientSecret: "client-secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	const stateID = "timeout-state"
	const binding = "timeout-browser-binding"
	cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
		ProviderID: provider.ID, BrowserBinding: binding,
	})

	previousExchange := exchangeOAuthAuthorizationCode
	exchangeOAuthAuthorizationCode = func(
		context.Context, oauth.Config, string, string, string,
	) (oauth.Tokens, error) {
		return oauth.Tokens{}, context.DeadlineExceeded
	}
	t.Cleanup(func() { exchangeOAuthAuthorizationCode = previousExchange })

	rec := runOAuthCallbackForTest(d, provider.ID, stateID, stateID, binding)
	if rec.Code != http.StatusFound ||
		!strings.Contains(rec.Header().Get("Location"), "oauth_error=oauth_provider_timeout") {
		t.Fatalf("callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	assertNoOAuthSessionCookies(t, rec)
}
