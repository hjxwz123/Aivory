package config

import (
	"path/filepath"
	"testing"
)

func TestRequestSignaturesAreRequiredByDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AIVORY_ENV", "development")
	t.Setenv("JWT_SECRET", "request-signature-config-test-secret")
	t.Setenv("DATABASE_URL", filepath.Join(root, "aivory.db"))
	t.Setenv("UPLOAD_DIR", filepath.Join(root, "uploads"))
	t.Setenv("AIVORY_LOCAL_STORAGE_DIR", filepath.Join(root, "objects"))
	t.Setenv("ARTIFACT_DIR", filepath.Join(root, "artifacts"))
	t.Setenv("BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("AIVORY_REQUEST_SIGNATURES_REQUIRED", "")

	if cfg := Load(); !cfg.RequestSignaturesRequired {
		t.Fatal("request signatures are disabled in the default server configuration")
	}

	t.Setenv("AIVORY_REQUEST_SIGNATURES_REQUIRED", "false")
	if cfg := Load(); cfg.RequestSignaturesRequired {
		t.Fatal("explicit request-signature compatibility override was ignored")
	}
}
