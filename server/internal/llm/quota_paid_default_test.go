package llm

import (
	"context"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestModelWithoutAnyFreeGrantUsesCredits(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "quota-paid-default.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_paid_default','Paid Default',0)`,
		`INSERT INTO users(id,email,password_hash,role,group_id,credits_permanent) VALUES('u_paid_default','paid-default@example.test','hash','user','ug_paid_default',10)`,
		`INSERT INTO channels(id,name,type) VALUES('ch_paid_default','Paid Default','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,price_input,price_output) VALUES('m_paid_default','ch_paid_default','chat','paid-default','Paid Default',1,1)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := store.SetSetting(db, "credits_per_usd", 10.0); err != nil {
		t.Fatalf("set credits rate: %v", err)
	}

	ctx := context.Background()
	orchestrator := &Orchestrator{db: db}
	model := &store.Model{ID: "m_paid_default", PriceInput: 1, PriceOutput: 1}
	if err := store.SetPermanentCredits(ctx, db, "u_paid_default", 0); err != nil {
		t.Fatalf("clear credits: %v", err)
	}
	if msg, ok, payCredits, _ := orchestrator.checkModelQuota(ctx, "u_paid_default", model); msg == "" || ok || payCredits {
		t.Fatalf("no free grant without credits = (%q,%v,%v), want blocked", msg, ok, payCredits)
	}

	if err := store.SetPermanentCredits(ctx, db, "u_paid_default", 10); err != nil {
		t.Fatalf("restore credits: %v", err)
	}
	msg, ok, payCredits, remaining := orchestrator.checkModelQuota(ctx, "u_paid_default", model)
	if msg != "" || !ok || !payCredits || remaining != -1 {
		t.Fatalf("checkModelQuota = (%q,%v,%v,%v), want admitted credit-paid turn", msg, ok, payCredits, remaining)
	}
}

func TestExplicitUnlimitedGrantRemainsFree(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "quota-explicit-free.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_explicit_free','Explicit Free',0)`,
		`INSERT INTO users(id,email,password_hash,role,group_id) VALUES('u_explicit_free','explicit-free@example.test','hash','user','ug_explicit_free')`,
		`INSERT INTO channels(id,name,type) VALUES('ch_explicit_free','Explicit Free','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m_explicit_free','ch_explicit_free','chat','explicit-free','Explicit Free')`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_explicit_free','ug_explicit_free',604800,'count',0)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	orchestrator := &Orchestrator{db: db}
	model := &store.Model{ID: "m_explicit_free"}
	msg, ok, payCredits, remaining := orchestrator.checkModelQuota(context.Background(), "u_explicit_free", model)
	if msg != "" || !ok || payCredits || remaining != -1 {
		t.Fatalf("checkModelQuota = (%q,%v,%v,%v), want explicit unlimited free grant", msg, ok, payCredits, remaining)
	}
}

func TestChargeTurnCreditsUsesAuthoritativeLedger(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "quota-credit-ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default,credit_allowance,credit_period_seconds) VALUES('ug_ledger','Ledger',0,5,604800)`,
		`INSERT INTO users(id,email,password_hash,role,group_id,credits_permanent) VALUES('u_ledger','ledger@example.test','hash','user','ug_ledger',4)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := store.SetSetting(db, "credits_per_usd", 10.0); err != nil {
		t.Fatalf("set credits rate: %v", err)
	}

	orchestrator := &Orchestrator{db: db}
	timed, total := orchestrator.chargeTurnCredits(context.Background(), "u_ledger", 0.7)
	if timed != 5 || total != 7 {
		t.Fatalf("charge = timed %.2f total %.2f, want 5/7", timed, total)
	}
	balance, err := store.GetCreditBalance(context.Background(), db, "u_ledger")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.TimedRemaining != 0 || balance.Permanent != 2 || balance.Available != 2 {
		t.Fatalf("balance after charge = %+v, want timed 0 permanent/available 2", balance)
	}
}
