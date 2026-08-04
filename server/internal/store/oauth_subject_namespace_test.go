package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestMigrateReclassifiesOnlyLegacyUserInfoOIDCProviders(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE oauth_providers (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL,
		icon TEXT NOT NULL DEFAULT '', client_id TEXT NOT NULL DEFAULT '',
		client_secret TEXT NOT NULL DEFAULT '', auth_url TEXT NOT NULL DEFAULT '',
		token_url TEXT NOT NULL DEFAULT '', userinfo_url TEXT NOT NULL DEFAULT '',
		scopes TEXT NOT NULL DEFAULT '', team_id TEXT NOT NULL DEFAULT '',
		key_id TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		sort_order INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO oauth_providers(id,kind,name,userinfo_url,enabled) VALUES
		('oa_legacy','oidc','Legacy UserInfo','https://legacy.example.test/me',1),
		('oa_draft','oidc','OIDC Draft','',0)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for id, wantKind := range map[string]string{"oa_legacy": "oauth2", "oa_draft": "oidc"} {
		var kind, marker, scopes string
		if err := db.QueryRow(`SELECT kind,subject_namespace,scopes FROM oauth_providers WHERE id=?`, id).Scan(&kind, &marker, &scopes); err != nil {
			t.Fatal(err)
		}
		if kind != wantKind || marker != "" {
			t.Fatalf("provider %s kind=%q marker=%q, want kind=%q empty marker", id, kind, marker, wantKind)
		}
		if id == "oa_legacy" && scopes != "openid email profile" {
			t.Fatalf("legacy UserInfo scopes=%q, want materialized previous default", scopes)
		}
	}
}

func TestInitializeOAuthProviderSubjectNamespaceMigratesLegacySubjectsOnce(t *testing.T) {
	db := setupOAuthDB(t)
	ctx := context.Background()
	const namespace = "oauth:v1:test-generation:"
	exec(t, db, `INSERT INTO oauth_identities(provider_id,subject,user_id,email) VALUES(?,?,?,?)`,
		"oa_google", "x", "u1", "a@b.c")
	exec(t, db, `INSERT INTO oauth_identities(provider_id,subject,user_id,email) VALUES(?,?,?,?)`,
		"oa_google", namespace+"x", "u2", "c@d.e")
	expected, err := GetOAuthProvider(ctx, db, "oa_google")
	if err != nil {
		t.Fatal(err)
	}

	initialized, err := InitializeOAuthProviderSubjectNamespace(ctx, db, *expected, namespace)
	if err != nil {
		t.Fatalf("initialize namespace: %v", err)
	}
	if initialized.SubjectNamespace != namespace {
		t.Fatalf("provider namespace = %q, want %q", initialized.SubjectNamespace, namespace)
	}
	// A second callback may still hold the same pre-migration snapshot. It must
	// observe the completed generation and succeed without prefixing again.
	if _, err := InitializeOAuthProviderSubjectNamespace(ctx, db, *expected, namespace); err != nil {
		t.Fatalf("idempotent initialize with legacy snapshot: %v", err)
	}

	for subject, wantOwner := range map[string]string{
		namespace + "x":             "u1",
		namespace + namespace + "x": "u2",
	} {
		owner, err := FindOAuthIdentityUser(ctx, db, "oa_google", subject)
		if err != nil || owner != wantOwner {
			t.Fatalf("identity %q owner=%q err=%v, want %q", subject, owner, err, wantOwner)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_identities WHERE provider_id='oa_google'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("migrated identity count=%d err=%v", count, err)
	}
}

func TestInitializeOAuthProviderSubjectNamespaceRejectsEveryStaleTrustField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column string
		value  string
	}{
		{"kind", "kind", "github"},
		{"client id", "client_id", "changed-client"},
		{"client secret", "client_secret", "changed-secret"},
		{"issuer", "issuer_url", "https://issuer.example.test"},
		{"jwks", "jwks_url", "https://issuer.example.test/keys"},
		{"authorization endpoint", "auth_url", "https://issuer.example.test/authorize"},
		{"token endpoint", "token_url", "https://issuer.example.test/token"},
		{"userinfo endpoint", "userinfo_url", "https://issuer.example.test/me"},
		{"scopes", "scopes", "openid profile"},
		{"Apple team", "team_id", "TEAM2"},
		{"Apple key", "key_id", "KEY2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupOAuthDB(t)
			expected, err := GetOAuthProvider(t.Context(), db, "oa_google")
			if err != nil {
				t.Fatal(err)
			}
			exec(t, db, `INSERT INTO oauth_identities(provider_id,subject,user_id,email) VALUES('oa_google','raw','u1','a@b.c')`)
			exec(t, db, "UPDATE oauth_providers SET "+tc.column+"=? WHERE id='oa_google'", tc.value)

			if _, err := InitializeOAuthProviderSubjectNamespace(
				t.Context(), db, *expected, "oauth:v1:stale-test:",
			); !errors.Is(err, ErrOAuthProviderChanged) {
				t.Fatalf("initialize after stale %s error=%v, want ErrOAuthProviderChanged", tc.column, err)
			}
			var marker, subject string
			if err := db.QueryRow(`SELECT subject_namespace FROM oauth_providers WHERE id='oa_google'`).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT subject FROM oauth_identities WHERE provider_id='oa_google'`).Scan(&subject); err != nil {
				t.Fatal(err)
			}
			if marker != "" || subject != "raw" {
				t.Fatalf("stale initialize mutated marker=%q subject=%q", marker, subject)
			}
		})
	}
}

func TestUpdateOAuthProviderCASCannotMixConcurrentAppleSecretAndOAuth2Trust(t *testing.T) {
	db := setupOAuthDB(t)
	ctx := context.Background()
	exec(t, db, `UPDATE oauth_providers SET kind='apple', client_id='apple-client', client_secret='', team_id='TEAM1', key_id='KEY1' WHERE id='oa_google'`)
	exec(t, db, `INSERT INTO oauth_identities(provider_id,subject,user_id,email) VALUES('oa_google','apple-raw','u1','a@b.c')`)
	expected, err := GetOAuthProvider(ctx, db, "oa_google")
	if err != nil {
		t.Fatal(err)
	}

	appleSecret := "-----BEGIN PRIVATE KEY-----\nAPPLE-P8\n-----END PRIVATE KEY-----"
	oauth2Kind := "oauth2"
	authURL := "https://login.example.test/authorize"
	tokenURL := "https://login.example.test/token"
	userinfoURL := "https://login.example.test/me"
	type result struct{ err error }
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	run := func(patch OAuthProviderPatch, nextNamespace string) {
		start.Wait()
		_, err := UpdateOAuthProviderCAS(
			ctx, db, expected.ID, patch, *expected,
			"oauth:v1:apple-generation:", nextNamespace,
		)
		results <- result{err: err}
	}
	go run(OAuthProviderPatch{ClientSecret: &appleSecret}, "oauth:v1:apple-generation:")
	go run(OAuthProviderPatch{
		Kind: &oauth2Kind, AuthURL: &authURL, TokenURL: &tokenURL, UserInfoURL: &userinfoURL,
	}, "oauth:v1:oauth2-generation:")
	start.Done()

	successes, stale := 0, 0
	for range 2 {
		switch err := (<-results).err; {
		case err == nil:
			successes++
		case errors.Is(err, ErrOAuthProviderChanged):
			stale++
		default:
			t.Fatalf("concurrent CAS error=%v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent CAS successes=%d stale=%d, want 1/1", successes, stale)
	}
	stored, err := GetOAuthProvider(ctx, db, expected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Kind == "oauth2" && stored.ClientSecret == appleSecret {
		t.Fatal("concurrent CAS combined generic OAuth2 trust with the Apple private key")
	}
	if stored.SubjectNamespace == "" {
		t.Fatal("successful CAS did not initialize the provider namespace")
	}
}
