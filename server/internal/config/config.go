// Package config loads environment-driven configuration for the API server.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultDevJWTSecret = "dev-secret-change-me-aivory-2026"

// Config holds the resolved environment for one server process.
type Config struct {
	Listen       string
	Env          string
	DatabaseURL  string
	RedisURL     string
	QdrantURL    string
	QdrantAPIKey string
	JWTSecret    string
	// JWTSecretEphemeral is true when JWT_SECRET was not provided and a random
	// secret was minted at boot (dev/local only). Sessions reset on restart.
	JWTSecretEphemeral bool
	AccessTTL          time.Duration
	RefreshTTL         time.Duration
	AllowedOrigins     []string
	StaticDir          string
	UploadDir          string
	ArtifactDir        string
	BackupDir          string
	MaxUploadBytes     int64
	MaxBackupBytes     int64
	DailyMessages      int
	DailyImages        int
	SearchProvider     string
	SearchAPIKey       string
	SearchBaseURL      string
	EmbeddingBaseURL   string
	EmbeddingAPIKey    string
	EmbeddingModel     string
	EmbeddingDim       int
	SandboxBaseURL     string
	SandboxAPIKey      string
	MinerUAPIURL       string
	MinerUAPIKey       string
	// OAuthCallbackBaseURL pins the ONE scheme://host whose OAuth callback path is
	// registered with the providers (domain A). When the site answers on several
	// domains but the provider only accepts a single redirect_uri, set this so every
	// flow — no matter which domain it starts on — hands the provider the callback
	// it trusts. Empty = derive from the request host (single-domain deployments).
	OAuthCallbackBaseURL string
	// OAuthReturnOrigins is the allowlist of scheme://host values a login may be
	// bounced back to after completing on the canonical host (the site's bound
	// domains: A, B, C…). It is the open-redirect guard for the cross-domain
	// hand-off — only an exact match here may be a redirect target.
	OAuthReturnOrigins []string
}

