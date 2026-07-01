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
