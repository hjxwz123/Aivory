package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestVectorChunkStatusesEmptyScopeDoesNotQueryQdrant(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "")
	got, err := q.VectorChunkStatuses(context.Background(), 1536, Scope{})
	if err != nil {
		t.Fatalf("VectorChunkStatuses: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("statuses = %#v, want empty", got)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("Qdrant requests = %d, want 0 for empty scope", got)
	}
}

func TestAllVectorChunkStatusesExplicitlyRunsUnfilteredScan(t *testing.T) {
	const dim = 1536
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/aivory_c1536/points/scroll" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"points":[{"payload":{"chunk_id":"ch1"},"vector":[0.5]}],"next_page_offset":null}}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "")
	q.ensured[dim] = true
	got, err := q.AllVectorChunkStatuses(context.Background(), dim)
	if err != nil {
		t.Fatalf("AllVectorChunkStatuses: %v", err)
	}
	if st := got["ch1"]; !st.Exists || !st.HasVector {
		t.Fatalf("ch1 status = %+v", st)
	}
	if _, ok := gotBody["filter"]; ok {
		t.Fatalf("global scan unexpectedly sent filter: %#v", gotBody["filter"])
	}
}

func TestVectorChunkStatusesNonEmptyScopeAlwaysFilters(t *testing.T) {
	const dim = 768
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"points":[],"next_page_offset":null}}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "")
	q.ensured[dim] = true
	if _, err := q.VectorChunkStatuses(context.Background(), dim, Scope{ConversationID: "conv-1"}); err != nil {
		t.Fatalf("VectorChunkStatuses: %v", err)
	}
	filter, ok := gotBody["filter"].(map[string]any)
	if !ok {
		t.Fatalf("scoped scan has no filter: %#v", gotBody)
	}
	should, ok := filter["should"].([]any)
	if !ok || len(should) != 1 {
		t.Fatalf("filter.should = %#v, want one condition", filter["should"])
	}
}

func TestDeleteByDocumentSweepsCollectionsWithBoundedConcurrency(t *testing.T) {
	var active, maximum, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections" {
			_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"aivory_c256"},{"name":"aivory_c512"},{"name":"aivory_c768"},{"name":"aivory_c1024"},{"name":"aivory_c1536"},{"name":"aivory_c3072"}]}}`))
			return
		}
		deletes.Add(1)
		now := active.Add(1)
		for {
			old := maximum.Load()
			if now <= old || maximum.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	q := NewQdrant(srv.URL, "")
	if err := q.DeleteByDocument(context.Background(), "doc-1"); err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}
	if got := deletes.Load(); got != 6 {
		t.Fatalf("delete requests = %d, want 6", got)
	}
	if got := maximum.Load(); got <= 1 || got > 4 {
		t.Fatalf("maximum delete concurrency = %d, want 2..4", got)
	}
}
