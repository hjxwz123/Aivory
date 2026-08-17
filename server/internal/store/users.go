package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/mail"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned when a queried row is missing.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned when the actor is authenticated but lacks the
// workspace role required for the operation (§workspace RBAC).
var ErrForbidden = errors.New("forbidden")

// ErrUserEmailInvalid and ErrUserEmailExists distinguish invalid input from a
// collision when an administrator changes an account's sign-in email.
var (
	ErrUserEmailInvalid   = errors.New("invalid user email")
	ErrUserEmailExists    = errors.New("user email already exists")
	ErrAlreadyInitialized = errors.New("deployment already initialized")
	ErrPasswordAlreadySet = errors.New("password already set")
)

// NormalizeUserEmail returns the canonical sign-in address accepted for every
// account creation and email-change path. ParseAddress alone accepts display
// names, so require its parsed address to match the trimmed input exactly.
func NormalizeUserEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if email == "" || len(email) > 320 {
		return "", ErrUserEmailInvalid
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || !strings.Contains(email, "@") {
		return "", ErrUserEmailInvalid
	}
	return email, nil
}

// Pagination defaults/caps.
var (
	listUsersPagedDefault    = 200
	listUsersPagedMax        = 500
	listUsersBySearchDefault = 50
	listUsersBySearchMax     = 200
)

// FindUserByEmail returns nil + ErrNotFound when the user does not exist.
func FindUserByEmail(ctx context.Context, db *sql.DB, email string) (*User, error) {
	var u User
	var settings string
	var totpEnabled int
	var passwordSet int
	err := db.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, password_changed_at, COALESCE(credits_permanent,0), sort_order, created_at FROM users WHERE email=?`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.PasswordChangedAt, &u.CreditsPermanent, &u.SortOrder, &u.CreatedAt)
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
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, password_changed_at, COALESCE(credits_permanent,0), sort_order, created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.PasswordChangedAt, &u.CreditsPermanent, &u.SortOrder, &u.CreatedAt)
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

// UserAuthState is the small, authoritative subset of a user row required on
// every authenticated request. It intentionally bypasses application caches so
// account blocks, role changes, and token-version revocations take effect on
// every server replica as soon as the database transaction commits.
type UserAuthState struct {
	Role          string
	Status        string
	TokenVer      int
	SessionActive bool
}

// GetUserAuthState loads the authorization-critical fields for one user.
func GetUserAuthState(ctx context.Context, db *sql.DB, userID string) (UserAuthState, error) {
	return GetUserAuthStateForSession(ctx, db, userID, "")
}

// GetUserAuthStateForSession also checks a stable refresh-session family when
// sessionID is present in the access token. A per-device sign-out therefore
// invalidates that device's access token immediately, not just its refresh token.
func GetUserAuthStateForSession(ctx context.Context, db *sql.DB, userID, sessionID string) (UserAuthState, error) {
	var state UserAuthState
	var sessionActive int
	err := db.QueryRowContext(ctx,
		`SELECT u.role, u.status, u.token_ver,
		        CASE WHEN ?=''
		             OR EXISTS (
		               SELECT 1 FROM refresh_tokens rt
		               WHERE rt.user_id=u.id AND rt.revoked=0 AND rt.expires_at>?
		                 AND CASE WHEN trim(rt.session_id)<>'' THEN rt.session_id ELSE rt.jti END=?
		             )
		             THEN 1 ELSE 0 END
		 FROM users u WHERE u.id=?`,
		sessionID, time.Now().Unix(), sessionID, userID,
	).Scan(&state.Role, &state.Status, &state.TokenVer, &sessionActive)
	if errors.Is(err, sql.ErrNoRows) {
		return UserAuthState{}, ErrNotFound
	}
	state.SessionActive = sessionActive == 1
	return state, err
}

// maybeExpireGroup downgrades the user's group when group_expires_at has passed.
// Best-effort: if the DB write fails (concurrent expiry race), the in-memory
// User still reflects the expired state so the caller sees the right tier.
func maybeExpireGroup(ctx context.Context, db *sql.DB, u *User) {
	now := time.Now().Unix()
	if u.GroupExpiresAt <= 0 || now < u.GroupExpiresAt {
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
		`UPDATE users
		    SET group_id=?, group_expires_at=0, previous_group_id='',
		        credit_cycle_anchor=CASE
		            WHEN credit_cycle_anchor>=? THEN credit_cycle_anchor+1
		            ELSE ?
		        END,
		        quota_cycle_anchor=CASE
		            WHEN quota_cycle_anchor>=? THEN quota_cycle_anchor+1
		            ELSE ?
		        END
		  WHERE id=? AND group_expires_at=?`,
		prev, now, now, now, now, u.ID, u.GroupExpiresAt)
	u.GroupID = prev
	u.GroupExpiresAt = 0
	u.PreviousGroupID = ""
}

// ExpireDueUserGroups normalizes every elapsed temporary membership before an
// administrator paginates or counts a group. Doing this once ahead of both
// queries prevents a page from reporting a total that still includes users
// which ListUsersByGroup then lazily moves to another tier row by row.
func ExpireDueUserGroups(ctx context.Context, db *sql.DB) error {
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `UPDATE users
		SET group_id=CASE
		        WHEN trim(previous_group_id)<>'' AND EXISTS (
		          SELECT 1 FROM user_groups expiry_previous_group
		           WHERE expiry_previous_group.id=users.previous_group_id
		        ) THEN previous_group_id
		        ELSE ?
		    END,
		    group_expires_at=0,
		    previous_group_id='',
		    credit_cycle_anchor=CASE
		        WHEN credit_cycle_anchor>=? THEN credit_cycle_anchor+1 ELSE ? END,
		    quota_cycle_anchor=CASE
		        WHEN quota_cycle_anchor>=? THEN quota_cycle_anchor+1 ELSE ? END
		WHERE group_expires_at>0 AND group_expires_at<=?`,
		DefaultGroupID, now, now, now, now, now)
	return err
}

// PasswordFor reads the bcrypt hash for the user.
func PasswordFor(ctx context.Context, db *sql.DB, userID string) (string, error) {
	var h string
	err := db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", userID).Scan(&h)
	return h, err
}

// CreateUser inserts a new user (default role=user, status=active).
func CreateUser(ctx context.Context, db *sql.DB, email, name, pwHash string) (*User, error) {
	return CreateUserWithState(ctx, db, email, name, pwHash, "user", "active", true)
}

// CreateUserWithRole inserts a new user with an explicit role ('user' |
// 'admin'). Used by the admin "create user" flow; CreateUser delegates here
// with role='user' for the normal registration path.
func CreateUserWithRole(ctx context.Context, db *sql.DB, email, name, pwHash, role string) (*User, error) {
	return CreateUserWithState(ctx, db, email, name, pwHash, role, "active", true)
}

// CreateUserWithState inserts the complete authentication state in one
// statement. Registration and OAuth callers use this to create pending and/or
// password-less accounts without a fail-open active-account window.
func CreateUserWithState(ctx context.Context, db *sql.DB, email, name, pwHash, role, status string, passwordSet bool) (*User, error) {
	id, err := createUserWithState(ctx, db, email, name, pwHash, role, status, passwordSet)
	if err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, id)
}

func createUserWithState(ctx context.Context, ex RowExecer, email, name, pwHash, role, status string, passwordSet bool) (string, error) {
	id := genID("u")
	var err error
	email, err = NormalizeUserEmail(email)
	if err != nil {
		return "", err
	}
	if role != "admin" {
		role = "user"
	}
	if status != "active" && status != "pending" {
		return "", errors.New("status must be 'active' or 'pending'")
	}
	if name == "" {
		// Pick name from the part before "@" as a sensible default.
		name = email
		if idx := strings.Index(email, "@"); idx > 0 {
			name = email[:idx]
		}
	}
	sortOrder := 0
	_ = ex.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM users`).Scan(&sortOrder)
	// New accounts default long-term memory OFF (opt-in): the user turns it on in
	// onboarding / Personalization if the global master switch allows. (§ memory)
	passwordSetInt := 0
	if passwordSet {
		passwordSetInt = 1
	}
	now := time.Now().Unix()
	_, err = ex.ExecContext(ctx,
		`INSERT INTO users(id, email, password_hash, name, role, status, password_set, settings, credit_cycle_anchor, quota_cycle_anchor, sort_order)
		 VALUES(?, ?, ?, ?, ?, ?, ?, '{"memory_enabled":false}', ?, ?, ?)`,
		id, email, pwHash, name, role, status, passwordSetInt, now, now, sortOrder)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CreateInitialAdmin atomically claims first-run setup and creates its sole
