package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAHandlerCachePolicy(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.html":                  "<!doctype html><div id=\"current-build\"></div>",
		"version.json":                `{"version":"build-current"}`,
		"sw.js":                       "self.addEventListener('fetch', () => {})",
		"manifest.webmanifest":        `{"name":"Aivory"}`,
		"assets/index-current123.js":  "console.log('current')",
		"assets/index-current123.css": "body { color: black; }",
	}
	for name, contents := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", name, err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	handler := spaHandler(dir, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	tests := []struct {
		name         string
		url          string
		wantStatus   int
		wantCache    string
		wantBodyPart string
	}{
		{
			name:         "fingerprinted javascript",
			url:          "/assets/index-current123.js",
			wantStatus:   http.StatusOK,
			wantCache:    immutableAssetCacheControl,
			wantBodyPart: "console.log('current')",
		},
		{
			name:         "fingerprinted css",
			url:          "/assets/index-current123.css",
			wantStatus:   http.StatusOK,
			wantCache:    immutableAssetCacheControl,
			wantBodyPart: "color: black",
		},
		{
			name:         "root document",
			url:          "/",
			wantStatus:   http.StatusOK,
			wantCache:    noStoreCacheControl,
			wantBodyPart: "current-build",
		},
		{
			name:       "explicit index document redirect",
			url:        "/index.html",
			wantStatus: http.StatusMovedPermanently,
			wantCache:  noStoreCacheControl,
		},
		{
			name:         "client route fallback",
			url:          "/chat/thread-1",
			wantStatus:   http.StatusOK,
			wantCache:    noStoreCacheControl,
			wantBodyPart: "current-build",
		},
		{
			name:         "deployment version",
			url:          "/version.json",
			wantStatus:   http.StatusOK,
			wantCache:    noStoreCacheControl,
			wantBodyPart: "build-current",
		},
		{
			name:         "service worker",
			url:          "/sw.js",
			wantStatus:   http.StatusOK,
			wantCache:    noStoreCacheControl,
			wantBodyPart: "addEventListener",
		},
		{
			name:         "non-fingerprinted public file",
			url:          "/manifest.webmanifest",
			wantStatus:   http.StatusOK,
			wantCache:    revalidateCacheControl,
			wantBodyPart: "Aivory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.url, nil))
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if got := recorder.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
			if tt.wantBodyPart != "" && !strings.Contains(recorder.Body.String(), tt.wantBodyPart) {
				t.Fatalf("body = %q, want it to contain %q", recorder.Body.String(), tt.wantBodyPart)
			}
		})
	}
}

func TestSPAHandlerDoesNotFallbackForMissingBuildAsset(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("current-build-index"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	handler := spaHandler(dir, http.NotFoundHandler())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/index-old-build.js", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Cache-Control"); got != noStoreCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, noStoreCacheControl)
	}
	if strings.Contains(recorder.Body.String(), "current-build-index") {
		t.Fatalf("missing asset unexpectedly fell back to index.html: %q", recorder.Body.String())
	}
}
