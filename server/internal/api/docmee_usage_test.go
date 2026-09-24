package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aivory/server/internal/store"
)

// Reported bug: an AI PPT generation charged credits but never appeared in
// Admin → Usage & billing.
//
// Root cause: settling a credit reservation writes credit_ledger only, while the
// usage page reads usage_logs (and the billing totals read usage_stats, which the
// database mirrors from usage_logs). Neither table was ever written, so a charged
// deck was invisible in reporting even though the credits really moved.
//
// These tests pin the row that closes the gap, the roll-up the billing summary
// renders, and the properties they must keep: one row per deck, credits that
// match what the ledger actually took, and no row at all when nothing was billed.
//
// They drive the API-mode flow (create deck → render), which is where the charge
// and the usage write now live.

func TestAiPPTChargeRecordsUsageForBillingPage(t *testing.T) {
	fixture := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, fixture, fixture.user)

	rendered := aipptRender(t, fixture, deckID)
	if rendered.Credits != 10 {
		t.Fatalf("render = %+v, want 10 credits", rendered)
	}
	if !rendered.UsageRecorded {
		t.Fatal("render reported usage_recorded=false; the deck would be invisible in usage reporting")
	}

	rows := docmeeUsageRows(t, fixture.db)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1 for one billed deck", len(rows))
	}
	if rows[0].Purpose != "ppt" {
		t.Fatalf("purpose = %q, want ppt", rows[0].Purpose)
	}
	if rows[0].UserID != "u1" {
		t.Fatalf("row user = %q, want u1", rows[0].UserID)
	}
	if got := docmeeUsageCredits(t, fixture.db); got != 10 {
		t.Fatalf("billing-summary credits = %v, want 10", got)
	}
	// The row must identify the deck it paid for: the admin usage page renders
	// these fields as the AI PPT call detail.
	if rows[0].AiPPT == nil {
		t.Fatal("ppt row carries no AI PPT detail")
	}
	if rows[0].AiPPT.Event != store.AiPPTUsageEventGenerate {
		t.Fatalf("event = %q, want %q", rows[0].AiPPT.Event, store.AiPPTUsageEventGenerate)
	}
	if rows[0].AiPPT.PptID != fixture.stub.pptID {
		t.Fatalf("ppt id = %q, want the rendered deck's %q", rows[0].AiPPT.PptID, fixture.stub.pptID)
	}
	if rows[0].AiPPT.Subject == "" || rows[0].AiPPT.DeckID == "" {
		t.Fatalf("detail = %+v, want the deck's subject and record id", rows[0].AiPPT)
	}

	// Re-rendering the same deck is already paid for: no second debit, and no
	// second usage row.
	replay := aipptRender(t, fixture, deckID)
	if replay.Credits != 10 || replay.CreditsAvailable != 15 {
		t.Fatalf("replay = %+v, want the original 10-credit charge and an unchanged balance", replay)
	}
	if got := len(docmeeUsageRows(t, fixture.db)); got != 1 {
		t.Fatalf("usage rows after replay = %d, want still 1", got)
	}
	if got := docmeeUsageCredits(t, fixture.db); got != 10 {
		t.Fatalf("billing-summary credits after replay = %v, want 10", got)
	}
}

// Two different decks are two billable units and must both show up.
func TestAiPPTTwoDecksRecordTwoUsageRows(t *testing.T) {
	fixture := aipptTestDeps(t, 40, nil)

	first := createDeck(t, fixture, fixture.user)
	aipptRender(t, fixture, first)
	// The second deck must be a DIFFERENT upstream deck, so give the stub a new id.
	fixture.stub.pptID = "ppt_stub_2"
	second := createDeck(t, fixture, fixture.user)
	aipptRender(t, fixture, second)

	if got := len(docmeeUsageRows(t, fixture.db)); got != 2 {
		t.Fatalf("usage rows = %d, want 2 for two billed decks", got)
	}
	if got := docmeeUsageCredits(t, fixture.db); got != 20 {
		t.Fatalf("billing-summary credits = %v, want 20", got)
	}
}

