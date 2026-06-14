// Package store — redeem codes & redemptions (§ redeem codes).
//
// Admins create RedeemCodes (a generated string + a target user_group + a
// duration). Users redeem them through the subscription page, which records a
// RedeemRedemption row and bumps their users.group_id / group_expires_at.
//
// Codes are single-use by default (max_uses=1); used_count is updated in the
// same transaction as the redemption insert so concurrent redemptions of the
// same code can't all win.
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

// RedeemCode is a single row in redeem_codes.
type RedeemCode struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	GroupID      string `json:"group_id"`
	DurationDays int    `json:"duration_days"`
	MaxUses      int    `json:"max_uses"`
	UsedCount    int    `json:"used_count"`
	ExpiresAt    int64  `json:"expires_at"`
	Enabled      bool   `json:"enabled"`
	Note         string `json:"note"`
	BatchName    string `json:"batch_name"`
	CreatedBy    string `json:"created_by"`
	CreatedAt    int64  `json:"created_at"`
}

// RedeemRedemption is one row in redeem_redemptions.
type RedeemRedemption struct {
	ID              string `json:"id"`
	CodeID          string `json:"code_id"`
	UserID          string `json:"user_id"`
	GroupID         string `json:"group_id"`
	PreviousGroupID string `json:"previous_group_id"`
	GrantedAt       int64  `json:"granted_at"`
	ExpiresAt       int64  `json:"expires_at"`
}

// ErrRedeemCodeExists is returned by CreateRedeemCode when the generated code
// collides with an existing row (extremely unlikely with 60 bits of entropy,
// but a defined error helps the bulk-generate loop retry deterministically).
var ErrRedeemCodeExists = errors.New("redeem code already exists")

const redeemCodeCols = `id, code, group_id, duration_days, max_uses, used_count, expires_at, enabled, note, batch_name, created_by, created_at`

func scanRedeemCode(s scanner) (RedeemCode, error) {
	var rc RedeemCode
	var enabled int
	if err := s.Scan(&rc.ID, &rc.Code, &rc.GroupID, &rc.DurationDays, &rc.MaxUses, &rc.UsedCount, &rc.ExpiresAt, &enabled, &rc.Note, &rc.BatchName, &rc.CreatedBy, &rc.CreatedAt); err != nil {
		return rc, err
	}
	rc.Enabled = enabled != 0
	return rc, nil
}

