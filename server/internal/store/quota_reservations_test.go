package store

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestModelQuotaStartsAtMembershipActivationAndIgnoresOldGroup(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_free','Free',1)`,
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_pro','Pro',0)`,
		`INSERT INTO users(id,email,password_hash,group_id,quota_cycle_anchor) VALUES('quota-user','quota@example.test','hash','ug_free',1234)`,
		`INSERT INTO channels(id,name,type) VALUES('quota-channel','Quota','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('quota-model','quota-channel','chat','quota-model','Quota')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('quota-model','ug_free',2419200,'count',1)`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('quota-model','ug_pro',2419200,'count',1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	freeQuota, _ := GetModelQuota(ctx, db, "quota-model", "ug_free")
	reservation, allowed, err := ReserveModelQuota(ctx, db, "quota-user", "quota-model", QuotaScopeModelChat, *freeQuota, 1, false)
	if err != nil || !allowed {
		t.Fatalf("reserve free quota = %v, %v", allowed, err)
	}
	if _, err := FinalizeQuotaReservation(ctx, db, reservation.ID, 1); err != nil {
		t.Fatalf("finalize free quota: %v", err)
	}

	before := time.Now().Unix()
	if err := SetUserGroup(ctx, db, "quota-user", "ug_pro", 0); err != nil {
		t.Fatalf("activate pro: %v", err)
	}
	scope, err := GetUserQuotaScope(ctx, db, "quota-user")
	if err != nil {
		t.Fatalf("get pro scope: %v", err)
	}
	if scope.Anchor < before || scope.GroupID != "ug_pro" {
		t.Fatalf("pro scope = %+v, want activation-anchored pro scope", scope)
	}
	proQuota, _ := GetModelQuota(ctx, db, "quota-model", "ug_pro")
	if _, allowed, err := ReserveModelQuota(ctx, db, "quota-user", "quota-model", QuotaScopeModelChat, *proQuota, 1, false); err != nil || !allowed {
		t.Fatalf("old-group usage consumed pro quota: allowed=%v err=%v", allowed, err)
	}
}

func TestDeletingUsageLogsDoesNotRestoreModelOrDailyQuota(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('quota-group','Quota',0)`,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES('quota-user','quota@example.test','hash','quota-group')`,
		`INSERT INTO channels(id,name,type) VALUES('quota-channel','Quota','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('quota-model','quota-channel','chat','quota-model','Quota')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('quota-model','quota-group',604800,'count',1)`,
		`INSERT INTO usage_logs(user_id,model_id,purpose) VALUES('quota-user','quota-model','chat')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	q, _ := GetModelQuota(ctx, db, "quota-model", "quota-group")
	reservation, allowed, err := ReserveModelQuota(ctx, db, "quota-user", "quota-model", QuotaScopeModelChat, *q, 1, false)
	if err != nil || !allowed {
		t.Fatalf("reserve model quota = %v, %v", allowed, err)
	}
	if _, err := FinalizeQuotaReservation(ctx, db, reservation.ID, 1); err != nil {
		t.Fatalf("finalize model quota: %v", err)
	}
	dayStart := time.Now().Truncate(24 * time.Hour).Unix()
	daily, allowed, err := ReserveFixedQuota(ctx, db, "quota-user", QuotaScopeDailyImage, 1, 1, dayStart, dayStart+86400)
	if err != nil || !allowed {
		t.Fatalf("reserve daily quota = %v, %v", allowed, err)
	}
	if _, err := FinalizeQuotaReservation(ctx, db, daily.ID, 1); err != nil {
		t.Fatalf("finalize daily quota: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id='quota-user'`); err != nil {
		t.Fatalf("delete usage logs: %v", err)
	}
	if _, allowed, err := ReserveModelQuota(ctx, db, "quota-user", "quota-model", QuotaScopeModelChat, *q, 1, false); err != nil || allowed {
		t.Fatalf("model quota after analytics deletion = allowed %v err %v, want blocked", allowed, err)
	}
	if _, allowed, err := ReserveFixedQuota(ctx, db, "quota-user", QuotaScopeDailyImage, 1, 1, dayStart, dayStart+86400); err != nil || allowed {
		t.Fatalf("daily quota after analytics deletion = allowed %v err %v, want blocked", allowed, err)
	}
}

