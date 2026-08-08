package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openCreditsTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "credits.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, ctx
}

func seedCreditGroupsAndUser(t *testing.T, ctx context.Context, db *sql.DB, freeAllowance, proAllowance float64) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default,credit_allowance,credit_period_seconds) VALUES('ug_free','Free',1,?,604800)`,
		`INSERT INTO user_groups(id,name,is_default,credit_allowance,credit_period_seconds) VALUES('ug_pro','Pro',0,?,2419200)`,
	} {
		allowance := freeAllowance
		if strings.Contains(query, "ug_pro") {
			allowance = proAllowance
		}
		if _, err := db.ExecContext(ctx, query, allowance); err != nil {
			t.Fatalf("seed group: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor) VALUES('u1','credits@example.test','hash','ug_free',?)`,
		time.Now().Unix()-3600); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func TestProCreditCycleStartsWhenMembershipBegins(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 100)

	before := time.Now().Unix()
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", 0); err != nil {
		t.Fatalf("set pro group: %v", err)
	}
	after := time.Now().Unix()
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.CycleAnchor < before || balance.CycleAnchor > after {
		t.Fatalf("cycle anchor = %d, want membership time in %d..%d", balance.CycleAnchor, before, after)
	}
	const fourWeeks = 28 * 24 * 60 * 60
	if balance.PeriodSeconds != fourWeeks || balance.ResetsAt != balance.CycleAnchor+fourWeeks {
		t.Fatalf("pro cycle = period %d reset %d, want %d and anchor+period %d",
			balance.PeriodSeconds, balance.ResetsAt, fourWeeks, balance.CycleAnchor+fourWeeks)
	}
}

func TestCreditUsageIsIsolatedAcrossGroupChanges(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if _, err := DebitCredits(ctx, db, "u1", 7, "test", "free"); err != nil {
		t.Fatalf("debit free credits: %v", err)
	}
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", 0); err != nil {
		t.Fatalf("set pro group: %v", err)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get pro balance: %v", err)
	}
	if balance.TimedUsed != 0 || balance.TimedRemaining != 10 {
		t.Fatalf("new group balance = used %.2f remaining %.2f, want 0/10", balance.TimedUsed, balance.TimedRemaining)
	}
}

func TestDeletingUsageLogsDoesNotRefundCredits(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if _, err := DebitCredits(ctx, db, "u1", 7, "test", "turn"); err != nil {
		t.Fatalf("debit: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO usage_logs(user_id,model_id,purpose,credits) VALUES('u1','m1','chat',7)`); err != nil {
		t.Fatalf("insert usage log: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id='u1'`); err != nil {
		t.Fatalf("delete usage logs: %v", err)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.TimedUsed != 7 || balance.TimedRemaining != 3 {
		t.Fatalf("balance after log deletion = used %.2f remaining %.2f, want 7/3", balance.TimedUsed, balance.TimedRemaining)
	}
}

func TestConcurrentCreditDebitsCannotOverdrawBalance(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := DebitCredits(ctx, db, "u1", 8, "test", "concurrent")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var successes, insufficient int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInsufficientCredits):
			insufficient++
		default:
			t.Fatalf("concurrent debit: %v", err)
		}
	}
	if successes != 1 || insufficient != 1 {
		t.Fatalf("concurrent results = success %d insufficient %d, want 1/1", successes, insufficient)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.TimedUsed != 8 || balance.TimedRemaining != 2 || balance.Permanent != 0 || balance.Available != 2 {
		t.Fatalf("balance = %+v, want timed used 8, remaining/available 2 and no debt", balance)
	}
}

func TestConcurrentCreditReservationsCannotExceedBalance(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ReserveCredits(ctx, db, "u1", 8, "test", fmt.Sprintf("reservation-%d", i), time.Hour)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, insufficient int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInsufficientCredits) {
			insufficient++
		} else {
			t.Fatalf("reserve credits: %v", err)
		}
	}
	if successes != 1 || insufficient != 1 {
		t.Fatalf("reservation results = success %d insufficient %d, want 1/1", successes, insufficient)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.Reserved != 8 || balance.Available != 2 {
		t.Fatalf("reserved balance = %+v, want reserved 8 available 2", balance)
	}
}

