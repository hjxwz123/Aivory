package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// AI PPT usage reporting.
//
// The ledger is the money, usage_logs is the report: settling a reservation
// writes credit_ledger only, while Admin → Usage & billing reads usage_logs, so
// every AI PPT call publishes one row here (purpose "ppt", memo built by
// store.AiPPTGenerateUsageMemo / store.AiPPTEditUsageMemo). The row's credits
// column carries what the ledger actually took, so a free generation is still
// visible as a call without distorting any billing total.

// AI PPT (§ Docmee / 文多多, API mode).
//
// Our own UI drives the vendor's V2 API through the handlers in
// aippt_handlers.go; this file owns the configuration and the server-side
// credentials. Two things must never live in the browser:
//
//   - the Docmee API key, which mints the short-lived per-user token that every
//     upstream call carries, and
//   - the platform credit ledger, which bills one fixed price per generated deck.
//
// The config endpoint only ever reports non-secret flags, the prices and the
// caller's own balance; the key is masked on admin reads.

const (
	// docmeeAttemptSourceType is the credit-ledger source for a PPT generation
	// attempt. The final billed key is the upstream PPT id.
	docmeeAttemptSourceType = "ppt"

	docmeeDefaultAPIBaseURL    = "https://docmee.cn"
	docmeeDefaultCreditsPerPPT = 10
	// Pinned by default: the editor surface loads this script, and a deployment
	// can point it at a self-hosted copy or an internal mirror.
	docmeeDefaultSDKURL           = "https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js"
	docmeeDefaultTokenHours       = 2
	docmeeReservationTTL          = 30 * time.Minute
	docmeeTokenCacheTTL           = 20 * time.Minute
	docmeeUpstreamTimeout         = 20 * time.Second
	docmeeUpstreamResponseReadCap = 64 << 10
	docmeeSessionRateLimit        = 30
	docmeeMaxAttemptIDLen         = 128
	docmeeMaxUpstreamMessageLen   = 240
	docmeeMaxUpstreamURLBytes     = 2048
	// docmeeDefaultMaxUploadMB mirrors Docmee's own upload guidance (≤50MB/file).
	docmeeDefaultMaxUploadMB = 50
)

var (
	errDocmeeDisabled      = errors.New("AI PPT is disabled")
	errDocmeeNotConfigured = errors.New("AI PPT is not configured — set the Docmee API key in Admin → Credits & quotas")
	errDocmeeInsufficient  = errors.New("insufficient credits")
)

// docmeeConfig is the resolved runtime configuration. Settings live in the
// admin settings table (see settingsKeys); the API key and API base URL also
// honour environment fallbacks so a self-hoster can wire it without the UI.
type docmeeConfig struct {
	Enabled       bool
	APIKey        string
	APIBaseURL    string
	CreditsPerPPT float64
	TokenHours    int
	// Editor surface (vendor iframe): creation runs on our own UI, but real
	// slide-level editing is only possible in Docmee's editor, which is loaded
	// from SDKURL and needs DOMAIN on the international build.
	SDKURL string
	Domain string
	// API-mode settings: what one edit costs the user, the fallback template when
	// the picker has nothing selected, and the per-file cap for upload inputs.
	EditCredits       float64
	DefaultTemplateID string
	MaxUploadMB       int
}

// editBillingEnabled mirrors billingEnabled for the edit operations (AI rewrite /
// template change). Zero price means edits are free.
func (c docmeeConfig) editBillingEnabled(d Deps) bool {
	return c.EditCredits > 0 && globalCreditsPerUSD(d) > 0
}

// configured reports whether the iframe can be served at all: the feature is on
// and an upstream API key exists.
func (c docmeeConfig) configured() bool {
	return c.Enabled && strings.TrimSpace(c.APIKey) != ""
}

// billingEnabled reports whether a generation is charged. Billing requires both
// a per-deck price and the platform-wide credit system (§ credits,
// credits_per_usd > 0); otherwise the feature is free — the sane behaviour for
// self-hosters who never turned credits on.
func (c docmeeConfig) billingEnabled(d Deps) bool {
	return c.CreditsPerPPT > 0 && globalCreditsPerUSD(d) > 0
}

// docmeeSettingString reads a string setting, falling back to an env var and
// then to def when the stored value is absent or blank — the MinerU pattern, so
// blanking a field in the admin UI re-enables the deployment's env default.
func docmeeSettingString(d Deps, key, envKey, def string) string {
	if raw, err := store.GetSetting(d.DB, key); err == nil && len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if envKey == "" {
		return def
	}
	return strings.TrimSpace(envcfg.Str(envKey, def))
}

func docmeeSettingBool(d Deps, key string, def bool) (bool, bool) {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil || len(raw) == 0 {
		return def, false
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return def, false
	}
	return v, true
}

