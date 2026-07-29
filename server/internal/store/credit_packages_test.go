package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCreditPackageCRUDAndPublicFiltering(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "credit-packages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	public, err := CreateCreditPackage(ctx, db, CreditPackage{
		Name: "  Starter  ", Description: "  First package  ", Credits: 1000,
		PriceAmountMinor: 199, Enabled: true, SortOrder: 5,
	})
	if err != nil {
		t.Fatalf("create public package: %v", err)
	}
	if public.Name != "Starter" || public.Description != "First package" || !public.Enabled {
		t.Fatalf("created package not normalized: %+v", public)
	}
	disabled, err := CreateCreditPackage(ctx, db, CreditPackage{Name: "Disabled", Credits: 2000, PriceAmountMinor: 299})
	if err != nil {
		t.Fatalf("create disabled package: %v", err)
	}
	draft, err := CreateCreditPackage(ctx, db, CreditPackage{Name: "Draft", Credits: 3000, Enabled: true})
	if err != nil {
		t.Fatalf("create zero-price draft: %v", err)
	}

	visible, err := ListPublicCreditPackages(ctx, db)
	if err != nil {
		t.Fatalf("list public packages: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != public.ID {
		t.Fatalf("public packages = %+v, want only %s", visible, public.ID)
	}

	name := "Plus"
	credits := 2500.0
	price := int64(499)
	enabled := true
	updated, err := UpdateCreditPackage(ctx, db, disabled.ID, CreditPackagePatch{
		Name: &name, Credits: &credits, PriceAmountMinor: &price, Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("update package: %v", err)
	}
	if updated.Name != name || updated.Credits != credits || updated.PriceAmountMinor != price || !updated.Enabled {
		t.Fatalf("updated package = %+v", updated)
	}
	if err := ReorderCreditPackages(ctx, db, []string{updated.ID, public.ID, draft.ID}); err != nil {
		t.Fatalf("reorder packages: %v", err)
	}
	all, err := ListCreditPackages(ctx, db)
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	if len(all) != 3 || all[0].ID != updated.ID || all[1].ID != public.ID {
		t.Fatalf("reordered packages = %+v", all)
	}
	if err := DeleteCreditPackage(ctx, db, public.ID); err != nil {
		t.Fatalf("delete package: %v", err)
	}
	if err := DeleteCreditPackage(ctx, db, public.ID); err != ErrNotFound {
		t.Fatalf("delete missing package error = %v, want ErrNotFound", err)
	}
}

func TestMigrateLegacyCreditPackageIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "legacy-credit-package.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(db, "permanent_credit_purchase_credits", 10000.0); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(db, "permanent_credit_purchase_price_amount_minor", int64(899)); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyCreditPackage(ctx, db); err != nil {
		t.Fatalf("migrate legacy package: %v", err)
	}
	if err := SetSetting(db, "permanent_credit_purchase_credits", 99999.0); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyCreditPackage(ctx, db); err != nil {
		t.Fatalf("repeat legacy package migration: %v", err)
	}
	packages, err := ListCreditPackages(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].ID != "cp_legacy_default" || packages[0].Credits != 10000 || packages[0].PriceAmountMinor != 899 {
		t.Fatalf("migrated packages = %+v", packages)
	}
	var legacyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN (
		'permanent_credit_purchase_credits',
		'permanent_credit_purchase_price_amount_minor',
		'group_buy_url',
		'credit_buy_url',
		'credit_packages_from_legacy_settings_v1'
	)`).Scan(&legacyCount); err != nil || legacyCount != 0 {
		t.Fatalf("legacy settings remaining = %d, err=%v", legacyCount, err)
	}
}
