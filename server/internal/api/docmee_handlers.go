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

// AI PPT (§ Docmee / 文多多 AiPPT iframe integration, "接入方案二").
//
// The browser embeds Docmee's iframe UI SDK (a <script> file loaded from a
// configurable CDN URL, not an npm dependency) and talks to the SDK through its
// onMessage callback. Two things must never live in the browser:
//
//   - the Docmee API key, which mints the per-user iframe token, and
//   - the platform credit ledger, which bills one fixed price per generated
//     deck.
//
// So the browser gets a short-lived token from POST /api/me/ppt/session, and
// every billing transition is an authenticated API call:
//
//	session → hold `credits_per_ppt` from the user's balance (402 when short)
//	charge  → settle that hold under the upstream PPT id (idempotent per deck)
//	release → refund the hold when generation failed or was abandoned
//
// A hold that is never settled expires on its own (docmeeReservationTTL), so a
// closed tab can never strand a user's credits.

const (
	// docmeeAttemptSourceType is the credit-ledger source for a PPT generation
	// attempt. The final billed key is the upstream PPT id.
	docmeeAttemptSourceType = "ppt"

	docmeeDefaultAPIBaseURL = "https://docmee.cn"
	// Pinned by default so a deployment's served SDK cannot drift under it; the
	// admin can point this at a self-hosted copy or an internal mirror.
	docmeeDefaultSDKURL           = "https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js"
	docmeeDefaultCreatorVersion   = "v2"
	docmeeDefaultCreditsPerPPT    = 10
	docmeeDefaultTokenHours       = 2
	docmeeReservationTTL          = 30 * time.Minute
	docmeeTokenCacheTTL           = 20 * time.Minute
	docmeeUpstreamTimeout         = 20 * time.Second
	docmeeUpstreamResponseReadCap = 64 << 10
	docmeeSessionRateLimit        = 30
	// Charge is called from SDK lifecycle events (and their retries), so it gets a
	// roomier bucket than the token/attempt calls that start a generation.
	docmeeChargeRateLimit         = 90
	docmeeMaxAttemptIDLen         = 128
	docmeeMaxUpstreamMessageLen   = 240
	docmeeMaxUpstreamURLBytes     = 2048
)

var (
	errDocmeeDisabled       = errors.New("AI PPT is disabled")
	errDocmeeNotConfigured  = errors.New("AI PPT is not configured — set the Docmee API key in Admin → Credits & quotas")
	errDocmeeInsufficient   = errors.New("insufficient credits")
	errDocmeeUnknownAttempt = errors.New("unknown or expired AI PPT session")
)

// docmeeConfig is the resolved runtime configuration. Settings live in the
// admin settings table (see settingsKeys); the API key and API base URL also
// honour environment fallbacks so a self-hoster can wire it without the UI.
type docmeeConfig struct {
	Enabled        bool
	APIKey         string
	APIBaseURL     string
	Domain         string
	SDKURL         string
	SDKBaseURL     string
	CreatorVersion string
	CreditsPerPPT  float64
	TokenHours     int
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

func normalizeDocmeeCreatorVersion(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "v1":
		return "v1"
	case "", "v2":
		return docmeeDefaultCreatorVersion
	default:
		return docmeeDefaultCreatorVersion
	}
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
		APIKey:        docmeeSettingString(d, "docmee_api_key", "DOCMEE_API_KEY", ""),
		APIBaseURL:    docmeeSettingString(d, "docmee_api_base_url", "DOCMEE_API_BASE_URL", docmeeDefaultAPIBaseURL),
		Domain:        docmeeSettingString(d, "docmee_domain", "DOCMEE_DOMAIN", ""),
		SDKURL:        docmeeSettingString(d, "docmee_sdk_url", "DOCMEE_SDK_URL", docmeeDefaultSDKURL),
		SDKBaseURL:    docmeeSettingString(d, "docmee_sdk_base_url", "DOCMEE_SDK_BASE_URL", ""),
		CreditsPerPPT: docmeeSettingFloat(d, "docmee_credits_per_ppt", docmeeDefaultCreditsPerPPT),
		TokenHours:    docmeeSettingInt(d, "docmee_token_hours", docmeeDefaultTokenHours),
	}
	// An unset enable flag follows the key: present key ⇒ feature on. Storing an
	// explicit false always wins, so an admin can park the integration without
	// deleting credentials.
	if enabled, set := docmeeSettingBool(d, "docmee_enabled", false); set {
		cfg.Enabled = enabled
	} else {
		cfg.Enabled = strings.TrimSpace(cfg.APIKey) != ""
	}
	cfg.CreatorVersion = normalizeDocmeeCreatorVersion(
		docmeeSettingString(d, "docmee_creator_version", "", docmeeDefaultCreatorVersion))
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
	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"enabled":               cfg.configured(),
		"configured":            strings.TrimSpace(cfg.APIKey) != "",
		"credits_enabled":       cfg.billingEnabled(d),
		"credits_per_ppt":       cfg.CreditsPerPPT,
		"credits_available":     balance.Available,
		"reservation_ttl":       int64(docmeeReservationTTL / time.Second),
		"sdk_url":               cfg.SDKURL,
		"domain":                cfg.Domain,
		"sdk_base_url":          cfg.SDKBaseURL,
		"creator_version":       cfg.CreatorVersion,
		"download_button":       true,
		"outline_export_format": "md",
	})
}

