package api

import (
	"context"
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
// These tests pin the row that closes the gap, the roll-up that the billing
// summary renders, and the two properties they must keep: one row per deck, and
// credits that match what the ledger actually took.
func TestDocmeeChargeRecordsUsageForBillingPage(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))

	attempt := docmeeOpenAttempt(t, fixture)
	charge := docmeeCharge(t, fixture, attempt, "deck-usage-1")
	if charge.Credits != 10 {
		t.Fatalf("charge = %+v, want 10 credits", charge)
	}
	if !charge.UsageRecorded {
		t.Fatal("charge reported usage_recorded=false; the deck would be invisible in usage reporting")
	}

	// The row the admin usage table renders.
	rows := docmeeUsageRows(t, fixture)
	if len(rows) != 1 {
		t.Fatalf("usage rows = %d, want exactly 1 for one billed deck", len(rows))
	}
	if rows[0].Purpose != "ppt" {
		t.Fatalf("purpose = %q, want %q", rows[0].Purpose, "ppt")
	}
	if rows[0].UserID != "u1" {
		t.Fatalf("row user = %q, want u1", rows[0].UserID)
	}

	// The roll-up the billing summary renders must now account for the credits.
	if got := docmeeUsageCredits(t, fixture); got != 10 {
		t.Fatalf("billing-summary credits = %v, want 10", got)
	}

	// Replaying the charge (an SDK retry, or a reloaded page re-reporting the
	// same deck) must not add a second row or double the roll-up.
	replay := docmeeCharge(t, fixture, attempt, "deck-usage-1")
	if !replay.AlreadyCharged {
		t.Fatalf("replay = %+v, want already_charged", replay)
	}
	if got := len(docmeeUsageRows(t, fixture)); got != 1 {
		t.Fatalf("usage rows after replay = %d, want still 1 (a replay must not double-count)", got)
	}
	if got := docmeeUsageCredits(t, fixture); got != 10 {
		t.Fatalf("billing-summary credits after replay = %v, want 10", got)
	}
}

// A page reload opens a fresh attempt and reports the SAME deck id. The ledger
// already bills that deck once; usage reporting must agree.
func TestDocmeeReopenedAttemptDoesNotDuplicateUsage(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))

	first := docmeeOpenAttempt(t, fixture)
	docmeeCharge(t, fixture, first, "deck-shared")

	second := docmeeOpenAttempt(t, fixture)
	if second == first {
		t.Fatal("second attempt reused the first attempt id")
	}
	docmeeCharge(t, fixture, second, "deck-shared")

	if got := len(docmeeUsageRows(t, fixture)); got != 1 {
		t.Fatalf("usage rows = %d, want 1 for one deck billed from two attempts", got)
	}
	if got := docmeeUsageCredits(t, fixture); got != 10 {
		t.Fatalf("billing-summary credits = %v, want 10", got)
	}
}

// Two different decks are two billable units and must both show up.
func TestDocmeeTwoDecksRecordTwoUsageRows(t *testing.T) {
	fixture := docmeeTestDeps(t, 40, docmeeBillingSettings(nil))

	docmeeCharge(t, fixture, docmeeOpenAttempt(t, fixture), "deck-a")
	docmeeCharge(t, fixture, docmeeOpenAttempt(t, fixture), "deck-b")

	rows := docmeeUsageRows(t, fixture)
	if len(rows) != 2 {
		t.Fatalf("usage rows = %d, want 2 for two billed decks", len(rows))
	}
	if got := docmeeUsageCredits(t, fixture); got != 20 {
		t.Fatalf("billing-summary credits = %v, want 20", got)
	}
}

// A failed-then-retried write must be repairable: if the original usage write
// never landed, a later replay of the same charge has to backfill it rather than
// leave the deck permanently missing from reporting.
func TestDocmeeReplayBackfillsAMissingUsageRow(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))
	ctx := context.Background()

	attempt := docmeeOpenAttempt(t, fixture)
	docmeeCharge(t, fixture, attempt, "deck-backfill")

	// Simulate the lost write (e.g. the process died between the debit and the
	// insert): the ledger has it, usage reporting does not.
	if _, err := fixture.db.ExecContext(ctx, `DELETE FROM usage_logs WHERE purpose='ppt'`); err != nil {
		t.Fatalf("delete usage row: %v", err)
	}
	if got := len(docmeeUsageRows(t, fixture)); got != 0 {
		t.Fatalf("precondition: rows = %d, want 0", got)
	}

	replay := docmeeCharge(t, fixture, attempt, "deck-backfill")
	if !replay.AlreadyCharged || !replay.UsageRecorded {
		t.Fatalf("replay = %+v, want already_charged with usage_recorded", replay)
	}
	if got := len(docmeeUsageRows(t, fixture)); got != 1 {
		t.Fatalf("usage rows after backfill = %d, want 1", got)
	}
}

// A generation that was never charged (billing off) must not fabricate a usage
// row — the report has to stay a record of money that actually moved.
func TestDocmeeUnbilledChargeRecordsNoUsage(t *testing.T) {
	// Credits-per-deck 0 disables billing entirely.
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(map[string]any{"docmee_credits_per_ppt": 0}))

	attempt := docmeeOpenAttempt(t, fixture)
	charge := docmeeCharge(t, fixture, attempt, "deck-free")

	if charge.Credits != 0 {
		t.Fatalf("charge = %+v, want 0 credits when billing is off", charge)
	}
	if got := len(docmeeUsageRows(t, fixture)); got != 0 {
		t.Fatalf("usage rows = %d, want 0 for an unbilled generation", got)
	}
	if got := docmeeUsageCredits(t, fixture); got != 0 {
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

func docmeeOpenAttempt(t *testing.T, fixture docmeeFixture) string {
	t.Helper()
	rec, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	if body.AttemptID == "" {
		t.Fatal("attempt returned no id")
	}
	return body.AttemptID
}

func docmeeCharge(t *testing.T, fixture docmeeFixture, attemptID, pptID string) docmeeChargeResult {
	t.Helper()
	rec, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": attemptID, "ppt_id": pptID})
	meDocmeeChargeHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("charge status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out docmeeChargeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode charge: %v", err)
	}
	return out
}

// docmeeUsageRows returns the AI-PPT rows the admin usage table would list.
func docmeeUsageRows(t *testing.T, fixture docmeeFixture) []store.AdminUsageRecord {
	t.Helper()
	rows, err := store.AdminUsageRecords(context.Background(), fixture.db,
		store.UsageFilter{ModelID: ""}, 50, 0)
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
func docmeeUsageCredits(t *testing.T, fixture docmeeFixture) float64 {
	t.Helper()
	now := time.Now().Unix() + 60
	totals, err := store.AdminUsageTotalsBetween(context.Background(), fixture.db, 0, now)
	if err != nil {
		t.Fatalf("AdminUsageTotalsBetween: %v", err)
	}
	return totals.Credits
}
