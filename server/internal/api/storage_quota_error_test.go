package api

import (
	"fmt"
	"testing"

	"aivory/server/internal/store"
)

func TestChatRunErrorEventMapsStorageQuotaWithoutLeakingInternalError(t *testing.T) {
	ev := chatRunErrorEvent(fmt.Errorf("save user message: %w", store.ErrStorageQuotaExceeded), "message-1")
	if ev.Type != "error" || ev.MessageID != "message-1" {
		t.Fatalf("event=%#v, want scoped error event", ev)
	}
	if ev.Code != store.ErrStorageQuotaExceeded.Error() {
		t.Fatalf("code=%q, want %q", ev.Code, store.ErrStorageQuotaExceeded.Error())
	}
	if ev.Message == "" || ev.Message == chatRunErrorMessage {
		t.Fatalf("message=%q, want actionable storage message", ev.Message)
	}
}
