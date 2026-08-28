package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The placeholder for an OCR-needing document without a working MinerU config
// must carry the machine code that userDocuments() lifts into the client-facing
// error_code, and keep the substrings pinned by ingest_parse_failure_test.go.
func TestParseDocumentUnconfiguredCarriesParserCode(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(docPath, []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, extracted, err := parseDocument(
		context.Background(), docPath, "image/png", "scan.png",
		"", "", nil, []string{"mineru_api_url is empty"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if extracted {
		t.Fatal("unconfigured OCR path must report extracted=false")
	}
	if !strings.Contains(content, ParserNotConfiguredCode) {
		t.Fatalf("placeholder must carry %q for the client-facing error_code, got %q", ParserNotConfiguredCode, content)
	}
	// Pinned contract with tests + the admin drill-down: the human prose keeps
	// naming MinerU and the generic extraction prefix survives.
	if !strings.Contains(content, "could not extract text") || !strings.Contains(content, "MinerU") {
		t.Fatalf("placeholder lost a pinned diagnostic substring: %q", content)
	}
	// The missing-config detail must still be spelled out for admins.
	if !strings.Contains(content, "mineru_api_url is empty") {
		t.Fatalf("placeholder lost the missing-config summary: %q", content)
	}
}
