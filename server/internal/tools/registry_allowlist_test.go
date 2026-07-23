package tools

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
)

func TestRegistryRunRejectsToolOutsideResolvedModelPolicy(t *testing.T) {
	registry := NewRegistry(nil, nil, config.Config{}, log.New(io.Discard, "", 0))
	_, _, err := registry.Run(context.Background(), "web_search", []byte(`{"query":"test"}`), &llm.ToolContext{BuiltinTools: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "not enabled for this model") {
		t.Fatalf("disallowed registry call error = %v", err)
	}
}
