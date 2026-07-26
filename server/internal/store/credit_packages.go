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

// CreditPackage is an administrator-defined permanent-credit top-up offer.
// SettlementCurrency is attached by the API from the global setting and is not
// persisted on each package.
type CreditPackage struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	Credits            float64 `json:"credits"`
	PriceAmountMinor   int64   `json:"price_amount_minor"`
	Enabled            bool    `json:"enabled"`
	SortOrder          int     `json:"sort_order"`
	CreatedAt          int64   `json:"created_at"`
	UpdatedAt          int64   `json:"updated_at"`
	SettlementCurrency string  `json:"settlement_currency,omitempty"`
}

type CreditPackagePatch struct {
	Name             *string  `json:"name"`
	Description      *string  `json:"description"`
	Credits          *float64 `json:"credits"`
	PriceAmountMinor *int64   `json:"price_amount_minor"`
	Enabled          *bool    `json:"enabled"`
	SortOrder        *int     `json:"sort_order"`
}

const creditPackageCols = `id, name, description, credits, price_amount_minor, enabled, sort_order, created_at, updated_at`

// DefaultSettlementCurrency is persisted for fresh and restored deployments.
// Prices remain currency-agnostic integer minor units throughout the schema.
const DefaultSettlementCurrency = "USD"

func scanCreditPackage(s scanner) (CreditPackage, error) {
	var p CreditPackage
	var enabled int
	if err := s.Scan(&p.ID, &p.Name, &p.Description, &p.Credits, &p.PriceAmountMinor, &enabled, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	p.Enabled = enabled == 1
	return p, nil
}

func listCreditPackages(ctx context.Context, db *sql.DB, publicOnly bool) ([]CreditPackage, error) {
	query := `SELECT ` + creditPackageCols + ` FROM credit_packages`
	if publicOnly {
		query += ` WHERE enabled=1 AND credits>0 AND price_amount_minor>0`
	}
	query += ` ORDER BY sort_order, name`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreditPackage{}
	for rows.Next() {
		p, err := scanCreditPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func ListCreditPackages(ctx context.Context, db *sql.DB) ([]CreditPackage, error) {
	return listCreditPackages(ctx, db, false)
}

func ListPublicCreditPackages(ctx context.Context, db *sql.DB) ([]CreditPackage, error) {
	return listCreditPackages(ctx, db, true)
}

func GetCreditPackage(ctx context.Context, db *sql.DB, id string) (*CreditPackage, error) {
	p, err := scanCreditPackage(db.QueryRowContext(ctx, `SELECT `+creditPackageCols+` FROM credit_packages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func CreateCreditPackage(ctx context.Context, db *sql.DB, p CreditPackage) (*CreditPackage, error) {
	if p.ID == "" {
		p.ID = genID("cp")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO credit_packages(id, name, description, credits, price_amount_minor, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.Credits, p.PriceAmountMinor, boolInt(p.Enabled), p.SortOrder, now, now); err != nil {
		return nil, err
	}
	return GetCreditPackage(ctx, db, p.ID)
}

func UpdateCreditPackage(ctx context.Context, db *sql.DB, id string, p CreditPackagePatch) (*CreditPackage, error) {
	parts := []string{}
	args := []any{}
	set := func(column string, value any) {
		parts = append(parts, column+"=?")
		args = append(args, value)
	}
	if p.Name != nil {
		set("name", strings.TrimSpace(*p.Name))
	}
	if p.Description != nil {
		set("description", strings.TrimSpace(*p.Description))
	}
	if p.Credits != nil {
		set("credits", *p.Credits)
	}
	if p.PriceAmountMinor != nil {
		set("price_amount_minor", *p.PriceAmountMinor)
	}
	if p.Enabled != nil {
		set("enabled", boolInt(*p.Enabled))
	}
	if p.SortOrder != nil {
		set("sort_order", *p.SortOrder)
	}
	if len(parts) == 0 {
		return GetCreditPackage(ctx, db, id)
	}
	set("updated_at", time.Now().Unix())
	args = append(args, id)
	result, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE credit_packages SET %s WHERE id=?`, strings.Join(parts, ", ")), args...)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return GetCreditPackage(ctx, db, id)
}

func DeleteCreditPackage(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM credit_packages WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func ReorderCreditPackages(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE credit_packages SET sort_order=?, updated_at=? WHERE id=?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const legacyCreditPackageMigrationMarker = "credit_packages_from_legacy_settings_v1"

type settingMigrationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type creditPackageMigrationExecer interface {
	settingMigrationExecer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// EnsureSettlementCurrencySetting keeps the default in the database rather
// than relying on process configuration or a response-only fallback. DO NOTHING
// preserves an administrator's selected currency and imported current backups.
func EnsureSettlementCurrencySetting(ctx context.Context, ex settingMigrationExecer) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('settlement_currency', '"USD"', ?)
		 ON CONFLICT(key) DO NOTHING`, time.Now().Unix())
	return err
}

// MigrateLegacyCreditPackage imports the retired single-offer settings into one
// regular package. The fixed id plus marker make retries and old backup imports
// idempotent.
func MigrateLegacyCreditPackage(ctx context.Context, ex creditPackageMigrationExecer) error {
	var marker string
	err := ex.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCreditPackageMigrationMarker).Scan(&marker)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if marker != "" {
		return nil
	}
	var creditsRaw, priceRaw string
	creditsErr := ex.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='permanent_credit_purchase_credits'`).Scan(&creditsRaw)
	priceErr := ex.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='permanent_credit_purchase_price_amount_minor'`).Scan(&priceRaw)
	if creditsErr != nil && !errors.Is(creditsErr, sql.ErrNoRows) {
		return creditsErr
	}
	if priceErr != nil && !errors.Is(priceErr, sql.ErrNoRows) {
		return priceErr
	}
	var credits float64
	var price int64
	if json.Unmarshal([]byte(creditsRaw), &credits) != nil || json.Unmarshal([]byte(priceRaw), &price) != nil || credits <= 0 || price <= 0 {
		return nil
	}
	now := time.Now().Unix()
	if _, err := ex.ExecContext(ctx,
		`INSERT INTO credit_packages(id, name, description, credits, price_amount_minor, enabled, sort_order, created_at, updated_at)
		 VALUES('cp_legacy_default', 'Permanent credits', '', ?, ?, 1, 0, ?, ?)
		 ON CONFLICT(id) DO NOTHING`, credits, price, now, now); err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES(?, '1', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		legacyCreditPackageMigrationMarker, now)
	return err
}