// Load reads environment variables, applying production-safe defaults so the
// server starts in development with zero configuration.
func Load() Config {
	cfg := Config{
		Listen:       getenv("AIVORY_LISTEN", ":8787"),
		Env:          getenv("AIVORY_ENV", "development"),
		DatabaseURL:  getenv("DATABASE_URL", "./data/aivory.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"),
		RedisURL:     getenv("REDIS_URL", ""),
		QdrantURL:    getenv("QDRANT_URL", ""),
		QdrantAPIKey: getenv("QDRANT_API_KEY", ""),
		JWTSecret:    getenv("JWT_SECRET", ""),
		// Short-lived access tokens limit the damage window if a token is stolen:
		// the stolen token expires quickly even without explicit revocation.
		// 30 minutes is the recommended ceiling; operators may lower it further
		// via ACCESS_TTL without touching RefreshTTL (7–30 days is fine there).
		AccessTTL:      getenvDuration("ACCESS_TTL", 30*time.Minute),
		RefreshTTL:     getenvDuration("REFRESH_TTL", 30*24*time.Hour),
		AllowedOrigins: getenvList("ALLOWED_ORIGINS", []string{"http://localhost:5173", "http://127.0.0.1:5173"}),
		// STATIC_DIR points at the built SPA (dist/). When set, the API process
		// also serves the frontend from the SAME origin (single-container deploy),
		// so there is no cross-origin and any domain the server is reached on just
		// works. Empty = API-only (dev with the Vite proxy, or a separate web tier).
		StaticDir:            getenv("STATIC_DIR", ""),
		UploadDir:            getenv("UPLOAD_DIR", "./data/uploads"),
		ArtifactDir:          getenv("ARTIFACT_DIR", "./data/artifacts"),
		BackupDir:            getenv("BACKUP_DIR", "./data/backups"),
		MaxUploadBytes:       getenvInt64("MAX_UPLOAD_BYTES", 50*1024*1024),
		MaxBackupBytes:       getenvInt64("MAX_BACKUP_BYTES", 20*1024*1024*1024),
		DailyMessages:        getenvInt("DAILY_MESSAGE_LIMIT", 200),
		DailyImages:          getenvInt("IMAGE_DAILY_LIMIT", 30),
		SearchProvider:       getenv("SEARCH_PROVIDER", ""),
		SearchAPIKey:         getenv("SEARCH_API_KEY", ""),
		SearchBaseURL:        getenv("SEARCH_BASE_URL", ""),
		EmbeddingBaseURL:     getenv("EMBEDDING_BASE_URL", ""),
		EmbeddingAPIKey:      getenv("EMBEDDING_API_KEY", ""),
		EmbeddingModel:       getenv("EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDim:         getenvInt("EMBEDDING_DIM", 1536),
		SandboxBaseURL:       getenv("SANDBOX_BASE_URL", ""),
		SandboxAPIKey:        getenv("SANDBOX_API_KEY", ""),
		MinerUAPIURL:         getenv("MINERU_API_URL", ""),
		MinerUAPIKey:         getenv("MINERU_API_KEY", ""),
		OAuthCallbackBaseURL: strings.TrimRight(getenv("OAUTH_CALLBACK_BASE_URL", ""), "/"),
		OAuthReturnOrigins:   getenvList("OAUTH_RETURN_ORIGINS", nil),
	}
	_ = os.MkdirAll(cfg.UploadDir, 0o755)
	_ = os.MkdirAll(cfg.ArtifactDir, 0o755)
	_ = os.MkdirAll(cfg.BackupDir, 0o755)

	// JWT secret resolution — NEVER sign tokens with the source-committed dev
	// default (a public signing key = forgeable admin). When JWT_SECRET is unset
	// (or left at the old dev default) in a NON-deployed environment, mint a random
	// EPHEMERAL secret so even a misconfigured/exposed dev instance is unforgeable;
	// sessions reset on restart, which is acceptable for dev. A DEPLOYED environment
	// (explicit prod env or a Postgres DSN) instead leaves it empty so Validate
	// fails closed and forces the operator to set a real, stable secret.
	if cfg.JWTSecret == "" || cfg.JWTSecret == defaultDevJWTSecret {
		if !looksDeployed(cfg) {
			cfg.JWTSecret = randomSecret()
			cfg.JWTSecretEphemeral = true
			log.Printf("[config] JWT_SECRET not set — using a random ephemeral secret; all sessions reset on restart. Set JWT_SECRET for stable sessions.")
		}
	}
	return cfg
}

// randomSecret mints a 32-byte hex secret from crypto/rand. On the (practically
// impossible) failure of the system RNG it returns the dev default — Validate
// still blocks that in any deployed environment, so dev simply keeps booting.
func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return defaultDevJWTSecret
	}
	return hex.EncodeToString(b)
}

// looksDeployed reports whether this process looks like a real deployment rather
// than local dev: an explicit non-dev AIVORY_ENV, OR a Postgres DATABASE_URL
// (real deployments use Postgres; local dev uses SQLite).
func looksDeployed(cfg Config) bool {
	dev := cfg.Env == "" || cfg.Env == "development" || cfg.Env == "dev" || cfg.Env == "test" || cfg.Env == "local"
	return !dev || isPostgresURL(cfg.DatabaseURL)
}

// Validate refuses to boot with forgeable tokens / a known admin password in
// anything that looks like a real deployment (§8.1 — A13). Call from main()
// right after Load(); it returns an error so the process aborts with a clear
// message.
//
// "Looks deployed" = an explicit non-dev AIVORY_ENV (production/prod/staging/…)
// OR a Postgres DATABASE_URL. The Postgres signal closes the original hole where
// an operator forgot to set AIVORY_ENV=production (default is "development") and
// silently booted with the public dev JWT secret: real deployments use Postgres,
// local dev uses SQLite. SQLite + an explicit dev env still boots with defaults.
func Validate(cfg Config) error {
	if !looksDeployed(cfg) {
		return nil
	}
	if cfg.JWTSecret == "" || cfg.JWTSecret == defaultDevJWTSecret {
		return fmt.Errorf("refusing to start: JWT_SECRET is unset or at the built-in dev default in a non-development deployment (AIVORY_ENV=%q, Postgres=%v) — set a long random JWT_SECRET", cfg.Env, isPostgresURL(cfg.DatabaseURL))
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
