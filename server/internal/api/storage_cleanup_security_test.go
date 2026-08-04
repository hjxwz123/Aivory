package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/fileguard"
)

func TestCleanupStoragePathCannotEscapeConfiguredRoots(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	artifacts := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openMigrated(t, filepath.Join(root, "cleanup.db"))
	defer db.Close()
	d := Deps{DB: db, Config: config.Config{UploadDir: uploads, ArtifactDir: artifacts}}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, []byte("outside"))
	if _, err := cleanupOneStoragePath(t.Context(), d, nil, outside); !errors.Is(err, fileguard.ErrOutsideRoot) {
		t.Fatalf("outside cleanup error=%v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was deleted: %v", err)
	}

	link := filepath.Join(uploads, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := cleanupOneStoragePath(t.Context(), d, nil, link); !errors.Is(err, fileguard.ErrOutsideRoot) {
		t.Fatalf("symlink cleanup error=%v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("symlink target was deleted: %v", err)
	}

	if _, err := cleanupOneStoragePath(t.Context(), d, nil, "/proc/self/environ"); !errors.Is(err, fileguard.ErrOutsideRoot) {
		t.Fatalf("proc cleanup error=%v, want ErrOutsideRoot", err)
	}
}
