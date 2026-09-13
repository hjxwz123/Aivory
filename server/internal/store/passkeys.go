package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Passkey is one registered WebAuthn credential (a "passkey" on the user's
// device). CredentialID and PublicKey are raw authenticator bytes (CBOR);
// they must never be serialized to API responses.
type Passkey struct {
	ID           string `json:"id"`
	UserID       string `json:"user_id"`
	CredentialID []byte `json:"-"`
	PublicKey    []byte `json:"-"`
	SignCount    uint32 `json:"-"`
	Name         string `json:"name"`
	CreatedAt    int64  `json:"created_at"`
	LastUsedAt   int64  `json:"last_used_at"`
}

// ErrPasskeyNotFound is returned by DeletePasskey/GetPasskey when no row
// matches the (optionally user-scoped) id.
var ErrPasskeyNotFound = errors.New("passkey_not_found")

// CreatePasskey stores a new credential for a user. IDs are minted here when
// empty; CredentialID and PublicKey are required.
func CreatePasskey(ctx context.Context, db *sql.DB, p *Passkey) error {
	if p == nil || p.UserID == "" || len(p.CredentialID) == 0 || len(p.PublicKey) == 0 {
		return errors.New("passkey fields required")
	}
	if p.ID == "" {
		p.ID = genID("pk")
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().Unix()
	}
	p.Name = truncateLoginHistoryText(strings.TrimSpace(p.Name), 64)
	_, err := db.ExecContext(ctx,
		`INSERT INTO passkeys(id,user_id,credential_id,public_key,sign_count,name,created_at,last_used_at) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.UserID, p.CredentialID, p.PublicKey, p.SignCount, p.Name, p.CreatedAt, p.LastUsedAt,
	)
	return err
}

// ListPasskeys returns a user's credentials newest-first (public fields only
// are meaningful to clients; callers project away the raw bytes).
func ListPasskeys(ctx context.Context, db *sql.DB, userID string) ([]Passkey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id,user_id,sign_count,name,created_at,last_used_at FROM passkeys WHERE user_id=? ORDER BY created_at DESC, id DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var signCount sql.NullInt64
		if err := rows.Scan(&p.ID, &p.UserID, &signCount, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		p.SignCount = uint32(signCount.Int64)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPasskeyCredentials returns full credential rows (raw ID + public key
// included) for WebAuthn verification — never for API serialization.
func ListPasskeyCredentials(ctx context.Context, db *sql.DB, userID string) ([]Passkey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id,user_id,credential_id,public_key,sign_count,name,created_at,last_used_at FROM passkeys WHERE user_id=? ORDER BY created_at DESC, id DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPasskeys reports how many credentials a user has registered.
func CountPasskeys(ctx context.Context, db *sql.DB, userID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id=?`, userID).Scan(&count)
	return count, err
}

// GetPasskeyByCredentialID resolves the credential an assertion was signed
// with. Returns sql.ErrNoRows when unknown.
func GetPasskeyByCredentialID(ctx context.Context, db *sql.DB, credentialID []byte) (*Passkey, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id,user_id,credential_id,public_key,sign_count,name,created_at,last_used_at FROM passkeys WHERE credential_id=?`,
		credentialID)
	return scanPasskey(row)
}

func scanPasskey(row *sql.Row) (*Passkey, error) {
	var p Passkey
	err := row.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.PublicKey, &p.SignCount, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPasskeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePasskey removes a credential, scoped to its owner so one user can
// never delete another's passkey by guessing ids.
func DeletePasskey(ctx context.Context, db *sql.DB, userID, id string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM passkeys WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrPasskeyNotFound
	}
	return nil
}

// DeleteAllPasskeysForUser removes every credential of a user (admin reset).
// Returns how many rows were deleted.
func DeleteAllPasskeysForUser(ctx context.Context, db *sql.DB, userID string) (int, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id=?`, userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// TouchPasskey records the new signature counter and last-use time after a
// successful assertion.
func TouchPasskey(ctx context.Context, db *sql.DB, id string, signCount uint32) error {
	_, err := db.ExecContext(ctx,
		`UPDATE passkeys SET sign_count=?, last_used_at=? WHERE id=?`,
		signCount, time.Now().Unix(), id)
	return err
}
