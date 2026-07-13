package llm

import (
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

// §B5 request logging: requestSnapshotFor gates whether a SUCCESS usage row
// carries the full provider request. Error rows always carry it; success rows
// only when log_full_requests is on AND log_errors_only is off.
func TestRequestSnapshotForGating(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "reqlog.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	o := &Orchestrator{db: db}

	rec := newProviderRequestRecorder()
	rec.last = providerRequestSnapshot{Method: "POST", URL: "https://api/x", Header: "h", Body: `{"model":"m"}`}

	captured := func() bool {
		_, _, _, body := o.requestSnapshotFor(rec, false)
		return body != ""
	}
	set := func(key string, v bool) {
		if err := store.SetSetting(db, key, v); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	// Default (both unset): master off → success rows NOT captured.
	if captured() {
		t.Fatal("default: success row should not capture the request body")
	}
	// Error rows ALWAYS captured, even with master off.
	if _, _, _, b := o.requestSnapshotFor(rec, true); b == "" {
		t.Fatal("error row must always capture the request body")
	}

	// Master on, errors-only defaults true (unset) → still errors-only.
	set("log_full_requests", true)
	if captured() {
		t.Fatal("master on + errors-only default(true): success row should not capture")
	}
	// Master on, errors-only explicitly true → errors-only.
	set("log_errors_only", true)
	if captured() {
		t.Fatal("master on + errors-only true: success row should not capture")
	}
	// Master on, errors-only off → capture EVERY request.
	set("log_errors_only", false)
	if !captured() {
		t.Fatal("master on + errors-only off: success row must capture the full request body")
	}
	// Master back off → errors-only regardless of the child value.
	set("log_full_requests", false)
	if captured() {
		t.Fatal("master off: success row should not capture even with errors-only off")
	}

	// A nil recorder never panics and returns empty.
	if _, _, _, b := o.requestSnapshotFor(nil, true); b != "" {
		t.Fatal("nil recorder must return empty")
	}
}