func TestCreditSettlementFailureKeepsReservation(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if _, err := ReserveCredits(ctx, db, "u1", 4, "test", "settlement-failure", time.Hour); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_credit_ledger BEFORE INSERT ON credit_ledger BEGIN SELECT RAISE(FAIL, 'forced ledger failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if _, err := SettleCreditReservation(ctx, db, "test", "settlement-failure", 4); err == nil {
		t.Fatal("settlement unexpectedly succeeded")
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM credit_reservations WHERE source_type='test' AND source_id='settlement-failure'`).Scan(&status); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if status != CreditReservationReserved {
		t.Fatalf("reservation status = %q, want reserved", status)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.Reserved != 4 || balance.Available != 6 {
		t.Fatalf("balance after failed settlement = %+v, want held reservation", balance)
	}
}

func TestCreditArithmeticUsesExactMicros(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 0, 0)
	if err := SetPermanentCredits(ctx, db, "u1", 0.3); err != nil {
		t.Fatalf("set permanent credits: %v", err)
	}
	for i := 0; i < 3; i++ {
		source := fmt.Sprintf("decimal-%d", i)
		if _, err := ReserveCredits(ctx, db, "u1", 0.1, "test", source, time.Hour); err != nil {
			t.Fatalf("reserve decimal %d: %v", i, err)
		}
		if _, err := SettleCreditReservation(ctx, db, "test", source, 0.1); err != nil {
			t.Fatalf("settle decimal %d: %v", i, err)
		}
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.Permanent != 0 || balance.Available != 0 {
		t.Fatalf("decimal balance = %+v, want exact zero", balance)
	}
}

func TestSameGroupRenewalPreservesCreditCycle(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", time.Now().Add(7*24*time.Hour).Unix()); err != nil {
		t.Fatalf("set pro group: %v", err)
	}
	var anchor int64
	if err := db.QueryRowContext(ctx, `SELECT credit_cycle_anchor FROM users WHERE id='u1'`).Scan(&anchor); err != nil {
		t.Fatalf("read anchor: %v", err)
	}
	if _, err := DebitCredits(ctx, db, "u1", 4, "test", "before-renewal"); err != nil {
		t.Fatalf("debit before renewal: %v", err)
	}
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", time.Now().Add(14*24*time.Hour).Unix()); err != nil {
		t.Fatalf("renew pro group: %v", err)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get renewed balance: %v", err)
	}
	if balance.CycleAnchor != anchor || balance.TimedUsed != 4 || balance.TimedRemaining != 6 {
		t.Fatalf("renewed balance = %+v, want anchor %d and used/remaining 4/6", balance, anchor)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE users SET group_expires_at=? WHERE id='u1'`,
		time.Now().Unix()-1); err != nil {
		t.Fatalf("expire pro membership: %v", err)
	}
	before := time.Now().Unix()
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", time.Now().Add(14*24*time.Hour).Unix()); err != nil {
		t.Fatalf("reactivate expired pro group: %v", err)
	}
	balance, err = GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get reactivated balance: %v", err)
	}
	if balance.CycleAnchor < before || balance.TimedUsed != 0 || balance.TimedRemaining != 10 {
		t.Fatalf("reactivated balance = %+v, want a fresh cycle", balance)
	}
}

