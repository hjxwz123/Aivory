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

func TestListRedeemCodesSearchFiltersBeforeLimit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "redeem-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUserGroup(ctx, db, UserGroup{ID: "ug_search", Name: "Search"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []RedeemCode{
		{ID: "old_id", Code: "EARLY-1234", BatchName: "October", Note: "Support 50%_bonus!", CreatedAt: 1, Enabled: true},
		{ID: "new_id", Code: "RECENT-5678", BatchName: "November", Note: "Regular allocation", CreatedAt: 2, Enabled: false},
	} {
		row.GroupID, row.MaxUses = "ug_search", 1
		if _, err := CreateRedeemCode(ctx, db, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		filter RedeemCodeFilter
		want   string
	}{
		{"newest default", RedeemCodeFilter{Limit: 1}, "new_id"},
		{"old code before limit", RedeemCodeFilter{Search: "  early-1234  ", Limit: 1}, "old_id"},
		{"unformatted code", RedeemCodeFilter{Search: "EARLY1234"}, "old_id"},
		{"canonical code", RedeemCodeFilter{Search: "EARL-Y123-4"}, "old_id"},
		{"case-insensitive batch", RedeemCodeFilter{Search: "octOBer"}, "old_id"},
		{"literal wildcards", RedeemCodeFilter{Search: "%_bonus!"}, "old_id"},
		{"id", RedeemCodeFilter{Search: "old_id"}, "old_id"},
		{"combined filters", RedeemCodeFilter{Search: "Support", BatchName: "October", Status: "unused"}, "old_id"},
		{"conflicting batch", RedeemCodeFilter{Search: "Support", BatchName: "November"}, ""},
		{"conflicting status", RedeemCodeFilter{Search: "early", Status: "invalid"}, ""},
		{"quote is literal", RedeemCodeFilter{Search: "' OR 1=1 --"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := ListRedeemCodes(ctx, db, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(rows) != 0 {
					t.Fatalf("got %v, want no rows", rows)
				}
			} else if len(rows) != 1 || rows[0].ID != tc.want {
				t.Fatalf("got %v, want %s", rows, tc.want)
			}
		})
	}
}