func TestConcurrentModelQuotaReservationsAdmitOnlyLimit(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('quota-group','Quota',0)`,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES('quota-user','quota@example.test','hash','quota-group')`,
		`INSERT INTO channels(id,name,type) VALUES('quota-channel','Quota','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('quota-model','quota-channel','chat','quota-model','Quota')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('quota-model','quota-group',604800,'count',1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	q, _ := GetModelQuota(ctx, db, "quota-model", "quota-group")
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, allowed, err := ReserveModelQuota(context.Background(), db, "quota-user", "quota-model", QuotaScopeModelChat, *q, 1, false)
			results <- err == nil && allowed
		}()
	}
	wg.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 1 {
		t.Fatalf("concurrent quota allowed %d requests, want 1", allowedCount)
	}
}

func TestModelQuotaReservationRejectsStaleConfiguration(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('quota-group','Quota',0)`,
		`INSERT INTO users(id,email,password_hash,group_id) VALUES('quota-user','quota@example.test','hash','quota-group')`,
		`INSERT INTO channels(id,name,type) VALUES('quota-channel','Quota','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('quota-model','quota-channel','chat','quota-model','Quota')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('quota-model','quota-group',604800,'count',10)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	stale, err := GetModelQuota(ctx, db, "quota-model", "quota-group")
	if err != nil {
		t.Fatalf("read quota: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE model_group_quotas SET limit_value=1 WHERE model_id='quota-model' AND group_id='quota-group'`); err != nil {
		t.Fatalf("change quota: %v", err)
	}
	if _, _, err := ReserveModelQuota(ctx, db, "quota-user", "quota-model", QuotaScopeModelChat, *stale, 2, false); !errors.Is(err, ErrQuotaConfigChanged) {
		t.Fatalf("stale quota error = %v, want %v", err, ErrQuotaConfigChanged)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE user_id='quota-user'`).Scan(&rows); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if rows != 0 {
		t.Fatalf("stale quota created %d reservations, want 0", rows)
	}
}

func TestConcurrentDailyTokenReservationsCannotOversubscribe(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('token-user','token@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := SetSetting(db, "daily_token_limit", 100); err != nil {
		t.Fatalf("set token limit: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, allowed, err := ReserveDailyTokenQuota(context.Background(), db, "token-user", 60)
			results <- err == nil && allowed
		}()
	}
	wg.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 1 {
		t.Fatalf("concurrent token quota allowed %d requests, want 1", allowedCount)
	}
}

func TestDailyTokenQuotaBackfillsCurrentLegacyUsageOnce(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash) VALUES('legacy-token-user','legacy-token@example.test','hash')`,
		`INSERT INTO usage_logs(user_id,model_id,purpose,input_tokens,output_tokens,created_at) VALUES('legacy-token-user','m1','chat',30,20,strftime('%s','now'))`,
		`DELETE FROM settings WHERE key='daily_token_quota_ledger_backfill_v1'`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := migrateDailyTokenQuotaLedger(ctx, db); err != nil {
		t.Fatalf("backfill daily tokens: %v", err)
	}
	if err := migrateDailyTokenQuotaLedger(ctx, db); err != nil {
		t.Fatalf("repeat daily-token backfill: %v", err)
	}
	var used, rows int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(actual_micros),0),COUNT(*) FROM quota_ledger WHERE user_id='legacy-token-user' AND scope_type=?`,
		QuotaScopeDailyToken).Scan(&used, &rows); err != nil {
		t.Fatalf("read backfill: %v", err)
	}
	if used != 50*CreditMicrosPerUnit || rows != 1 {
		t.Fatalf("backfilled tokens = %d across %d rows, want %d across 1", used, rows, 50*CreditMicrosPerUnit)
	}
}

func TestFixedQuotaRejectsMicrosOverflow(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('overflow-user','overflow@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	overflow := int64(math.MaxInt64/CreditMicrosPerUnit + 1)
	if int64(int(overflow)) != overflow {
		t.Skip("int is too small to represent the overflow boundary")
	}
	_, _, err := ReserveFixedQuota(ctx, db, "overflow-user", QuotaScopeDailyToken, int(overflow), int(overflow), 1, 2)
	if !errors.Is(err, ErrInvalidCreditConfig) {
		t.Fatalf("overflow error = %v, want %v", err, ErrInvalidCreditConfig)
	}
}

func TestBillingTablesRejectInvalidLedgerRows(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('constraint-user','constraint@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	queries := []string{
		`INSERT INTO credit_reservations(id,user_id,amount_micros,status,expires_at) VALUES('bad-credit','constraint-user',-1,'reserved',1)`,
		`INSERT INTO quota_ledger(id,user_id,scope_type,window_start,limit_type,reserved_micros,status,expires_at) VALUES('bad-quota','constraint-user','daily_token',1,'count',-1,'reserved',2)`,
		`INSERT INTO billing_usage(id,user_id,input_tokens,currency) VALUES('bad-usage','constraint-user',-1,'USD')`,
	}
	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query); err == nil {
			t.Errorf("invalid ledger row unexpectedly accepted: %s", query)
		}
	}
}

func TestFinalizeQuotaReservationIsIdempotent(t *testing.T) {
	db, ctx := openCreditsTestDB(t)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('finalize-user','finalize@example.test','hash')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	reservation, allowed, err := ReserveFixedQuota(
		ctx, db, "finalize-user", QuotaScopeDailyToken, 5, 10, 1, time.Now().Unix()+3600,
	)
	if err != nil || !allowed {
		t.Fatalf("reserve quota = allowed %v err %v", allowed, err)
	}
	if overage, err := FinalizeQuotaReservation(ctx, db, reservation.ID, 7); err != nil || overage != 2 {
		t.Fatalf("first finalize = overage %v err %v, want 2", overage, err)
	}
	if overage, err := FinalizeQuotaReservation(ctx, db, reservation.ID, 9); err != nil || overage != 2 {
		t.Fatalf("repeat finalize = overage %v err %v, want stored 2", overage, err)
	}
}
