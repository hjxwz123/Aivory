package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// DefaultGroupID is the always-present free tier id (seeded in Seed).
const DefaultGroupID = "ug_free"

var ErrInvalidCreditConfig = errors.New("invalid credit allowance or refresh period")

func validateCreditConfig(allowance float64, periodSeconds int) error {
	if math.IsNaN(allowance) || math.IsInf(allowance, 0) || allowance < 0 || periodSeconds < 0 {
		return ErrInvalidCreditConfig
	}
	micros, err := CreditsToMicros(allowance)
	if err != nil || allowance > 0 && micros == 0 {
		return ErrInvalidCreditConfig
	}
	if allowance > 0 && periodSeconds == 0 {
		return ErrInvalidCreditConfig
	}
	return nil
}

func validateUserGroupBilling(g UserGroup) error {
	if err := validateCreditConfig(g.CreditAllowance, g.CreditPeriodSeconds); err != nil {
		return err
	}
	if g.MonthlyPriceAmountMinor < 0 || g.YearlyPriceAmountMinor < 0 ||
		g.MaxProjects < 0 || g.MaxKBs < 0 || g.MaxWorkspaces < 0 || g.MaxStorageMB < 0 {
		return ErrInvalidCreditConfig
	}
	return nil
}

func ValidateUserGroupBilling(g UserGroup) error {
	return validateUserGroupBilling(g)
}

func mustCreditMicros(amount float64) int64 {
	micros, _ := CreditsToMicros(amount)
	return micros
}

const userGroupCols = `id, name, description, features, COALESCE(monthly_price_amount_minor,0), COALESCE(yearly_price_amount_minor,0), is_default, sort_order, COALESCE(max_projects,0), COALESCE(max_kbs,0), COALESCE(credit_allowance,0), COALESCE(credit_period_seconds,0), COALESCE(max_workspaces,0), COALESCE(is_public,1), COALESCE(is_purchasable,1), COALESCE(max_storage_mb,0), COALESCE(permissions,'{}'), created_at, updated_at`

func scanUserGroup(s scanner) (UserGroup, error) {
	var g UserGroup
	var features string
	var permissions string
	var def, isPub, isPurchasable int
	if err := s.Scan(&g.ID, &g.Name, &g.Description, &features, &g.MonthlyPriceAmountMinor, &g.YearlyPriceAmountMinor, &def, &g.SortOrder, &g.MaxProjects, &g.MaxKBs, &g.CreditAllowance, &g.CreditPeriodSeconds, &g.MaxWorkspaces, &isPub, &isPurchasable, &g.MaxStorageMB, &permissions, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return g, err
	}
	g.IsDefault = def == 1
	g.IsPublic = isPub == 1
	g.IsPurchasable = isPurchasable == 1
	g.Features = json.RawMessage(orDefaultJSON(features))
	var err error
	g.Permissions, err = NormalizeUserGroupPermissions(json.RawMessage(permissions))
	if err != nil {
		return g, err
	}
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

// CreateUserGroupWithPermissions is the API-facing create path. permissions is
// nil only when an older client omitted the field, in which case the new group
// inherits the backwards-compatible permissive policy. A present JSON object is
// normalized as submitted, including an intentional all-false capability set.
func CreateUserGroupWithPermissions(ctx context.Context, db *sql.DB, g UserGroup, isPurchasable bool, permissions *json.RawMessage) (*UserGroup, error) {
	if permissions == nil {
		g.Permissions = DefaultUserGroupPermissions()
	} else {
		normalized, err := NormalizeUserGroupPermissions(*permissions)
		if err != nil {
			return nil, err
		}
		g.Permissions = normalized
	}
	return createUserGroup(ctx, db, g, isPurchasable)
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
	permissions := g.Permissions
	if isZeroUserGroupPermissions(permissions) {
		permissions = DefaultUserGroupPermissions()
	}
	permissionsRaw, err := json.Marshal(permissions)
	if err != nil {
		return nil, err
	}
	permissions, err = NormalizeUserGroupPermissions(permissionsRaw)
	if err != nil {
		return nil, err
	}
	permissionsText, err := permissionsJSON(permissions)
	if err != nil {
		return nil, err
	}
	if err := validateUserGroupBilling(g); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_groups(id, name, description, features, monthly_price_amount_minor, yearly_price_amount_minor, is_default, sort_order, max_projects, max_kbs, credit_allowance, credit_allowance_micros, credit_period_seconds, max_workspaces, is_public, is_purchasable, max_storage_mb, permissions, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, string(g.Features), g.MonthlyPriceAmountMinor, g.YearlyPriceAmountMinor, g.SortOrder, g.MaxProjects, g.MaxKBs, g.CreditAllowance, mustCreditMicros(g.CreditAllowance), g.CreditPeriodSeconds, g.MaxWorkspaces, boolInt(g.IsPublic), boolInt(isPurchasable), g.MaxStorageMB, permissionsText, now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_user_groups_name_unique", "user_groups.name") {
			return nil, ErrUserGroupNameExists
		}
		return nil, err
	}
	return GetUserGroup(ctx, db, g.ID)
}