// A failed-then-retried write must be repairable: if the original usage write
// never landed, a later render of the same deck has to backfill it rather than
// leave the deck permanently missing from reporting.
func TestAiPPTReRenderBackfillsAMissingUsageRow(t *testing.T) {
	fixture := aipptTestDeps(t, 25, nil)
	deckID := createDeck(t, fixture, fixture.user)
	aipptRender(t, fixture, deckID)

	// Simulate the lost write (the process died between the debit and the insert).
	if _, err := fixture.db.ExecContext(context.Background(), `DELETE FROM usage_logs WHERE purpose='ppt'`); err != nil {
		t.Fatalf("delete usage row: %v", err)
	}
	if got := len(docmeeUsageRows(t, fixture.db)); got != 0 {
		t.Fatalf("precondition: rows = %d, want 0", got)
	}

	backfill := aipptRender(t, fixture, deckID)
	if !backfill.UsageRecorded {
		t.Fatalf("re-render = %+v, want usage_recorded", backfill)
	}
	if got := len(docmeeUsageRows(t, fixture.db)); got != 1 {
		t.Fatalf("usage rows after backfill = %d, want 1", got)
	}
	// The backfilled row must carry the amount the ledger actually took. (The
	// usage_stats roll-up is deliberately not asserted here: this test removes
	// only the log row, which is not a state the app can reach on its own.)
	var logged float64
	if err := fixture.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(credits),0) FROM usage_logs WHERE purpose='ppt'`).Scan(&logged); err != nil {
		t.Fatalf("sum usage credits: %v", err)
	}
	if logged != 10 {
		t.Fatalf("backfilled usage credits = %v, want 10", logged)
	}
}

// A generation that was never charged (billing off platform-wide, or a price of
// 0) is still a CALL the admin must be able to see. The row is written with
// credits=0, so it shows up in the report without moving any money.
func TestAiPPTUnbilledGenerationStillRecordsTheCall(t *testing.T) {
	// Credits-per-deck 0 disables billing entirely.
	fixture := aipptTestDeps(t, 25, map[string]any{"docmee_credits_per_ppt": 0})
	deckID := createDeck(t, fixture, fixture.user)

	rendered := aipptRender(t, fixture, deckID)
	if rendered.Credits != 0 {
		t.Fatalf("render = %+v, want 0 credits when billing is off", rendered)
	}
	rows := docmeeUsageRows(t, fixture.db)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want 1 for an unbilled generation", len(rows))
	}
	if rows[0].Credits != 0 {
		t.Fatalf("row credits = %v, want 0 — the row reports the call, not a charge", rows[0].Credits)
	}
	if got := docmeeUsageCredits(t, fixture.db); got != 0 {
		t.Fatalf("billing-summary credits = %v, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// helpers

type docmeeChargeResult struct {
	Credits          float64 `json:"credits"`
	AlreadyCharged   bool    `json:"already_charged"`
	CreditsAvailable float64 `json:"credits_available"`
	UsageRecorded    bool    `json:"usage_recorded"`
}

// aipptRender runs the render endpoint (the step that charges) and returns the
// billing/usage outcome.
func aipptRender(t *testing.T, fixture aipptFixture, deckID string) docmeeChargeResult {
	t.Helper()
	rec, req := aipptReq(t, fixture, fixture.user, http.MethodPost, "/api/me/ppt/decks/"+deckID+"/pptx",
		map[string]any{"template_id": "tpl_stub_1", "markdown": "# 主题\n## 章节\n### 页面"})
	meAiPPTGenerateHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("render status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out docmeeChargeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode render: %v", err)
	}
	return out
}

// docmeeUsageRows returns the AI-PPT rows the admin usage table would list.
func docmeeUsageRows(t *testing.T, db *sql.DB) []store.AdminUsageRecord {
	t.Helper()
	rows, err := store.AdminUsageRecords(context.Background(), db, store.UsageFilter{ModelID: ""}, 50, 0)
	if err != nil {
		t.Fatalf("AdminUsageRecords: %v", err)
	}
	out := make([]store.AdminUsageRecord, 0, len(rows))
	for _, row := range rows {
		if row.Purpose == "ppt" {
			out = append(out, row)
		}
	}
	return out
}

// docmeeUsageCredits reads the credit column of the billing summary — the number
// the "Usage & billing" totals box renders, sourced from usage_stats.
func docmeeUsageCredits(t *testing.T, db *sql.DB) float64 {
	t.Helper()
	now := time.Now().Unix() + 60
	totals, err := store.AdminUsageTotalsBetween(context.Background(), db, 0, now)
	if err != nil {
		t.Fatalf("AdminUsageTotalsBetween: %v", err)
	}
	return totals.Credits
}
