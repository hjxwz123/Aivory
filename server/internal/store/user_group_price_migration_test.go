package store

import (
	"path/filepath"
	"testing"
)

func TestMigrateLegacyUserGroupPriceBackfillRunsOnce(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-user-group-price.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer db.Close()

	for _, ddl := range []string{
		`CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE user_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			features TEXT NOT NULL DEFAULT '[]',
			price_usd REAL NOT NULL DEFAULT 0,
			price_cny REAL NOT NULL DEFAULT 0,
			is_default INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO user_groups(id, name, price_usd, price_cny)
			VALUES('ug_legacy', 'Legacy', 9.99, 69)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("prepare legacy database with %q: %v", ddl, err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	var monthly, yearly int64
	if err := db.QueryRow(`SELECT monthly_price_amount_minor, yearly_price_amount_minor FROM user_groups WHERE id='ug_legacy'`).Scan(&monthly, &yearly); err != nil {
		t.Fatalf("read migrated price: %v", err)
	}
	if monthly != 999 || yearly != 0 {
		t.Fatalf("migrated monthly/yearly prices = %d/%d, want 999/0", monthly, yearly)
	}
	var isPurchasable int
	if err := db.QueryRow(`SELECT is_purchasable FROM user_groups WHERE id='ug_legacy'`).Scan(&isPurchasable); err != nil {
		t.Fatalf("read migrated purchase availability: %v", err)
	}
	if isPurchasable != 1 {
		t.Fatalf("migrated is_purchasable = %d, want 1 to preserve existing group behavior", isPurchasable)
	}
	var marker string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='user_group_billing_prices_backfill_v2'`).Scan(&marker); err != nil {
		t.Fatalf("read migration marker: %v", err)
	}
	if marker != "1" {
		t.Fatalf("migration marker = %q, want 1", marker)
	}

	if _, err := db.Exec(`UPDATE user_groups SET monthly_price_amount_minor=0, yearly_price_amount_minor=4242 WHERE id='ug_legacy'`); err != nil {
		t.Fatalf("set administrator prices: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM settings WHERE key='user_group_billing_prices_backfill_v2'`); err != nil {
		t.Fatalf("remove migration marker: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT monthly_price_amount_minor, yearly_price_amount_minor FROM user_groups WHERE id='ug_legacy'`).Scan(&monthly, &yearly); err != nil {
		t.Fatalf("read prices after second migrate: %v", err)
	}
	if monthly != 0 || yearly != 4242 {
		t.Fatalf("second migrate overwrote administrator prices: got %d/%d, want 0/4242", monthly, yearly)
	}
}

func TestMigrateSingleSettlementPriceBecomesMonthlyPrice(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "single-user-group-price.db"))
	if err != nil {
		t.Fatalf("open single-price database: %v", err)
	}
	defer db.Close()

	for _, ddl := range []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE user_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			features TEXT NOT NULL DEFAULT '[]',
			price_amount_minor INTEGER NOT NULL DEFAULT 0,
			is_default INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO user_groups(id, name, price_amount_minor) VALUES('ug_single', 'Single', 1299)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("prepare single-price database with %q: %v", ddl, err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var monthly, yearly int64
	if err := db.QueryRow(`SELECT monthly_price_amount_minor, yearly_price_amount_minor FROM user_groups WHERE id='ug_single'`).Scan(&monthly, &yearly); err != nil {
		t.Fatalf("read migrated prices: %v", err)
	}
	if monthly != 1299 || yearly != 0 {
		t.Fatalf("migrated monthly/yearly prices = %d/%d, want 1299/0", monthly, yearly)
	}
}