func docmeeSettingFloat(d Deps, key string, def float64) float64 {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil || len(raw) == 0 {
		return def
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil || v < 0 {
		return def
	}
	return v
}

func docmeeSettingInt(d Deps, key string, def int) int {
	raw, err := store.GetSetting(d.DB, key)
	if err != nil || len(raw) == 0 {
		return def
	}
	var v int
	if json.Unmarshal(raw, &v) != nil || v < 0 {
		return def
	}
	return v
}

func docmeeConfigFor(d Deps) docmeeConfig {
	cfg := docmeeConfig{
		APIKey:            docmeeSettingString(d, "docmee_api_key", "DOCMEE_API_KEY", ""),
		APIBaseURL:        docmeeSettingString(d, "docmee_api_base_url", "DOCMEE_API_BASE_URL", docmeeDefaultAPIBaseURL),
		SDKURL:            docmeeSettingString(d, "docmee_sdk_url", "DOCMEE_SDK_URL", docmeeDefaultSDKURL),
		Domain:            docmeeSettingString(d, "docmee_domain", "DOCMEE_DOMAIN", ""),
		CreditsPerPPT:     docmeeSettingFloat(d, "docmee_credits_per_ppt", docmeeDefaultCreditsPerPPT),
		TokenHours:        docmeeSettingInt(d, "docmee_token_hours", docmeeDefaultTokenHours),
		EditCredits:       docmeeSettingFloat(d, "docmee_edit_credits", 0),
		DefaultTemplateID: docmeeSettingString(d, "docmee_default_template_id", "", ""),
		MaxUploadMB:       docmeeSettingInt(d, "docmee_max_upload_mb", docmeeDefaultMaxUploadMB),
	}
	// An unset enable flag follows the key: present key ⇒ feature on. Storing an
	// explicit false always wins, so an admin can park the integration without
	// deleting credentials.
	if enabled, set := docmeeSettingBool(d, "docmee_enabled", false); set {
		cfg.Enabled = enabled
	} else {
		cfg.Enabled = strings.TrimSpace(cfg.APIKey) != ""
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/")
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = docmeeDefaultAPIBaseURL
	}
	return cfg
}

// docmeeUIDForUser derives the upstream uid from the local user id. Docmee
// isolates each uid's PPT library, so this must stay stable — and hashing keeps
// our internal ids out of a third party's records.
func docmeeUIDForUser(userID string) string {
	sum := sha256.Sum256([]byte("aivory:" + userID))
	return "aivory-" + hex.EncodeToString(sum[:])[:24]
}

// docmeeUpstreamClient is the transport for the token-minting call. The base URL
// is admin-controlled (and may legitimately be an internal reverse proxy, per
// Docmee's 接口转发 guide), so this follows the admin-MCP precedent of trusting
// an admin-configured host rather than the SSRF-hardened user-fetch client.
func docmeeUpstreamClient(d Deps) *http.Client {
	if d.DocmeeHTTPClient != nil {
		return d.DocmeeHTTPClient
	}
	return &http.Client{Timeout: docmeeUpstreamTimeout}
}

func docmeeSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// normalizeDocmeeURL trims an admin-entered URL and fills in a missing scheme, so
// pasting "app.xpptx.com" is accepted instead of rejecting the whole settings
// save (which would also silently drop the enable flag saved alongside it). Values
// that cannot be a URL — spaces, no host, oversized — are still refused.
func normalizeDocmeeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > docmeeMaxUpstreamURLBytes {
		return "", errors.New("url too long")
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := neturl.Parse(trimmed)
	if err != nil || parsed.Host == "" || strings.ContainsAny(parsed.Host, " \t") {
		return "", errors.New("invalid url")
	}
	return trimmed, nil
}

type docmeeTokenPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	} `json:"data"`
	Token string `json:"token"`
}

