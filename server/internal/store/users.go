package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// ErrNotFound is returned when a queried row is missing.
var ErrNotFound = errors.New("not found")

// FindUserByEmail returns nil + ErrNotFound when the user does not exist.
func FindUserByEmail(ctx context.Context, db *sql.DB, email string) (*User, error) {
	var u User
	var settings string
	var totpEnabled int
	var passwordSet int
	err := db.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, COALESCE(credits_permanent,0), created_at FROM users WHERE email=?`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.CreditsPermanent, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TotpEnabled = totpEnabled != 0
	u.HasPassword = passwordSet != 0
	u.Settings = json.RawMessage(settings)
	// Lazy expiry: when the membership window has elapsed, demote back to the
	// previous group (or the default) and clear the window.
	maybeExpireGroup(ctx, db, &u)
	return &u, nil
}

// FindUserByID looks up a user by primary key.
func FindUserByID(ctx context.Context, db *sql.DB, id string) (*User, error) {
	var u User
	var settings string
	var totpEnabled int
	var passwordSet int
	err := db.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, COALESCE(credits_permanent,0), created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.CreditsPermanent, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.TotpEnabled = totpEnabled != 0
	u.HasPassword = passwordSet != 0
	u.Settings = json.RawMessage(settings)
	maybeExpireGroup(ctx, db, &u)
	return &u, nil
}

// maybeExpireGroup downgrades the user's group when group_expires_at has passed.
// Best-effort: if the DB write fails (concurrent expiry race), the in-memory
// User still reflects the expired state so the caller sees the right tier.
func maybeExpireGroup(ctx context.Context, db *sql.DB, u *User) {
	if u.GroupExpiresAt <= 0 || time.Now().Unix() < u.GroupExpiresAt {
		return
	}
	prev := u.PreviousGroupID
	if prev == "" {
		prev = DefaultGroupID
	}
	// Verify the target group still exists before flipping — admin could have
	// deleted the previous group in the meantime, in which case fall back to
	// the default.
	if _, err := GetUserGroup(ctx, db, prev); err != nil {
		prev = DefaultGroupID
	}
	_, _ = db.ExecContext(ctx,
		`UPDATE users SET group_id=?, group_expires_at=0, previous_group_id='' WHERE id=? AND group_expires_at=?`,
		prev, u.ID, u.GroupExpiresAt)
	u.GroupID = prev
	u.GroupExpiresAt = 0
	u.PreviousGroupID = ""
}

// PasswordFor reads the bcrypt hash for the user.
func PasswordFor(ctx context.Context, db *sql.DB, userID string) (string, error) {
	var h string
	err := db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", userID).Scan(&h)
	return h, err
}

// CreateUser inserts a new user (default role=user, status=active).
func CreateUser(ctx context.Context, db *sql.DB, email, name, pwHash string) (*User, error) {
	return CreateUserWithRole(ctx, db, email, name, pwHash, "user")
}

// CreateUserWithRole inserts a new user with an explicit role ('user' |
// 'admin'). Used by the admin "create user" flow; CreateUser delegates here
// with role='user' for the normal registration path.
func CreateUserWithRole(ctx context.Context, db *sql.DB, email, name, pwHash, role string) (*User, error) {
	id := genID("u")
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email required")
	}
	if role != "admin" {
		role = "user"
	}
	if name == "" {
		// Pick name from the part before "@" as a sensible default.
		name = email
		if idx := strings.Index(email, "@"); idx > 0 {
			name = email[:idx]
		}
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO users(id, email, password_hash, name, role, settings) VALUES(?, ?, ?, ?, ?, '{}')`,
		id, email, pwHash, name, role)
	if err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, id)
}

