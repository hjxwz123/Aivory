package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func TestModelUsesCreditsWhenGroupHasNoFreeGrant(t *testing.T) {
	model := store.Model{ID: "m_paid"}
	if !modelUsesCredits(context.Background(), Deps{}, "u_paid", model, map[string]store.ModelGroupQuota{}) {
		t.Fatal("model without a free grant must be shown as credit-paid")
	}
}

func TestModelUsesCreditsReadsMembershipAnchoredQuotaLedger(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "model-credit-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_groups(id,name,is_default) VALUES(?,?,1)`, store.DefaultGroupID, "Free"); err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().Unix() - 3600
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash,role,group_id,quota_cycle_anchor) VALUES(?,?,?,?,?,?)`,
		"u_metered", "metered@example.com", "h", "user", store.DefaultGroupID, anchor); err != nil {
		t.Fatal(err)
	}
	model := store.Model{ID: "m_metered", Kind: "chat"}
	quota := store.ModelGroupQuota{
		ModelID: model.ID, GroupID: store.DefaultGroupID, PeriodSeconds: 4 * 7 * 24 * 60 * 60,
		LimitType: "count", LimitValue: 1,
	}
	for _, query := range []string{
		`INSERT INTO channels(id,name,type) VALUES('metered-channel','Metered','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m_metered','metered-channel','chat','m_metered','Metered')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_metered','ug_free',2419200,'count',1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	grants := map[string]store.ModelGroupQuota{model.ID: quota}
	if modelUsesCredits(ctx, Deps{DB: db}, "u_metered", model, grants) {
		t.Fatal("unused membership-anchored allowance must be shown as free")
	}
	reservation, allowed, err := store.ReserveModelQuota(
		ctx, db, "u_metered", model.ID, store.QuotaScopeModelChat, quota, 1, false,
	)
	if err != nil || !allowed {
		t.Fatalf("reserve quota: allowed=%v err=%v", allowed, err)
	}
	if _, err := store.FinalizeQuotaReservation(ctx, db, reservation.ID, 1); err != nil {
		t.Fatal(err)
	}
	if !modelUsesCredits(ctx, Deps{DB: db}, "u_metered", model, grants) {
		t.Fatal("exhausted authoritative allowance must be shown as credit-paid")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id=?`, "u_metered"); err != nil {
		t.Fatal(err)
	}
	if !modelUsesCredits(ctx, Deps{DB: db}, "u_metered", model, grants) {
		t.Fatal("deleting analytics must not make an exhausted model look free")
	}
}

func TestModelWithExplicitUnlimitedGrantDoesNotUseCredits(t *testing.T) {
	model := store.Model{ID: "m_free"}
	grants := map[string]store.ModelGroupQuota{
		model.ID: {ModelID: model.ID, GroupID: "ug_free", LimitValue: 0},
	}
	if modelUsesCredits(context.Background(), Deps{}, "u_free", model, grants) {
		t.Fatal("explicit unlimited free grant must not be shown as credit-paid")
	}
}