// CreateRedeemCode inserts one code. When rc.Code is empty a new code is
// generated. When rc.ID is empty an ID is minted. Newly created codes default
// to enabled=true unless rc.Enabled is explicitly set false on a row that
// already has a CreatedAt (caller is replaying with an existing timestamp).
func CreateRedeemCode(ctx context.Context, db *sql.DB, rc RedeemCode) (*RedeemCode, error) {
	if strings.TrimSpace(rc.GroupID) == "" {
		return nil, errors.New("group_id required")
	}
	if _, err := GetUserGroup(ctx, db, rc.GroupID); err != nil {
		return nil, fmt.Errorf("group not found: %w", err)
	}
	if rc.ID == "" {
		rc.ID = genID("rc")
	}
	if rc.Code == "" {
		rc.Code = GenRedeemCode()
	} else {
		rc.Code = NormalizeRedeemCode(rc.Code)
	}
	if rc.DurationDays < 0 {
		rc.DurationDays = 0
	}
	if rc.MaxUses <= 0 {
		rc.MaxUses = 1
	}
	en := 1
	if !rc.Enabled && rc.CreatedAt > 0 {
		// Only honour an explicit false when caller is rehydrating an existing
		// row (carries the original CreatedAt); fresh inserts default to true.
		en = 0
	}
	if rc.CreatedAt == 0 {
		rc.CreatedAt = time.Now().Unix()
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO redeem_codes(`+redeemCodeCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		rc.ID, rc.Code, rc.GroupID, rc.DurationDays, rc.MaxUses, rc.UsedCount, rc.ExpiresAt, en, rc.Note, rc.BatchName, rc.CreatedBy, rc.CreatedAt)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "unique") || strings.Contains(low, "duplicate") {
			return nil, ErrRedeemCodeExists
		}
		return nil, err
	}
	return GetRedeemCode(ctx, db, rc.ID)
}

// BulkGenerateRedeemCodes creates `count` codes sharing the same group +
// duration + expiry + batch_name. Codes are generated with high-entropy
// crypto/rand; collisions are retried up to 5 times each.
func BulkGenerateRedeemCodes(ctx context.Context, db *sql.DB, template RedeemCode, count int) ([]RedeemCode, error) {
	if count <= 0 {
		count = 1
	}
	if count > 1000 {
		count = 1000
	}
	out := make([]RedeemCode, 0, count)
	for i := 0; i < count; i++ {
		var created *RedeemCode
		for attempt := 0; attempt < 5; attempt++ {
			tpl := template
			tpl.ID = ""
			tpl.Code = ""
			tpl.UsedCount = 0
			tpl.Enabled = true
			tpl.CreatedAt = 0
			row, err := CreateRedeemCode(ctx, db, tpl)
			if err == nil {
				created = row
				break
			}
			if errors.Is(err, ErrRedeemCodeExists) {
				continue
			}
			return out, err
		}
		if created == nil {
			return out, errors.New("could not generate a unique code after 5 attempts")
		}
		out = append(out, *created)
	}
	return out, nil
}

// GetRedeemCode returns one row by id.
func GetRedeemCode(ctx context.Context, db *sql.DB, id string) (*RedeemCode, error) {
	rc, err := scanRedeemCode(db.QueryRowContext(ctx, `SELECT `+redeemCodeCols+` FROM redeem_codes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rc, nil
}

// FindRedeemCodeByCode looks up a row by its public code string. Used by the
// user-facing redeem endpoint.
func FindRedeemCodeByCode(ctx context.Context, db *sql.DB, code string) (*RedeemCode, error) {
	norm := NormalizeRedeemCode(code)
	rc, err := scanRedeemCode(db.QueryRowContext(ctx, `SELECT `+redeemCodeCols+` FROM redeem_codes WHERE code=?`, norm))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rc, nil
}

// RedeemCodeFilter narrows ListRedeemCodes.
type RedeemCodeFilter struct {
	BatchName string // exact match if set
	Status    string // "" | "unused" | "redeemed" | "disabled" | "expired"
	Limit     int
	Offset    int
}

// ListRedeemCodes returns rows newest-first, with optional filters.
func ListRedeemCodes(ctx context.Context, db *sql.DB, f RedeemCodeFilter) ([]RedeemCode, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + redeemCodeCols + ` FROM redeem_codes WHERE 1=1`
	args := []any{}
	if f.BatchName != "" {
		q += ` AND batch_name=?`
		args = append(args, f.BatchName)
	}
	switch f.Status {
	case "unused":
		q += ` AND used_count<max_uses AND enabled=1`
	case "redeemed":
		q += ` AND used_count>=max_uses`
	case "disabled":
		q += ` AND enabled=0`
	case "expired":
		q += ` AND expires_at>0 AND expires_at<?`
		args = append(args, time.Now().Unix())
	}
	q += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedeemCode{}
	for rows.Next() {
		rc, err := scanRedeemCode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, rows.Err()
}

// RedeemCodePatch carries selective edits — admin can flip enabled, change the
// note/batch, or extend the code-expiry deadline.
type RedeemCodePatch struct {
	Enabled   *bool   `json:"enabled"`
	Note      *string `json:"note"`
	BatchName *string `json:"batch_name"`
	ExpiresAt *int64  `json:"expires_at"`
	MaxUses   *int    `json:"max_uses"`
}

// UpdateRedeemCode applies a patch.
func UpdateRedeemCode(ctx context.Context, db *sql.DB, id string, p RedeemCodePatch) (*RedeemCode, error) {
	parts := []string{}
	args := []any{}
	if p.Enabled != nil {
		parts = append(parts, "enabled=?")
		v := 0
		if *p.Enabled {
			v = 1
		}
		args = append(args, v)
	}
	if p.Note != nil {
		parts = append(parts, "note=?")
		args = append(args, *p.Note)
	}
	if p.BatchName != nil {
		parts = append(parts, "batch_name=?")
		args = append(args, *p.BatchName)
	}
	if p.ExpiresAt != nil {
		parts = append(parts, "expires_at=?")
		args = append(args, *p.ExpiresAt)
	}
	if p.MaxUses != nil {
		parts = append(parts, "max_uses=?")
		args = append(args, *p.MaxUses)
	}
	if len(parts) == 0 {
		return GetRedeemCode(ctx, db, id)
	}
	args = append(args, id)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("UPDATE redeem_codes SET %s WHERE id=?", strings.Join(parts, ", ")), args...); err != nil {
		return nil, err
	}
	return GetRedeemCode(ctx, db, id)
}

// DeleteRedeemCode removes the row. Redemptions cascade-delete via FK, so
// already-redeemed codes also lose their audit row; if you want to preserve
// the audit trail prefer setting enabled=false.
func DeleteRedeemCode(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM redeem_codes WHERE id=?`, id)
	return err
}

// DeleteRedeemBatch removes every code in a named batch. Useful for revoking a
// whole campaign at once. Returns the number of codes removed.
func DeleteRedeemBatch(ctx context.Context, db *sql.DB, batchName string) (int64, error) {
	if batchName == "" {
		return 0, errors.New("batch_name required")
	}
	res, err := db.ExecContext(ctx, `DELETE FROM redeem_codes WHERE batch_name=?`, batchName)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Sentinel errors for the redeem flow — handlers map these to user-readable
// strings + i18n keys via the API layer.
var (
	ErrRedeemCodeInvalid     = errors.New("redeem code invalid")
	ErrRedeemCodeUsed        = errors.New("redeem code already used")
	ErrRedeemCodeExpired     = errors.New("redeem code expired")
	ErrRedeemCodeDisabled    = errors.New("redeem code disabled")
	ErrRedeemAlreadyOwned    = errors.New("you already redeemed this code")
)

// RedeemCodeForUser atomically validates the code and grants the configured
// group to the user. On success returns the redemption row and the updated
// user. The user's group_id is set to the code's group_id; group_expires_at
// is set to now()+duration_days (or 0 for permanent codes).
//
// If the user is already on a non-default tier, that tier becomes
// previous_group_id so it can be restored after expiry.
func RedeemCodeForUser(ctx context.Context, db *sql.DB, userID, raw string) (*RedeemRedemption, *User, error) {
	code := NormalizeRedeemCode(raw)
	if code == "" {
		return nil, nil, ErrRedeemCodeInvalid
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var rc RedeemCode
	var enabled int
	err = tx.QueryRowContext(ctx,
		`SELECT `+redeemCodeCols+` FROM redeem_codes WHERE code=?`, code).
		Scan(&rc.ID, &rc.Code, &rc.GroupID, &rc.DurationDays, &rc.MaxUses, &rc.UsedCount, &rc.ExpiresAt, &enabled, &rc.Note, &rc.BatchName, &rc.CreatedBy, &rc.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrRedeemCodeInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	rc.Enabled = enabled != 0
	if !rc.Enabled {
		return nil, nil, ErrRedeemCodeDisabled
	}
	if rc.ExpiresAt > 0 && time.Now().Unix() > rc.ExpiresAt {
		return nil, nil, ErrRedeemCodeExpired
	}
	if rc.UsedCount >= rc.MaxUses {
		return nil, nil, ErrRedeemCodeUsed
	}

	// Verify the group still exists (admin could have deleted it after issue).
	var groupExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_groups WHERE id=?`, rc.GroupID).Scan(&groupExists); err != nil {
		return nil, nil, err
	}
	if groupExists == 0 {
		return nil, nil, ErrRedeemCodeInvalid
	}

	// Look up the user's current state.
	var u User
	var settings string
	var totpEnabled int
	err = tx.QueryRowContext(ctx,
		`SELECT id, email, name, role, status, token_ver, settings, group_id, group_expires_at, previous_group_id, totp_secret, totp_enabled, created_at FROM users WHERE id=?`, userID,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &u.TokenVer, &settings, &u.GroupID, &u.GroupExpiresAt, &u.PreviousGroupID, &u.TotpSecret, &totpEnabled, &u.CreatedAt)
	if err != nil {
		return nil, nil, err
	}
	u.TotpEnabled = totpEnabled != 0
	u.Settings = json.RawMessage(settings)

	// Prevent the same user from redeeming the same code twice (matches the
	// UNIQUE(code_id, user_id) index — checked here for a clean error).
	var prior int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_redemptions WHERE code_id=? AND user_id=?`, rc.ID, userID).Scan(&prior); err != nil {
		return nil, nil, err
	}
	if prior > 0 {
		return nil, nil, ErrRedeemAlreadyOwned
	}

	now := time.Now().Unix()
	var newExpiresAt int64
	if rc.DurationDays > 0 {
		// If the user is already on the same group with time left, stack the
		// duration on top of their existing expiry. Otherwise start the window
		// from now.
		base := now
		if u.GroupID == rc.GroupID && u.GroupExpiresAt > now {
			base = u.GroupExpiresAt
		}
		newExpiresAt = base + int64(rc.DurationDays)*86400
	}
	// previous_group_id: keep what's already there if the user is moving to a
	// new tier from a non-default one; reset to "" when granting permanent
	// access; record the current tier when upgrading from default.
	prevGroup := u.PreviousGroupID
	if u.GroupID != rc.GroupID {
		// Upgrading to a different tier — remember what to fall back to.
		if u.GroupID != DefaultGroupID {
			prevGroup = u.GroupID
		} else {
			prevGroup = ""
		}
	}
	// If we're granting permanent access, the previous-group fallback is moot.
	if rc.DurationDays == 0 {
		prevGroup = ""
	}

	// Insert the redemption row.
	red := RedeemRedemption{
		ID:              genID("rd"),
		CodeID:          rc.ID,
		UserID:          userID,
		GroupID:         rc.GroupID,
		PreviousGroupID: prevGroup,
		GrantedAt:       now,
		ExpiresAt:       newExpiresAt,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO redeem_redemptions(id, code_id, user_id, group_id, previous_group_id, granted_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
		red.ID, red.CodeID, red.UserID, red.GroupID, red.PreviousGroupID, red.GrantedAt, red.ExpiresAt); err != nil {
		return nil, nil, err
	}

	// Bump the code's used_count, guarding the cap atomically inside the TX.
	res, err := tx.ExecContext(ctx,
		`UPDATE redeem_codes SET used_count=used_count+1 WHERE id=? AND used_count<max_uses`,
		rc.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Someone else won the race for the last slot.
		return nil, nil, ErrRedeemCodeUsed
	}

	// Flip the user's group + expiry.
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id=? WHERE id=?`,
		rc.GroupID, newExpiresAt, prevGroup, userID); err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}

	u.GroupID = rc.GroupID
	u.GroupExpiresAt = newExpiresAt
	u.PreviousGroupID = prevGroup
	return &red, &u, nil
}

// ListRedemptionsForUser returns the audit trail for a single user (most
// recent first). Used by the admin user-detail view and potentially the user's
// own subscription history (future work).
func ListRedemptionsForUser(ctx context.Context, db *sql.DB, userID string) ([]RedeemRedemption, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, code_id, user_id, group_id, previous_group_id, granted_at, expires_at FROM redeem_redemptions WHERE user_id=? ORDER BY granted_at DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedeemRedemption{}
	for rows.Next() {
		var r RedeemRedemption
		if err := rows.Scan(&r.ID, &r.CodeID, &r.UserID, &r.GroupID, &r.PreviousGroupID, &r.GrantedAt, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRedemptionsForCode returns every redemption of one code (admin audit).
func ListRedemptionsForCode(ctx context.Context, db *sql.DB, codeID string) ([]RedeemRedemption, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, code_id, user_id, group_id, previous_group_id, granted_at, expires_at FROM redeem_redemptions WHERE code_id=? ORDER BY granted_at DESC`,
		codeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedeemRedemption{}
	for rows.Next() {
		var r RedeemRedemption
		if err := rows.Scan(&r.ID, &r.CodeID, &r.UserID, &r.GroupID, &r.PreviousGroupID, &r.GrantedAt, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