func TestCreditConfigChangeStartsFreshCycle(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if err := SetUserGroup(ctx, db, "u1", "ug_pro", 0); err != nil {
		t.Fatalf("set pro group: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET credit_cycle_anchor=1234 WHERE id='u1'`); err != nil {
		t.Fatalf("set known anchor: %v", err)
	}
	if _, err := DebitCredits(ctx, db, "u1", 4, "test", "old-config"); err != nil {
		t.Fatalf("debit old config: %v", err)
	}
	allowance := 20.0
	period := 14 * 24 * 60 * 60
	before := time.Now().Unix()
	if _, err := UpdateUserGroup(ctx, db, "ug_pro", UserGroupPatch{
		CreditAllowance: &allowance, CreditPeriodSeconds: &period,
	}); err != nil {
		t.Fatalf("update credit config: %v", err)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.CycleAnchor < before || balance.TimedUsed != 0 || balance.TimedRemaining != 20 {
		t.Fatalf("balance after config change = %+v, want fresh 20-credit cycle", balance)
	}

	if _, err := CreateUserGroup(ctx, db, UserGroup{
		ID: "ug_invalid", Name: "Invalid", CreditAllowance: 1,
	}); !errors.Is(err, ErrInvalidCreditConfig) {
		t.Fatalf("invalid credit config error = %v, want %v", err, ErrInvalidCreditConfig)
	}
	if _, err := CreateUserGroup(ctx, db, UserGroup{
		ID: "ug_overflow", Name: "Overflow", CreditAllowance: math.MaxFloat64, CreditPeriodSeconds: 30 * 24 * 60 * 60,
	}); !errors.Is(err, ErrInvalidCreditConfig) {
		t.Fatalf("overflow credit config error = %v, want %v", err, ErrInvalidCreditConfig)
	}
}

func TestGroupExpiryAndDeletionResetCreditCycles(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if _, err := db.ExecContext(ctx,
		`UPDATE users SET group_id='ug_pro', group_expires_at=?, previous_group_id='ug_free', credit_cycle_anchor=1234 WHERE id='u1'`,
		time.Now().Unix()-1); err != nil {
		t.Fatalf("seed expired pro membership: %v", err)
	}
	beforeExpiry := time.Now().Unix()
	u, err := FindUserByID(ctx, db, "u1")
	if err != nil {
		t.Fatalf("expire group: %v", err)
	}
	var anchor int64
	if err := db.QueryRowContext(ctx, `SELECT credit_cycle_anchor FROM users WHERE id='u1'`).Scan(&anchor); err != nil {
		t.Fatalf("read expired anchor: %v", err)
	}
	if u.GroupID != DefaultGroupID || anchor < beforeExpiry {
		t.Fatalf("expired membership = group %q anchor %d, want default with fresh anchor", u.GroupID, anchor)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE users SET group_id='ug_pro', group_expires_at=?, previous_group_id='', credit_cycle_anchor=1234 WHERE id='u1'`,
		time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatalf("seed deletable membership: %v", err)
	}
	beforeDelete := time.Now().Unix()
	if err := DeleteUserGroup(ctx, db, "ug_pro"); err != nil {
		t.Fatalf("delete pro group: %v", err)
	}
	var groupID, previousGroup string
	var expiresAt int64
	if err := db.QueryRowContext(ctx,
		`SELECT group_id, group_expires_at, previous_group_id, credit_cycle_anchor FROM users WHERE id='u1'`,
	).Scan(&groupID, &expiresAt, &previousGroup, &anchor); err != nil {
		t.Fatalf("read user after group deletion: %v", err)
	}
	if groupID != DefaultGroupID || expiresAt != 0 || previousGroup != "" || anchor < beforeDelete {
		t.Fatalf("deleted-group user = group %q expiry %d previous %q anchor %d", groupID, expiresAt, previousGroup, anchor)
	}
}

func TestAddPermanentCreditsRejectsAggregateOverflow(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	initial := int64(math.MaxInt64 - 5)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,credits_permanent_micros) VALUES('overflow-topup','overflow-topup@example.test','hash',?)`,
		initial); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := AddPermanentCredits(ctx, db, "overflow-topup", 0.000010); !errors.Is(err, ErrInvalidCreditAmount) {
		t.Fatalf("overflow top-up error = %v, want %v", err, ErrInvalidCreditAmount)
	}
	var got int64
	if err := db.QueryRowContext(ctx,
		`SELECT credits_permanent_micros FROM users WHERE id='overflow-topup'`).Scan(&got); err != nil {
		t.Fatalf("read credits: %v", err)
	}
	if got != initial {
		t.Fatalf("credits after rejected top-up = %d, want %d", got, initial)
	}
}

func TestAdjustPermanentCreditsAndClaimNotificationOnce(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 10)
	if err := SetPermanentCredits(ctx, db, "u1", 5); err != nil {
		t.Fatalf("seed permanent credits: %v", err)
	}

	adjustment, err := AdjustPermanentCredits(ctx, db, "u1", 2.25, true, "  Service recovery credit  ")
	if err != nil {
		t.Fatalf("add permanent credits: %v", err)
	}
	if adjustment.Before != 5 || adjustment.After != 7.25 || adjustment.Delta != 2.25 || adjustment.NotificationID == "" {
		t.Fatalf("add adjustment = %+v, want before=5 after=7.25 delta=2.25 with notification", adjustment)
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance after add: %v", err)
	}
	if balance.Permanent != 7.25 || balance.TimedRemaining != 10 {
		t.Fatalf("balance after add = permanent %.2f timed %.2f, want 7.25 and 10", balance.Permanent, balance.TimedRemaining)
	}

	notice, err := ClaimCreditAdjustmentNotification(ctx, db, "u1")
	if err != nil {
		t.Fatalf("claim notification: %v", err)
	}
	if notice == nil || notice.ID != adjustment.NotificationID || notice.Direction != "add" || notice.Amount != 2.25 || notice.Reason != "Service recovery credit" {
		t.Fatalf("claimed notification = %+v", notice)
	}
	again, err := ClaimCreditAdjustmentNotification(ctx, db, "u1")
	if err != nil {
		t.Fatalf("claim notification again: %v", err)
	}
	if again != nil {
		t.Fatalf("second claim = %+v, want nil", again)
	}

	adjustment, err = AdjustPermanentCredits(ctx, db, "u1", -3, false, "ignored")
	if err != nil {
		t.Fatalf("remove permanent credits: %v", err)
	}
	if adjustment.Before != 7.25 || adjustment.After != 4.25 || adjustment.Delta != -3 || adjustment.NotificationID != "" {
		t.Fatalf("remove adjustment = %+v, want before=7.25 after=4.25 delta=-3", adjustment)
	}
	balance, err = GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance after remove: %v", err)
	}
	if balance.Permanent != 4.25 || balance.TimedRemaining != 10 {
		t.Fatalf("balance after remove = permanent %.2f timed %.2f, want 4.25 and 10", balance.Permanent, balance.TimedRemaining)
	}

	if _, err := AdjustPermanentCredits(ctx, db, "u1", -5, true, "Should not be committed"); !errors.Is(err, ErrInsufficientPermanentCredits) {
		t.Fatalf("over-removal error = %v, want %v", err, ErrInsufficientPermanentCredits)
	}
	balance, err = GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance after rejected removal: %v", err)
	}
	if balance.Permanent != 4.25 || balance.TimedRemaining != 10 {
		t.Fatalf("balance after rejected removal = permanent %.2f timed %.2f, want unchanged", balance.Permanent, balance.TimedRemaining)
	}
	if notice, err := ClaimCreditAdjustmentNotification(ctx, db, "u1"); err != nil || notice != nil {
		t.Fatalf("notification after rejected removal = %+v, err=%v; want nil", notice, err)
	}
}

