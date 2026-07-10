package api

import (
	"path/filepath"
	"testing"

	"aurelia/server/internal/config"
	"aurelia/server/internal/store"
)

// TestUploadLimitBytes locks in §4.6 per-kind upload caps: images default to
// 5 MB (seeded), non-image files fall back to the env ceiling, admin overrides
// apply, and no override can exceed the env ceiling.
func TestUploadLimitBytes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	const ceiling = int64(50) << 20
	d := Deps{DB: db, Config: config.Config{MaxUploadBytes: ceiling}}

	// Seeded default: images capped at 5 MB.
	if got := uploadLimitBytes(d, "image"); got != 5<<20 {
		t.Errorf("image default = %d, want %d", got, 5<<20)
	}
	// Non-image with no admin cap → the env ceiling.
	if got := uploadLimitBytes(d, "text"); got != ceiling {
		t.Errorf("file default = %d, want %d", got, ceiling)
	}
	// Admin tightens the image cap to 2 MB.
	if err := store.SetSetting(db, "max_image_upload_mb", 2); err != nil {
		t.Fatalf("set image: %v", err)
	}
	if got := uploadLimitBytes(d, "image"); got != 2<<20 {
		t.Errorf("image after override = %d, want %d", got, 2<<20)
	}
	// An admin value above the env ceiling is clamped down to it (tighten-only).
	if err := store.SetSetting(db, "max_file_upload_mb", 999); err != nil {
		t.Fatalf("set file: %v", err)
	}
	if got := uploadLimitBytes(d, "text"); got != ceiling {
		t.Errorf("file over-ceiling = %d, want clamp %d", got, ceiling)
	}
	// 0 means "use default" → env ceiling for non-image files.
	if err := store.SetSetting(db, "max_file_upload_mb", 0); err != nil {
		t.Fatalf("reset file: %v", err)
	}
	if got := uploadLimitBytes(d, "text"); got != ceiling {
		t.Errorf("file zero = %d, want %d", got, ceiling)
	}
}

// TestUploadTooLargeError phrases the breach with the actual limit and kind.
func TestUploadTooLargeError(t *testing.T) {
	if got := uploadTooLargeError("image", 5<<20).Error(); got != "image exceeds the maximum size of 5 MB" {
		t.Errorf("image message = %q", got)
	}
	if got := uploadTooLargeError("text", 50<<20).Error(); got != "file exceeds the maximum size of 50 MB" {
		t.Errorf("file message = %q", got)
	}
}