func isZeroUserGroupPermissions(p UserGroupPermissions) bool {
	return p.Prompts.Mode == "" && len(p.Prompts.IDs) == 0 &&
		p.Skills.Mode == "" && len(p.Skills.IDs) == 0 &&
		p.Tools.Mode == "" && len(p.Tools.IDs) == 0 &&
		!p.AllowSharing && !p.AllowKnowledgeBases && !p.AllowKnowledgeBaseSharing && !p.AllowFileUpload &&
		!p.AllowConversationExport && !p.AllowVoiceTranscription &&
		!p.AllowMemory && !p.AllowDrawing
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
	Permissions             *json.RawMessage `json:"permissions"`
}

func UpdateUserGroup(ctx context.Context, db *sql.DB, id string, p UserGroupPatch) (*UserGroup, error) {
	updated, _, err := UpdateUserGroupWithPermissionChange(ctx, db, id, p)
	return updated, err
}

// UpdateUserGroupWithPermissionChange compares permissions under the same row
// lock as the write, so concurrent administrator saves cannot make revocation
// events use a stale before-value.
func UpdateUserGroupWithPermissionChange(ctx context.Context, db *sql.DB, id string, p UserGroupPatch) (*UserGroup, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	currentQuery := `SELECT ` + userGroupCols + ` FROM user_groups WHERE id=?`
	if usePostgres {
		currentQuery += ` FOR UPDATE`
	}
	current, err := scanUserGroup(tx.QueryRowContext(ctx, currentQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	allowance := current.CreditAllowance
	periodSeconds := current.CreditPeriodSeconds
	if p.CreditAllowance != nil {
		allowance = *p.CreditAllowance
	}
	if p.CreditPeriodSeconds != nil {
		periodSeconds = *p.CreditPeriodSeconds
	}
	if err := validateCreditConfig(allowance, periodSeconds); err != nil {
		return nil, false, err
	}
	if p.MonthlyPriceAmountMinor != nil && *p.MonthlyPriceAmountMinor < 0 ||
		p.YearlyPriceAmountMinor != nil && *p.YearlyPriceAmountMinor < 0 ||
		p.MaxProjects != nil && *p.MaxProjects < 0 ||
		p.MaxKBs != nil && *p.MaxKBs < 0 ||
		p.MaxWorkspaces != nil && *p.MaxWorkspaces < 0 ||
		p.MaxStorageMB != nil && *p.MaxStorageMB < 0 {
		return nil, false, ErrInvalidCreditConfig
	}
	creditConfigChanged := allowance != current.CreditAllowance || periodSeconds != current.CreditPeriodSeconds
	permissionsChanged := false

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
		parts = append(parts, "credit_allowance=?", "credit_allowance_micros=?")
		args = append(args, *p.CreditAllowance, mustCreditMicros(*p.CreditAllowance))
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
	if p.Permissions != nil {
		permissions, err := NormalizeUserGroupPermissions(*p.Permissions)
		if err != nil {
			return nil, false, err
		}
		permissionsChanged = !UserGroupPermissionsEqual(current.Permissions, permissions)
		permissionsText, err := permissionsJSON(permissions)
		if err != nil {
			return nil, false, err
		}
		parts = append(parts, "permissions=?")
		args = append(args, permissionsText)
	}
	if len(parts) == 0 {
		return &current, false, nil
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix(), id)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE user_groups SET %s WHERE id=?", strings.Join(parts, ", ")), args...); err != nil {
		if isUniqueIndexErr(err, "idx_user_groups_name_unique", "user_groups.name") {
			return nil, false, ErrUserGroupNameExists
		}
		return nil, false, err
	}
	if creditConfigChanged {
		now := time.Now().Unix()
		if _, err := tx.ExecContext(ctx,
			`UPDATE users
			    SET credit_cycle_anchor=CASE
			            WHEN credit_cycle_anchor>=? THEN credit_cycle_anchor+1
			            ELSE ?
			        END,
			        quota_cycle_anchor=CASE
			            WHEN quota_cycle_anchor>=? THEN quota_cycle_anchor+1
			            ELSE ?
			        END
			  WHERE group_id=?`,
			now, now, now, now, id); err != nil {
			return nil, false, err
		}
	}
	updated, err := scanUserGroup(tx.QueryRowContext(ctx, `SELECT `+userGroupCols+` FROM user_groups WHERE id=?`, id))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &updated, permissionsChanged, nil
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
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
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
		  WHERE group_id=?`,
		DefaultGroupID, now, now, now, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET previous_group_id='' WHERE previous_group_id=?`, id); err != nil {
		return err
	}
	// model_group_quotas rows cascade via FK.
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserGroup assigns a user to a group (admin action). Group identity is not
// stored in access-token claims; request authorization and quota scopes resolve
// it from the database. Preserve active sessions so an administrator changing a
// plan refreshes capabilities instead of unexpectedly signing the user out.
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
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`UPDATE users
		    SET credit_cycle_anchor=CASE
		            WHEN group_id<>? OR (group_expires_at>0 AND group_expires_at<=?) THEN
		                CASE WHEN credit_cycle_anchor>=? THEN credit_cycle_anchor+1 ELSE ? END
		            ELSE credit_cycle_anchor
		        END,
		        quota_cycle_anchor=CASE
		            WHEN group_id<>? OR (group_expires_at>0 AND group_expires_at<=?) THEN
		                CASE WHEN quota_cycle_anchor>=? THEN quota_cycle_anchor+1 ELSE ? END
		            ELSE quota_cycle_anchor
		        END,
		        group_id=?, group_expires_at=?, previous_group_id=''
		  WHERE id=?`,
		groupID, now, now, now, groupID, now, now, now, groupID, expiresAt, userID); err != nil {
		return err
	}
	return nil
}
