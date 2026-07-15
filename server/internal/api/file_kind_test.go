package api

import "testing"

func TestKindOfPrefersSupportedExtensionOverBrowserMIME(t *testing.T) {
	cases := []struct {
		mime, name, want string
	}{
		{"text/csv", "report.csv", "sheet"},
		{"text/tab-separated-values", "report.TSV", "sheet"},
		{"application/octet-stream", "report.xlsx", "sheet"},
		{"application/vnd.ms-excel", "report.xls", "sheet"},
		{"text/plain", "report.xlsm", "sheet"},
		{"text/plain", "photo.png", "image"},
		{"image/png", "notes.txt", "text"},
		{"image/png", "source.go", "code"},
		{"image/png", "deck.pptx", "doc"},
		{"image/png", "paper.pdf", "pdf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindOf(tc.mime, tc.name); got != tc.want {
				t.Fatalf("kindOf(%q, %q) = %q, want %q", tc.mime, tc.name, got, tc.want)
			}
		})
	}
}
