package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"aivory/server/internal/llm"
)

// installedCompactionStatusHandler returns the callback installed by NewRouter.
// The callback is intentionally not a public Orchestrator API: production code
// emits these lifecycle states only from automatic compaction. Keep the narrow
// reflection escape hatch in this API regression test rather than exposing a
// production-only test trigger.
func installedCompactionStatusHandler(t *testing.T, orchestrator *llm.Orchestrator) func(string, string, string, string) {
	t.Helper()
	field := reflect.ValueOf(orchestrator).Elem().FieldByName("onCompactionStatus")
	if !field.IsValid() {
		t.Fatal("llm.Orchestrator no longer has a compaction status callback")
	}
	if field.IsNil() {
		t.Fatal("NewRouter did not install the compaction status callback")
	}
	readable := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	handler, ok := readable.Interface().(func(string, string, string, string))
	if !ok || handler == nil {
		t.Fatal("installed compaction status callback has an unexpected type")
	}
	return handler
}

func TestNewRouterBridgesAutomaticCompactionLifecycleEvents(t *testing.T) {
	db := openMigrated(t, t.TempDir()+"/compaction-events.db")
	t.Cleanup(func() { _ = db.Close() })
	orchestrator := llm.NewOrchestrator(db, nil, nil, nil, eventsTestCache, nil, nil, nil, nil)
	d := Deps{DB: db, Cache: eventsTestCache, Orchestrator: orchestrator}
	_ = NewRouter(d)
	notify := installedCompactionStatusHandler(t, orchestrator)

	const (
		userID         = "compaction-event-user"
		otherUserID    = "compaction-event-other-user"
		conversationID = "compaction-event-conversation"
		operationID    = "cmp-operation-1"
	)
	target := eventsHub.register(userID)
	t.Cleanup(func() { eventsHub.unregister(userID, target) })
	other := eventsHub.register(otherUserID)
	t.Cleanup(func() { eventsHub.unregister(otherUserID, other) })

	for _, status := range []string{"started", "completed", "failed"} {
		notify(userID, conversationID, operationID, status)
		payload := waitEvent(t, target.ch, 2*time.Second)
		var event map[string]string
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("%s payload is not JSON: %v (%q)", status, err, payload)
		}
		if got, want := event["type"], "compaction."+status; got != want {
			t.Fatalf("%s event type = %q, want %q", status, got, want)
		}
		if got := event["conversation_id"]; got != conversationID {
			t.Fatalf("%s conversation_id = %q, want %q", status, got, conversationID)
		}
		if got := event["operation_id"]; got != operationID {
			t.Fatalf("%s operation_id = %q, want %q", status, got, operationID)
		}
		if _, exists := event["origin"]; exists {
			t.Fatalf("%s automatic event unexpectedly contains an origin: %v", status, event)
		}
	}

	select {
	case payload := <-other.ch:
		t.Fatalf("automatic compaction event leaked to another user: %q", payload)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestNewRouterIgnoresUnknownCompactionStatus(t *testing.T) {
	db := openMigrated(t, t.TempDir()+"/compaction-invalid-status.db")
	t.Cleanup(func() { _ = db.Close() })
	orchestrator := llm.NewOrchestrator(db, nil, nil, nil, eventsTestCache, nil, nil, nil, nil)
	d := Deps{DB: db, Cache: eventsTestCache, Orchestrator: orchestrator}
	_ = NewRouter(d)
	notify := installedCompactionStatusHandler(t, orchestrator)

	const userID = "compaction-invalid-status-user"
	conn := eventsHub.register(userID)
	t.Cleanup(func() { eventsHub.unregister(userID, conn) })
	notify(userID, "compaction-invalid-status-conversation", "cmp-invalid", "working")

	select {
	case payload := <-conn.ch:
		t.Fatalf("unknown compaction status published an event: %q", payload)
	case <-time.After(150 * time.Millisecond):
	}
}
