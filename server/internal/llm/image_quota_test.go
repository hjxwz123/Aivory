package llm

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func setupImageQuotaTest(t *testing.T) (*Orchestrator, *store.Model, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "image-quota.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO user_groups(id,name,is_default) VALUES('ug_images','Images',0)`,
		`INSERT INTO users(id,email,password_hash,role,group_id,credits_permanent) VALUES('u_images','images@example.test','hash','user','ug_images',1)`,
		`INSERT INTO channels(id,name,type) VALUES('ch_images','Images','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,price_per_image) VALUES('m_images','ch_images','image','image-test','Image Test',0.5)`,
		`INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_images','ug_images',604800,'cost',0.01)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if err := store.SetSetting(db, "credits_per_usd", 10.0); err != nil {
		t.Fatalf("set credits rate: %v", err)
	}
	return &Orchestrator{db: db}, &store.Model{ID: "m_images", PricePerImage: 0.5}, db
}

func TestImageQuotaRequiresCreditsForWholeClampedBatch(t *testing.T) {
	ctx := context.Background()
	orchestrator, model, db := setupImageQuotaTest(t)

	// Preserve chat's existing decision semantics: any positive balance admits a
	// turn to the later chat preflight. The stricter exact-cost check is image-only.
	if msg, ok, payCredits := orchestrator.creditDecision(ctx, "u_images", "ug_images"); msg != "" || !ok || !payCredits {
		t.Fatalf("chat creditDecision = (%q,%v,%v), want admitted credit turn", msg, ok, payCredits)
	}

	// Three images cost 3 * $0.50 * 10 = 15 credits. A positive balance of one
	// credit must not pass merely because it is non-zero.
	if msg, ok, payCredits := orchestrator.checkImageQuota(ctx, "u_images", model, 3); msg == "" || ok || payCredits {
		t.Fatalf("insufficient image credits = (%q,%v,%v), want blocked", msg, ok, payCredits)
	}

	if err := store.SetPermanentCredits(ctx, db, "u_images", 15); err != nil {
		t.Fatalf("set exact credits: %v", err)
	}
	if msg, ok, payCredits := orchestrator.checkImageQuota(ctx, "u_images", model, 3); msg != "" || !ok || !payCredits {
		t.Fatalf("exact image credits = (%q,%v,%v), want admitted credit turn", msg, ok, payCredits)
	}

	clamped := ClampImageGenerationCount(999)
	required := float64(clamped) * model.PricePerImage * orchestrator.creditsPerUSD()
	if err := store.SetPermanentCredits(ctx, db, "u_images", required); err != nil {
		t.Fatalf("set clamped-batch credits: %v", err)
	}
	if msg, ok, payCredits := orchestrator.checkImageQuota(ctx, "u_images", model, 999); msg != "" || !ok || !payCredits {
		t.Fatalf("clamped image credits = (%q,%v,%v), want admitted credit turn for n=%d", msg, ok, payCredits, clamped)
	}
}
