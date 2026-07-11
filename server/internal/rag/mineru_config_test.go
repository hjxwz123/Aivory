package rag

import (
	"database/sql"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestStorageBlockFromSettingsRejectsLocalForMinerU(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('storage_provider', '"local"')`); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()

	cfg, issues := storageBlockFromSettings(db)
	if cfg != nil {
		t.Fatalf("local storage must not be used for MinerU presigned fetch URLs: %+v", cfg)
	}
	joined := strings.Join(issues, "; ")
	if !strings.Contains(joined, "local") || !strings.Contains(joined, "presigned") {
		t.Fatalf("issues should explain why local storage cannot serve MinerU, got %q", joined)
	}
}

func TestMinerUConfigIssues(t *testing.T) {
	issues := minerUConfigIssues("", "", nil, []string{"storage_provider is empty"})
	joined := strings.Join(issues, "; ")
	for _, want := range []string{"mineru_api_url", "mineru_api_token", "storage_provider"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "sandbox_base_url") {
		t.Fatalf("MinerU direct upload should not require sandbox_base_url, got %q", joined)
	}
}
