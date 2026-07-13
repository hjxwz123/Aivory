package llm

import (
	"bytes"
	"net/http"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func mustHTTPRequest(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return req
}

// §B5 request logging: successRequestLoggingEnabled gates whether SUCCESS usage
// rows carry the full provider request (error rows always carry it — that path
// reads the recorder's `last` snapshot directly and is not settings-gated).
func TestSuccessRequestLoggingGating(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "reqlog.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	o := &Orchestrator{db: db}

	set := func(key string, v bool) {
		if err := store.SetSetting(db, key, v); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// Default (both unset): master off → success rows NOT captured.
	if o.successRequestLoggingEnabled() {
		t.Fatal("default: success rows should not capture the request body")
	}
	// Master on, errors-only defaults true (unset) → still errors-only.
	set("log_full_requests", true)
	if o.successRequestLoggingEnabled() {
		t.Fatal("master on + errors-only default(true): success rows should not capture")
	}
	// Master on, errors-only explicitly true → errors-only.
	set("log_errors_only", true)
	if o.successRequestLoggingEnabled() {
		t.Fatal("master on + errors-only true: success rows should not capture")
	}
	// Master on, errors-only off → capture EVERY request.
	set("log_errors_only", false)
	if !o.successRequestLoggingEnabled() {
		t.Fatal("master on + errors-only off: success rows must capture the full request body")
	}
	// Master back off → errors-only regardless of the child value.
	set("log_full_requests", false)
	if o.successRequestLoggingEnabled() {
		t.Fatal("master off: success rows should not capture even with errors-only off")
	}
}

// §B5-per-request usage rows: a multi-request turn splits into one row per
// upstream request with EXACT total preservation (tokens, cost, credits), while
// a single-request turn keeps the old single aggregated row.
func TestPerRequestUsageRowsSplitAndTotals(t *testing.T) {
	model := &store.Model{PriceInput: 10, PriceOutput: 30} // $/1M
	snaps := []providerRequestSnapshot{
		{Method: "POST", URL: "https://api/1", Body: "b1", Attempt: 1,
			Usage: Usage{InputTokens: 1000, OutputTokens: 200}, HasUsage: true},
		{Method: "POST", URL: "https://api/2", Body: "b2", Attempt: 2,
			Usage: Usage{InputTokens: 3000, OutputTokens: 500}, HasUsage: true},
		// A request that never completed a stream (e.g. failed then fell back):
		// no usage attached → no row of its own.
		{Method: "POST", URL: "https://api/3", Body: "b3", Attempt: 3},
	}
	// Turn totals carry MORE than the two attaches (residual 500/100 from an
	// overflow/fallback round) — the last row must absorb it.
	total := Usage{InputTokens: 4500, OutputTokens: 800}
	totalCost := computeCost(*model, total)
	totalCredits := 12.0

	rows := perRequestUsageRows(snaps, model, total, totalCost, totalCredits, true)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (only requests with usage)", len(rows))
	}
	if rows[0].Usage.InputTokens != 1000 || rows[0].Usage.OutputTokens != 200 {
		t.Fatalf("row0 usage = %+v", rows[0].Usage)
	}
	// Row 1 = its own 3000/500 + residual 500/100.
	if rows[1].Usage.InputTokens != 3500 || rows[1].Usage.OutputTokens != 600 {
		t.Fatalf("row1 usage (with residual) = %+v", rows[1].Usage)
	}
	if got := rows[0].Cost + rows[1].Cost; got != totalCost {
		t.Fatalf("cost sum %v != turn cost %v", got, totalCost)
	}
	if got := rows[0].Credits + rows[1].Credits; got != totalCredits {
		t.Fatalf("credits sum %v != turn credits %v", got, totalCredits)
	}
	if rows[0].Credits <= 0 || rows[1].Credits <= 0 {
		t.Fatalf("both rows of a credit-paid turn must carry credits: %+v", rows)
	}
	// Request snapshots ride along when includeReq is true.
	if rows[0].Body != "b1" || rows[1].Body != "b2" {
		t.Fatalf("request bodies not carried per row: %q %q", rows[0].Body, rows[1].Body)
	}

	// includeReq=false → no request fields on any row.
	for _, r := range perRequestUsageRows(snaps, model, total, totalCost, 0, false) {
		if r.Method != "" || r.Body != "" {
			t.Fatalf("includeReq=false must blank request fields: %+v", r)
		}
	}

	// Single completed request → one aggregated row (old behavior).
	single := perRequestUsageRows(snaps[:1], model, total, totalCost, totalCredits, true)
	if len(single) != 1 || single[0].Usage != total || single[0].Cost != totalCost || single[0].Credits != totalCredits {
		t.Fatalf("single-request turn must stay one aggregated row: %+v", single)
	}
}

// The recorder keeps every request in order, attaches usage to the request that
// produced it, and `last` still serves the error path with the full snapshot
// even when success-body capture is off.
func TestProviderRequestRecorderPerRequestList(t *testing.T) {
	rec := newProviderRequestRecorder() // captureAll off
	r1 := mustHTTPRequest(t, "https://api.example/v1/messages", `{"round":1}`)
	rec.record(r1)
	rec.attachUsage(Usage{InputTokens: 10, OutputTokens: 2})
	r2 := mustHTTPRequest(t, "https://api.example/v1/messages", `{"round":2}`)
	rec.record(r2)
	rec.attachUsage(Usage{InputTokens: 20, OutputTokens: 4})

	snaps := rec.snapshots()
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	if !snaps[0].HasUsage || snaps[0].Usage.InputTokens != 10 || snaps[1].Usage.InputTokens != 20 {
		t.Fatalf("usage not pinned per request: %+v", snaps)
	}
	// captureAll off → list entries hold no body; `last` keeps the full one.
	if snaps[1].Body != "" {
		t.Fatalf("captureAll=false must not keep bodies in the list: %q", snaps[1].Body)
	}
	if rec.snapshot().Body == "" {
		t.Fatal("last snapshot must always keep the full body for the error row")
	}

	// captureAll on → bodies kept per entry.
	rec2 := newProviderRequestRecorder()
	rec2.captureAll = true
	rec2.record(mustHTTPRequest(t, "https://api.example/v1/x", `{"keep":"me"}`))
	if s := rec2.snapshots(); len(s) != 1 || s[0].Body == "" {
		t.Fatalf("captureAll=true must keep the body in the list: %+v", s)
	}
}
