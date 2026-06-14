package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"aurelia/server/internal/sandbox"
	"aurelia/server/internal/store"
)

// settingsSandbox resolves the sandbox backend (base URL + API key) from admin
// settings on every call, so the sandbox can be configured from the admin page
// (settings keys `sandbox_base_url` / `sandbox_api_key`) without restarting the
// server. When a setting is blank it falls back to the env-provided default.
//
// design.md §4.5: SandboxService is the single dependency point — this wrapper
// keeps that contract while moving configuration from env to the DB. The keys
// are stored plaintext, consistent with the channel api_key policy.
type settingsSandbox struct {
	db          *sql.DB
	fallbackURL string
	fallbackKey string
}

func newSettingsSandbox(db *sql.DB, fallbackURL, fallbackKey string) *settingsSandbox {
	return &settingsSandbox{db: db, fallbackURL: fallbackURL, fallbackKey: fallbackKey}
}

// backend builds an HTTPSandbox from the current settings (re-read each call so
// admin changes apply immediately). NewSession + Exec stay consistent because
// the session id identifies the session on whatever backend is configured at
// the time, and config does not change mid-call.
func (s *settingsSandbox) backend() sandbox.Service {
	// A blank admin setting means "not configured" → use the env/boot default
	// (e.g. the bundled localhost sandbox). A non-blank setting always wins, so
	// an admin can still point at an external sandbox.
	base := s.settingString("sandbox_base_url", s.fallbackURL)
	if strings.TrimSpace(base) == "" {
		base = s.fallbackURL
	}
	key := s.settingString("sandbox_api_key", s.fallbackKey)
	if strings.TrimSpace(key) == "" {
		key = s.fallbackKey
	}
	storage := s.storageConfig()
	return sandbox.NewWithStorage(base, key, storage)
}

// storageConfig assembles the admin-driven archive bucket. Returns nil when
// no provider is configured — the sandbox sidecar then leaves archive/restore
// off and "reaped = gone" applies.
func (s *settingsSandbox) storageConfig() *sandbox.StorageConfig {
	provider := s.settingString("storage_provider", "")
	if provider != "s3" && provider != "aliyun_oss" {
		return nil
	}
	cfg := &sandbox.StorageConfig{
		Provider: provider,
		Prefix:   s.settingString("storage_prefix", "workspaces/"),
	}
	switch provider {
	case "s3":
		cfg.S3Bucket = s.settingString("storage_s3_bucket", "")
		cfg.S3Region = s.settingString("storage_s3_region", "")
		cfg.S3Endpoint = s.settingString("storage_s3_endpoint", "")
		cfg.S3AccessKey = s.settingString("storage_s3_access_key", "")
		cfg.S3SecretKey = s.settingString("storage_s3_secret_key", "")
	case "aliyun_oss":
		cfg.OSSBucket = s.settingString("storage_aliyun_bucket", "")
		cfg.OSSEndpoint = s.settingString("storage_aliyun_endpoint", "")
		cfg.OSSAccessKeyID = s.settingString("storage_aliyun_access_key_id", "")
		cfg.OSSAccessKeySecret = s.settingString("storage_aliyun_access_key_secret", "")
	}
	if !cfg.Effective() {
		return nil
	}
	return cfg
}

func (s *settingsSandbox) settingString(key, fallback string) string {
	if s.db == nil {
		return fallback
	}
	raw, err := store.GetSetting(s.db, key)
	if err != nil {
		// Row absent → fall back to the boot-time env value (admin never
		// touched this key).
		return fallback
	}
	// Row PRESENT — honour what the admin saved, including an empty string.
	// Saving "" is the UI gesture for "clear this field"; if we returned the
	// env value here, deleting the value in the admin UI wouldn't actually
	// disable the feature, which the operator would not expect.
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return fallback
	}
	return strings.TrimSpace(v)
}

func (s *settingsSandbox) Enabled() bool { return s.backend().Enabled() }

func (s *settingsSandbox) NewSession(ctx context.Context) (string, error) {
	return s.backend().NewSession(ctx)
}

func (s *settingsSandbox) Exec(ctx context.Context, sessionID, code string) (*sandbox.Result, error) {
	return s.backend().Exec(ctx, sessionID, code)
}

func (s *settingsSandbox) PutFile(ctx context.Context, sessionID, path string, data []byte) error {
	return s.backend().PutFile(ctx, sessionID, path, data)
}

func (s *settingsSandbox) GetFile(ctx context.Context, sessionID, path string) ([]byte, error) {
	return s.backend().GetFile(ctx, sessionID, path)
}

func (s *settingsSandbox) Release(ctx context.Context, sessionID string) error {
	return s.backend().Release(ctx, sessionID)
}
