package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestRedeemConfirmOverride verifies the group-switch confirm gate: a same-group
// code renews silently, a different-group code requires confirm (and consumes
// nothing until confirmed), and confirming overrides immediately from now.
func TestRedeemConfirmOverride(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rc.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := CreateUserGroup(ctx, db, UserGroup{ID: "ug_pro", Name: "Pro"}); err != nil {
		t.Fatalf("create ug_pro: %v", err)
	}
	if _, err := CreateUserGroup(ctx, db, UserGroup{ID: "ug_vip", Name: "VIP"}); err != nil {
		t.Fatalf("create ug_vip: %v", err)
	}

	origExpiry := time.Now().Add(10 * 24 * time.Hour).Unix()
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id,group_expires_at,credit_cycle_anchor) VALUES('u1','a@b.c','h','user','ug_pro',?,1234)`, origExpiry)

	proCode, err := CreateRedeemCode(ctx, db, RedeemCode{GroupID: "ug_pro", DurationDays: 30})
	if err != nil {
		t.Fatalf("create pro code: %v", err)
	}
	vipCode, err := CreateRedeemCode(ctx, db, RedeemCode{GroupID: "ug_vip", DurationDays: 30})
	if err != nil {
		t.Fatalf("create vip code: %v", err)
	}

	// 1) Same group → renews silently (no confirm), stacking onto the existing
	//    expiry rather than resetting from now.
	_, u, err := RedeemCodeForUser(ctx, db, "u1", proCode.Code, false)
	if err != nil {
		t.Fatalf("redeem same-group: %v", err)
	}
	if u.GroupID != "ug_pro" {
		t.Fatalf("same-group: group = %q, want ug_pro", u.GroupID)
	}
	if want := origExpiry + 30*86400; u.GroupExpiresAt != want {
		t.Fatalf("same-group expiry = %d, want %d (stacked)", u.GroupExpiresAt, want)
	}
	var anchor int64
	if err := db.QueryRowContext(ctx, `SELECT credit_cycle_anchor FROM users WHERE id='u1'`).Scan(&anchor); err != nil {
		t.Fatalf("read same-group anchor: %v", err)
	}
	if anchor != 1234 {
		t.Fatalf("same-group redemption reset credit anchor to %d, want 1234", anchor)
	}

	// 2) Different group, no confirm → ErrRedeemNeedsConfirm, nothing consumed.
	_, prev, err := RedeemCodeForUser(ctx, db, "u1", vipCode.Code, false)
	if !errors.Is(err, ErrRedeemNeedsConfirm) {
		t.Fatalf("diff-group no-confirm: err = %v, want ErrRedeemNeedsConfirm", err)
	}
	if prev.GroupID != "ug_pro" {
		t.Fatalf("preview current group = %q, want ug_pro", prev.GroupID)
	}
	// The vip code must NOT have been consumed.
	var used int
	if err := db.QueryRowContext(ctx, `SELECT used_count FROM redeem_codes WHERE id=?`, vipCode.ID).Scan(&used); err != nil {
		t.Fatalf("read used_count: %v", err)
	}
	if used != 0 {
		t.Fatalf("vip code used_count = %d after preview, want 0", used)
	}
	// And the user is still on Pro with the renewed expiry.
	var gid string
	if err := db.QueryRowContext(ctx, `SELECT group_id FROM users WHERE id='u1'`).Scan(&gid); err != nil {
		t.Fatalf("read group: %v", err)
	}
	if gid != "ug_pro" {
		t.Fatalf("user group after preview = %q, want ug_pro", gid)
	}

	// 3) Different group, confirm → overrides immediately, window from NOW.
	before := time.Now().Unix()
	_, u3, err := RedeemCodeForUser(ctx, db, "u1", vipCode.Code, true)
	if err != nil {
		t.Fatalf("redeem diff-group confirm: %v", err)
	}
	if u3.GroupID != "ug_vip" {
		t.Fatalf("confirm: group = %q, want ug_vip", u3.GroupID)
	}
	if u3.PreviousGroupID != "ug_pro" {
		t.Fatalf("confirm: previous_group = %q, want ug_pro", u3.PreviousGroupID)
	}
	// Expiry reset from now (≈ now + 30d), NOT stacked onto the old Pro expiry.
	wantLow, wantHigh := before+30*86400-5, time.Now().Unix()+30*86400+5
	if u3.GroupExpiresAt < wantLow || u3.GroupExpiresAt > wantHigh {
		t.Fatalf("confirm expiry = %d, want ~now+30d (%d..%d) — must reset, not stack", u3.GroupExpiresAt, wantLow, wantHigh)
	}
	if err := db.QueryRowContext(ctx, `SELECT credit_cycle_anchor FROM users WHERE id='u1'`).Scan(&anchor); err != nil {
		t.Fatalf("read switched-group anchor: %v", err)
	}
	if anchor < before || anchor > time.Now().Unix() {
		t.Fatalf("different-group redemption anchor = %d, want activation time >= %d", anchor, before)
	}
}
