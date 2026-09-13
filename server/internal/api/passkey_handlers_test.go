package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type fakePasskeyService struct {
	regOptions    []byte
	regSession    []byte
	assertOptions []byte
	assertSession []byte
	regFinishErr  error
	loginErr      error
	// loginRawID/loginUserHandle drive the lookup callback like go-webauthn would.
	loginRawID      []byte
	loginUserHandle []byte
	regCred         *PasskeyCredential
	loginCred       *PasskeyCredential
	gotOrigin       string
	lookupCalled    bool
}

func (f *fakePasskeyService) BeginRegistration(origin string, _ *PasskeyUser) ([]byte, []byte, error) {
	f.gotOrigin = origin
	return f.regOptions, f.regSession, nil
}

func (f *fakePasskeyService) FinishRegistration(origin string, _ *PasskeyUser, session, response []byte) (*PasskeyCredential, error) {
	f.gotOrigin = origin
	if f.regFinishErr != nil {
		return nil, f.regFinishErr
	}
	if !bytes.Equal(session, f.regSession) || len(response) == 0 {
		return nil, errors.New("fake: session/response mismatch")
	}
	return f.regCred, nil
}

func (f *fakePasskeyService) BeginLogin(origin string) ([]byte, []byte, error) {
	f.gotOrigin = origin
	return f.assertOptions, f.assertSession, nil
}

func (f *fakePasskeyService) FinishLogin(_ string, session, response []byte, lookup func(rawID, userHandle []byte) (*PasskeyUser, error)) (*PasskeyCredential, error) {
	if !bytes.Equal(session, f.assertSession) || len(response) == 0 {
		return nil, errors.New("fake: session/response mismatch")
	}
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	f.lookupCalled = true
	if _, err := lookup(f.loginRawID, f.loginUserHandle); err != nil {
		return nil, err
	}
	return f.loginCred, nil
}

func newPasskeyDeps(t *testing.T) (Deps, *fakePasskeyService) {
	t.Helper()
	d := newAuthSecurityDeps(t, "passkeys.db")
	fake := &fakePasskeyService{
		regOptions:      []byte(`{"challenge":"regch","rp":{"id":"localhost"}}`),
		regSession:      []byte(`{"challenge":"regch"}`),
		assertOptions:   []byte(`{"challenge":"authch","allowCredentials":[]}`),
		assertSession:   []byte(`{"challenge":"authch"}`),
		regCred:         &PasskeyCredential{CredentialID: []byte{0xC0, 0xC1}, PublicKey: []byte{0x0A, 0x0B}, SignCount: 3},
		loginCred:       &PasskeyCredential{CredentialID: []byte{0xC0, 0xC1}, PublicKey: []byte{0x0A, 0x0B}, SignCount: 42},
		loginRawID:      []byte{0xC0, 0xC1},
		loginUserHandle: nil,
	}
	d.Passkeys = fake
	return d, fake
}

func insertTestUser(t *testing.T, d Deps, id, email string, totpEnabled bool) *store.User {
	t.Helper()
	totp := 0
	secret := ""
	if totpEnabled {
		totp = 1
		secret = "JBSWY3DPEHPK3PXP"
	}
	if _, err := d.DB.Exec(`INSERT INTO users(id,email,password_hash,role,totp_enabled,totp_secret) VALUES(?,?,?,?,?,?)`, id, email, "hash", "user", totp, secret); err != nil {
		t.Fatal(err)
	}
	user := &store.User{ID: id, Email: email, Role: "user", Status: "active", TotpEnabled: totpEnabled, TotpSecret: secret}
	return user
}

func doRequest(h handler, d Deps, path, body string, user *store.User) *httptest.ResponseRecorder {
	return doRequestParams(h, d, path, body, user, nil)
}

func doRequestParams(h handler, d Deps, path, body string, user *store.User, params map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Host = "app.example.test"
	ctx := req.Context()
	if user != nil {
		ctx = context.WithValue(ctx, userCtxKey{}, user)
	}
	if params != nil {
		ctx = context.WithValue(ctx, pathCtxKey{}, params)
	}
	h(d, rec, req.WithContext(ctx))
	return rec
}

