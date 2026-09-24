package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The memo written into usage_logs.message_id is the only thing that ties an AI
// PPT usage row back to its deck, so the format is pinned for both writers and
// for the reader that resolves the deck.
func TestAiPPTUsageMemoRoundTrip(t *testing.T) {
	gen, ok := parseAiPPTUsageMemo(AiPPTGenerateUsageMemo("ppt_abc"))
	if !ok || !gen.byPPT || gen.pptID != "ppt_abc" || gen.event != AiPPTUsageEventGenerate {
		t.Fatalf("generate memo parsed as %+v (ok=%t)", gen, ok)
	}
	edit, ok := parseAiPPTUsageMemo(AiPPTEditUsageMemo(AiPPTUsageEventTemplate, "ppt_deck", "att_9"))
	if !ok || edit.byPPT || edit.deckID != "ppt_deck" || edit.event != AiPPTUsageEventTemplate {
		t.Fatalf("edit memo parsed as %+v (ok=%t)", edit, ok)
	}
	for _, memo := range []string{"", "ppt:", "ppt-edit:", "ppt-edit:rewrite:", "ppt-edit::att", "msg_1"} {
		if _, ok := parseAiPPTUsageMemo(memo); ok {
			t.Fatalf("memo %q must not parse as an AI PPT usage key", memo)
		}
	}
}

// AdminUsageRecords attaches the deck behind every "ppt" row: a generation is
// resolved through the vendor id, a charged edit through our own deck id, and a
// row whose deck was deleted keeps its event (and the vendor id from the memo)
// instead of losing the charge. Non-PPT rows keep no AI PPT detail at all.
func TestAdminUsageRecordsAttachesAiPPTDeckDetail(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "aippt-usage.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@x.com','h','A','user')`)

	deck, err := CreateAiPPTDeck(ctx, db, AiPPTDeck{
		UserID: "u1", PptID: "ppt_vendor_1", Subject: "季度复盘",
		TemplateName: "极简", Status: AiPPTDeckReady,
	})
	if err != nil {
		t.Fatalf("create deck: %v", err)
	}

	ins := `INSERT INTO usage_logs(user_id,model_id,message_id,purpose,credits,created_at) VALUES(?,'',?,?,?,?)`
	exec(t, db, ins, "u1", AiPPTGenerateUsageMemo("ppt_vendor_1"), AiPPTUsagePurpose, 10.0, 1000)
	exec(t, db, ins, "u1", AiPPTEditUsageMemo(AiPPTUsageEventRewrite, deck.ID, "att_1"), AiPPTUsagePurpose, 2.0, 2000)
	exec(t, db, ins, "u1", AiPPTGenerateUsageMemo("ppt_vendor_gone"), AiPPTUsagePurpose, 5.0, 3000)
	exec(t, db, `INSERT INTO usage_logs(user_id,model_id,purpose,created_at) VALUES('u1','m1','chat',500)`)

	rows, err := AdminUsageRecords(ctx, db, UsageFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("AdminUsageRecords: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	// Newest first: deleted-deck generation, edit, generation, chat.
	gone, edit, gen, chat := rows[0], rows[1], rows[2], rows[3]

	if gen.AiPPT == nil {
		t.Fatal("generation row carries no AI PPT detail")
	}
	if gen.AiPPT.Event != AiPPTUsageEventGenerate || gen.AiPPT.PptID != "ppt_vendor_1" ||
		gen.AiPPT.DeckID != deck.ID || gen.AiPPT.Subject != "季度复盘" ||
		gen.AiPPT.TemplateName != "极简" || gen.AiPPT.Status != AiPPTDeckReady {
		t.Fatalf("generation detail = %+v", gen.AiPPT)
	}
	if gen.Credits != 10 {
		t.Fatalf("generation credits = %v, want 10", gen.Credits)
	}

	if edit.AiPPT == nil || edit.AiPPT.Event != AiPPTUsageEventRewrite || edit.AiPPT.DeckID != deck.ID {
		t.Fatalf("edit detail = %+v, want the deck resolved through its own id", edit.AiPPT)
	}
	if edit.AiPPT.Subject != "季度复盘" {
		t.Fatalf("edit subject = %q, want the deck's subject", edit.AiPPT.Subject)
	}

	if gone.AiPPT == nil || gone.AiPPT.Event != AiPPTUsageEventGenerate ||
		gone.AiPPT.PptID != "ppt_vendor_gone" || gone.AiPPT.DeckID != "" {
		t.Fatalf("deleted-deck detail = %+v, want the memo's vendor id and no deck fields", gone.AiPPT)
	}
	if gone.Credits != 5 {
		t.Fatalf("deleted-deck credits = %v, want the charge to survive the deck", gone.Credits)
	}

	if chat.AiPPT != nil {
		t.Fatalf("chat row grew an AI PPT detail: %+v", chat.AiPPT)
	}
}