// docmeeCreateToken calls the upstream createApiToken endpoint
// (POST {base}/api/user/createApiToken, Api-Key header). The token is
// deliberately created without a `limit`: the platform credit ledger — not the
// upstream counter — is the single source of truth for what a user may spend.
func docmeeCreateToken(ctx context.Context, d Deps, cfg docmeeConfig, userID string) (string, error) {
	payload := map[string]any{"uid": docmeeUIDForUser(userID)}
	if cfg.TokenHours > 0 {
		payload["timeOfHours"] = cfg.TokenHours
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.APIBaseURL+"/api/user/createApiToken", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Api-Key", cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := docmeeUpstreamClient(d).Do(req)
	if err != nil {
		return "", fmt.Errorf("docmee createApiToken: %w", err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, docmeeUpstreamResponseReadCap))
	if readErr != nil {
		return "", fmt.Errorf("docmee createApiToken response: %w", readErr)
	}
	var out docmeeTokenPayload
	_ = json.Unmarshal(raw, &out)
	token := strings.TrimSpace(out.Data.Token)
	if token == "" {
		token = strings.TrimSpace(out.Token)
	}
	if resp.StatusCode >= 400 || token == "" {
		message := strings.TrimSpace(out.Message)
		if message == "" {
			message = string(raw)
		}
		return "", fmt.Errorf("docmee createApiToken rejected (http %d, code %d): %s",
			resp.StatusCode, out.Code, docmeeSnippet(message, docmeeMaxUpstreamMessageLen))
	}
	return token, nil
}

// docmeeToken mints (or reuses) the per-user iframe token. Tokens are cached
// briefly — long enough to survive a page reload, short enough that a rotated
// API key takes effect without a restart.
func docmeeToken(ctx context.Context, d Deps, cfg docmeeConfig, userID string) (string, error) {
	cacheKey := "docmee:token:v1:" + userID
	if d.Cache != nil {
		if token, ok := d.Cache.Get(cacheKey); ok && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}
	token, err := docmeeCreateToken(ctx, d, cfg, userID)
	if err != nil {
		return "", err
	}
	if d.Cache != nil {
		d.Cache.Set(cacheKey, token, docmeeTokenCacheTTL)
	}
	return token, nil
}

// meDocmeeConfigHandler reports everything the /ppt page needs to decide what to
// render: whether the integration is usable, the pinned SDK URL, the per-deck
// price, and the caller's spendable balance. Secrets (API key, API secret) are
// never included.
func meDocmeeConfigHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	cfg := docmeeConfigFor(d)
	allowed := aiPPTPermission(d, r) == nil
	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"enabled":           cfg.configured() && allowed,
		"allowed":           allowed,
		"configured":        strings.TrimSpace(cfg.APIKey) != "",
		"credits_enabled":   cfg.billingEnabled(d),
		"credits_per_ppt":   cfg.CreditsPerPPT,
		"credits_available": balance.Available,
		// API-mode fields (self-built UI, § AI PPT).
		"edit_credits":         cfg.EditCredits,
		"edit_credits_enabled": cfg.editBillingEnabled(d),
		"default_template_id":  cfg.DefaultTemplateID,
		"max_upload_mb":        cfg.MaxUploadMB,
		// Editor surface: the browser loads this SDK to open Docmee's editor for a
		// finished deck (slide-level editing is not something we rebuild).
		"sdk_url": cfg.SDKURL,
		"domain":  cfg.Domain,
	})
}

// docmeeReadyConfig resolves the configuration and answers the caller when the
// feature is not usable, so every handler reports the same typed states.
func docmeeReadyConfig(d Deps, w http.ResponseWriter) (docmeeConfig, bool) {
	cfg := docmeeConfigFor(d)
	// Unconfigured/disabled is an expected deployment state, not an internal
	// fault: answer with a typed code the page can explain, and never log it as a
	// 5xx server error. A missing key is reported as configuration (even when the
	// enable flag was never stored) so the admin sees the real remedy.
	if strings.TrimSpace(cfg.APIKey) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": errDocmeeNotConfigured.Error(), "code": "not_configured",
		})
		return cfg, false
	}
	if !cfg.Enabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": errDocmeeDisabled.Error(), "code": "disabled",
		})
		return cfg, false
	}
	return cfg, true
}

// recordDocmeeUsage writes the usage_logs/usage_stats row for one AI PPT call, at
// most once per memo key.
//
// The existence check runs before every insert (not only on replays) so an
// earlier best-effort failure is repaired by a later retry instead of leaving the
// call permanently invisible. `credits` is the amount the ledger actually took —
// 0 for a free generation — never the configured price, so an admin changing
// docmee_credits_per_ppt cannot rewrite what history says was charged.
func recordDocmeeUsage(ctx context.Context, d Deps, userID, memo string, credits float64) error {
	var existing int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_logs WHERE user_id=? AND purpose=? AND message_id=?`,
		userID, store.AiPPTUsagePurpose, memo,
	).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	return store.LogUsage(ctx, d.DB, store.UsageLog{
		UserID:    userID,
		MessageID: memo,
		Purpose:   store.AiPPTUsagePurpose,
		Credits:   credits,
		Status:    "ok",
		CreatedAt: time.Now().Unix(),
	})
}

// recordDocmeeDeckUsage publishes one generated deck (memo = the upstream ppt id,
// the same key the credit settlement uses, so a re-render cannot log it twice).
func recordDocmeeDeckUsage(ctx context.Context, d Deps, userID, pptID string, credits float64) error {
	return recordDocmeeUsage(ctx, d, userID, store.AiPPTGenerateUsageMemo(pptID), credits)
}
