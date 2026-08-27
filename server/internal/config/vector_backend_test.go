package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveVectorBackendPreservesAutoCompatibility(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "zero config stays disabled", cfg: Config{}, want: VectorBackendDisabled},
		{name: "auto without qdrant stays disabled", cfg: Config{VectorBackend: "auto"}, want: VectorBackendDisabled},
		{name: "auto with qdrant selects qdrant", cfg: Config{VectorBackend: "auto", QdrantURL: "http://qdrant:6333"}, want: VectorBackendQdrant},
		{name: "legacy empty mode with qdrant selects qdrant", cfg: Config{QdrantURL: "http://qdrant:6333"}, want: VectorBackendQdrant},
		{name: "explicit disabled ignores qdrant url", cfg: Config{VectorBackend: "disabled", QdrantURL: "http://qdrant:6333"}, want: VectorBackendDisabled},
		{name: "explicit sqlite", cfg: Config{VectorBackend: " SQLITE ", DatabaseURL: "/data/aivory.db"}, want: VectorBackendSQLite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveVectorBackend(tc.cfg)
			if err != nil {
				t.Fatalf("ResolveVectorBackend: %v", err)
			}
			if got != tc.want {
				t.Fatalf("backend = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveVectorBackendRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "qdrant needs url", cfg: Config{VectorBackend: "qdrant"}, want: "requires QDRANT_URL"},
		{name: "sqlite rejects postgres url", cfg: Config{VectorBackend: "sqlite", DatabaseURL: "postgres://db/aivory"}, want: "requires a SQLite DATABASE_URL"},
		{name: "unknown backend", cfg: Config{VectorBackend: "embedded"}, want: "invalid VECTOR_BACKEND"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveVectorBackend(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateChecksVectorBackendInDevelopment(t *testing.T) {
	err := Validate(Config{Env: "development", DatabaseURL: "./aivory.db", VectorBackend: "qdrant"})
	if err == nil || !strings.Contains(err.Error(), "requires QDRANT_URL") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestLoadCanonicalizesVectorBackend(t *testing.T) {
	t.Setenv("AIVORY_ENV", "test")
	t.Setenv("DATABASE_URL", filepath.Join(t.TempDir(), "aivory.db"))
	t.Setenv("VECTOR_BACKEND", " SQLite ")
	cfg := Load()
	if cfg.VectorBackend != VectorBackendSQLite {
		t.Fatalf("VectorBackend = %q, want sqlite", cfg.VectorBackend)
	}
}
