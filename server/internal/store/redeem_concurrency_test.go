package store

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestConcurrentSameGroupRedemptionsStackDurations(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	initialExpiry := time.Now().Add(24 * time.Hour).Unix()
	if _, err := db.ExecContext(ctx, `INSERT INTO user_groups(id,name,is_default) VALUES('ug_pro','Pro',0)`); err != nil {
		t.Fatalf("seed pro group: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,group_id,group_expires_at) VALUES('redeem-user','redeem@example.test','hash','ug_pro',?)`, initialExpiry); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	code1, err := CreateRedeemCode(ctx, db, RedeemCode{Code: "STACK-ONE", GroupID: "ug_pro", DurationDays: 1})
	if err != nil {
		t.Fatalf("create first code: %v", err)
	}
	code2, err := CreateRedeemCode(ctx, db, RedeemCode{Code: "STACK-TWO", GroupID: "ug_pro", DurationDays: 1})
	if err != nil {
		t.Fatalf("create second code: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, code := range []string{code1.Code, code2.Code} {
		code := code
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := RedeemCodeForUser(context.Background(), db, "redeem-user", code, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent redemption: %v", err)
		}
	}
	var expiry int64
	if err := db.QueryRowContext(ctx, `SELECT group_expires_at FROM users WHERE id='redeem-user'`).Scan(&expiry); err != nil {
		t.Fatalf("read final expiry: %v", err)
	}
	want := initialExpiry + 2*86400
	if expiry != want {
		t.Fatalf("final expiry = %d, want %d", expiry, want)
	}
}

func TestRedeemRejectsDurationAndBalanceOverflow(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_free','Free',1)`); err != nil {
		t.Fatalf("seed default group: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_pro','Pro',0)`); err != nil {
		t.Fatalf("seed pro group: %v", err)
	}
	overflowDays := int64(math.MaxInt64/86400 + 1)
	if int64(int(overflowDays)) == overflowDays {
		if _, err := CreateRedeemCode(ctx, db, RedeemCode{
			Code: "DURATION-OVERFLOW", GroupID: "ug_pro", DurationDays: int(overflowDays),
		}); !errors.Is(err, ErrRedeemCodeInvalid) {
			t.Fatalf("duration overflow error = %v, want %v", err, ErrRedeemCodeInvalid)
		}
	}

	initial := int64(math.MaxInt64 - 5)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,group_id,credits_permanent_micros) VALUES('credit-overflow-user','credit-overflow@example.test','hash','ug_free',?)`,
		initial); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	code, err := CreateRedeemCode(ctx, db, RedeemCode{
		Code: "CREDIT-OVERFLOW", Kind: RedeemKindCredits, Credits: 0.000010,
	})
	if err != nil {
		t.Fatalf("create credit code: %v", err)
	}
	if _, _, err := RedeemCodeForUser(ctx, db, "credit-overflow-user", code.Code, false); !errors.Is(err, ErrInvalidCreditAmount) {
		t.Fatalf("credit overflow error = %v, want %v", err, ErrInvalidCreditAmount)
	}
	var usedCount, redemptions int
	var balance int64
	if err := db.QueryRowContext(ctx, `SELECT used_count FROM redeem_codes WHERE id=?`, code.ID).Scan(&usedCount); err != nil {
		t.Fatalf("read used count: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_redemptions WHERE code_id=?`, code.ID).Scan(&redemptions); err != nil {
		t.Fatalf("read redemptions: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT credits_permanent_micros FROM users WHERE id='credit-overflow-user'`).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if usedCount != 0 || redemptions != 0 || balance != initial {
		t.Fatalf("rejected redemption mutated state: used=%d redemptions=%d balance=%d", usedCount, redemptions, balance)
	}
}
