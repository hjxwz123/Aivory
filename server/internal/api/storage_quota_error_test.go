package api

import (
	"fmt"
	"testing"

	"aivory/server/internal/llm"
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

func TestChatRunErrorEventMapsRuntimePermissionChanges(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "drawing",
			err:  fmt.Errorf("runtime policy: %w", llm.ErrDrawingPermission),
			code: errDrawingGroupPermission.Error(),
		},
		{
			name: "knowledge base",
			err:  fmt.Errorf("runtime policy: %w", llm.ErrKnowledgeBasePermission),
			code: errKnowledgeBaseGroupPermission.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := chatRunErrorEvent(tt.err, "message-1")
			if ev.Type != "error" || ev.MessageID != "message-1" || ev.Code != tt.code {
				t.Fatalf("event=%#v, want scoped permission error code %q", ev, tt.code)
			}
			if ev.Message == "" || ev.Message == chatRunErrorMessage || ev.Message == tt.err.Error() {
				t.Fatalf("message=%q, want actionable public copy", ev.Message)
			}
		})
	}
}