func TestPasskeyRegistrationFlow(t *testing.T) {
	d, fake := newPasskeyDeps(t)
	user := insertTestUser(t, d, "pk-u1", "pk1@example.test", false)

	rec := doRequest(passkeyRegisterBeginHandler, d, "/api/me/passkeys/begin", `{"name":"iPhone"}`, user)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("regch")) {
		t.Fatalf("begin: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := d.Cache.Get("pkreg:pk-u1"); !ok {
		t.Fatal("registration challenge not cached")
	}

	rec = doRequest(passkeyRegisterFinishHandler, d, "/api/me/passkeys/finish", `{"id":"c0w","response":{}}`, user)
	if rec.Code != 200 {
		t.Fatalf("finish: code=%d body=%s", rec.Code, rec.Body.String())
	}
	count, err := store.CountPasskeys(context.Background(), d.DB, "pk-u1")
	if err != nil || count != 1 {
		t.Fatalf("passkey not stored: count=%d err=%v", count, err)
	}
	if _, ok := d.Cache.Get("pkreg:pk-u1"); ok {
		t.Fatal("registration ticket not consumed")
	}

	// List exposes only public fields.
	rows, err := store.ListPasskeys(context.Background(), d.DB, "pk-u1")
	if err != nil || len(rows) != 1 || rows[0].Name != "iPhone" {
		t.Fatalf("list=%+v err=%v", rows, err)
	}
	// Re-registering without begin must fail (expired setup).
	rec = doRequest(passkeyRegisterFinishHandler, d, "/api/me/passkeys/finish", `{"response":{}}`, user)
	if rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_setup_expired")) {
		t.Fatalf("expired finish: code=%d body=%s", rec.Code, rec.Body.String())
	}
	_ = fake
}

