package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

// docmeeStubTransport answers the upstream createApiToken call without a
// network. calls counts how many tokens were actually minted, so a test can
// prove the session endpoint never reaches upstream when it should fail fast.
type docmeeStubTransport struct {
	calls   *int32
	token   string
	status  int
	payload string
}

func (s docmeeStubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.calls != nil {
		atomic.AddInt32(s.calls, 1)
	}
	if got := r.Header.Get("Api-Key"); got != "sk_test_key" {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"code":401,"message":"bad api key"}`)),
			Header:     http.Header{},
		}, nil
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	body := s.payload
	if body == "" {
		token := s.token
		if token == "" {
			token = "tok_stub"
		}
		body = `{"code":0,"message":"ok","data":{"token":"` + token + `"}}`
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

type docmeeFixture struct {
	deps  Deps
	db    *sql.DB
	user  *store.User
	calls *int32
}

// docmeeTestDeps seeds one credit-holding user plus whatever admin settings the
// case needs, and wires a stub upstream so no test touches the network.
func docmeeTestDeps(t *testing.T, allowance float64, settings map[string]any) docmeeFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db,
		`INSERT INTO user_groups(id,name,is_default,is_public,credit_allowance,credit_period_seconds) VALUES('ug_free','Free',1,1,?,86400)`,
		allowance)
	mustExec(t, db,
		`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor) VALUES('u1','docmee@example.test','hash','ug_free',?)`,
		time.Now().Unix()-60)
	// The settings cache is process-global and keyed by setting name, not by
	// database, so each fixture must drop what a previous test cached before it
	// reads its own freshly migrated schema. Seed writes invalidate the keys they
	// touch; this covers everything else (including negative cache entries).
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	for key, value := range settings {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	store.InvalidateConfig()
	calls := new(int32)
	return docmeeFixture{
		deps: Deps{
			DB:               db,
			Cache:            cache.NewMemory(),
			DocmeeHTTPClient: &http.Client{Transport: docmeeStubTransport{calls: calls}},
		},
		db:    db,
		user:  &store.User{ID: "u1", GroupID: "ug_free"},
		calls: calls,
	}
}

// docmeeRequest builds an authenticated request for the signed-in fixture user.
func docmeeRequest(t *testing.T, fixture docmeeFixture, method, path string, body any) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, fixture.user))
	return httptest.NewRecorder(), req
}

// docmeeBillingSettings is the minimal configuration that enables the iframe and
// per-deck credit billing: an upstream key plus the platform-wide credit rate.
func docmeeBillingSettings(extra map[string]any) map[string]any {
	settings := map[string]any{
		"docmee_api_key":         "sk_test_key",
		"credits_per_usd":        100.0,
		"docmee_credits_per_ppt": 10.0,
	}
	for key, value := range extra {
		settings[key] = value
	}
	return settings
}

func docmeeBalance(t *testing.T, db *sql.DB) store.CreditBalance {
	t.Helper()
	balance, err := store.GetCreditBalance(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return balance
}

func TestDocmeeTokenMintsOnceAndCaches(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/token", nil)
	meDocmeeTokenHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if minted.Token != "tok_stub" {
		t.Fatalf("token = %q, want the upstream token", minted.Token)
	}
	// Taking a token holds nothing: the balance is untouched until an attempt.
	if balance := docmeeBalance(t, fixture.db); balance.Available != 25 || balance.Reserved != 0 {
		t.Fatalf("balance after token = %v/%v, want 25/0", balance.Available, balance.Reserved)
	}
	rec, req = docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/token", nil)
	meDocmeeTokenHandler(fixture.deps, rec, req)
	if got := atomic.LoadInt32(fixture.calls); got != 1 {
		t.Fatalf("upstream token calls = %d, want 1 (second call served from cache)", got)
	}
}

func TestDocmeeAttemptHoldsCreditsAndChargeBillsTheDeckOnce(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))

	// First attempt: holds one deck's price.
	rec, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attempt status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var attempt struct {
		AttemptID        string  `json:"attempt_id"`
		CreditsPerPPT    float64 `json:"credits_per_ppt"`
		CreditsAvailable float64 `json:"credits_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &attempt); err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	if attempt.AttemptID == "" || attempt.CreditsPerPPT != 10 || attempt.CreditsAvailable != 15 {
		t.Fatalf("attempt = %+v, want an id, price 10 and 15 available (10 held from 25)", attempt)
	}

	charge, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": attempt.AttemptID, "ppt_id": "deck-1"})
	meDocmeeChargeHandler(fixture.deps, charge, req)
	if charge.Code != http.StatusOK {
		t.Fatalf("charge status = %d, want 200; body=%s", charge.Code, charge.Body.String())
	}
	var charged struct {
		Credits          float64 `json:"credits"`
		AlreadyCharged   bool    `json:"already_charged"`
		CreditsAvailable float64 `json:"credits_available"`
	}
	if err := json.Unmarshal(charge.Body.Bytes(), &charged); err != nil {
		t.Fatalf("decode charge: %v", err)
	}
	if charged.Credits != 10 || charged.AlreadyCharged || charged.CreditsAvailable != 15 {
		t.Fatalf("charge = %+v, want 10 credits charged with 15 left", charged)
	}
	// Replaying the same charge for the same attempt is idempotent.
	replay, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": attempt.AttemptID, "ppt_id": "deck-1"})
	meDocmeeChargeHandler(fixture.deps, replay, req)
	if err := json.Unmarshal(replay.Body.Bytes(), &charged); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if !charged.AlreadyCharged || charged.CreditsAvailable != 15 {
		t.Fatalf("replay = %+v, want already charged with an unchanged balance", charged)
	}

	// A reloaded page opens a fresh attempt and reports the SAME deck: that must
	// not bill twice, and its own hold must be refunded.
	rec, req = docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	var second struct {
		AttemptID        string  `json:"attempt_id"`
		CreditsAvailable float64 `json:"credits_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second attempt: %v", err)
	}
	if second.AttemptID == attempt.AttemptID {
		t.Fatalf("second attempt reused id %q", second.AttemptID)
	}
	if second.CreditsAvailable != 5 {
		t.Fatalf("second attempt available = %v, want 5 (10 charged plus 10 held)", second.CreditsAvailable)
	}
	duplicate, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": second.AttemptID, "ppt_id": "deck-1"})
	meDocmeeChargeHandler(fixture.deps, duplicate, req)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate charge status = %d, want 200; body=%s", duplicate.Code, duplicate.Body.String())
	}
	if err := json.Unmarshal(duplicate.Body.Bytes(), &charged); err != nil {
		t.Fatalf("decode duplicate charge: %v", err)
	}
	if !charged.AlreadyCharged || charged.Credits != 10 || charged.CreditsAvailable != 15 {
		t.Fatalf("duplicate charge = %+v, want already charged, one 10-credit debit, 15 available", charged)
	}

	// Explicit release refunds a hold that will never settle.
	rec, req = docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	var third struct {
		AttemptID        string  `json:"attempt_id"`
		CreditsAvailable float64 `json:"credits_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &third); err != nil {
		t.Fatalf("decode third attempt: %v", err)
	}
	if third.CreditsAvailable != 5 {
		t.Fatalf("third attempt available = %v, want 5 after the second hold", third.CreditsAvailable)
	}
	release, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/release",
		map[string]string{"attempt_id": third.AttemptID})
	meDocmeeReleaseHandler(fixture.deps, release, req)
	if release.Code != http.StatusOK {
		t.Fatalf("release status = %d, want 200; body=%s", release.Code, release.Body.String())
	}
	var released struct {
		Released         bool    `json:"released"`
		CreditsAvailable float64 `json:"credits_available"`
	}
	if err := json.Unmarshal(release.Body.Bytes(), &released); err != nil {
		t.Fatalf("decode release: %v", err)
	}
	if !released.Released || released.CreditsAvailable != 15 {
		t.Fatalf("release = %+v, want refunded to 15", released)
	}
}

