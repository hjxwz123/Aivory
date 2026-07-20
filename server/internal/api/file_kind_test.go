package api

import "testing"

func TestKindOfUsesExtensionAndConservativeImageMIME(t *testing.T) {
	cases := []struct {
		mime, name, want string
	}{
		{"text/csv", "report.csv", "sheet"},
		{"text/tab-separated-values", "report.TSV", "sheet"},
		{"application/octet-stream", "report.xlsx", "sheet"},
		{"application/vnd.ms-excel", "report.xls", "sheet"},
		{"text/plain", "report.xlsm", "sheet"},
		{"text/plain", "photo.png", "image"},
		{"", "camera.HEIC", "image"},
		{"application/octet-stream", "reference.avif", "image"},
		{"", "scan.tiff", "image"},
		{"image/png", "notes.txt", "image"},
		{"image/svg+xml", "source.go", "image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindOf(tc.mime, tc.name); got != tc.want {
				t.Fatalf("kindOf(%q, %q) = %q, want %q", tc.mime, tc.name, got, tc.want)
			}
		})
	}
}

func TestKindOfRecognizesEveryAlwaysAllowedImageExtension(t *testing.T) {
	for _, ext := range alwaysAllowedImageExtensions {
		name := "reference." + ext
		if got := kindOf("", name); got != "image" {
			t.Errorf("kindOf(%q) = %q, want image", name, got)
		}
	}
}

func TestDetectUploadedImageMIMERejectsDisguisedImages(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	avif := append([]byte{0, 0, 0, 24}, []byte("ftypavif")...)
	cases := []struct {
		name, filename, declared string
		data                     []byte
		want                     string
	}{
		{name: "png bytes with text metadata", filename: "notes.txt", declared: "text/plain", data: png, want: "image/png"},
		{name: "avif bytes without extension", filename: "payload.dat", declared: "application/octet-stream", data: avif, want: "image/avif"},
		{name: "svg text bytes", filename: "source.txt", declared: "text/plain", data: []byte(`<?xml version="1.0"?><svg viewBox="0 0 1 1"></svg>`), want: "image/svg+xml"},
		{name: "empty browser MIME for HEIC", filename: "camera.HEIC", want: "image/heic"},
		{name: "ordinary text", filename: "notes.txt", declared: "text/plain", data: []byte("hello"), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectUploadedImageMIME(tc.filename, tc.declared, tc.data); got != tc.want {
				t.Fatalf("detectUploadedImageMIME() = %q, want %q", got, tc.want)
			}
		})
	}
}
