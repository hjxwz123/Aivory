package store

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestListRedeemCodesStatusFilters(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "redeem-filters.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := CreateUserGroup(ctx, db, UserGroup{ID: "ug_redeem", Name: "Redeem"}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	create := func(id string, maxUses, usedCount int, enabled bool, expiresAt int64) {
		t.Helper()
		_, err := CreateRedeemCode(ctx, db, RedeemCode{
			ID: id, Code: id, GroupID: "ug_redeem", MaxUses: maxUses,
			UsedCount: usedCount, Enabled: enabled, ExpiresAt: expiresAt,
			CreatedAt: time.Now().Unix(),
		})
		if err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	create("unused", 1, 0, true, 0)
	create("partial", 2, 1, true, 0)
	create("used", 1, 1, true, 0)
	create("disabled", 1, 0, false, 0)
	create("expired", 1, 0, true, time.Now().Add(-time.Hour).Unix())
	create("disabled-used", 1, 1, false, 0)

	wantByStatus := map[string][]string{
		"unused":  {"unused"},
		"partial": {"partial"},
		"used":    {"used"},
		"invalid": {"disabled", "disabled-used", "expired"},
	}
	for status, want := range wantByStatus {
		rows, err := ListRedeemCodes(ctx, db, RedeemCodeFilter{Status: status})
		if err != nil {
			t.Fatalf("list %s: %v", status, err)
		}
		got := make([]string, len(rows))
		for i, row := range rows {
			got[i] = row.ID
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("%s IDs = %v, want %v", status, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s IDs = %v, want %v", status, got, want)
			}
		}
	}
}
