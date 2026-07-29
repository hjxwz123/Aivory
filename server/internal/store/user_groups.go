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

// DefaultGroupID is the always-present free tier id (seeded in Seed).
const DefaultGroupID = "ug_free"

const userGroupCols = `id, name, description, features, COALESCE(monthly_price_amount_minor,0), COALESCE(yearly_price_amount_minor,0), is_default, sort_order, COALESCE(max_projects,0), COALESCE(max_kbs,0), COALESCE(credit_allowance,0), COALESCE(credit_period_seconds,0), COALESCE(max_workspaces,0), COALESCE(is_public,1), COALESCE(is_purchasable,1), COALESCE(max_storage_mb,0), created_at, updated_at`

func scanUserGroup(s scanner) (UserGroup, error) {
	var g UserGroup
	var features string
	var def, isPub, isPurchasable int
	if err := s.Scan(&g.ID, &g.Name, &g.Description, &features, &g.MonthlyPriceAmountMinor, &g.YearlyPriceAmountMinor, &def, &g.SortOrder, &g.MaxProjects, &g.MaxKBs, &g.CreditAllowance, &g.CreditPeriodSeconds, &g.MaxWorkspaces, &isPub, &isPurchasable, &g.MaxStorageMB, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return g, err
	}
	g.IsDefault = def == 1
	g.IsPublic = isPub == 1
	g.IsPurchasable = isPurchasable == 1
	g.Features = json.RawMessage(orDefaultJSON(features))
	return g, nil
}

func orDefaultJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