// meDocmeeTokenHandler mints (or reuses) the short-lived iframe token. It
// deliberately takes NO credit hold: the token is needed the moment the page
// loads, while credits are only held when the user actually starts a generation
// (meDocmeeAttemptHandler).
func meDocmeeTokenHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	cfg, ok := docmeeReadyConfig(d, w)
	if !ok {
		return
	}
	if !rateLimitUser(d, u.ID, "ppt", docmeeSessionRateLimit, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT sessions — try again shortly"))
		return
	}
	token, err := docmeeToken(r.Context(), d, cfg, u.ID)
	if err != nil {
		// The upstream message can echo the admin's key/uid, so it stays in the
		// server log; the client gets a typed, retryable error.
		if d.Logger != nil {
			d.Logger.Printf("docmee token mint failed (user=%s): %v", u.ID, err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "AI PPT service is temporarily unavailable", "code": "upstream_error",
		})
		return
	}
	writeJSON(w, 200, map[string]any{
		"token":      token,
		"expires_in": int64(docmeeTokenCacheTTL / time.Second),
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

// meDocmeeAttemptHandler opens one generation attempt by holding the per-deck
// price against the user's balance. Charging happens later, when the upstream
// reports the deck: see meDocmeeChargeHandler.
func meDocmeeAttemptHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	cfg, ok := docmeeReadyConfig(d, w)
	if !ok {
		return
	}
	if !rateLimitUser(d, u.ID, "ppt", docmeeSessionRateLimit, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT sessions — try again shortly"))
		return
	}

	attemptID := store.GenID("ppt")
	billing := cfg.billingEnabled(d)
	if billing {
		if _, err := store.ReserveCredits(r.Context(), d.DB, u.ID, cfg.CreditsPerPPT,
			docmeeAttemptSourceType, attemptID, docmeeReservationTTL); err != nil {
			if errors.Is(err, store.ErrInsufficientCredits) {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error":           errDocmeeInsufficient.Error(),
					"code":            "insufficient_credits",
					"credits_per_ppt": cfg.CreditsPerPPT,
				})
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"attempt_id":        attemptID,
		"expires_at":        time.Now().Add(docmeeReservationTTL).Unix(),
		"credits_per_ppt":   cfg.CreditsPerPPT,
		"credits_charged":   false,
		"credits_available": balance.Available,
	})
}

type docmeeChargeRequest struct {
	AttemptID string `json:"attempt_id"`
	PptID     string `json:"ppt_id"`
}

// meDocmeeChargeHandler settles one generation. The upstream PPT id becomes the
// billed key, so a replayed event (or a reloaded page re-reporting the same deck)
// can never debit twice — see store.SettleCreditReservationByKey.
func meDocmeeChargeHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body docmeeChargeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	attemptID := strings.TrimSpace(body.AttemptID)
	if attemptID == "" || len(attemptID) > docmeeMaxAttemptIDLen {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	cfg := docmeeConfigFor(d)
	if !cfg.billingEnabled(d) {
		writeJSON(w, 200, map[string]any{"credits": 0, "already_charged": false, "credits_per_ppt": 0})
		return
	}
	// Separate bucket from token/attempt: an SDK retry storm must not starve the
	// calls that open the next generation.
	if !rateLimitUser(d, u.ID, "ppt-charge", docmeeChargeRateLimit, time.Minute) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many AI PPT charge attempts — try again shortly"))
		return
	}
	finalID := strings.TrimSpace(body.PptID)
	if finalID == "" {
		// No upstream id (older SDK builds only send the attempt): bill the
		// attempt itself, which keeps the charge idempotent within the session.
		finalID = attemptID
	}
	if len(finalID) > docmeeMaxAttemptIDLen*2 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}

	debit, already, err := store.SettleCreditReservationByKey(r.Context(), d.DB, u.ID,
		docmeeAttemptSourceType, attemptID, docmeeAttemptSourceType, finalID, cfg.CreditsPerPPT)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": errDocmeeUnknownAttempt.Error(), "code": "unknown_attempt"})
		return
	case errors.Is(err, store.ErrCreditReservationReleased):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "generation hold expired", "code": "attempt_released"})
		return
	case errors.Is(err, store.ErrCreditReservationConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "charge already in flight", "code": "charge_conflict"})
		return
	case errors.Is(err, store.ErrInsufficientCredits):
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error": errDocmeeInsufficient.Error(), "code": "insufficient_credits",
		})
		return
	case errors.Is(err, store.ErrInvalidCreditAmount), errors.Is(err, store.ErrCreditReservationSourceID):
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"credits":           debit.Total,
		"already_charged":   already,
		"credits_per_ppt":   cfg.CreditsPerPPT,
		"credits_available": balance.Available,
	})
}

type docmeeReleaseRequest struct {
	AttemptID string `json:"attempt_id"`
}

// meDocmeeReleaseHandler refunds a hold whose generation failed or was
// abandoned. It is intentionally idempotent: an unknown or already-terminal hold
// reports released=false instead of failing.
func meDocmeeReleaseHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	var body docmeeReleaseRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	attemptID := strings.TrimSpace(body.AttemptID)
	if attemptID == "" || len(attemptID) > docmeeMaxAttemptIDLen {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	hold, err := store.LookupCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, 200, map[string]any{"released": false})
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// A hold id is not a capability: only its owner may refund it.
	if hold.UserID != u.ID {
		writeError(w, http.StatusForbidden, errPermissionDenied)
		return
	}
	if err := store.ReleaseCreditReservation(r.Context(), d.DB, docmeeAttemptSourceType, attemptID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	balance, err := store.GetCreditBalance(r.Context(), d.DB, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"released":          true,
		"credits_available": balance.Available,
	})
}
