package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalStorageDirectory(t *testing.T) {
	t.Run("defaults under upload directory", func(t *testing.T) {
		uploadDir := filepath.Join(t.TempDir(), "uploads")
		t.Setenv("AIVORY_ENV", "test")
		t.Setenv("DATABASE_URL", filepath.Join(t.TempDir(), "aivory.db"))
		t.Setenv("UPLOAD_DIR", uploadDir)
		t.Setenv("AIVORY_LOCAL_STORAGE_DIR", "")

		cfg := Load()
		want := filepath.Join(uploadDir, "object-storage")
		if cfg.LocalStorageDir != want {
			t.Fatalf("LocalStorageDir = %q, want %q", cfg.LocalStorageDir, want)
		}
		if info, err := os.Stat(want); err != nil || !info.IsDir() {
			t.Fatalf("default local storage directory was not created: info=%v err=%v", info, err)
		}
	})

	t.Run("honors explicit override", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "objects")
		t.Setenv("AIVORY_ENV", "test")
		t.Setenv("DATABASE_URL", filepath.Join(t.TempDir(), "aivory.db"))
		t.Setenv("AIVORY_LOCAL_STORAGE_DIR", dir)

		cfg := Load()
		if cfg.LocalStorageDir != dir {
			t.Fatalf("LocalStorageDir = %q, want %q", cfg.LocalStorageDir, dir)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("explicit local storage directory was not created: info=%v err=%v", info, err)
		}
	})
}
