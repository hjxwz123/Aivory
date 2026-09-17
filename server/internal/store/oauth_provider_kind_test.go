package store

import (
	"context"
	"errors"
	"testing"
)

func TestOAuthProviderStoreAllowsGenericOAuth2AndRejectsUnknownKinds(t *testing.T) {
	db := setupOAuthDB(t)
	ctx := context.Background()

	created, err := CreateOAuthProvider(ctx, db, OAuthProvider{
		Kind: "oauth2", Name: "Generic OAuth", ClientID: "client-id",
		AuthURL: "https://login.example.test/authorize", TokenURL: "https://login.example.test/token",
		UserInfoURL: "https://login.example.test/me", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateOAuthProvider(oauth2): %v", err)
	}
	if created.Kind != "oauth2" || created.AuthURL != "https://login.example.test/authorize" ||
		created.TokenURL != "https://login.example.test/token" || created.UserInfoURL != "https://login.example.test/me" {
		t.Fatalf("stored generic OAuth2 provider = %+v", created)
	}

	if _, err := CreateOAuthProvider(ctx, db, OAuthProvider{Kind: "saml", Name: "Unknown"}); !errors.Is(err, ErrInvalidOAuthProviderKind) {
		t.Fatalf("CreateOAuthProvider(unknown) error = %v", err)
	}
	unknown := "saml"
	if _, err := UpdateOAuthProvider(ctx, db, created.ID, OAuthProviderPatch{Kind: &unknown}); !errors.Is(err, ErrInvalidOAuthProviderKind) {
		t.Fatalf("UpdateOAuthProvider(unknown) error = %v", err)
	}
	reloaded, err := GetOAuthProvider(ctx, db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Kind != "oauth2" {
		t.Fatalf("invalid kind patch changed stored kind to %q", reloaded.Kind)
	}
}

func TestOAuthProviderStorePersistsWeComAgentID(t *testing.T) {
	db := setupOAuthDB(t)
	created, err := CreateOAuthProvider(t.Context(), db, OAuthProvider{
		Kind: "wecom", Name: "WeCom", ClientID: "ww-corp", ClientSecret: "secret",
		AgentID: "1000002", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateOAuthProvider(wecom): %v", err)
	}
	if created.Kind != "wecom" || created.AgentID != "1000002" || !created.HasSecret || created.ClientSecret != "" {
		t.Fatalf("created WeCom provider = %+v", created)
	}
	stored, err := GetOAuthProvider(t.Context(), db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentID != "1000002" || stored.ClientSecret != "secret" {
		t.Fatalf("stored WeCom provider = %+v", stored)
	}
}
