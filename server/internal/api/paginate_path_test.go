package api

import (
	"testing"

	"aivory/server/internal/store"
)

// TestPaginatePath covers the reverse-pagination windowing used by the
// conversation/message endpoints: no-limit = whole path; limit = trailing
// window with a cursor; before = older page above the cursor.
func TestPaginatePath(t *testing.T) {
	mk := func(ids ...string) []store.Message {
		out := make([]store.Message, len(ids))
		for i, id := range ids {
			out[i] = store.Message{ID: id}
		}
		return out
	}
	ids := func(ms []store.Message) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	full := mk("a", "b", "c", "d", "e") // oldest→newest

	// No limit → whole path, no pagination.
	if w, more, cur := paginatePath(full, "", 0); !eq(ids(w), []string{"a", "b", "c", "d", "e"}) || more || cur != "" {
		t.Fatalf("no-limit: got %v more=%v cur=%q", ids(w), more, cur)
	}

	// limit=2 → last two, hasMore, cursor = oldest returned ("d").
	w, more, cur := paginatePath(full, "", 2)
	if !eq(ids(w), []string{"d", "e"}) || !more || cur != "d" {
		t.Fatalf("limit2: got %v more=%v cur=%q want [d e] true d", ids(w), more, cur)
	}

	// Older page before "d", limit 2 → the two above it ("b","c"), still more,
	// cursor "b".
	w, more, cur = paginatePath(full, "d", 2)
	if !eq(ids(w), []string{"b", "c"}) || !more || cur != "b" {
		t.Fatalf("before d: got %v more=%v cur=%q want [b c] true b", ids(w), more, cur)
	}

	// Reaching the head: before "b", limit 5 → just "a", no more.
	w, more, cur = paginatePath(full, "b", 5)
	if !eq(ids(w), []string{"a"}) || more || cur != "" {
		t.Fatalf("before b: got %v more=%v cur=%q want [a] false ''", ids(w), more, cur)
	}

	// limit ≥ len → whole path, no more.
	if w, more, _ := paginatePath(full, "", 10); !eq(ids(w), []string{"a", "b", "c", "d", "e"}) || more {
		t.Fatalf("limit10: got %v more=%v", ids(w), more)
	}
}