// administrator. The unique settings key serializes concurrent setup attempts
// on both SQLite and PostgreSQL; the claim rolls back if user creation fails.
func CreateInitialAdmin(ctx context.Context, db *sql.DB, email, name, pwHash string) (*User, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES('_internal_setup_complete', 'true')
		 ON CONFLICT(key) DO NOTHING`)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return nil, err
		}
		return nil, ErrAlreadyInitialized
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, ErrAlreadyInitialized
	}
	id, err := createUserWithState(ctx, tx, email, name, pwHash, "admin", "active", true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	activeAdminIDs, err := lockActiveAdminIDs(ctx, tx)
	if err != nil {
		return err
	}
	query := `SELECT role, status FROM users WHERE id=?`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	var currentRole, currentStatus string
	if err := tx.QueryRowContext(ctx, query, userID).Scan(&currentRole, &currentStatus); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if currentRole == "admin" && currentStatus == "active" && role == "user" && !hasOtherActiveAdmin(activeAdminIDs, userID) {
		return ErrLastAdmin
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET role=?, token_ver=token_ver+1 WHERE id=?`, role, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND revoked=0`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func lockActiveAdminIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	query := `SELECT id FROM users WHERE role='admin' AND status='active' ORDER BY id`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func hasOtherActiveAdmin(ids []string, userID string) bool {
	for _, id := range ids {
		if id != userID {
			return true
		}
	}
	return false
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
	ok, err := SetUserStatusGuarded(ctx, db, userID, status)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// ActivatePendingUser completes email verification without reviving an account
// that an administrator concurrently banned or marked for deletion.
func ActivatePendingUser(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	result, err := db.ExecContext(ctx,
		`UPDATE users SET status='active' WHERE id=? AND status='pending'`, userID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// MemoryEnabledGlobal reports the GLOBAL admin `memory_enabled` master switch
// (default true). When false, long-term memory is unavailable to EVERY user —
// already-enabled users included — regardless of their per-user override, and the
// client hides the per-user toggle. Admins flip it on the system settings page.
func MemoryEnabledGlobal(db *sql.DB) bool {
	global := true
	if raw, err := GetSetting(db, "memory_enabled"); err == nil {
		_ = json.Unmarshal(raw, &global)
	}
	return global
}

// MemoryEnabledForUser reports whether long-term memory is active for this user.
// Memory is on unless EITHER the global admin setting OR the user's per-user
// override is explicitly false (both default to enabled). Used to gate both
// memory injection (orchestrator) and extraction (memory worker) so a user who
// turns memory off in Personalization gets no memory in any conversation.
func MemoryEnabledForUser(ctx context.Context, db *sql.DB, userID string) bool {
	if !MemoryEnabledGlobal(db) {
		return false
	}
	if permissions, err := UserGroupPermissionsForUser(ctx, db, userID); err != nil || !permissions.AllowMemory {
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
	batch := 500
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

// backfillUserSortOrder gives legacy accounts stable explicit slots matching
// the pre-sort admin list order: newest accounts first, then id for ties.
func backfillUserSortOrder(db *sql.DB) {
	const batch = 500
	offset := 0
	for {
		rows, err := db.Query(`SELECT id FROM users ORDER BY created_at DESC, id LIMIT ? OFFSET ?`, batch, offset)
		if err != nil {
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				continue
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			return
		}
		for i, id := range ids {
			_, _ = db.Exec(`UPDATE users SET sort_order=? WHERE id=?`, offset+i, id)
		}
		if len(ids) < batch {
			return
		}
		offset += len(ids)
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

// SetUserEmail changes only a user's sign-in email. The address is stored in a
// canonical lowercase form so admin edits cannot create case-only duplicates.
func SetUserEmail(ctx context.Context, db *sql.DB, userID, email string) error {
	var err error
	email, err = NormalizeUserEmail(email)
	if err != nil {
		return err
	}

	var targetID string
	err = db.QueryRowContext(ctx, `SELECT id FROM users WHERE id=?`, userID).Scan(&targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var existingID string
	err = db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE lower(trim(email))=? AND id<>? LIMIT 1`, email, userID,
	).Scan(&existingID)
	if err == nil {
		return ErrUserEmailExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	result, err := db.ExecContext(ctx, `UPDATE users SET email=? WHERE id=?`, email, userID)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "unique") || strings.Contains(low, "duplicate") {
			return ErrUserEmailExists
		}
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword writes a new bcrypt hash, rotates the token version (kills
// outstanding access tokens) AND revokes all refresh tokens (§A4) — otherwise a
// stolen refresh token survives a password reset and can re-mint a session,
// defeating the reset.
func UpdateUserPassword(ctx context.Context, db *sql.DB, userID, newHash string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE users
		 SET password_hash=?, password_set=1, password_changed_at=?, token_ver=token_ver+1
		 WHERE id=?`, newHash, time.Now().Unix(), userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND revoked=0`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateUserPasswordIfCurrent is the self-service password-change primitive.
// The handler may spend time verifying bcrypt before it gets here, so the old
// hash comparison must be part of the UPDATE: two concurrent requests that
// both verified the same old password cannot overwrite one another afterward.
func UpdateUserPasswordIfCurrent(ctx context.Context, db *sql.DB, userID, expectedHash, newHash string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE users
		 SET password_hash=?, password_set=1, password_changed_at=?, token_ver=token_ver+1
		 WHERE id=? AND password_hash=?`,
		newHash, time.Now().Unix(), userID, expectedHash)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, userID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		return ErrPasswordChanged
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND revoked=0`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetInitialPassword writes the first password for an account that never had one
// (created via OAuth). Unlike UpdateUserPassword it does NOT rotate the token
// version or revoke refresh tokens — the user is mid-session and we want them to
// stay logged in and continue straight into the app. The conditional update is
// authoritative so concurrent first-password requests cannot overwrite one another.
func SetInitialPassword(ctx context.Context, db *sql.DB, userID, newHash string) error {
	result, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash=?, password_set=1, password_changed_at=? WHERE id=? AND password_set=0`,
		newHash, time.Now().Unix(), userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var passwordSet int
	if err := db.QueryRowContext(ctx, `SELECT password_set FROM users WHERE id=?`, userID).Scan(&passwordSet); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return ErrPasswordAlreadySet
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
	return ListUsersPaged(ctx, db, listUsersPagedDefault, 0)
}

// ListUsersPaged returns users with pagination support. Limit defaults to 200
// and is capped at 500 to prevent unbounded queries at scale.
func ListUsersPaged(ctx context.Context, db *sql.DB, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = listUsersPagedDefault
	}
	if limit > listUsersPagedMax {
		limit = listUsersPagedMax
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, password_changed_at, last_seen_at, COALESCE(credits_permanent,0), sort_order, created_at FROM users ORDER BY sort_order, created_at DESC, id LIMIT ? OFFSET ?`,
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
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.PasswordChangedAt, &u.LastSeenAt, &u.CreditsPermanent, &u.SortOrder, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.TotpEnabled = totpEnabled != 0
		u.HasPassword = passwordSet != 0
		u.Settings = json.RawMessage(settings)
		out = append(out, u)
	}
	return out, rows.Err()
}

const userSelectCols = `id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, password_set, password_changed_at, last_seen_at, COALESCE(credits_permanent,0), sort_order, created_at`

func scanUsers(rows *sql.Rows) ([]User, error) {
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var settings string
		var totpEnabled int
		var passwordSet int
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &passwordSet, &u.PasswordChangedAt, &u.LastSeenAt, &u.CreditsPermanent, &u.SortOrder, &u.CreatedAt); err != nil {
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
		limit = listUsersBySearchDefault
	}
	if limit > listUsersBySearchMax {
		limit = listUsersBySearchMax
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + userSelectCols + ` FROM users`
	var rows *sql.Rows
	var err error
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		rows, err = db.QueryContext(ctx, q+` WHERE LOWER(email) LIKE ? OR LOWER(name) LIKE ? ORDER BY sort_order, created_at DESC, id LIMIT ? OFFSET ?`, like, like, limit, offset)
	} else {
		rows, err = db.QueryContext(ctx, q+` ORDER BY sort_order, created_at DESC, id LIMIT ? OFFSET ?`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	users, err := scanUsers(rows)
	if err != nil {
		return nil, err
	}
	// Membership expiry is lazy elsewhere (FindUserByID/Email). Normalize only
	// the rows returned by this paged admin query, after the cursor is closed, so
	// an admin page reflects current tiers without scanning the whole user table.
	for i := range users {
		maybeExpireGroup(ctx, db, &users[i])
	}
	return users, nil
}

// ReorderUsers persists the visible admin users page order. Because the users
// surface is paginated, keep the original sort_order slots occupied by this
// page and only swap which user sits in each slot; users outside the page keep
// their relative position.
func ReorderUsers(ctx context.Context, db *sql.DB, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	existingIDs := make([]string, 0, len(ids))
	slots := make([]int, 0, len(ids))
	for _, id := range ids {
		var slot int
		err := tx.QueryRowContext(ctx, `SELECT sort_order FROM users WHERE id=?`, id).Scan(&slot)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		existingIDs = append(existingIDs, id)
		slots = append(slots, slot)
	}
	if len(slots) == 0 {
		return tx.Commit()
	}
	for i := 1; i < len(slots); i += 1 {
		key := slots[i]
		j := i - 1
		for j >= 0 && slots[j] > key {
			slots[j+1] = slots[j]
			j -= 1
		}
		slots[j+1] = key
	}
	for i, id := range existingIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET sort_order=? WHERE id=?`, slots[i], id); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// ListUsersByGroup returns one group's members using the same stable ordering
// and expiry normalization as the administrator's main user list.
func ListUsersByGroup(ctx context.Context, db *sql.DB, groupID, search string, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = listUsersBySearchDefault
	}
	if limit > listUsersBySearchMax {
		limit = listUsersBySearchMax
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + userSelectCols + ` FROM users WHERE group_id=?`
	args := []any{groupID}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q += ` AND (LOWER(email) LIKE ? OR LOWER(name) LIKE ?)`
		args = append(args, like, like)
	}
	q += ` ORDER BY sort_order, created_at DESC, id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanUsers(rows)
}

func CountUsersByGroup(ctx context.Context, db *sql.DB, groupID, search string) (int, error) {
	q := `SELECT COUNT(*) FROM users WHERE group_id=?`
	args := []any{groupID}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q += ` AND (LOWER(email) LIKE ? OR LOWER(name) LIKE ?)`
		args = append(args, like, like)
	}
	var count int
	err := db.QueryRowContext(ctx, q, args...).Scan(&count)
	return count, err
}

// SetPermanentCredits overwrites a user's non-expiring credit balance (admin
// edit on the users page, § credits). Floored at 0.
func SetPermanentCredits(ctx context.Context, db *sql.DB, userID string, credits float64) error {
	if credits < 0 {
		credits = 0
	}
	micros, err := CreditsToMicros(credits)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`UPDATE users SET credits_permanent=?, credits_permanent_micros=? WHERE id=?`,
		creditsFromMicros(micros), micros, userID)
	return err
}

// AddPermanentCredits atomically applies a positive top-up. A negative existing
// balance represents fully recorded provider usage that exceeded preflight, so
// top-ups repay it rather than silently flooring the account at zero. Debits must
// use DebitCredits so they are recorded in the billing ledger.
func AddPermanentCredits(ctx context.Context, db *sql.DB, userID string, delta float64) error {
	micros, err := CreditsToMicros(delta)
	if err != nil || micros <= 0 {
		return ErrInvalidCreditAmount
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := addPermanentCreditsMicrosTx(ctx, tx, userID, micros); err != nil {
		return err
	}
	return tx.Commit()
}

func addPermanentCreditsMicrosTx(ctx context.Context, tx *sql.Tx, userID string, deltaMicros int64) error {
	if deltaMicros <= 0 {
		return ErrInvalidCreditAmount
	}
	if !usePostgres {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET credits_permanent_micros=credits_permanent_micros WHERE id=?`, userID); err != nil {
			return err
		}
	}
	query := `SELECT CASE
	    WHEN COALESCE(credits_permanent_micros,0)=0 AND COALESCE(credits_permanent,0)<>0
	    THEN CAST(ROUND(credits_permanent*1000000) AS BIGINT)
	    ELSE COALESCE(credits_permanent_micros,0)
	 END FROM users WHERE id=?`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	var current int64
	if err := tx.QueryRowContext(ctx, query, userID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if current > 0 && deltaMicros > math.MaxInt64-current {
		return ErrInvalidCreditAmount
	}
	next := current + deltaMicros
	_, err := tx.ExecContext(ctx,
		`UPDATE users SET credits_permanent_micros=?, credits_permanent=CAST(? AS DOUBLE PRECISION)/1000000.0 WHERE id=?`,
		next, next, userID)
	return err
}

// PermanentCredits returns a user's non-expiring balance.
func PermanentCredits(ctx context.Context, db *sql.DB, userID string) (float64, error) {
	var micros int64
	err := db.QueryRowContext(ctx,
		`SELECT CASE
		    WHEN COALESCE(credits_permanent_micros,0)=0 AND COALESCE(credits_permanent,0)<>0
		    THEN CAST(ROUND(credits_permanent*1000000) AS BIGINT)
		    ELSE COALESCE(credits_permanent_micros,0)
		 END FROM users WHERE id=?`, userID).Scan(&micros)
	return creditsFromMicros(micros), err
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

type userDeletionWorkspace struct {
	ID      string
	OwnerID string
}

// userDeletionResourceScope is the authoritative scope for containers removed
// with an account: the user's personal/orphaned rows plus every row in a
// sole-member workspace owned by that user. Rows in another live workspace are
// collaborative state and must be transferred instead of deleted.
func userDeletionResourceScope(alias string) string {
	return fmt.Sprintf(`(
		(%[1]s.user_id=? AND (
			COALESCE(%[1]s.workspace_id,'')=''
			OR NOT EXISTS (
				SELECT 1 FROM workspaces deletion_resource_workspace
				 WHERE deletion_resource_workspace.id=%[1]s.workspace_id
			)
		))
		OR EXISTS (
			SELECT 1 FROM workspaces deletion_owned_workspace
			 WHERE deletion_owned_workspace.id=%[1]s.workspace_id
			   AND deletion_owned_workspace.owner_id=?
			   AND NOT EXISTS (
				 SELECT 1 FROM workspace_members deletion_other_member
				  WHERE deletion_other_member.workspace_id=deletion_owned_workspace.id
				    AND deletion_other_member.user_id<>?
			   )
		)
	)`, alias)
}

func userDeletionResourceScopeArgs(userID string) []any {
	return []any{userID, userID, userID}
}

// userDeletionFileScope includes files owned by the deleted account that are
// personal, orphaned, or still-private drafts, plus every uploader's file in a
// conversation that is itself being removed. A committed file in somebody
// else's live workspace is deliberately outside this scope.
func userDeletionFileScope(alias string) string {
	return fmt.Sprintf(`(
		(%[1]s.user_id=? AND (
			%[1]s.draft=1
			OR %[1]s.conversation_id IS NULL
			OR EXISTS (
				SELECT 1 FROM conversations deletion_file_container
				 WHERE deletion_file_container.id=%[1]s.conversation_id
				   AND (
					COALESCE(deletion_file_container.workspace_id,'')=''
					OR NOT EXISTS (
						SELECT 1 FROM workspaces deletion_file_workspace
						 WHERE deletion_file_workspace.id=deletion_file_container.workspace_id
					)
					OR EXISTS (
						SELECT 1 FROM workspaces deletion_file_owned_workspace
						 WHERE deletion_file_owned_workspace.id=deletion_file_container.workspace_id
						   AND deletion_file_owned_workspace.owner_id=?
						   AND NOT EXISTS (
							 SELECT 1 FROM workspace_members deletion_file_other_member
							  WHERE deletion_file_other_member.workspace_id=deletion_file_owned_workspace.id
							    AND deletion_file_other_member.user_id<>?
						   )
					)
				   )
			)
		))
		OR EXISTS (
			SELECT 1 FROM conversations deletion_file_conversation
			 WHERE deletion_file_conversation.id=%[1]s.conversation_id
			   AND %s
		)
	)`, alias, userDeletionResourceScope("deletion_file_conversation"))
}

func userDeletionFileScopeArgs(userID string) []any {
	return []any{userID, userID, userID, userID, userID, userID}
}

func validateUserDeletionReadState(ctx context.Context, q RowExecer, userID string) error {
	var count int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workspaces deletion_workspace
		 WHERE deletion_workspace.owner_id=?
		   AND EXISTS (
			 SELECT 1 FROM workspace_members deletion_member
			  WHERE deletion_member.workspace_id=deletion_workspace.id
			    AND deletion_member.user_id<>?
		   )`, userID, userID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrWorkspaceOwnership
	}
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM workspaces deletion_workspace
		  JOIN users deletion_owner ON deletion_owner.id=deletion_workspace.owner_id
		 WHERE deletion_workspace.owner_id<>?
		   AND deletion_owner.status='deleting'
		   AND (
			 EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=deletion_workspace.id AND m.user_id=?)
			 OR EXISTS (SELECT 1 FROM conversations c WHERE c.workspace_id=deletion_workspace.id AND c.user_id=?)
			 OR EXISTS (SELECT 1 FROM projects p WHERE p.workspace_id=deletion_workspace.id AND p.user_id=?)
			 OR EXISTS (SELECT 1 FROM knowledge_bases k WHERE k.workspace_id=deletion_workspace.id AND k.user_id=?)
			 OR EXISTS (
				SELECT 1 FROM files f JOIN conversations c ON c.id=f.conversation_id
				 WHERE c.workspace_id=deletion_workspace.id AND f.user_id=?
			 )
			 OR EXISTS (
				SELECT 1 FROM conversation_shares s JOIN conversations c ON c.id=s.conversation_id
				 WHERE c.workspace_id=deletion_workspace.id AND s.user_id=?
			 )
		   )`, userID, userID, userID, userID, userID, userID, userID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrWorkspaceOwnerDeleting
	}
	return nil
}

// lockUserDeletionWorkspacesTx takes the same workspace-row lock used by join,
// leave, kick, and workspace resource creation. Locks are acquired in stable ID
// order before any user-row lock to keep concurrent member/owner deletion safe.
func lockUserDeletionWorkspacesTx(ctx context.Context, tx *sql.Tx, userID string) ([]userDeletionWorkspace, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT deletion_workspace.id, deletion_workspace.owner_id
		  FROM workspaces deletion_workspace
		 WHERE deletion_workspace.owner_id=?
		    OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=deletion_workspace.id AND m.user_id=?)
		    OR EXISTS (SELECT 1 FROM conversations c WHERE c.workspace_id=deletion_workspace.id AND c.user_id=?)
		    OR EXISTS (SELECT 1 FROM projects p WHERE p.workspace_id=deletion_workspace.id AND p.user_id=?)
		    OR EXISTS (SELECT 1 FROM knowledge_bases k WHERE k.workspace_id=deletion_workspace.id AND k.user_id=?)
		    OR EXISTS (
			 SELECT 1 FROM files f JOIN conversations c ON c.id=f.conversation_id
			  WHERE c.workspace_id=deletion_workspace.id AND f.user_id=?
		    )
		    OR EXISTS (
			 SELECT 1 FROM conversation_shares s JOIN conversations c ON c.id=s.conversation_id
			  WHERE c.workspace_id=deletion_workspace.id AND s.user_id=?
		    )
		 ORDER BY deletion_workspace.id`, userID, userID, userID, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	workspaces := []userDeletionWorkspace{}
	for rows.Next() {
		var workspace userDeletionWorkspace
		if err := rows.Scan(&workspace.ID, &workspace.OwnerID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		res, err := tx.ExecContext(ctx, `UPDATE workspaces SET id=id WHERE id=?`, workspace.ID)
		if err != nil {
			return nil, err
		}
		if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if affected != 1 {
			return nil, ErrWorkspaceOwnerDeleting
		}
	}
	return workspaces, nil
}

func lockUserDeletionUsersTx(ctx context.Context, tx *sql.Tx, userID string, workspaces []userDeletionWorkspace) (bool, error) {
	ids := map[string]struct{}{userID: {}}
	for _, workspace := range workspaces {
		ids[workspace.OwnerID] = struct{}{}
	}
	ordered := keys(ids)
	sort.Strings(ordered)
	for _, id := range ordered {
		res, err := tx.ExecContext(ctx, `UPDATE users SET id=id WHERE id=?`, id)
		if err != nil {
			return false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if affected != 1 {
			if id == userID {
				return false, nil
			}
			return false, ErrWorkspaceOwnerDeleting
		}
	}
	return true, nil
}

func validateLockedUserDeletionWorkspaces(ctx context.Context, tx *sql.Tx, userID string, workspaces []userDeletionWorkspace) error {
	for _, workspace := range workspaces {
		if workspace.OwnerID == userID {
			var otherMembers int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id<>?`,
				workspace.ID, userID).Scan(&otherMembers); err != nil {
				return err
			}
			if otherMembers > 0 {
				return ErrWorkspaceOwnership
			}
			continue
		}
		var ownerStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, workspace.OwnerID).Scan(&ownerStatus); err != nil {
			return ErrWorkspaceOwnerDeleting
		}
		if ownerStatus == "deleting" {
			return ErrWorkspaceOwnerDeleting
		}
	}
	return nil
}

type namedWorkspaceResourceTransfer struct {
	ID          string
	Name        string
	WorkspaceID string
	OwnerID     string
}

func transferNamedWorkspaceResourcesTx(ctx context.Context, tx *sql.Tx, table, userID string) error {
	if table != "projects" && table != "knowledge_bases" {
		return errors.New("unsupported named workspace resource")
	}
	query := fmt.Sprintf(`
		SELECT deletion_resource.id, deletion_resource.name,
		       deletion_resource.workspace_id, deletion_workspace.owner_id
		  FROM %s deletion_resource
		  JOIN workspaces deletion_workspace ON deletion_workspace.id=deletion_resource.workspace_id
		 WHERE deletion_resource.user_id=? AND deletion_workspace.owner_id<>?
		 ORDER BY deletion_resource.workspace_id, deletion_resource.id`, table)
	rows, err := tx.QueryContext(ctx, query, userID, userID)
	if err != nil {
		return err
	}
	resources := []namedWorkspaceResourceTransfer{}
	for rows.Next() {
		var resource namedWorkspaceResourceTransfer
		if err := rows.Scan(&resource.ID, &resource.Name, &resource.WorkspaceID, &resource.OwnerID); err != nil {
			_ = rows.Close()
			return err
		}
		resources = append(resources, resource)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, resource := range resources {
		candidate := resource.Name
		for attempt := 0; ; attempt++ {
			var conflicts int
			conflictQuery := fmt.Sprintf(`
				SELECT COUNT(*) FROM %s
				 WHERE user_id=? AND COALESCE(workspace_id,'')=? AND id<>?
				   AND lower(trim(name))=lower(trim(?))`, table)
			if err := tx.QueryRowContext(ctx, conflictQuery,
				resource.OwnerID, resource.WorkspaceID, resource.ID, candidate).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts == 0 {
				break
			}
			base := strings.TrimSpace(resource.Name)
			if attempt == 0 {
				candidate = fmt.Sprintf("%s [%s]", base, resource.ID)
			} else {
				candidate = fmt.Sprintf("%s [%s-%d]", base, resource.ID, attempt+1)
			}
		}
		updateQuery := fmt.Sprintf(`UPDATE %s SET user_id=?, name=? WHERE id=? AND user_id=?`, table)
		res, err := tx.ExecContext(ctx, updateQuery, resource.OwnerID, candidate, resource.ID, userID)
		if err != nil {
			return err
		}
		if affected, rowsErr := res.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if affected != 1 {
			return ErrWorkspaceOwnerDeleting
		}
	}
	return nil
}

// transferUserWorkspaceResourcesTx anonymizes the departing creator while
// preserving every committed collaborative resource under the workspace's
// canonical owner. Workspace row locks make the name-conflict resolution and
// bulk ownership updates atomic against concurrent creates and membership loss.
func transferUserWorkspaceResourcesTx(ctx context.Context, tx *sql.Tx, userID string, workspaces []userDeletionWorkspace) error {
	if err := validateLockedUserDeletionWorkspaces(ctx, tx, userID, workspaces); err != nil {
		return err
	}
	if err := transferNamedWorkspaceResourcesTx(ctx, tx, "projects", userID); err != nil {
		return err
	}
	if err := transferNamedWorkspaceResourcesTx(ctx, tx, "knowledge_bases", userID); err != nil {
		return err
	}
	for _, query := range []string{
		`UPDATE conversations
		    SET user_id=(SELECT owner_id FROM workspaces WHERE id=conversations.workspace_id)
		  WHERE user_id=? AND COALESCE(workspace_id,'')<>''
		    AND EXISTS (SELECT 1 FROM workspaces w WHERE w.id=conversations.workspace_id AND w.owner_id<>?)`,
		`UPDATE files
		    SET user_id=(
			SELECT w.owner_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=files.conversation_id
		    )
		  WHERE user_id=? AND draft=0
		    AND EXISTS (
			SELECT 1 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=files.conversation_id AND w.owner_id<>?
		    )`,
		`UPDATE conversation_shares
		    SET user_id=(
			SELECT w.owner_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=conversation_shares.conversation_id
		    )
		  WHERE user_id=?
		    AND EXISTS (
			SELECT 1 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=conversation_shares.conversation_id AND w.owner_id<>?
		    )`,
	} {
		if _, err := tx.ExecContext(ctx, query, userID, userID); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUser permanently removes personal data and sole-owner workspaces while
// preserving collaborative rows in somebody else's workspace. All SQL changes
// run in one transaction; local storage cleanup is best-effort after commit.
func DeleteUser(ctx context.Context, db *sql.DB, userID string, storageRoots ...string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete user: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	workspaces, err := lockUserDeletionWorkspacesTx(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("delete user: lock workspaces: %w", err)
	}
	exists, err := lockUserDeletionUsersTx(ctx, tx, userID, workspaces)
	if err != nil {
		return fmt.Errorf("delete user: lock users: %w", err)
	}
	if !exists {
		return nil
	}
	if err := validateLockedUserDeletionWorkspaces(ctx, tx, userID, workspaces); err != nil {
		return err
	}
	if pending, err := hasPendingPaymentOrdersForUser(ctx, tx, userID); err != nil {
		return fmt.Errorf("delete user: inspect payment orders: %w", err)
	} else if pending {
		return ErrPaymentOrdersPendingForUser
	}

	plan, err := buildUserCleanupPlan(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("delete user: build cleanup plan: %w", err)
	}
	if err := transferUserWorkspaceResourcesTx(ctx, tx, userID, workspaces); err != nil {
		return fmt.Errorf("delete user: transfer workspace resources: %w", err)
	}

	fileScope := userDeletionFileScope("deletion_file")
	keeperScope := userDeletionFileScope("deletion_keeper_file")
	documentArgs := append(userDeletionFileScopeArgs(userID), userDeletionFileScopeArgs(userID)...)
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM documents
		 WHERE storage_path<>''
		   AND EXISTS (
			 SELECT 1 FROM files deletion_file
			  WHERE deletion_file.storage_path=documents.storage_path AND `+fileScope+`
		   )
		   AND NOT EXISTS (
			 SELECT 1 FROM files deletion_keeper_file
			  WHERE deletion_keeper_file.storage_path=documents.storage_path AND NOT (`+keeperScope+`)
		   )`, documentArgs...); err != nil {
		return fmt.Errorf("delete user: delete file documents: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM files WHERE `+userDeletionFileScope("files"),
		userDeletionFileScopeArgs(userID)...); err != nil {
		return fmt.Errorf("delete user: delete files: %w", err)
	}

	resourceArgs := userDeletionResourceScopeArgs(userID)
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM messages WHERE conversation_id IN (
			SELECT deletion_conversation.id FROM conversations deletion_conversation
			 WHERE `+userDeletionResourceScope("deletion_conversation")+`
		)`, resourceArgs...); err != nil {
		return fmt.Errorf("delete user: delete messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversations WHERE `+userDeletionResourceScope("conversations"),
		resourceArgs...); err != nil {
		return fmt.Errorf("delete user: delete conversations: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM knowledge_bases WHERE `+userDeletionResourceScope("knowledge_bases"),
		resourceArgs...); err != nil {
		return fmt.Errorf("delete user: delete knowledge bases: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM projects WHERE `+userDeletionResourceScope("projects"),
		resourceArgs...); err != nil {
		return fmt.Errorf("delete user: delete projects: %w", err)
	}
	// Invitations are live capabilities, not historical content. Removing those
	// created by the departing user prevents a usable token from outliving its
	// creator and also supports databases upgraded from the old restrictive FK.
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_invites WHERE created_by=?`, userID); err != nil {
		return fmt.Errorf("delete user: delete workspace invites: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspaces WHERE owner_id=?`, userID); err != nil {
		return fmt.Errorf("delete user: delete workspaces: %w", err)
	}
	for _, query := range []string{
		`DELETE FROM memories WHERE user_id=?`,
		`DELETE FROM refresh_tokens WHERE user_id=?`,
		`DELETE FROM usage_logs WHERE user_id=?`,
		`DELETE FROM files WHERE user_id=?`,
		`DELETE FROM users WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, userID); err != nil {
			return fmt.Errorf("delete user: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete user: commit: %w", err)
	}

	for _, path := range plan.StoragePaths {
		referenced, refErr := StoragePathReferenced(context.Background(), db, path)
		if refErr != nil {
			log.Printf("delete user %s: check storage refs for %q: %v", userID, path, refErr)
			continue
		}
		if referenced {
			continue
		}
		if err := removeLocalStoragePath(path, storageRoots...); err != nil && !os.IsNotExist(err) {
			log.Printf("delete user %s: remove file %q: %v", userID, path, err)
		}
	}
	return nil
}

// UserCleanupPlan is the side-state snapshot callers need before DeleteUser
// removes rows: vector scopes plus physical storage refs. It intentionally uses
// raw ids/paths so API handlers can perform best-effort Qdrant and S3/OSS cleanup
// after the SQL delete commits.
type UserCleanupPlan struct {
	ConversationIDs []string
	KBIDs           []string
	DocumentIDs     []string
	StoragePaths    []string
}

func BuildUserCleanupPlan(ctx context.Context, db *sql.DB, userID string) (UserCleanupPlan, error) {
	return buildUserCleanupPlan(ctx, db, userID)
}

func buildUserCleanupPlan(ctx context.Context, q RowExecer, userID string) (UserCleanupPlan, error) {
	var plan UserCleanupPlan
	if err := validateUserDeletionReadState(ctx, q, userID); err != nil {
		return plan, err
	}
	collectIDs := func(query string, args ...any) ([]string, error) {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	var err error
	if plan.ConversationIDs, err = collectIDs(
		`SELECT cleanup_conversation.id FROM conversations cleanup_conversation WHERE `+userDeletionResourceScope("cleanup_conversation")+` ORDER BY cleanup_conversation.id`,
		userDeletionResourceScopeArgs(userID)...); err != nil {
		return plan, err
	}
	if plan.KBIDs, err = collectIDs(
		`SELECT cleanup_kb.id FROM knowledge_bases cleanup_kb WHERE `+userDeletionResourceScope("cleanup_kb")+` ORDER BY cleanup_kb.id`,
		userDeletionResourceScopeArgs(userID)...); err != nil {
		return plan, err
	}
	documentWhere := `(
		EXISTS (
			SELECT 1 FROM conversations cleanup_document_conversation
			 WHERE cleanup_document_conversation.id=cleanup_document.conversation_id
			   AND ` + userDeletionResourceScope("cleanup_document_conversation") + `
		)
		OR EXISTS (
			SELECT 1 FROM knowledge_bases cleanup_document_kb
			 WHERE cleanup_document_kb.id=cleanup_document.kb_id
			   AND ` + userDeletionResourceScope("cleanup_document_kb") + `
		)
		OR (
			cleanup_document.storage_path<>''
			AND EXISTS (
				SELECT 1 FROM files cleanup_document_file
				 WHERE cleanup_document_file.storage_path=cleanup_document.storage_path
				   AND ` + userDeletionFileScope("cleanup_document_file") + `
			)
			AND NOT EXISTS (
				SELECT 1 FROM files cleanup_document_keeper
				 WHERE cleanup_document_keeper.storage_path=cleanup_document.storage_path
				   AND NOT (` + userDeletionFileScope("cleanup_document_keeper") + `)
			)
		)
	)`
	documentArgs := []any{}
	documentArgs = append(documentArgs, userDeletionResourceScopeArgs(userID)...)
	documentArgs = append(documentArgs, userDeletionResourceScopeArgs(userID)...)
	documentArgs = append(documentArgs, userDeletionFileScopeArgs(userID)...)
	documentArgs = append(documentArgs, userDeletionFileScopeArgs(userID)...)
	if plan.DocumentIDs, err = collectIDs(
		`SELECT DISTINCT cleanup_document.id FROM documents cleanup_document WHERE `+documentWhere+` ORDER BY cleanup_document.id`,
		documentArgs...); err != nil {
		return plan, err
	}
	paths := map[string]struct{}{}
	addPaths := func(query string, args ...any) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			if strings.TrimSpace(p) != "" {
				paths[p] = struct{}{}
			}
		}
		return rows.Err()
	}
	if err := addPaths(
		`SELECT cleanup_file.storage_path FROM files cleanup_file WHERE cleanup_file.storage_path<>'' AND `+userDeletionFileScope("cleanup_file"),
		userDeletionFileScopeArgs(userID)...); err != nil {
		return plan, err
	}
	if err := addPaths(
		`SELECT DISTINCT cleanup_document.storage_path FROM documents cleanup_document WHERE cleanup_document.storage_path<>'' AND `+documentWhere,
		documentArgs...); err != nil {
		return plan, err
	}
	if err := addPaths(`
		SELECT cleanup_artifact.storage_path
		  FROM artifacts cleanup_artifact
		  JOIN messages cleanup_message ON cleanup_message.id=cleanup_artifact.message_id
		  JOIN conversations cleanup_conversation ON cleanup_conversation.id=cleanup_message.conversation_id
		 WHERE cleanup_artifact.storage_path<>''
		   AND `+userDeletionResourceScope("cleanup_conversation"),
		userDeletionResourceScopeArgs(userID)...); err != nil {
		return plan, err
	}
	plan.StoragePaths = keys(paths)
	return plan, nil
}
