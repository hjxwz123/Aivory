package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

// §23 realtime notify stream tests. The hub is a package-level singleton whose
// bus subscription starts once per process, so every test shares one memory
// cache (matching production: one cache instance per process).

var eventsTestCache = cache.NewMemory()

func eventsTestDeps() Deps { return Deps{Cache: eventsTestCache} }

// waitEvent polls a connection channel with a deadline so tests never hang.
func waitEvent(t *testing.T, ch chan string, within time.Duration) string {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(within):
		t.Fatalf("no event delivered within %s", within)
		return ""
	}
}

func TestPublishUserEventDeliversToOwnUserOnly(t *testing.T) {
	d := eventsTestDeps()
	connA := eventsHub.register("user-a")
	defer eventsHub.unregister("user-a", connA)
	connB := eventsHub.register("user-b")
	defer eventsHub.unregister("user-b", connB)

	req := httptest.NewRequest("POST", "/api/conversations/c1/messages", nil)
	req.Header.Set("X-Device-Id", "device-1")
	publishUserEvent(d, req, "user-a", "conversation.updated", "c1")

	payload := waitEvent(t, connA.ch, 2*time.Second)
	var ev map[string]string
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("payload not JSON: %v (%q)", err, payload)
	}
	if ev["type"] != "conversation.updated" || ev["conversation_id"] != "c1" || ev["origin"] != "device-1" {
		t.Fatalf("event = %v, want type/conversation_id/origin populated", ev)
	}
	// user-b must NOT receive user-a's event.
	select {
	case p := <-connB.ch:
		t.Fatalf("cross-user leak: user-b received %q", p)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPublishUserEventOmitsEmptyFields(t *testing.T) {
	d := eventsTestDeps()
	conn := eventsHub.register("user-c")
	defer eventsHub.unregister("user-c", conn)

	// No request (no origin), no conversation id — e.g. the import broadcast.
	publishUserEvent(d, nil, "user-c", "conversation.created", "")
	payload := waitEvent(t, conn.ch, 2*time.Second)
	var ev map[string]string
	_ = json.Unmarshal([]byte(payload), &ev)
	if ev["type"] != "conversation.created" {
		t.Fatalf("type = %q", ev["type"])
	}
	if _, has := ev["conversation_id"]; has {
		t.Error("empty conversation_id must be omitted")
	}
	if _, has := ev["origin"]; has {
		t.Error("origin must be omitted without an X-Device-Id header")
	}
}

func TestEventsHubCapEvictsOldest(t *testing.T) {
	user := "user-cap"
	first := eventsHub.register(user)
	var conns []*eventsConn
	for i := 0; i < eventsMaxConnsPerUser; i++ { // pushes the total past the cap
		conns = append(conns, eventsHub.register(user))
	}
	defer func() {
		for _, c := range conns {
			eventsHub.unregister(user, c)
		}
	}()
	select {
	case <-first.done:
		// evicted — expected
	case <-time.After(time.Second):
		t.Fatal("oldest connection was not evicted past the per-user cap")
	}
}

func TestEventsStreamHandlerStreamsHelloAndEvents(t *testing.T) {
	d := eventsTestDeps()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/events", nil)
	req = req.WithContext(context.WithValue(ctx, userCtxKey{}, &store.User{ID: "user-stream"}))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		eventsStreamHandler(d, rec, req)
	}()

	// Wait for the hello frame, then publish an event to this user.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), `{"type":"hello"}`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rec.Body.String(), `{"type":"hello"}`) {
		cancel()
		t.Fatal("hello frame never written")
	}
	publishUserEvent(d, nil, "user-stream", "conversation.updated", "c9")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), "conversation.updated") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit on context cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `data: {"type":"hello"}`) {
		t.Errorf("missing hello frame:\n%s", body)
	}
	if !strings.Contains(body, `"type":"conversation.updated"`) || !strings.Contains(body, `"conversation_id":"c9"`) {
		t.Errorf("published event missing from stream:\n%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering: no missing (nginx would buffer the stream)")
	}
}

var _ = http.NoBody // keep http import if assertions above change
