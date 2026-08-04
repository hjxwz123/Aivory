package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestOAuthProviderCallbackGuardRejectsEveryStaleSecurityField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(testing.TB, RowExecer)
	}{
		{"disabled", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET enabled=0 WHERE id='oa_google'`)
		}},
		{"deleted", func(t testing.TB, ex RowExecer) { execTB(t, ex, `DELETE FROM oauth_providers WHERE id='oa_google'`) }},
		{"kind", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET kind='github' WHERE id='oa_google'`)
		}},
		{"client id", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET client_id='next-client' WHERE id='oa_google'`)
		}},
		{"client secret", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET client_secret='next-secret' WHERE id='oa_google'`)
		}},
		{"issuer", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET issuer_url='https://next.example.test' WHERE id='oa_google'`)
		}},
		{"jwks", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET jwks_url='https://next.example.test/keys' WHERE id='oa_google'`)
		}},
		{"authorization endpoint", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET auth_url='https://next.example.test/auth' WHERE id='oa_google'`)
		}},
		{"token endpoint", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET token_url='https://next.example.test/token' WHERE id='oa_google'`)
		}},
		{"userinfo endpoint", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET userinfo_url='https://next.example.test/me' WHERE id='oa_google'`)
		}},
		{"scopes", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET scopes='openid email' WHERE id='oa_google'`)
		}},
		{"Apple team", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET team_id='NEXTTEAM' WHERE id='oa_google'`)
		}},
		{"Apple key", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET key_id='NEXTKEY' WHERE id='oa_google'`)
		}},
		{"subject namespace", func(t testing.TB, ex RowExecer) {
			execTB(t, ex, `UPDATE oauth_providers SET subject_namespace='oauth:v1:next:' WHERE id='oa_google'`)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupOAuthDB(t)
			exec(t, db, `UPDATE oauth_providers
				SET client_secret='secret', subject_namespace='oauth:v1:callback:'
				WHERE id='oa_google'`)
			provider, err := GetOAuthProvider(t.Context(), db, "oa_google")
			if err != nil {
				t.Fatal(err)
			}
			guard := NewOAuthProviderCallbackGuard(*provider)
			tc.mutate(t, db)
			if err := ValidateOAuthProviderCallbackGuard(t.Context(), db, guard); !errors.Is(err, ErrOAuthProviderChanged) {
				t.Fatalf("validate stale callback error=%v, want ErrOAuthProviderChanged", err)
			}
		})
	}
}

