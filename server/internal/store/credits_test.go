package store

import (
	"context"
	"database/sql"
	"errors"
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

func TestConcurrentCreditDebitsRecordFullOverage(t *testing.T) {
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
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent debit: %v", err)
		}
	}
	balance, err := GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.TimedUsed != 10 || balance.TimedRemaining != 0 || balance.Permanent != -6 || balance.Available != 0 {
		t.Fatalf("balance = %+v, want timed used 10, permanent -6, available 0", balance)
	}
	if err := AddPermanentCredits(ctx, db, "u1", 5); err != nil {
		t.Fatalf("top up overage debt: %v", err)
	}
	balance, err = GetCreditBalance(ctx, db, "u1")
	if err != nil {
		t.Fatalf("get topped-up balance: %v", err)
	}
	if balance.Permanent != -1 || balance.Available != 0 {
		t.Fatalf("partial top-up balance = %+v, want permanent -1 and available 0", balance)
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
