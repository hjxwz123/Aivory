package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestRedeemCreditsCode verifies the 'credits'-kind redeem flow: creation
// validates the amount and pins the FK placeholder, redemption adds to the
// user's permanent balance without touching their group (and without the
// group-switch confirm gate), and the used/dup guards still apply.
func TestRedeemCreditsCode(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rcc.db"))
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
	// The default group is normally seeded by Seed() at startup; the credits
	// code's placeholder group_id FK needs it present.
	exec(t, db, `INSERT INTO user_groups(id, name, is_default) VALUES(?, 'Free', 1)`, DefaultGroupID)

	expiry := time.Now().Add(10 * 24 * time.Hour).Unix()
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id,group_expires_at,credits_permanent) VALUES('u1','a@b.c','h','user','ug_pro',?,7)`, expiry)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id) VALUES('u2','x@y.z','h','user','ug_free')`)

	// Zero / negative amounts are rejected.
	if _, err := CreateRedeemCode(ctx, db, RedeemCode{Kind: RedeemKindCredits}); err == nil {
		t.Fatal("create with credits=0: want error, got nil")
	}

	code, err := CreateRedeemCode(ctx, db, RedeemCode{Kind: RedeemKindCredits, Credits: 50, MaxUses: 2})
	if err != nil {
		t.Fatalf("create credits code: %v", err)
	}
	if code.GroupID != DefaultGroupID || code.DurationDays != 0 {
		t.Fatalf("credits code group/duration = %q/%d, want placeholder %q/0", code.GroupID, code.DurationDays, DefaultGroupID)
	}

	// Redeeming adds to the permanent balance, leaves the group + expiry alone,
	// and never asks for the group-switch confirmation (confirm=false).
	red, u, err := RedeemCodeForUser(ctx, db, "u1", code.Code, false)
	if err != nil {
		t.Fatalf("redeem credits: %v", err)
	}
	if red.Credits != 50 {
		t.Fatalf("redemption credits = %v, want 50", red.Credits)
	}
	if u.CreditsPermanent != 57 {
		t.Fatalf("balance = %v, want 57", u.CreditsPermanent)
	}
	if u.GroupID != "ug_pro" || u.GroupExpiresAt != expiry {
		t.Fatalf("group changed: %q/%d, want ug_pro/%d", u.GroupID, u.GroupExpiresAt, expiry)
	}
	if got, err := PermanentCredits(ctx, db, "u1"); err != nil || got != 57 {
		t.Fatalf("stored balance = %v (err %v), want 57", got, err)
	}

	// Same user can't redeem the same code twice.
	if _, _, err := RedeemCodeForUser(ctx, db, "u1", code.Code, false); !errors.Is(err, ErrRedeemAlreadyOwned) {
		t.Fatalf("re-redeem: err = %v, want ErrRedeemAlreadyOwned", err)
	}

	// Second slot goes to another user; the cap then blocks everyone.
	if _, _, err := RedeemCodeForUser(ctx, db, "u2", code.Code, false); err != nil {
		t.Fatalf("second-user redeem: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,group_id) VALUES('u3','q@w.e','h','user','ug_free')`)
	if _, _, err := RedeemCodeForUser(ctx, db, "u3", code.Code, false); !errors.Is(err, ErrRedeemCodeUsed) {
		t.Fatalf("over-cap redeem: err = %v, want ErrRedeemCodeUsed", err)
	}
}
