// Package config loads environment-driven configuration for the API server.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultDevJWTSecret = "dev-secret-change-me-aurelia-2026"

// Config holds the resolved environment for one server process.
type Config struct {
	Listen           string
	Env              string
	DatabaseURL      string
	RedisURL         string
	QdrantURL        string
	QdrantAPIKey     string
	JWTSecret        string
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	AllowedOrigins   []string
	UploadDir        string
	ArtifactDir      string
	MaxUploadBytes   int64
	DailyMessages    int
	DailyImages      int
	SearchProvider   string
	SearchAPIKey     string
	SearchBaseURL    string
	EmbeddingBaseURL string
	EmbeddingAPIKey  string
	EmbeddingModel   string
	EmbeddingDim     int
	SandboxBaseURL   string
	SandboxAPIKey    string
	MinerUAPIURL     string
	MinerUAPIKey     string
}

// Load reads environment variables, applying production-safe defaults so the
// server starts in development with zero configuration.
func Load() Config {
	cfg := Config{
		Listen:           getenv("AURELIA_LISTEN", ":8787"),
		Env:              getenv("AURELIA_ENV", "development"),
		DatabaseURL:      getenv("DATABASE_URL", "./data/aurelia.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"),
		RedisURL:         getenv("REDIS_URL", ""),
		QdrantURL:        getenv("QDRANT_URL", ""),
		QdrantAPIKey:     getenv("QDRANT_API_KEY", ""),
		JWTSecret:        getenv("JWT_SECRET", defaultDevJWTSecret),
		// Short-lived access tokens limit the damage window if a token is stolen:
		// the stolen token expires quickly even without explicit revocation.
		// 30 minutes is the recommended ceiling; operators may lower it further
		// via ACCESS_TTL without touching RefreshTTL (7–30 days is fine there).
		AccessTTL:        getenvDuration("ACCESS_TTL", 30*time.Minute),
		RefreshTTL:       getenvDuration("REFRESH_TTL", 30*24*time.Hour),
		AllowedOrigins:   getenvList("ALLOWED_ORIGINS", []string{"http://localhost:5173", "http://127.0.0.1:5173"}),
		UploadDir:        getenv("UPLOAD_DIR", "./data/uploads"),
		ArtifactDir:      getenv("ARTIFACT_DIR", "./data/artifacts"),
		MaxUploadBytes:   getenvInt64("MAX_UPLOAD_BYTES", 50*1024*1024),
		DailyMessages:    getenvInt("DAILY_MESSAGE_LIMIT", 200),
		DailyImages:      getenvInt("IMAGE_DAILY_LIMIT", 30),
		SearchProvider:   getenv("SEARCH_PROVIDER", ""),
		SearchAPIKey:     getenv("SEARCH_API_KEY", ""),
		SearchBaseURL:    getenv("SEARCH_BASE_URL", ""),
		EmbeddingBaseURL: getenv("EMBEDDING_BASE_URL", ""),
		EmbeddingAPIKey:  getenv("EMBEDDING_API_KEY", ""),
		EmbeddingModel:   getenv("EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDim:     getenvInt("EMBEDDING_DIM", 1536),
		SandboxBaseURL:   getenv("SANDBOX_BASE_URL", ""),
		SandboxAPIKey:    getenv("SANDBOX_API_KEY", ""),
		MinerUAPIURL:     getenv("MINERU_API_URL", ""),
		MinerUAPIKey:     getenv("MINERU_API_KEY", ""),
	}
	_ = os.MkdirAll(cfg.UploadDir, 0o755)
	_ = os.MkdirAll(cfg.ArtifactDir, 0o755)
	return cfg
}

// Validate refuses to boot with forgeable tokens / a known admin password in
// anything that looks like a real deployment (§8.1 — A13). Call from main()
// right after Load(); it returns an error so the process aborts with a clear
// message.
//
// "Looks deployed" = an explicit non-dev AURELIA_ENV (production/prod/staging/…)
// OR a Postgres DATABASE_URL. The Postgres signal closes the original hole where
// an operator forgot to set AURELIA_ENV=production (default is "development") and
// silently booted with the public dev JWT secret: real deployments use Postgres,
// local dev uses SQLite. SQLite + an explicit dev env still boots with defaults.
func Validate(cfg Config) error {
	dev := cfg.Env == "" || cfg.Env == "development" || cfg.Env == "dev" || cfg.Env == "test" || cfg.Env == "local"
	looksDeployed := !dev || isPostgresURL(cfg.DatabaseURL)
	if !looksDeployed {
		return nil
	}
	if cfg.JWTSecret == "" || cfg.JWTSecret == defaultDevJWTSecret {
		return fmt.Errorf("refusing to start: JWT_SECRET is unset or at the built-in dev default in a non-development deployment (AURELIA_ENV=%q, Postgres=%v) — set a long random JWT_SECRET", cfg.Env, isPostgresURL(cfg.DatabaseURL))
	}
	if len(cfg.JWTSecret) < 32 {
		return fmt.Errorf("refusing to start: JWT_SECRET is too short (%d chars; need ≥32)", len(cfg.JWTSecret))
	}
	// No admin is seeded from the environment any more — the first account is
	// created through the first-run setup flow (§ first-run setup), so there is no
	// default admin password to guard here.
	return nil
}

// isPostgresURL reports whether the DSN addresses PostgreSQL (mirrors the
// store's detection without importing it, to keep config dependency-free).
func isPostgresURL(dsn string) bool {
	l := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(l, "postgres://") || strings.HasPrefix(l, "postgresql://") ||
		(strings.Contains(l, "host=") && strings.Contains(l, "dbname="))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
func getenvInt64(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
func getenvDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
func getenvList(k string, def []string) []string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	out := []string{}
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == ',' {
			seg := v[start:i]
			// trim spaces
			for len(seg) > 0 && (seg[0] == ' ' || seg[0] == '\t') {
				seg = seg[1:]
			}
			for len(seg) > 0 && (seg[len(seg)-1] == ' ' || seg[len(seg)-1] == '\t') {
				seg = seg[:len(seg)-1]
			}
			if seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}