// ListUserGroups returns every group in the admin-defined display order.
func ListUserGroups(ctx context.Context, db *sql.DB) ([]UserGroup, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+userGroupCols+` FROM user_groups ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserGroup{}
	for rows.Next() {
		g, err := scanUserGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetUserGroup returns one group by id.
func GetUserGroup(ctx context.Context, db *sql.DB, id string) (*UserGroup, error) {
	g, err := scanUserGroup(db.QueryRowContext(ctx, `SELECT `+userGroupCols+` FROM user_groups WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GetUserGroupByName returns a group by case-insensitive, trimmed name.
func GetUserGroupByName(ctx context.Context, db *sql.DB, name string) (*UserGroup, error) {
	g, err := scanUserGroup(db.QueryRowContext(ctx, `SELECT `+userGroupCols+` FROM user_groups WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CreateUserGroup inserts a non-default group.
func CreateUserGroup(ctx context.Context, db *sql.DB, g UserGroup) (*UserGroup, error) {
	return createUserGroup(ctx, db, g, true)
}

// CreateUserGroupWithPurchaseAvailability inserts a non-default group and
// explicitly sets whether members may purchase it. CreateUserGroup retains the
// historical default of allowing purchases for callers that predate this flag.
func CreateUserGroupWithPurchaseAvailability(ctx context.Context, db *sql.DB, g UserGroup, isPurchasable bool) (*UserGroup, error) {
	return createUserGroup(ctx, db, g, isPurchasable)
}

func createUserGroup(ctx context.Context, db *sql.DB, g UserGroup, isPurchasable bool) (*UserGroup, error) {
	if g.ID == "" {
		g.ID = genID("ug")
	}
	g.Name = strings.TrimSpace(g.Name)
	g.Description = strings.TrimSpace(g.Description)
	if len(g.Features) == 0 {
		g.Features = json.RawMessage("[]")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id, name, description, features, monthly_price_amount_minor, yearly_price_amount_minor, is_default, sort_order, max_projects, max_kbs, credit_allowance, credit_period_seconds, max_workspaces, is_public, is_purchasable, max_storage_mb, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, string(g.Features), g.MonthlyPriceAmountMinor, g.YearlyPriceAmountMinor, g.SortOrder, g.MaxProjects, g.MaxKBs, g.CreditAllowance, g.CreditPeriodSeconds, g.MaxWorkspaces, boolInt(g.IsPublic), boolInt(isPurchasable), g.MaxStorageMB, now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_groups_name_unique", "user_groups.name") {
			return nil, ErrUserGroupNameExists
		}
		return nil, err
	}
	return GetUserGroup(ctx, db, g.ID)
}

// ReorderUserGroups assigns sort_order = position for each id in one
// transaction. This includes the default group; it remains non-deletable but is
// not pinned above paid tiers.
func ReorderUserGroups(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE user_groups SET sort_order=?, updated_at=? WHERE id=?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UserGroupPatch carries selective group edits.
type UserGroupPatch struct {
	Name                    *string          `json:"name"`
	Description             *string          `json:"description"`
	Features                *json.RawMessage `json:"features"`
	MonthlyPriceAmountMinor *int64           `json:"monthly_price_amount_minor"`
	YearlyPriceAmountMinor  *int64           `json:"yearly_price_amount_minor"`
	SortOrder               *int             `json:"sort_order"`
	MaxProjects             *int             `json:"max_projects"`
	MaxKBs                  *int             `json:"max_kbs"`
	CreditAllowance         *float64         `json:"credit_allowance"`
	CreditPeriodSeconds     *int             `json:"credit_period_seconds"`
	MaxWorkspaces           *int             `json:"max_workspaces"`
	MaxStorageMB            *int             `json:"max_storage_mb"`
	IsPublic                *bool            `json:"is_public"`
	IsPurchasable           *bool            `json:"is_purchasable"`
}

func UpdateUserGroup(ctx context.Context, db *sql.DB, id string, p UserGroupPatch) (*UserGroup, error) {
	parts := []string{}
	args := []any{}
	if p.Name != nil {
		parts = append(parts, "name=?")
		args = append(args, strings.TrimSpace(*p.Name))
	}
	if p.Description != nil {
		parts = append(parts, "description=?")
		args = append(args, strings.TrimSpace(*p.Description))
	}
	if p.Features != nil {
		parts = append(parts, "features=?")
		args = append(args, string(*p.Features))
	}
	if p.MonthlyPriceAmountMinor != nil {
		parts = append(parts, "monthly_price_amount_minor=?")
		args = append(args, *p.MonthlyPriceAmountMinor)
	}
	if p.YearlyPriceAmountMinor != nil {
		parts = append(parts, "yearly_price_amount_minor=?")
		args = append(args, *p.YearlyPriceAmountMinor)
	}
	if p.SortOrder != nil {
		parts = append(parts, "sort_order=?")
		args = append(args, *p.SortOrder)
	}
	if p.MaxProjects != nil {
		parts = append(parts, "max_projects=?")
		args = append(args, *p.MaxProjects)
	}
	if p.MaxKBs != nil {
		parts = append(parts, "max_kbs=?")
		args = append(args, *p.MaxKBs)
	}
	if p.CreditAllowance != nil {
		parts = append(parts, "credit_allowance=?")
		args = append(args, *p.CreditAllowance)
	}
	if p.CreditPeriodSeconds != nil {
		parts = append(parts, "credit_period_seconds=?")
		args = append(args, *p.CreditPeriodSeconds)
	}
	if p.MaxWorkspaces != nil {
		parts = append(parts, "max_workspaces=?")
		args = append(args, *p.MaxWorkspaces)
	}
	if p.MaxStorageMB != nil {
		parts = append(parts, "max_storage_mb=?")
		args = append(args, *p.MaxStorageMB)
	}
	if p.IsPublic != nil {
		parts = append(parts, "is_public=?")
		args = append(args, boolInt(*p.IsPublic))
	}
	if p.IsPurchasable != nil {
		parts = append(parts, "is_purchasable=?")
		args = append(args, boolInt(*p.IsPurchasable))
	}
	if len(parts) == 0 {
		return GetUserGroup(ctx, db, id)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix(), id)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("UPDATE user_groups SET %s WHERE id=?", strings.Join(parts, ", ")), args...); err != nil {
		if isUniqueIndexErr(err, "idx_user_groups_name_unique", "user_groups.name") {
			return nil, ErrUserGroupNameExists
		}
		return nil, err
	}
	return GetUserGroup(ctx, db, id)
}

// ValidatePaymentUserGroupPurchasable checks that a membership tier may still
// be purchased. It is used when resuming an order created before an admin
// paused a tier; checkout creation performs the same check under its order
// transaction in CreatePaymentOrder.
func ValidatePaymentUserGroupPurchasable(ctx context.Context, db *sql.DB, id string) error {
	g, err := GetUserGroup(ctx, db, id)
	if errors.Is(err, ErrNotFound) {
		return ErrPaymentProductUnavailable
	}
	if err != nil {
		return err
	}
	if g.IsDefault || !g.IsPublic {
		return ErrPaymentProductUnavailable
	}
	if !g.IsPurchasable {
		return ErrPaymentUserGroupNotPurchasable
	}
	return nil
}

// DeleteUserGroup removes a group and reassigns its members to the default.
// The default group cannot be deleted.
func DeleteUserGroup(ctx context.Context, db *sql.DB, id string) error {
	if id == DefaultGroupID {
		return errors.New("the default group cannot be deleted")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	groupQuery := `SELECT is_default FROM user_groups WHERE id=?`
	if usePostgres {
		groupQuery += ` FOR UPDATE`
	}
	var isDefault int
	if err := tx.QueryRowContext(ctx, groupQuery, id).Scan(&isDefault); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if isDefault != 0 {
		return errors.New("the default group cannot be deleted")
	}
	hasPendingOrders, err := hasPendingPaymentOrdersForUserGroup(ctx, tx, id)
	if err != nil {
		return err
	}
	if hasPendingOrders {
		return ErrPaymentOrdersPendingForGroup
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET group_id=? WHERE group_id=?`, DefaultGroupID, id); err != nil {
		return err
	}
	// model_group_quotas rows cascade via FK.
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserGroup assigns a user to a group (admin action). Bumps the token
// version so the group change (and its quota limits) takes effect immediately —
// the group_id lives in the access-token claims, so outstanding tokens must be
// invalidated (§ FIX-4, same pattern as SetUserRole / SetUserStatus).
// expiresAt is the unix-seconds expiry (0 = permanent). When set, the group
// downgrades back to the default tier once it passes (see maybeExpireGroup), so
// previous_group_id is cleared.
func SetUserGroup(ctx context.Context, db *sql.DB, userID, groupID string, expiresAt int64) error {
	if _, err := GetUserGroup(ctx, db, groupID); err != nil {
		return err
	}
	if expiresAt < 0 {
		expiresAt = 0
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE users SET group_id=?, group_expires_at=?, previous_group_id='' WHERE id=?`,
		groupID, expiresAt, userID); err != nil {
		return err
	}
	return BumpTokenVersion(ctx, db, userID)
}
