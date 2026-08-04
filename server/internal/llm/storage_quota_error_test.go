package llm

import (
	"errors"
	"testing"

	"aivory/server/internal/store"
)

func TestNormalizeMessageCreateErrorPreservesStorageQuotaSentinel(t *testing.T) {
	err := normalizeMessageCreateError("save user message", "", store.ErrStorageQuotaExceeded)
	if !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("normalized error=%v, want storage quota sentinel", err)
	}
}
