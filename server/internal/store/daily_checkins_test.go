package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDailyCheckinConcurrentClaimAndSystemMidnight(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 0, 0)
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 15, 55, 0, 0, time.UTC)
	if _, _, err := ClaimDailyCheckin(ctx, db, "u1", now, zone); !errors.Is(err, ErrDailyCheckinDisabled) {
		t.Fatalf("disabled claim returned %v", err)
	}
	if err := SetSetting(db, DailyCheckinKey, DailyCheckinConfig{Enabled: true, Mode: "random", Pool: "permanent", Min: 2.25, Max: 2.27}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		claim   *DailyCheckin
		awarded bool
		err     error
	}
	const parallel = 8
	results := make(chan result, parallel)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, awarded, err := ClaimDailyCheckin(ctx, db, "u1", now, zone)
			results <- result{claim, awarded, err}
		}()
	}
	wg.Wait()
	close(results)
	var awards int
	var amount float64
	for got := range results {
		if got.err != nil || got.claim == nil || got.claim.Date != "2026-11-23" || got.claim.Credits < 2.25 || got.claim.Credits > 2.27 {
			t.Fatalf("unexpected claim: %+v", got)
		}
		if amount != 0 && got.claim.Credits != amount {
			t.Fatal("duplicate claim changed the reward")
		}
		amount = got.claim.Credits
		if got.awarded {
			awards++
		}
	}
	if balance, err := PermanentCredits(ctx, db, "u1"); err != nil || balance != amount || awards != 1 {
		t.Fatalf("awards=%d, balance=%v, err=%v", awards, balance, err)
	}
	claim, awarded, err := ClaimDailyCheckin(ctx, db, "u1", now.Add(5*time.Minute), zone)
	if err != nil || !awarded || claim.Date != "2026-11-24" {
		t.Fatalf("claim after local midnight: %+v, awarded=%t, err=%v", claim, awarded, err)
	}
}

func TestTimedCheckinCreditsDebitAndExpireAtSystemMidnight(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 10, 0)
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, end := CalendarDay(now, zone)
	if err := SetSetting(db, DailyCheckinKey, DailyCheckinConfig{Enabled: true, Mode: "fixed", Pool: "timed", Fixed: 3}); err != nil {
		t.Fatal(err)
	}
	claim, awarded, err := ClaimDailyCheckin(ctx, db, "u1", now, zone)
	if err != nil || !awarded || claim.ExpiresAt != end.Unix() {
		t.Fatalf("timed claim=%+v, awarded=%t, err=%v", claim, awarded, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	debit, err := debitCreditsTx(ctx, tx, "u1", 2*CreditMicrosPerUnit, "test", "timed", now.Unix())
	if err != nil || debit.Timed != 2 || debit.Permanent != 0 {
		t.Fatalf("debit=%+v err=%v", debit, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		timestamp int64
		want      float64
	}{{now.Unix(), 1}, {end.Unix(), 0}} {
		balance, err := creditBalanceFrom(ctx, db, "u1", check.timestamp, false)
		if err != nil || balance.CheckinRemaining != check.want || balance.TimedRemaining != 10+check.want {
			t.Fatalf("balance at %v = %+v, err=%v", check.timestamp, balance, err)
		}
	}
}

func TestDailyUsageRespectsSystemDateAndUser(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 0, 0)
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 17, 5, 0, 0, time.UTC)
	start, _ := CalendarDay(now, zone)
	for _, row := range []struct {
		id, user   string
		at, tokens int64
	}{
		{"before", "u1", start.Unix() - 1, 7}, {"after", "u1", start.Unix(), 13},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO billing_usage(id,user_id,purpose,input_tokens,created_at) VALUES(?,?,?,?,?)`,
			row.id, row.user, "chat", row.tokens, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO credit_ledger(id,user_id,group_id,kind,amount,amount_micros,created_at)
		VALUES('spend','u1','ug_free',?,0.75,750000,?)`, CreditLedgerCheckinDebit, start.Unix()); err != nil {
		t.Fatal(err)
	}
	days, err := ListDailyUsage(ctx, db, "u1", now, zone)
	if err != nil || len(days) != DailyUsageDays {
		t.Fatalf("usage days=%d, err=%v", len(days), err)
	}
	if days[len(days)-1] != (DailyUsage{Date: "2026-11-24", Tokens: 13, Credits: .75}) || days[len(days)-2].Tokens != 7 {
		t.Fatalf("last two days = %+v", days[len(days)-2:])
	}
}

func TestDailyCheckinHistoryIsLimitedToUserAndMonth(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	seedCreditGroupsAndUser(t, ctx, db, 0, 0)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash) VALUES('u2','other@example.test','hash')`); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ user, day string }{
		{"u1", "2026-11-30"}, {"u1", "2026-12-31"}, {"u1", "2026-12-01"}, {"u1", "2027-01-01"}, {"u2", "2026-12-02"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO daily_checkins(user_id,day,claimed_at,amount_micros,remaining_micros,expires_at)
			VALUES(?,?,100,2250000,0,200)`, entry.user, entry.day); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := ListDailyCheckins(ctx, db, "u1", time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(claims) != 2 ||
		claims[0] != (DailyCheckin{Date: "2026-12-01", Credits: 2.25, ExpiresAt: 200}) ||
		claims[1] != (DailyCheckin{Date: "2026-12-31", Credits: 2.25, ExpiresAt: 200}) {
		t.Fatalf("December claims = %+v, err=%v", claims, err)
	}
	claims, err = ListDailyCheckins(ctx, db, "u1", time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || claims == nil || len(claims) != 0 {
		t.Fatalf("February claims = %#v, err=%v", claims, err)
	}
}

func TestCreditCycleKeepsLocalRefreshHourAcrossDaylightSaving(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 3, 7, 9, 30, 0, 0, zone)
	start, end := calendarCreditCycle(anchor.Unix(), 86400, anchor.Add(time.Hour).Unix(), zone)
	if start != anchor.Unix() || end != anchor.Add(23*time.Hour).Unix() {
		t.Fatalf("cycle = %s to %s", time.Unix(start, 0).In(zone), time.Unix(end, 0).In(zone))
	}
}
