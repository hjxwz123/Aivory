package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const oauthCols = `id, kind, name, icon, client_id, client_secret, auth_url, token_url, userinfo_url, scopes, team_id, key_id, enabled, sort_order, updated_at`

func scanOAuthProvider(s scanner) (OAuthProvider, error) {
	var p OAuthProvider
	var en int
	if err := s.Scan(&p.ID, &p.Kind, &p.Name, &p.Icon, &p.ClientID, &p.ClientSecret,
		&p.AuthURL, &p.TokenURL, &p.UserInfoURL, &p.Scopes, &p.TeamID, &p.KeyID,
		&en, &p.SortOrder, &p.UpdatedAt); err != nil {
		return p, err
	}
	p.Enabled = en == 1
	p.HasSecret = p.ClientSecret != ""
	return p, nil
}

// ListOAuthProviders returns every provider with the secret stripped. Admin
// list shape.
func ListOAuthProviders(ctx context.Context, db *sql.DB) ([]OAuthProvider, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+oauthCols+` FROM oauth_providers ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthProvider{}
	for rows.Next() {
		p, err := scanOAuthProvider(rows)
		if err != nil {
			return nil, err
		}
		p.ClientSecret = "" // never leak
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListEnabledOAuthProviders returns the enabled providers (secret stripped) for
// the public login page.
func ListEnabledOAuthProviders(ctx context.Context, db *sql.DB) ([]OAuthProvider, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+oauthCols+` FROM oauth_providers WHERE enabled=1 ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthProvider{}
	for rows.Next() {
		p, err := scanOAuthProvider(rows)
		if err != nil {
			return nil, err
		}
		p.ClientSecret = ""
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetOAuthProvider returns one provider including the client_secret (used by
// the OAuth callback, never by list handlers).
func GetOAuthProvider(ctx context.Context, db *sql.DB, id string) (*OAuthProvider, error) {
	row := db.QueryRowContext(ctx, `SELECT `+oauthCols+` FROM oauth_providers WHERE id=?`, id)
	p, err := scanOAuthProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetOAuthProviderByName returns a provider by case-insensitive, trimmed name.
func GetOAuthProviderByName(ctx context.Context, db *sql.DB, name string) (*OAuthProvider, error) {
	row := db.QueryRowContext(ctx, `SELECT `+oauthCols+` FROM oauth_providers WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`, name)
	p, err := scanOAuthProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateOAuthProvider inserts a row and returns it (secret stripped).
func CreateOAuthProvider(ctx context.Context, db *sql.DB, p OAuthProvider) (*OAuthProvider, error) {
	if p.ID == "" {
		p.ID = genID("oa")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Icon = strings.TrimSpace(p.Icon)
	p.ClientID = strings.TrimSpace(p.ClientID)
	p.AuthURL = strings.TrimSpace(p.AuthURL)
	p.TokenURL = strings.TrimSpace(p.TokenURL)
	p.UserInfoURL = strings.TrimSpace(p.UserInfoURL)
	p.Scopes = strings.TrimSpace(p.Scopes)
	p.TeamID = strings.TrimSpace(p.TeamID)
	p.KeyID = strings.TrimSpace(p.KeyID)
	if _, err := db.ExecContext(ctx, `INSERT INTO oauth_providers(`+oauthCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Kind, p.Name, p.Icon, p.ClientID, p.ClientSecret,
		p.AuthURL, p.TokenURL, p.UserInfoURL, p.Scopes, p.TeamID, p.KeyID,
		boolInt(p.Enabled), p.SortOrder, time.Now().Unix()); err != nil {
		if isUniqueIndexErr(err, "idx_oauth_providers_name_unique", "oauth_providers.name") {
			return nil, ErrOAuthProviderNameExists
		}
		return nil, err
	}
	out, err := GetOAuthProvider(ctx, db, p.ID)
	if err != nil {
		return nil, err
	}
	out.ClientSecret = ""
	return out, nil
}

// OAuthProviderPatch carries selective updates. A nil ClientSecret (or empty
// string) leaves the stored secret untouched, mirroring ChannelPatch.APIKey.
type OAuthProviderPatch struct {
	Kind         *string `json:"kind"`
	Name         *string `json:"name"`
	Icon         *string `json:"icon"`
	ClientID     *string `json:"client_id"`
	ClientSecret *string `json:"client_secret"`
	AuthURL      *string `json:"auth_url"`
	TokenURL     *string `json:"token_url"`
	UserInfoURL  *string `json:"userinfo_url"`
	Scopes       *string `json:"scopes"`
	TeamID       *string `json:"team_id"`
	KeyID        *string `json:"key_id"`
	Enabled      *bool   `json:"enabled"`
	SortOrder    *int    `json:"sort_order"`
}

func UpdateOAuthProvider(ctx context.Context, db *sql.DB, id string, patch OAuthProviderPatch) (*OAuthProvider, error) {
	parts := []string{}
	args := []any{}
	set := func(col string, v any) { parts = append(parts, col+"=?"); args = append(args, v) }
	if patch.Kind != nil {
		set("kind", *patch.Kind)
	}
	if patch.Name != nil {
		set("name", strings.TrimSpace(*patch.Name))
	}
	if patch.Icon != nil {
		set("icon", strings.TrimSpace(*patch.Icon))
	}
	if patch.ClientID != nil {
		set("client_id", strings.TrimSpace(*patch.ClientID))
	}
	if patch.ClientSecret != nil && *patch.ClientSecret != "" {
		set("client_secret", *patch.ClientSecret)
	}
	if patch.AuthURL != nil {
		set("auth_url", strings.TrimSpace(*patch.AuthURL))
	}
	if patch.TokenURL != nil {
		set("token_url", strings.TrimSpace(*patch.TokenURL))
	}
	if patch.UserInfoURL != nil {
		set("userinfo_url", strings.TrimSpace(*patch.UserInfoURL))
	}
	if patch.Scopes != nil {
		set("scopes", strings.TrimSpace(*patch.Scopes))
	}
	if patch.TeamID != nil {
		set("team_id", strings.TrimSpace(*patch.TeamID))
	}
	if patch.KeyID != nil {
		set("key_id", strings.TrimSpace(*patch.KeyID))
	}
	if patch.Enabled != nil {
		set("enabled", boolInt(*patch.Enabled))
	}
	if patch.SortOrder != nil {
		set("sort_order", *patch.SortOrder)
	}
	if len(parts) == 0 {
		return GetOAuthProvider(ctx, db, id)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	args = append(args, id)
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("UPDATE oauth_providers SET %s WHERE id=?", strings.Join(parts, ", ")),
		args...); err != nil {
		if isUniqueIndexErr(err, "idx_oauth_providers_name_unique", "oauth_providers.name") {
			return nil, ErrOAuthProviderNameExists
		}
		return nil, err
	}
	out, err := GetOAuthProvider(ctx, db, id)
	if err != nil {
		return nil, err
	}
	out.ClientSecret = ""
	return out, nil
}

// DeleteOAuthProvider removes the provider. Orphaned oauth_identities rows are
// harmless (a future provider gets a new id and never matches the old subject),
// so we leave them rather than cascade.
func DeleteOAuthProvider(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM oauth_providers WHERE id=?", id)
	return err
}

// ===== Identity linking =====

// FindOAuthIdentityUser returns the local user id linked to (providerID,
// subject), or ErrNotFound.
func FindOAuthIdentityUser(ctx context.Context, db *sql.DB, providerID, subject string) (string, error) {
	var uid string
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM oauth_identities WHERE provider_id=? AND subject=?`, providerID, subject,
	).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return uid, err
}

// LinkOAuthIdentity records (providerID, subject) → userID. Idempotent: a
// repeat link just refreshes the stored email.
func LinkOAuthIdentity(ctx context.Context, db *sql.DB, providerID, subject, userID, email string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO oauth_identities(provider_id, subject, user_id, email)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(provider_id, subject) DO UPDATE SET user_id=excluded.user_id, email=excluded.email`,
		providerID, subject, userID, strings.ToLower(email))
	return err
}

// ListOAuthIdentitiesForUser returns every third-party identity bound to the
// user, joined with its provider row for display (§ identity linking). INNER
// JOIN drops orphaned rows whose provider was deleted — those can no longer log
// in or be meaningfully shown, so they're invisible (and harmless).
func ListOAuthIdentitiesForUser(ctx context.Context, db *sql.DB, userID string) ([]OAuthIdentity, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT i.provider_id, i.subject, i.email, i.created_at, p.name, p.kind, p.icon, p.enabled
		FROM oauth_identities i
		JOIN oauth_providers p ON p.id = i.provider_id
		WHERE i.user_id = ?
		ORDER BY p.sort_order, p.name, i.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthIdentity{}
	for rows.Next() {
		var it OAuthIdentity
		var en int
		if err := rows.Scan(&it.ProviderID, &it.Subject, &it.Email, &it.CreatedAt,
			&it.ProviderName, &it.ProviderKind, &it.ProviderIcon, &en); err != nil {
			return nil, err
		}
		it.ProviderEnabled = en == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountOAuthIdentitiesForUser counts the user's bound identities — used by the
// unbind lockout guard (an account with no password must keep at least one).
func CountOAuthIdentitiesForUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_identities WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

// BindOAuthIdentity links (providerID, subject) to userID, conflict-checked
// (§ identity linking). Unlike LinkOAuthIdentity — the LOGIN path, which
// reassigns on conflict — binding must REFUSE if the identity already belongs to
// a different account: both "someone logs in with Google A, another account
// tries to bind A" and "account 1 bound Google B, account 2 tries B" reduce to
// this single (provider, subject) primary-key collision.
//
// Insert-if-absent (ON CONFLICT DO NOTHING) then inspect the owner, so the
// check and the write are one atomic statement — no TOCTOU between a concurrent
// bind of the same identity. Re-binding the caller's own identity is a no-op
// success (refreshes the email).
func BindOAuthIdentity(ctx context.Context, db *sql.DB, providerID, subject, userID, email string) error {
	res, err := db.ExecContext(ctx,
		`INSERT INTO oauth_identities(provider_id, subject, user_id, email)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(provider_id, subject) DO NOTHING`,
		providerID, subject, userID, strings.ToLower(email))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil // freshly bound
	}
	// The row already existed — resolve who owns it.
	owner, err := FindOAuthIdentityUser(ctx, db, providerID, subject)
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrOAuthIdentityConflict
	}
	// Already ours — idempotent; refresh the stored email.
	_, _ = db.ExecContext(ctx,
		`UPDATE oauth_identities SET email=? WHERE provider_id=? AND subject=?`,
		strings.ToLower(email), providerID, subject)
	return nil
}

// UnbindOAuthIdentity removes (providerID, subject) IF it belongs to userID.
// Scoped by user_id so a caller can never delete another account's link. Returns
// true when a row was actually removed (false → nothing matched → 404).
func UnbindOAuthIdentity(ctx context.Context, db *sql.DB, providerID, subject, userID string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM oauth_identities WHERE provider_id=? AND subject=? AND user_id=?`,
		providerID, subject, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CreateOAuthUser provisions a local account for a first-time social login. The
// password is a random throwaway hash — the user signs in via the provider, or
// later sets a real password through the forgot-password flow.
func CreateOAuthUser(ctx context.Context, db *sql.DB, email, name string) (*User, error) {
	rb := make([]byte, 24)
	if _, err := rand.Read(rb); err != nil {
		return nil, err
	}
	hash, err := hashPassword(hex.EncodeToString(rb))
	if err != nil {
		return nil, err
	}
	u, err := CreateUser(ctx, db, email, name, hash)
	if err != nil {
		return nil, err
	}
	// The hash above is a random throwaway the user never chose — mark the
	// account password-unset so the client forces a set-password step
	// (§ third-party login has no password).
	if _, err := db.ExecContext(ctx, `UPDATE users SET password_set=0 WHERE id=?`, u.ID); err != nil {
		return nil, err
	}
	u.HasPassword = false
	return u, nil
}
