package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const oauthCols = `id, kind, name, icon, client_id, client_secret, issuer_url, jwks_url, auth_url, token_url, userinfo_url, scopes, team_id, key_id, subject_namespace, enabled, sort_order, updated_at`

// OAuthProviderCallbackGuard is an opaque, non-secret snapshot of the exact
// provider trust and credential generation used for an OAuth callback. It is
// safe to carry in server-side handoff/2FA cache payloads; validation always
// recomputes the digest from the current database row.
type OAuthProviderCallbackGuard struct {
	ProviderID string `json:"provider_id"`
	Snapshot   string `json:"snapshot"`
}

// NewOAuthProviderCallbackGuard binds a callback to every field which can
// affect token exchange, identity verification, or subject interpretation.
func NewOAuthProviderCallbackGuard(p OAuthProvider) OAuthProviderCallbackGuard {
	return OAuthProviderCallbackGuard{
		ProviderID: p.ID,
		Snapshot:   oauthProviderCallbackSnapshot(p),
	}
}

func oauthProviderCallbackSnapshot(p OAuthProvider) string {
	h := sha256.New()
	for _, field := range []string{
		p.ID, p.Kind, p.ClientID, p.ClientSecret, p.IssuerURL, p.JWKSURL,
		p.AuthURL, p.TokenURL, p.UserInfoURL, p.Scopes, p.TeamID, p.KeyID,
		p.SubjectNamespace,
	} {
		// Length framing avoids delimiter ambiguity without serializing secrets.
		_, _ = fmt.Fprintf(h, "%d:", len(field))
		_, _ = io.WriteString(h, field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidOAuthProviderKind is the single persisted-kind allowlist shared by the
// store and HTTP admin boundary. oauth2 is intentionally separate from oidc:
// only oidc consumes signed ID tokens, while oauth2 obtains identity from a
// configured userinfo endpoint.
func ValidOAuthProviderKind(kind string) bool {
	switch kind {
	case "google", "github", "apple", "oidc", "oauth2":
		return true
	default:
		return false
	}
}

// MigrateLegacyOAuthProviderKinds reclassifies the pre-OIDC-verification
// custom UserInfo shape. It is used both during schema migration and after a
// version-1 logical backup is restored into an already-migrated database.
func MigrateLegacyOAuthProviderKinds(ctx context.Context, ex RowExecer) error {
	_, err := ex.ExecContext(ctx, `UPDATE oauth_providers
		SET kind='oauth2',
		    scopes=CASE WHEN trim(scopes)='' THEN 'openid email profile' ELSE scopes END
		WHERE kind='oidc' AND trim(subject_namespace)=''
		  AND trim(issuer_url)='' AND trim(jwks_url)=''
		  AND trim(userinfo_url)<>''`)
	return err
}

func scanOAuthProvider(s scanner) (OAuthProvider, error) {
	var p OAuthProvider
	var en int
	if err := s.Scan(&p.ID, &p.Kind, &p.Name, &p.Icon, &p.ClientID, &p.ClientSecret,
		&p.IssuerURL, &p.JWKSURL, &p.AuthURL, &p.TokenURL, &p.UserInfoURL, &p.Scopes, &p.TeamID, &p.KeyID,
		&p.SubjectNamespace, &en, &p.SortOrder, &p.UpdatedAt); err != nil {
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
	if !ValidOAuthProviderKind(p.Kind) {
		return nil, ErrInvalidOAuthProviderKind
	}
	if p.ID == "" {
		p.ID = genID("oa")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Icon = strings.TrimSpace(p.Icon)
	p.ClientID = strings.TrimSpace(p.ClientID)
	p.IssuerURL = strings.TrimSpace(p.IssuerURL)
	p.JWKSURL = strings.TrimSpace(p.JWKSURL)
	p.AuthURL = strings.TrimSpace(p.AuthURL)
	p.TokenURL = strings.TrimSpace(p.TokenURL)
	p.UserInfoURL = strings.TrimSpace(p.UserInfoURL)
	p.Scopes = strings.TrimSpace(p.Scopes)
	p.TeamID = strings.TrimSpace(p.TeamID)
	p.KeyID = strings.TrimSpace(p.KeyID)
	if _, err := db.ExecContext(ctx, `INSERT INTO oauth_providers(`+oauthCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Kind, p.Name, p.Icon, p.ClientID, p.ClientSecret,
		p.IssuerURL, p.JWKSURL, p.AuthURL, p.TokenURL, p.UserInfoURL, p.Scopes, p.TeamID, p.KeyID,
		p.SubjectNamespace, boolInt(p.Enabled), p.SortOrder, time.Now().Unix()); err != nil {
		if isUniqueIndexErr(err, "idx_oauth_providers_name_unique", "oauth_providers.name") {
			return nil, ErrOAuthProviderNameExists
		}
		if isUniqueIndexErr(err, "oauth_providers.id", "oauth_providers_pkey") {
			return nil, ErrOAuthProviderIDExists
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
	IssuerURL    *string `json:"issuer_url"`
	JWKSURL      *string `json:"jwks_url"`
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
	if patch.Kind != nil && !ValidOAuthProviderKind(*patch.Kind) {
		return nil, ErrInvalidOAuthProviderKind
	}
	// Trust and credential fields must move together with subject_namespace.
	// Production callers use UpdateOAuthProviderCAS below; refusing the legacy
	// write path prevents a future caller from silently reinterpreting existing
	// identities under a new provider configuration.
	if oauthProviderTrustPatch(patch) {
		return nil, ErrOAuthProviderChanged
	}
	parts, args := oauthProviderPatchSQL(patch)
	if len(parts) == 0 {
		return GetOAuthProvider(ctx, db, id)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix(), id)
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

func oauthProviderTrustPatch(patch OAuthProviderPatch) bool {
	return patch.Kind != nil || patch.ClientID != nil || patch.ClientSecret != nil ||
		patch.IssuerURL != nil || patch.JWKSURL != nil || patch.AuthURL != nil ||
		patch.TokenURL != nil || patch.UserInfoURL != nil
}

func oauthProviderPatchSQL(patch OAuthProviderPatch) ([]string, []any) {
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
	if patch.IssuerURL != nil {
		set("issuer_url", strings.TrimSpace(*patch.IssuerURL))
	}
	if patch.JWKSURL != nil {
		set("jwks_url", strings.TrimSpace(*patch.JWKSURL))
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
	return parts, args
}

// InitializeOAuthProviderSubjectNamespace upgrades a legacy provider row and
// all of its raw identities as one transaction. The provider row is the shared
// serialization point for callbacks and administrator trust changes.
func InitializeOAuthProviderSubjectNamespace(
	ctx context.Context,
	db *sql.DB,
	expected OAuthProvider,
	namespace string,
) (*OAuthProvider, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := InitializeOAuthProviderSubjectNamespaceTx(ctx, tx, expected, namespace)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return current, nil
}

// GetOAuthProviderForUpdate locks and returns a provider inside a caller-owned
// transaction. It is used by configuration import before it transforms a row's
// trust domain.
func GetOAuthProviderForUpdate(ctx context.Context, tx *sql.Tx, id string) (*OAuthProvider, error) {
	current, err := lockOAuthProvider(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &current, nil
}

// InitializeOAuthProviderSubjectNamespaceTx is the caller-owned transaction
// form used by configuration import. The transaction must be committed by the
// caller after its provider UPSERT succeeds.
func InitializeOAuthProviderSubjectNamespaceTx(
	ctx context.Context,
	tx *sql.Tx,
	expected OAuthProvider,
	namespace string,
) (*OAuthProvider, error) {
	if strings.TrimSpace(expected.ID) == "" || namespace == "" {
		return nil, ErrOAuthProviderChanged
	}
	current, err := lockOAuthProvider(ctx, tx, expected.ID)
	if err != nil {
		return nil, err
	}
	if !sameOAuthProviderTrustState(current, expected) {
		return nil, ErrOAuthProviderChanged
	}
	if expected.SubjectNamespace != "" && current.SubjectNamespace != expected.SubjectNamespace {
		return nil, ErrOAuthProviderChanged
	}
	switch current.SubjectNamespace {
	case "":
		if err := migrateLegacyOAuthSubjects(ctx, tx, current.ID, namespace); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE oauth_providers SET subject_namespace=? WHERE id=?`, namespace, current.ID); err != nil {
			return nil, err
		}
		current.SubjectNamespace = namespace
	case namespace:
		// A concurrent callback may have completed the identical migration while
		// this caller waited for the provider-row lock. Treat that as success.
	default:
		return nil, ErrOAuthProviderChanged
	}
	return &current, nil
}

// UpdateOAuthProviderCAS applies an administrator patch only if the complete
// credential/trust snapshot still matches the row that was validated by the
// handler. Legacy identity migration and namespace rotation happen under the
// same provider-row lock.
func UpdateOAuthProviderCAS(
	ctx context.Context,
	db *sql.DB,
	id string,
	patch OAuthProviderPatch,
	expected OAuthProvider,
	currentNamespace string,
	nextNamespace string,
) (*OAuthProvider, error) {
	if patch.Kind != nil && !ValidOAuthProviderKind(*patch.Kind) {
		return nil, ErrInvalidOAuthProviderKind
	}
	if strings.TrimSpace(id) == "" || expected.ID != id || currentNamespace == "" || nextNamespace == "" {
		return nil, ErrOAuthProviderChanged
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := lockOAuthProvider(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !sameOAuthProviderTrustState(current, expected) {
		return nil, ErrOAuthProviderChanged
	}
	if expected.SubjectNamespace != "" && current.SubjectNamespace != expected.SubjectNamespace {
		return nil, ErrOAuthProviderChanged
	}
	switch current.SubjectNamespace {
	case "":
		if err := migrateLegacyOAuthSubjects(ctx, tx, id, currentNamespace); err != nil {
			return nil, err
		}
	case currentNamespace:
		// Already initialized, including by a callback which committed while this
		// request waited for the row lock.
	default:
		return nil, ErrOAuthProviderChanged
	}

	parts, args := oauthProviderPatchSQL(patch)
	parts = append(parts, "subject_namespace=?", "updated_at=?")
	args = append(args, nextNamespace, time.Now().Unix(), id)
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE oauth_providers SET %s WHERE id=?", strings.Join(parts, ", ")),
		args...); err != nil {
		if isUniqueIndexErr(err, "idx_oauth_providers_name_unique", "oauth_providers.name") {
			return nil, ErrOAuthProviderNameExists
		}
		return nil, err
	}
	updated, err := scanOAuthProvider(tx.QueryRowContext(ctx,
		`SELECT `+oauthCols+` FROM oauth_providers WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	updated.ClientSecret = ""
	return &updated, nil
}

func lockOAuthProvider(ctx context.Context, tx *sql.Tx, id string) (OAuthProvider, error) {
	// A no-op write is portable across SQLite and PostgreSQL and obtains the row
	// write lock before the snapshot is read. It also avoids depending on
	// SQLite's unsupported SELECT ... FOR UPDATE syntax.
	_, err := tx.ExecContext(ctx,
		`UPDATE oauth_providers SET subject_namespace=subject_namespace WHERE id=?`, id)
	if err != nil {
		return OAuthProvider{}, err
	}
	p, err := scanOAuthProvider(tx.QueryRowContext(ctx,
		`SELECT `+oauthCols+` FROM oauth_providers WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthProvider{}, ErrNotFound
	}
	return p, err
}

// ValidateOAuthProviderCallbackGuard serializes with administrator provider
// writes and proves that the provider is still enabled and is exactly the
// credential/trust generation used before the callback's external requests.
func ValidateOAuthProviderCallbackGuard(
	ctx context.Context,
	db *sql.DB,
	guard OAuthProviderCallbackGuard,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateOAuthProviderCallbackGuardTx(ctx, tx, guard); err != nil {
		return err
	}
	return tx.Commit()
}

func validateOAuthProviderCallbackGuardTx(
	ctx context.Context,
	tx *sql.Tx,
	guard OAuthProviderCallbackGuard,
) error {
	if strings.TrimSpace(guard.ProviderID) == "" || len(guard.Snapshot) != sha256.Size*2 {
		return ErrOAuthProviderChanged
	}
	current, err := lockOAuthProvider(ctx, tx, guard.ProviderID)
	if errors.Is(err, ErrNotFound) {
		return ErrOAuthProviderChanged
	}
	if err != nil {
		return err
	}
	currentSnapshot := oauthProviderCallbackSnapshot(current)
	if !current.Enabled || len(currentSnapshot) != len(guard.Snapshot) ||
		subtle.ConstantTimeCompare([]byte(currentSnapshot), []byte(guard.Snapshot)) != 1 {
		return ErrOAuthProviderChanged
	}
	return nil
}

func lockOAuthCallbackUserTx(ctx context.Context, tx *sql.Tx, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return ErrNotFound
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET token_ver=token_ver WHERE id=?`, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func sameOAuthProviderTrustState(a, b OAuthProvider) bool {
	return a.Kind == b.Kind &&
		a.ClientID == b.ClientID &&
		a.ClientSecret == b.ClientSecret &&
		a.IssuerURL == b.IssuerURL &&
		a.JWKSURL == b.JWKSURL &&
		a.AuthURL == b.AuthURL &&
		a.TokenURL == b.TokenURL &&
		a.UserInfoURL == b.UserInfoURL &&
		a.Scopes == b.Scopes &&
		a.TeamID == b.TeamID &&
		a.KeyID == b.KeyID
}

func migrateLegacyOAuthSubjects(ctx context.Context, tx *sql.Tx, providerID, namespace string) error {
	// Move through a random temporary namespace so even an unusual raw-subject
	// set such as {"x", namespace+"x"} cannot collide partway through the bulk
	// primary-key update.
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return fmt.Errorf("generate OAuth subject migration namespace: %w", err)
	}
	temporary := "oauth:migrate:" + hex.EncodeToString(entropy[:]) + ":"
	var collision int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM oauth_identities
		 WHERE provider_id=? AND substr(subject, 1, ?)=?`,
		providerID, len(temporary), temporary).Scan(&collision); err != nil {
		return err
	}
	if collision != 0 {
		return errors.New("OAuth subject migration namespace collision")
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE oauth_identities SET subject=? || subject WHERE provider_id=?`, temporary, providerID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE oauth_identities SET subject=? || substr(subject, ?) WHERE provider_id=?`,
		namespace, len(temporary)+1, providerID)
	return err
}

func ReorderOAuthProviders(ctx context.Context, db *sql.DB, ids []string) error {
	return reorderAdminRecords(ctx, db, "oauth_providers", ids)
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

// LinkOAuthIdentity records (providerID, subject) → userID without ever
// reassigning an identity owned by another account.
func LinkOAuthIdentity(ctx context.Context, db *sql.DB, providerID, subject, userID, email string) error {
	return BindOAuthIdentity(ctx, db, providerID, subject, userID, email)
}

// LinkOAuthIdentityForCallback validates the callback's provider generation and
// records the identity under the same transaction. An administrator disable,
// delete, or trust/credential rotation which commits first makes the bind fail
// closed; if this transaction locks first, the administrator write follows it.
func LinkOAuthIdentityForCallback(
	ctx context.Context,
	db *sql.DB,
	guard OAuthProviderCallbackGuard,
	providerID, subject, userID, email string,
) error {
	if providerID != guard.ProviderID {
		return ErrOAuthProviderChanged
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Configuration import locks its active administrator before provider rows.
	// Keep the same user -> provider order for every existing-user callback path.
	if err := lockOAuthCallbackUserTx(ctx, tx, userID); err != nil {
		return err
	}
	if err := validateOAuthProviderCallbackGuardTx(ctx, tx, guard); err != nil {
		return err
	}
	if err := bindOAuthIdentity(ctx, tx, providerID, subject, userID, email); err != nil {
		return err
	}
	return tx.Commit()
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
	return bindOAuthIdentity(ctx, db, providerID, subject, userID, email)
}

// BindOAuthIdentityForSession atomically proves that the exact authenticated
// session which initiated a delayed OAuth link is still authoritative and then
// binds the provider identity. Password resets, status changes and every
// session-revocation primitive take the same user-row lock first, so none can
// commit between this validation and the identity INSERT.
func BindOAuthIdentityForSession(
	ctx context.Context,
	db *sql.DB,
	providerID, subject, userID, email string,
	expectedTokenVer int,
	sessionID string,
) error {
	return bindOAuthIdentityForSession(
		ctx, db, nil, providerID, subject, userID, email, expectedTokenVer, sessionID,
	)
}

// BindOAuthIdentityForCallbackSession extends BindOAuthIdentityForSession with
// an atomic provider-generation check for delayed OAuth callback linking.
func BindOAuthIdentityForCallbackSession(
	ctx context.Context,
	db *sql.DB,
	guard OAuthProviderCallbackGuard,
	providerID, subject, userID, email string,
	expectedTokenVer int,
	sessionID string,
) error {
	if providerID != guard.ProviderID {
		return ErrOAuthProviderChanged
	}
	return bindOAuthIdentityForSession(
		ctx, db, &guard, providerID, subject, userID, email, expectedTokenVer, sessionID,
	)
}

func bindOAuthIdentityForSession(
	ctx context.Context,
	db *sql.DB,
	guard *OAuthProviderCallbackGuard,
	providerID, subject, userID, email string,
	expectedTokenVer int,
	sessionID string,
) error {
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(userID) == "" || sessionID == "" || expectedTokenVer < 0 {
		return ErrOAuthLinkSessionExpired
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// This is the common first lock used by refresh rotation/revocation and
	// password changes. The predicates make status/token-version validation part
	// of the locked write rather than a check-then-act read.
	locked, err := tx.ExecContext(ctx,
		`UPDATE users SET token_ver=token_ver
		 WHERE id=? AND status='active' AND token_ver=?`,
		userID, expectedTokenVer)
	if err != nil {
		return err
	}
	if n, err := locked.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrOAuthLinkSessionExpired
	}
	if guard != nil {
		if err := validateOAuthProviderCallbackGuardTx(ctx, tx, *guard); err != nil {
			return err
		}
	}

	query := `SELECT jti FROM refresh_tokens
		 WHERE user_id=? AND revoked=0 AND expires_at>?
		   AND CASE WHEN trim(session_id)<>'' THEN session_id ELSE jti END=?
		 ORDER BY jti`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query, userID, time.Now().Unix(), sessionID)
	if err != nil {
		return err
	}
	found := rows.Next()
	if found {
		var jti string
		if err := rows.Scan(&jti); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return ErrOAuthLinkSessionExpired
	}

	if err := bindOAuthIdentity(ctx, tx, providerID, subject, userID, email); err != nil {
		return err
	}
	return tx.Commit()
}

func bindOAuthIdentity(ctx context.Context, ex RowExecer, providerID, subject, userID, email string) error {
	res, err := ex.ExecContext(ctx,
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
	var owner string
	if err := ex.QueryRowContext(ctx,
		`SELECT user_id FROM oauth_identities WHERE provider_id=? AND subject=?`,
		providerID, subject).Scan(&owner); err != nil {
		return err
	}
	if owner != userID {
		return ErrOAuthIdentityConflict
	}
	// Already ours — idempotent; refresh the stored email.
	_, _ = ex.ExecContext(ctx,
		`UPDATE oauth_identities SET email=? WHERE provider_id=? AND subject=?`,
		strings.ToLower(email), providerID, subject)
	return nil
}

// UnbindOAuthIdentity removes (providerID, subject) IF it belongs to userID.
// Scoped by user_id so a caller can never delete another account's link. Returns
// true when a row was actually removed (false → nothing matched → 404).
func UnbindOAuthIdentity(ctx context.Context, db *sql.DB, providerID, subject, userID string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// A harmless UPDATE acquires the per-user write lock on both PostgreSQL and
	// SQLite. Concurrent removals for two different identity rows must serialize
	// on this shared row before either counts the remaining login methods.
	locked, err := tx.ExecContext(ctx, `UPDATE users SET password_set=password_set WHERE id=?`, userID)
	if err != nil {
		return false, err
	}
	if n, _ := locked.RowsAffected(); n == 0 {
		return false, nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM oauth_identities WHERE provider_id=? AND subject=? AND user_id=?`,
		providerID, subject, userID).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	var passwordSet, identityCount int
	if err := tx.QueryRowContext(ctx, `SELECT password_set FROM users WHERE id=?`, userID).Scan(&passwordSet); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_identities WHERE user_id=?`, userID).Scan(&identityCount); err != nil {
		return false, err
	}
	if passwordSet == 0 && identityCount <= 1 {
		return false, ErrOAuthLastLoginMethod
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM oauth_identities WHERE provider_id=? AND subject=? AND user_id=?`,
		providerID, subject, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CreateOAuthUser provisions a local account for a first-time social login. The
// password is a random throwaway hash — the user signs in via the provider, or
// later sets a real password through the forgot-password flow.
func CreateOAuthUser(ctx context.Context, db *sql.DB, providerID, subject, email, name, status string) (*User, error) {
	return createOAuthUser(ctx, db, nil, providerID, subject, email, name, status)
}

// CreateOAuthUserForCallback validates and locks the provider generation in the
// same transaction which creates the local user and its first identity.
func CreateOAuthUserForCallback(
	ctx context.Context,
	db *sql.DB,
	guard OAuthProviderCallbackGuard,
	providerID, subject, email, name, status string,
) (*User, error) {
	if providerID != guard.ProviderID {
		return nil, ErrOAuthProviderChanged
	}
	return createOAuthUser(ctx, db, &guard, providerID, subject, email, name, status)
}

func createOAuthUser(
	ctx context.Context,
	db *sql.DB,
	guard *OAuthProviderCallbackGuard,
	providerID, subject, email, name, status string,
) (*User, error) {
	rb := make([]byte, 24)
	if _, err := rand.Read(rb); err != nil {
		return nil, err
	}
	hash, err := hashPassword(hex.EncodeToString(rb))
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if guard != nil {
		if err := validateOAuthProviderCallbackGuardTx(ctx, tx, *guard); err != nil {
			return nil, err
		}
	}
	// Status/password_set and the provider identity are committed together. A
	// concurrent callback that wins the identity key makes this transaction roll
	// back, so no password-less orphan account is left behind.
	userID, err := createUserWithState(ctx, tx, email, name, hash, "user", status, false)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_identities(provider_id, subject, user_id, email)
		 VALUES(?, ?, ?, ?) ON CONFLICT(provider_id, subject) DO NOTHING`,
		providerID, subject, userID, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrOAuthIdentityConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, userID)
}