func TestOAuthProviderCallbackGuardIsAtomicWithIdentityAndSessionWrites(t *testing.T) {
	db := setupOAuthDB(t)
	exec(t, db, `UPDATE oauth_providers
		SET client_secret='secret', subject_namespace='oauth:v1:callback:'
		WHERE id='oa_google'`)
	provider, err := GetOAuthProvider(t.Context(), db, "oa_google")
	if err != nil {
		t.Fatal(err)
	}
	guard := NewOAuthProviderCallbackGuard(*provider)
	if err := SaveRefreshToken(
		t.Context(), db, "link-session", "u1", time.Now().Add(time.Hour), SessionMeta{SessionID: "link-session"},
	); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE oauth_providers SET client_secret='rotated-secret' WHERE id='oa_google'`)

	if err := LinkOAuthIdentityForCallback(
		t.Context(), db, guard, provider.ID, provider.SubjectNamespace+"link", "u1", "a@b.c",
	); !errors.Is(err, ErrOAuthProviderChanged) {
		t.Fatalf("guarded link error=%v, want ErrOAuthProviderChanged", err)
	}
	if _, err := CreateOAuthUserForCallback(
		t.Context(), db, guard, provider.ID, provider.SubjectNamespace+"create",
		"callback-create@example.test", "Callback Create", "active",
	); !errors.Is(err, ErrOAuthProviderChanged) {
		t.Fatalf("guarded create error=%v, want ErrOAuthProviderChanged", err)
	}
	if err := BindOAuthIdentityForCallbackSession(
		t.Context(), db, guard, provider.ID, provider.SubjectNamespace+"explicit-link",
		"u1", "a@b.c", 0, "link-session",
	); !errors.Is(err, ErrOAuthProviderChanged) {
		t.Fatalf("guarded explicit link error=%v, want ErrOAuthProviderChanged", err)
	}
	if err := SaveRefreshTokenForOAuthCallback(
		t.Context(), db, guard, "callback-jti", "u1", 0,
		OAuthCallbackSessionWithout2FA, "", time.Now().Add(time.Hour), SessionMeta{},
	); !errors.Is(err, ErrOAuthProviderChanged) {
		t.Fatalf("guarded session error=%v, want ErrOAuthProviderChanged", err)
	}

	var identities, createdUsers, sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_identities WHERE provider_id='oa_google'`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='callback-create@example.test'`).Scan(&createdUsers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE jti='callback-jti'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || createdUsers != 0 || sessions != 0 {
		t.Fatalf("stale guard persisted identities=%d users=%d sessions=%d", identities, createdUsers, sessions)
	}
}

func TestSaveRefreshTokenForOAuthCallbackEnforcesAuthenticationMode(t *testing.T) {
	newGuard := func(t *testing.T, db *sql.DB) OAuthProviderCallbackGuard {
		t.Helper()
		execTB(t, db, `UPDATE oauth_providers
			SET subject_namespace='oauth:v1:callback-auth:' WHERE id='oa_google'`)
		provider, err := GetOAuthProvider(t.Context(), db, "oa_google")
		if err != nil {
			t.Fatal(err)
		}
		return NewOAuthProviderCallbackGuard(*provider)
	}

	t.Run("without 2FA accepts disabled", func(t *testing.T) {
		db := setupOAuthDB(t)
		guard := newGuard(t, db)
		if err := SaveRefreshTokenForOAuthCallback(
			t.Context(), db, guard, "direct-jti", "u1", 0,
			OAuthCallbackSessionWithout2FA, "", time.Now().Add(time.Hour), SessionMeta{},
		); err != nil {
			t.Fatalf("direct OAuth session: %v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE jti='direct-jti'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("direct OAuth refresh count=%d err=%v, want 1", count, err)
		}
	})

	t.Run("without 2FA requires disabled", func(t *testing.T) {
		db := setupOAuthDB(t)
		guard := newGuard(t, db)
		if err := SetUserTotp(t.Context(), db, "u1", "NEW-SECRET", true); err != nil {
			t.Fatal(err)
		}
		err := SaveRefreshTokenForOAuthCallback(
			t.Context(), db, guard, "no-2fa-jti", "u1", 0,
			OAuthCallbackSessionWithout2FA, "", time.Now().Add(time.Hour), SessionMeta{},
		)
		if !errors.Is(err, ErrOAuthLoginStateChanged) {
			t.Fatalf("no-2FA session error=%v, want ErrOAuthLoginStateChanged", err)
		}
		assertNoRefreshTokenForCallbackGuardTest(t, db, "no-2fa-jti")
	})

	t.Run("verified 2FA accepts matching secret", func(t *testing.T) {
		db := setupOAuthDB(t)
		guard := newGuard(t, db)
		const secret = "VERIFIED-SECRET"
		if err := SetUserTotp(t.Context(), db, "u1", secret, true); err != nil {
			t.Fatal(err)
		}
		if err := SaveRefreshTokenForOAuthCallback(
			t.Context(), db, guard, "verified-2fa-jti", "u1", 0,
			OAuthCallbackSessionWithVerified2FA, secret, time.Now().Add(time.Hour), SessionMeta{},
		); err != nil {
			t.Fatalf("verified 2FA session: %v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE jti='verified-2fa-jti'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("verified 2FA refresh count=%d err=%v, want 1", count, err)
		}
	})

	t.Run("verified 2FA rejects secret rotation", func(t *testing.T) {
		db := setupOAuthDB(t)
		guard := newGuard(t, db)
		const verifiedSecret = "OLD-VERIFIED-SECRET"
		if err := SetUserTotp(t.Context(), db, "u1", "ROTATED-SECRET", true); err != nil {
			t.Fatal(err)
		}
		err := SaveRefreshTokenForOAuthCallback(
			t.Context(), db, guard, "rotated-2fa-jti", "u1", 0,
			OAuthCallbackSessionWithVerified2FA, verifiedSecret, time.Now().Add(time.Hour), SessionMeta{},
		)
		if !errors.Is(err, ErrOAuthLoginStateChanged) {
			t.Fatalf("rotated 2FA session error=%v, want ErrOAuthLoginStateChanged", err)
		}
		assertNoRefreshTokenForCallbackGuardTest(t, db, "rotated-2fa-jti")
	})
}

func assertNoRefreshTokenForCallbackGuardTest(t testing.TB, db RowExecer, jti string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM refresh_tokens WHERE jti=?`, jti).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refresh token %q was persisted", jti)
	}
}

func execTB(t testing.TB, ex RowExecer, query string, args ...any) {
	t.Helper()
	if _, err := ex.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