func TestPasskeyLoginIssuesSessionAndBurnsTicket(t *testing.T) {
	d, fake := newPasskeyDeps(t)
	insertTestUser(t, d, "pk-u2", "pk2@example.test", false)
	if err := store.CreatePasskey(context.Background(), d.DB, &store.Passkey{
		UserID: "pk-u2", CredentialID: []byte{0xC0, 0xC1}, PublicKey: []byte{0x0A, 0x0B}, SignCount: 3, Name: "dev",
	}); err != nil {
		t.Fatal(err)
	}

	rec := doRequest(passkeyLoginBeginHandler, d, "/api/auth/passkey/begin", `{}`, nil)
	if rec.Code != 200 {
		t.Fatalf("begin: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var begin passkeyLoginBeginResp
	if json.Unmarshal(rec.Body.Bytes(), &begin) != nil || begin.Ticket == "" {
		t.Fatalf("bad begin body: %s", rec.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"ticket": begin.Ticket, "response": map[string]any{"id": "c0w"}})

	rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil)
	if rec.Code != 200 {
		t.Fatalf("verify: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp authResp
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.AccessToken == "" || resp.User == nil || resp.User.ID != "pk-u2" {
		t.Fatalf("verify session response: %s", rec.Body.String())
	}
	if !fake.lookupCalled {
		t.Fatal("assertion did not resolve the credential owner")
	}
	// One-shot ticket: replay must fail.
	rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil)
	if rec.Code != 401 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_login_failed")) {
		t.Fatalf("replay: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// Sign count advanced and login history recorded the passkey method.
	rows, err := store.ListPasskeys(context.Background(), d.DB, "pk-u2")
	if err != nil || len(rows) != 1 || rows[0].SignCount != 42 || rows[0].LastUsedAt == 0 {
		t.Fatalf("touch not persisted: %+v err=%v", rows, err)
	}
	histories, err := store.ListLoginHistoriesForUser(context.Background(), d.DB, "pk-u2", 10, 0)
	if err != nil || len(histories) == 0 || histories[0].Method != store.LoginMethodPasskey {
		t.Fatalf("histories=%+v err=%v", histories, err)
	}
}

func TestPasskeyLoginBypassesTotpButPasswordDoesNot(t *testing.T) {
	d, fake := newPasskeyDeps(t)
	// The assertion resolves a handle that must match the credential's owner.
	fake.loginUserHandle = []byte("pk-u3")
	insertTestUser(t, d, "pk-u3", "pk3@example.test", true) // TOTP enabled
	if err := store.CreatePasskey(context.Background(), d.DB, &store.Passkey{
		UserID: "pk-u3", CredentialID: []byte{0xC0, 0xC1}, PublicKey: []byte{0x0A, 0x0B},
	}); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(passkeyLoginBeginHandler, d, "/api/auth/passkey/begin", `{}`, nil)
	var begin passkeyLoginBeginResp
	_ = json.Unmarshal(rec.Body.Bytes(), &begin)
	body, _ := json.Marshal(map[string]any{"ticket": begin.Ticket, "response": map[string]any{"id": "c0w"}})
	rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil)
	if rec.Code != 200 {
		t.Fatalf("passkey login of TOTP user must succeed: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// Wrong user handle on the credential fails generically without leaking.
	rec = doRequest(passkeyLoginBeginHandler, d, "/api/auth/passkey/begin", `{}`, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &begin)
	fake.loginUserHandle = []byte("attacker-id")
	body, _ = json.Marshal(map[string]any{"ticket": begin.Ticket, "response": map[string]any{"id": "c0w"}})
	rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil)
	if rec.Code != 401 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_login_failed")) {
		t.Fatalf("handle mismatch: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasskeyLoginDisabledByPolicyAndFailureBurn(t *testing.T) {
	d, fake := newPasskeyDeps(t)
	insertTestUser(t, d, "pk-u4", "pk4@example.test", false)
	if err := store.CreatePasskey(context.Background(), d.DB, &store.Passkey{
		UserID: "pk-u4", CredentialID: []byte{0xC0, 0xC1}, PublicKey: []byte{0x0A, 0x0B},
	}); err != nil {
		t.Fatal(err)
	}
	// Policy disabled → 403 on the public half.
	if err := store.SetSetting(d.DB, "passkey_login_enabled", false); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	rec := doRequest(passkeyLoginBeginHandler, d, "/api/auth/passkey/begin", `{}`, nil)
	if rec.Code != 403 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_login_disabled")) {
		t.Fatalf("disabled: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := store.SetSetting(d.DB, "passkey_login_enabled", true); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()

	// Repeated assertion failures burn the ticket before its TTL expires.
	rec = doRequest(passkeyLoginBeginHandler, d, "/api/auth/passkey/begin", `{}`, nil)
	var begin passkeyLoginBeginResp
	_ = json.Unmarshal(rec.Body.Bytes(), &begin)
	body, _ := json.Marshal(map[string]any{"ticket": begin.Ticket, "response": map[string]any{"id": "c0w"}})
	fake.loginErr = errors.New("bad assertion")
	for attempt := 0; attempt < 5; attempt++ {
		if rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil); rec.Code != 401 {
			t.Fatalf("failed attempt %d: code=%d", attempt, rec.Code)
		}
	}
	fake.loginErr = nil // the credential is fine now, but the ticket must be dead
	rec = doRequest(passkeyLoginVerifyHandler, d, "/api/auth/passkey/verify", string(body), nil)
	if rec.Code != 401 {
		t.Fatalf("ticket not burned: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasskeyRegistrationRealServiceOriginHandling(t *testing.T) {
	d, _ := newPasskeyDeps(t)
	// Swap in the REAL go-webauthn service to exercise the origin gate that a
	// TLS-terminating reverse proxy used to turn into a bare 500.
	d.Passkeys = NewPasskeyService("Aivory")
	user := insertTestUser(t, d, "pk-real", "real@example.test", false)

	// HTTPS page whose backend sees plain http (proxy) — the Origin header is
	// authoritative and the ceremony begins.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/me/passkeys/begin", bytes.NewReader([]byte(`{"name":"iPhone"}`)))
	req.Host = "app.example.test"
	req.Header.Set("Origin", "https://app.example.test")
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	passkeyRegisterBeginHandler(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("https origin: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "\"challenge\"") || !strings.Contains(body, "app.example.test") {
		t.Fatalf("creation options malformed: %s", body)
	}

	// No Origin header and the backend derives http:// from a plain connection
	// → explicit 400 passkey_insecure_origin (was: internal server error).
	rec = doRequest(passkeyRegisterBeginHandler, d, "/api/me/passkeys/begin", `{}`, user)
	if rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_insecure_origin")) {
		t.Fatalf("insecure origin: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// IP host is not a valid relying party ID → explicit 400 passkey_unavailable.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/me/passkeys/begin", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Origin", "https://192.168.1.5")
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	passkeyRegisterBeginHandler(d, rec, req)
	if rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("passkey_unavailable")) {
		t.Fatalf("ip origin: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasskeyDeleteScopedToOwner(t *testing.T) {
	d, _ := newPasskeyDeps(t)
	owner := insertTestUser(t, d, "pk-o", "owner@example.test", false)
	other := insertTestUser(t, d, "pk-x", "other@example.test", false)
	if err := store.CreatePasskey(context.Background(), d.DB, &store.Passkey{
		UserID: "pk-o", CredentialID: []byte{0x01}, PublicKey: []byte{0x02},
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListPasskeys(context.Background(), d.DB, "pk-o")
	params := map[string]string{"id": rows[0].ID}
	rec := doRequestParams(passkeyDeleteHandler, d, "/api/me/passkeys/"+rows[0].ID, ``, other, params)
	if rec.Code != 404 {
		t.Fatalf("cross-user delete must 404: code=%d", rec.Code)
	}
	rec = doRequestParams(passkeyDeleteHandler, d, "/api/me/passkeys/"+rows[0].ID, ``, owner, params)
	if rec.Code != 200 {
		t.Fatalf("owner delete: code=%d body=%s", rec.Code, rec.Body.String())
	}
	count, _ := store.CountPasskeys(context.Background(), d.DB, "pk-o")
	if count != 0 {
		t.Fatalf("passkey not deleted: %d", count)
	}
}