func TestAdjustPermanentCreditsRequiresReasonWhenNotifying(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('u-notify','notify@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if _, err := AdjustPermanentCredits(ctx, db, "u-notify", 1, true, " \n\t "); !errors.Is(err, ErrInvalidCreditNotification) {
		t.Fatalf("empty reason error = %v, want %v", err, ErrInvalidCreditNotification)
	}
	tooLong := strings.Repeat("理", CreditAdjustmentReasonMaxRunes+1)
	if _, err := AdjustPermanentCredits(ctx, db, "u-notify", 1, true, tooLong); !errors.Is(err, ErrInvalidCreditNotification) {
		t.Fatalf("long reason error = %v, want %v", err, ErrInvalidCreditNotification)
	}
	if got, err := PermanentCredits(ctx, db, "u-notify"); err != nil || got != 0 {
		t.Fatalf("balance after invalid notifications = %v, err=%v; want 0", got, err)
	}
}

func TestCreditBalanceSaturatesWhenAllowanceAndPermanentCreditsExceedInt64(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	anchor := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id,name,is_default,credit_allowance_micros,credit_period_seconds) VALUES('overflow-group','Overflow',0,?,604800)`,
		int64(math.MaxInt64-10)); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor,credits_permanent_micros) VALUES('overflow-user','overflow@example.test','hash','overflow-group',?,100)`,
		anchor); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	balance, err := GetCreditBalance(ctx, db, "overflow-user")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.availableMicros != math.MaxInt64 || balance.Available <= 0 {
		t.Fatalf("available balance = %d (%v), want saturated positive balance", balance.availableMicros, balance.Available)
	}
}