// SetUserRole changes a user's role between 'user' and 'admin'. Bumps the token
// version so the change takes effect on the next request (the role lives in the
// access-token claims, so outstanding tokens must be invalidated).
func SetUserRole(ctx context.Context, db *sql.DB, userID, role string) error {
	if role != "admin" && role != "user" {
		return errors.New("role must be 'user' or 'admin'")
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET role=? WHERE id=?`, role, userID); err != nil {
		return err
	}
	return BumpTokenVersion(ctx, db, userID)
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

// MemoryEnabledForUser reports whether long-term memory is active for this user.
// Memory is on unless EITHER the global admin setting OR the user's per-user
// override is explicitly false (both default to enabled). Used to gate both
// memory injection (orchestrator) and extraction (memory worker) so a user who
// turns memory off in Personalization gets no memory in any conversation.
func MemoryEnabledForUser(ctx context.Context, db *sql.DB, userID string) bool {
	global := true
	if raw, err := GetSetting(db, "memory_enabled"); err == nil {
		_ = json.Unmarshal(raw, &global)
	}
	if !global {
		return false
	}
	if raw, err := GetUserSettingKey(ctx, db, userID, "memory_enabled"); err == nil && len(raw) > 0 {
		user := true
		if json.Unmarshal(raw, &user) == nil && !user {
			return false
		}
	}
	return true
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

// UpdateUserSettings merges patch into users.settings (JSON object). It locks
// the user row while merging so concurrent PATCH /me/settings calls cannot lose
// each other's keys by writing a stale whole JSON blob.
func UpdateUserSettings(ctx context.Context, db *sql.DB, userID string, patch map[string]any) (*User, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck — best-effort after commit or early return

	selectSQL := `SELECT settings FROM users WHERE id=?`
	if usePostgres {
		selectSQL += ` FOR UPDATE`
	}
	row := tx.QueryRowContext(ctx, selectSQL, userID)
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
	if _, err := tx.ExecContext(ctx, `UPDATE users SET settings=? WHERE id=?`, string(b), userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, userID)
}

// backfillUserOnboarded marks legacy accounts as already past first-login
// onboarding. New accounts still start with `{}` and therefore see the wizard.
func backfillUserOnboarded(db *sql.DB) {
	const batch = 500
	last := ""
	for {
		rows, err := db.Query(`SELECT id, settings FROM users WHERE id > ? ORDER BY id LIMIT ?`, last, batch)
		if err != nil {
			return
		}
		type row struct{ id, settings string }
		buf := make([]row, 0, batch)
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.settings); err != nil {
				continue
			}
			buf = append(buf, r)
		}
		rows.Close()
		if len(buf) == 0 {
			return
		}
		for _, r := range buf {
			current := map[string]any{}
			if strings.TrimSpace(r.settings) != "" {
				_ = json.Unmarshal([]byte(r.settings), &current)
			}
			if _, exists := current["onboarded"]; !exists {
				current["onboarded"] = true
				if b, err := json.Marshal(current); err == nil {
					_, _ = db.Exec(`UPDATE users SET settings=? WHERE id=?`, string(b), r.id)
				}
			}
			last = r.id
		}
		if len(buf) < batch {
			return
		}
	}
}

// TouchLastSeen records the user's last authenticated activity (online status,
// § admin → users). Called from the auth middleware, throttled by a cache key so
// it's at most one cheap UPDATE per user per minute.
func TouchLastSeen(ctx context.Context, db *sql.DB, userID string, now int64) {
	_, _ = db.ExecContext(ctx, `UPDATE users SET last_seen_at=? WHERE id=?`, now, userID)
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

// UpdateUserPassword writes a new bcrypt hash, rotates the token version (kills
// outstanding access tokens) AND revokes all refresh tokens (§A4) — otherwise a
// stolen refresh token survives a password reset and can re-mint a session,
// defeating the reset.
func UpdateUserPassword(ctx context.Context, db *sql.DB, userID, newHash string) error {
	if _, err := db.ExecContext(ctx, `UPDATE users SET password_hash=?, password_set=1 WHERE id=?`, newHash, userID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, userID); err != nil {
		return err
	}
	return BumpTokenVersion(ctx, db, userID)
}

// SetInitialPassword writes the first password for an account that never had one
// (created via OAuth). Unlike UpdateUserPassword it does NOT rotate the token
// version or revoke refresh tokens — the user is mid-session and we want them to
// stay logged in and continue straight into the app. It is the caller's job to
// verify the account currently has no password (password_set=0).
func SetInitialPassword(ctx context.Context, db *sql.DB, userID, newHash string) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET password_hash=?, password_set=1 WHERE id=?`, newHash, userID)
	return err
}

// SetUserTotp stores the TOTP secret and enabled flag for a user (§ 2FA login).
// Setup writes the secret with enabled=false; enable flips it to true once the
// user proves possession with a valid code.
func SetUserTotp(ctx context.Context, db *sql.DB, userID, secret string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := db.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_enabled=? WHERE id=?`, secret, en, userID)
	return err
}

// DisableUserTotp clears 2FA for a user (self-service with a valid code, or an
// admin reset to recover a locked-out account).
func DisableUserTotp(ctx context.Context, db *sql.DB, userID string) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, userID)
	return err
}

// ListUsers returns every user (admin only). Paged in memory.
func ListUsers(ctx context.Context, db *sql.DB) ([]User, error) {
	return ListUsersPaged(ctx, db, 200, 0)
}

// ListUsersPaged returns users with pagination support. Limit defaults to 200
// and is capped at 500 to prevent unbounded queries at scale.
func ListUsersPaged(ctx context.Context, db *sql.DB, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, last_seen_at, COALESCE(credits_permanent,0), created_at FROM users ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var settings string
		var totpEnabled int
		var passwordSet int
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.LastSeenAt, &u.CreditsPermanent, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.TotpEnabled = totpEnabled != 0
		u.HasPassword = passwordSet != 0
		u.Settings = json.RawMessage(settings)
		out = append(out, u)
	}
	return out, rows.Err()
}

const userSelectCols = `id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, last_seen_at, COALESCE(credits_permanent,0), created_at`

func scanUsers(rows *sql.Rows) ([]User, error) {
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var settings string
		var totpEnabled int
		var passwordSet int
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.LastSeenAt, &u.CreditsPermanent, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.TotpEnabled = totpEnabled != 0
		u.HasPassword = passwordSet != 0
		u.Settings = json.RawMessage(settings)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListUsersBySearch returns paginated users matching an optional search term
// (matched against email and name case-insensitively). Limit is capped at 200.
func ListUsersBySearch(ctx context.Context, db *sql.DB, search string, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + userSelectCols + ` FROM users`
	var rows *sql.Rows
	var err error
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		rows, err = db.QueryContext(ctx, q+` WHERE LOWER(email) LIKE ? OR LOWER(name) LIKE ? ORDER BY created_at DESC LIMIT ? OFFSET ?`, like, like, limit, offset)
	} else {
		rows, err = db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	return scanUsers(rows)
}

// CountUsersBySearch returns total user count matching an optional search term.
func CountUsersBySearch(ctx context.Context, db *sql.DB, search string) (int, error) {
	var n int
	var err error
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE LOWER(email) LIKE ? OR LOWER(name) LIKE ?`, like, like).Scan(&n)
	} else {
		err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	}
	return n, err
}

