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
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	_, _, err := registry.Run(context.Background(), "aivory_web_search", []byte(`{"query":"test"}`), &llm.ToolContext{BuiltinTools: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "not enabled for this model") {
		t.Fatalf("disallowed registry call error = %v", err)
	}
}

func TestRegistryUsesAivoryNameForLocalWebSearch(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	found := false
	for _, definition := range registry.List("") {
		switch definition.Name {
		case "aivory_web_search":
			found = true
		case "web_search":
			t.Fatal("local registry reused the provider-hosted web_search name")
		}
	}
	if !found {
		t.Fatal("local registry did not expose aivory_web_search")
	}
}