func TestDocmeeAttemptFailsClosedWithoutCredits(t *testing.T) {
	fixture := docmeeTestDeps(t, 5, docmeeBillingSettings(nil))
	rec, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "insufficient_credits" {
		t.Fatalf("code = %q, want insufficient_credits", body.Code)
	}
	if got := atomic.LoadInt32(fixture.calls); got != 0 {
		t.Fatalf("upstream token calls = %d, want 0 (an attempt must not mint)", got)
	}
	balance := docmeeBalance(t, fixture.db)
	if balance.Reserved != 0 || balance.Available != 5 {
		t.Fatalf("balance = reserved %v available %v, want 0/5", balance.Reserved, balance.Available)
	}
}

func TestDocmeeEndpointsRequireConfiguration(t *testing.T) {
	// No key at all: the enable switch follows the key's presence.
	fixture := docmeeTestDeps(t, 100, map[string]any{"credits_per_usd": 100.0})
	for name, call := range map[string]func(Deps, http.ResponseWriter, *http.Request){
		"token":   meDocmeeTokenHandler,
		"attempt": meDocmeeAttemptHandler,
	} {
		rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/"+name, nil)
		call(fixture.deps, rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503; body=%s", name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "not configured") {
			t.Fatalf("%s body = %s, want a configuration hint", name, rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(fixture.calls); got != 0 {
		t.Fatalf("upstream token calls = %d, want 0", got)
	}

	// Key present but explicitly disabled: 503 with the disabled message.
	disabled := docmeeTestDeps(t, 100, map[string]any{
		"docmee_api_key":  "sk_test_key",
		"docmee_enabled":  false,
		"credits_per_usd": 100.0,
	})
	rec, req := docmeeRequest(t, disabled, http.MethodGet, "/api/me/ppt/token", nil)
	meDocmeeTokenHandler(disabled.deps, rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("disabled body = %s, want the disabled message", rec.Body.String())
	}
}

func TestDocmeeConfigReportsPriceWithoutSecrets(t *testing.T) {
	fixture := docmeeTestDeps(t, 40, docmeeBillingSettings(nil))
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/config", nil)
	meDocmeeConfigHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["enabled"] != true || body["credits_enabled"] != true {
		t.Fatalf("config flags = %v, want enabled plus credit-billed", body)
	}
	if body["credits_per_ppt"] != float64(10) || body["credits_available"] != float64(40) {
		t.Fatalf("config pricing = %v/%v, want 10/40", body["credits_per_ppt"], body["credits_available"])
	}
	if body["sdk_url"] == "" || body["creator_version"] != "v2" {
		t.Fatalf("config sdk = %v version = %v, want a pinned SDK URL and v2", body["sdk_url"], body["creator_version"])
	}
	for _, secret := range []string{"api_key", "docmee_api_key", "api_secret", "docmee_api_secret", "token"} {
		if _, present := body[secret]; present {
			t.Fatalf("config leaked %q: %s", secret, rec.Body.String())
		}
	}
}

func TestDocmeeBillingIsFreeWhenCreditsAreOff(t *testing.T) {
	// Without credits_per_usd the platform credit system is off, so a generation
	// must neither hold nor charge anything.
	fixture := docmeeTestDeps(t, 25, map[string]any{"docmee_api_key": "sk_test_key"})
	rec, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/attempt", nil)
	meDocmeeAttemptHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var attempt struct {
		AttemptID     string  `json:"attempt_id"`
		CreditsPerPPT float64 `json:"credits_per_ppt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &attempt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	charge, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": attempt.AttemptID, "ppt_id": "deck-free"})
	meDocmeeChargeHandler(fixture.deps, charge, req)
	if charge.Code != http.StatusOK {
		t.Fatalf("charge status = %d, want 200; body=%s", charge.Code, charge.Body.String())
	}
	var charged struct {
		Credits float64 `json:"credits"`
	}
	if err := json.Unmarshal(charge.Body.Bytes(), &charged); err != nil {
		t.Fatalf("decode charge: %v", err)
	}
	if charged.Credits != 0 {
		t.Fatalf("credits charged = %v, want 0 when the credit system is off", charged.Credits)
	}
	if balance := docmeeBalance(t, fixture.db); balance.Available != 25 {
		t.Fatalf("available = %v, want untouched 25", balance.Available)
	}
}

func TestDocmeeChargeAndReleaseGuardForeignWork(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))
	mustExec(t, fixture.db,
		`INSERT INTO users(id,email,password_hash,group_id,credit_cycle_anchor) VALUES('u2','other@example.test','hash','ug_free',?)`,
		time.Now().Unix()-60)
	hold, err := store.ReserveCredits(context.Background(), fixture.db, "u2", 10, docmeeAttemptSourceType, "ppt_foreign", docmeeReservationTTL)
	if err != nil {
		t.Fatalf("reserve foreign: %v", err)
	}

	release, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/release",
		map[string]string{"attempt_id": hold.SourceID})
	meDocmeeReleaseHandler(fixture.deps, release, req)
	if release.Code != http.StatusForbidden {
		t.Fatalf("foreign release status = %d, want 403; body=%s", release.Code, release.Body.String())
	}

	charge, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": hold.SourceID, "ppt_id": "deck-foreign"})
	meDocmeeChargeHandler(fixture.deps, charge, req)
	if charge.Code != http.StatusConflict {
		t.Fatalf("foreign charge status = %d, want 409; body=%s", charge.Code, charge.Body.String())
	}

	unknown, req := docmeeRequest(t, fixture, http.MethodPost, "/api/me/ppt/charge",
		map[string]string{"attempt_id": "ppt_missing", "ppt_id": "deck-x"})
	meDocmeeChargeHandler(fixture.deps, unknown, req)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown attempt status = %d, want 404; body=%s", unknown.Code, unknown.Body.String())
	}

	// The foreign hold is still live (the rejections must not release it either).
	if balance, err := store.GetCreditBalance(context.Background(), fixture.db, "u1"); err != nil {
		t.Fatalf("balance: %v", err)
	} else if balance.TimedRemaining != 25 || balance.Available != 25 {
		t.Fatalf("balance = remaining %v available %v, want untouched 25/25", balance.TimedRemaining, balance.Available)
	}
	if foreign, err := store.GetCreditBalance(context.Background(), fixture.db, "u2"); err != nil {
		t.Fatalf("foreign balance: %v", err)
	} else if foreign.Reserved != 10 {
		t.Fatalf("foreign reserved = %v, want the hold left in place (10)", foreign.Reserved)
	}
}

func TestDocmeeTokenReportsUpstreamFailureWithoutLeakingDetail(t *testing.T) {
	fixture := docmeeTestDeps(t, 25, docmeeBillingSettings(nil))
	fixture.deps.DocmeeHTTPClient = &http.Client{Transport: docmeeStubTransport{
		status:  http.StatusInternalServerError,
		payload: `{"code":500,"message":"upstream exploded with sk_test_key"}`,
	}}
	rec, req := docmeeRequest(t, fixture, http.MethodGet, "/api/me/ppt/token", nil)
	meDocmeeTokenHandler(fixture.deps, rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	// The upstream message can quote the admin's key, so it must not reach the
	// browser — only the typed, retryable error.
	if strings.Contains(rec.Body.String(), "sk_test_key") || strings.Contains(rec.Body.String(), "exploded") {
		t.Fatalf("body leaked upstream detail: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_error") {
		t.Fatalf("body = %s, want a typed upstream_error code", rec.Body.String())
	}
	// Nothing was held, so the balance is untouched.
	if balance := docmeeBalance(t, fixture.db); balance.Reserved != 0 || balance.Available != 25 {
		t.Fatalf("balance = reserved %v available %v, want 0/25", balance.Reserved, balance.Available)
	}
}

// The admin UI saves the whole Docmee block in one PATCH and then reads it back.
// A round trip must survive: the API key stays stored (masked on read), and the
// enable flag the admin chose is what the runtime resolves afterwards.
func TestDocmeeAdminSettingsRoundTripKeepsTheIntegrationEnabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	d := Deps{DB: db}

	write := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	rec := write(`{
		"credits_per_usd": 100,
		"docmee_enabled": true,
		"docmee_api_key": "sk_live_key",
		"docmee_credits_per_ppt": 10,
		"docmee_creator_version": "v2",
		"docmee_api_base_url": "https://docmee.cn",
		"docmee_sdk_url": "https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js",
		"docmee_token_hours": 2
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings PATCH status = %d; body=%s", rec.Code, rec.Body.String())
	}
	// The runtime config immediately reflects the saved values.
	cfg := docmeeConfigFor(d)
	if !cfg.Enabled || cfg.APIKey != "sk_live_key" || cfg.CreditsPerPPT != 10 {
		t.Fatalf("resolved config = %+v, want enabled with the stored key and price", cfg)
	}

	// Reading settings back is the reload path: the flag stays true and the key
	// comes back masked (never in plaintext).
	getRec := httptest.NewRecorder()
	adminSettingsGet(d, getRec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("settings GET status = %d", getRec.Code)
	}
	var stored map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if stored["docmee_enabled"] != true {
		t.Fatalf("reloaded docmee_enabled = %v, want true", stored["docmee_enabled"])
	}
	if stored["docmee_api_key"] != "••••••" {
		t.Fatalf("reloaded docmee_api_key = %v, want the display mask", stored["docmee_api_key"])
	}
	if stored["docmee_credits_per_ppt"] != float64(10) {
		t.Fatalf("reloaded credits per deck = %v, want 10", stored["docmee_credits_per_ppt"])
	}

	// A second save that echoes the mask (what the UI does) must not clear the key
	// or flip the flag.
	rec = write(`{"docmee_enabled": true, "docmee_api_key": "••••••"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("masked re-save status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); !cfg.Enabled || cfg.APIKey != "sk_live_key" {
		t.Fatalf("config after masked re-save = %+v, want unchanged key + enabled", cfg)
	}

	// Clearing the key turns the feature off even with the flag left true.
	rec = write(`{"docmee_api_key": ""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear-key status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); cfg.configured() {
		t.Fatalf("config after clearing the key = %+v, want unconfigured", cfg)
	}
}
// Admin-entered URLs are normalized rather than rejected, because a rejected
// PATCH would also silently drop the enable flag saved in the same request.
func TestDocmeeAdminSettingsNormalizesURLs(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "docmee-urls.db"))
	t.Cleanup(func() { _ = db.Close() })
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	d := Deps{DB: db}

	write := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	// Bare host: accepted, scheme filled in, and the enable flag in the same
	// request is applied.
	rec := write(`{"docmee_enabled": true, "docmee_api_key": "sk_live_key", "docmee_domain": "app.xpptx.com", "docmee_api_base_url": "docmee.cn"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bare-host PATCH status = %d; body=%s", rec.Code, rec.Body.String())
	}
	cfg := docmeeConfigFor(d)
	if cfg.Domain != "https://app.xpptx.com" {
		t.Fatalf("docmee_domain = %q, want https://app.xpptx.com", cfg.Domain)
	}
	if cfg.APIBaseURL != "https://docmee.cn" {
		t.Fatalf("docmee_api_base_url = %q, want https://docmee.cn", cfg.APIBaseURL)
	}
	if !cfg.Enabled {
		t.Fatalf("enable flag did not survive the same PATCH: %+v", cfg)
	}

	// Junk is still refused, and the failed patch changes nothing.
	rec = write(`{"docmee_sdk_url": "not a url"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("junk URL status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if cfg = docmeeConfigFor(d); cfg.SDKURL != docmeeDefaultSDKURL {
		t.Fatalf("docmee_sdk_url = %q, want the pinned default left untouched", cfg.SDKURL)
	}
}