// SetPermanentCredits overwrites a user's non-expiring credit balance (admin
// edit on the users page, § credits). Floored at 0.
func SetPermanentCredits(ctx context.Context, db *sql.DB, userID string, credits float64) error {
	if credits < 0 {
		credits = 0
	}
	_, err := db.ExecContext(ctx, `UPDATE users SET credits_permanent=? WHERE id=?`, credits, userID)
	return err
}

// AddPermanentCredits atomically adds delta (may be negative) to a user's
// permanent balance, flooring at 0. Used to debit credits after a paid turn and
// to top up after a purchase.
func AddPermanentCredits(ctx context.Context, db *sql.DB, userID string, delta float64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET credits_permanent = MAX(0, credits_permanent + ?) WHERE id=?`, delta, userID)
	return err
}

// PermanentCredits returns a user's non-expiring balance.
func PermanentCredits(ctx context.Context, db *sql.DB, userID string) (float64, error) {
	var c float64
	err := db.QueryRowContext(ctx, `SELECT COALESCE(credits_permanent,0) FROM users WHERE id=?`, userID).Scan(&c)
	return c, err
}

// ActiveAdminCount returns how many active admin accounts exist — used to refuse
// banning/demoting the last admin and locking the platform out (§D2).
func ActiveAdminCount(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&n)
	return n, err
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

// DeleteUser permanently removes a user and all related data (conversations,
// messages, memories, refresh tokens, usage logs, files, documents, artifacts).
// All DB deletes run inside a single transaction. Disk files are removed
// best-effort after the transaction commits so a partial disk failure never
// leaves the database in an inconsistent state.
func DeleteUser(ctx context.Context, db *sql.DB, userID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete user: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — intentional best-effort rollback

	// Collect storage paths before deleting rows so we can clean up disk files
	// after the transaction commits.
	var diskPaths []string

	// files table: directly user-owned.
	fileRows, err := tx.QueryContext(ctx, `SELECT storage_path FROM files WHERE user_id=?`, userID)
	if err != nil {
		return fmt.Errorf("delete user: query files: %w", err)
	}
	for fileRows.Next() {
		var p string
		if fileRows.Scan(&p) == nil && p != "" {
			diskPaths = append(diskPaths, p)
		}
	}
	fileRows.Close()

	// documents table: linked through conversations owned by the user.
	docRows, err := tx.QueryContext(ctx,
		`SELECT storage_path FROM documents WHERE conversation_id IN (SELECT id FROM conversations WHERE user_id=?)`,
		userID)
	if err != nil {
		return fmt.Errorf("delete user: query documents: %w", err)
	}
	for docRows.Next() {
		var p string
		if docRows.Scan(&p) == nil && p != "" {
			diskPaths = append(diskPaths, p)
		}
	}
	docRows.Close()

	// artifacts table: linked through messages → conversations owned by the user.
	artRows, err := tx.QueryContext(ctx,
		`SELECT storage_path FROM artifacts WHERE message_id IN (SELECT id FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE user_id=?))`,
		userID)
	if err != nil {
		return fmt.Errorf("delete user: query artifacts: %w", err)
	}
	for artRows.Next() {
		var p string
		if artRows.Scan(&p) == nil && p != "" {
			diskPaths = append(diskPaths, p)
		}
	}
	artRows.Close()

	// Delete DB rows. Order matters: child rows before parent rows.
	stmts := []string{
		`DELETE FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE user_id=?)`,
		`DELETE FROM conversations WHERE user_id=?`,
		`DELETE FROM memories WHERE user_id=?`,
		`DELETE FROM refresh_tokens WHERE user_id=?`,
		`DELETE FROM usage_logs WHERE user_id=?`,
		`DELETE FROM files WHERE user_id=?`,
		`DELETE FROM users WHERE id=?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete user: commit: %w", err)
	}

	// Best-effort disk cleanup after a successful commit. Log errors but do not
	// fail — missing files are harmless (already cleaned up or never written).
	for _, p := range diskPaths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Printf("delete user %s: remove file %q: %v", userID, p, err)
		}
	}
	return nil
}
