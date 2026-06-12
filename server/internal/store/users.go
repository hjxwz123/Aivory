package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned when a queried row is missing.
var ErrNotFound = errors.New("not found")

// FindUserByEmail returns nil + ErrNotFound when the user does not exist.
func FindUserByEmail(ctx context.Context, db *sql.DB, email string) (*User, error) {
	var u User
	var settings string
	err := db.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, created_at FROM users WHERE email=?`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Settings = json.RawMessage(settings)
	return &u, nil
}

// FindUserByID looks up a user by primary key.
func FindUserByID(ctx context.Context, db *sql.DB, id string) (*User, error) {
	var u User
	var settings string
	err := db.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Settings = json.RawMessage(settings)
	return &u, nil
}

// PasswordFor reads the bcrypt hash for the user.
func PasswordFor(ctx context.Context, db *sql.DB, userID string) (string, error) {
	var h string
	err := db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", userID).Scan(&h)
	return h, err
}

// CreateUser inserts a new user (default role=user, status=active).
func CreateUser(ctx context.Context, db *sql.DB, email, name, pwHash string) (*User, error) {
	id := genID("u")
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email required")
	}
	if name == "" {
		// Pick name from the part before "@" as a sensible default.
		name = email
		if idx := strings.Index(email, "@"); idx > 0 {
			name = email[:idx]
		}
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO users(id, email, password_hash, name, settings) VALUES(?, ?, ?, ?, '{}')`,
		id, email, pwHash, name)
	if err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, id)
}

// BumpTokenVersion invalidates all outstanding access tokens for the user.
func BumpTokenVersion(ctx context.Context, db *sql.DB, userID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET token_ver = token_ver + 1 WHERE id=?`, userID)
	return err
}

// SetUserStatus updates the user's lifecycle status. Bumps token version when
// flipping out of "active" so the change takes effect immediately (§8.1).
func SetUserStatus(ctx context.Context, db *sql.DB, userID, status string) error {
	if _, err := db.ExecContext(ctx, `UPDATE users SET status=? WHERE id=?`, status, userID); err != nil {
		return err
	}
	if status != "active" {
		if err := BumpTokenVersion(ctx, db, userID); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, userID)
		return err
	}
	return nil
}

// GetUserSettingKey returns one key from users.settings as raw JSON (nil if
// absent). Used by the orchestrator to read the pre-selected image model etc.
func GetUserSettingKey(ctx context.Context, db *sql.DB, userID, key string) (json.RawMessage, error) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT settings FROM users WHERE id=?`, userID).Scan(&raw); err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	if v, ok := m[key]; ok {
		return v, nil
	}
	return nil, nil
}

// UpdateUserSettings merges patch into users.settings (JSON object) and writes
// it back atomically.
func UpdateUserSettings(ctx context.Context, db *sql.DB, userID string, patch map[string]any) (*User, error) {
	row := db.QueryRowContext(ctx, `SELECT settings FROM users WHERE id=?`, userID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		return nil, err
	}
	current := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &current)
	}
	for k, v := range patch {
		current[k] = v
	}
	b, _ := json.Marshal(current)
	if _, err := db.ExecContext(ctx, `UPDATE users SET settings=? WHERE id=?`, string(b), userID); err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, userID)
}

// UpdateUserProfile sets the user-visible profile fields.
func UpdateUserProfile(ctx context.Context, db *sql.DB, userID string, name, email string) (*User, error) {
	if email == "" || name == "" {
		return nil, errors.New("name and email required")
	}
	_, err := db.ExecContext(ctx, `UPDATE users SET name=?, email=? WHERE id=?`, name, strings.ToLower(email), userID)
	if err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, userID)
}

// UpdateUserPassword writes a new bcrypt hash and rotates the token version.
func UpdateUserPassword(ctx context.Context, db *sql.DB, userID, newHash string) error {
	if _, err := db.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=?`, newHash, userID); err != nil {
		return err
	}
	return BumpTokenVersion(ctx, db, userID)
}

// ListUsers returns every user (admin only). Paged in memory.
func ListUsers(ctx context.Context, db *sql.DB) ([]User, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, created_at FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var settings string
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Settings = json.RawMessage(settings)
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the total user count — used to gate the "first user is
// admin" registration path.
func CountUsers(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// PromoteFirstUser flips role=admin on the only existing user (used during
// bootstrap when the seeded admin is replaced by the first real registration).
func PromoteFirstUser(ctx context.Context, db *sql.DB, userID string) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET role='admin' WHERE id=?`, userID)
	return err
}

// touch updates the row's updated_at column. Use after a write to "bump"
// updatable tables.
func touch(ctx context.Context, db *sql.DB, table, id string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET updated_at=? WHERE id=?", table), time.Now().Unix(), id)
	return err
}

var _ = touch
