package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"aivory/server/internal/store"
)

// End-to-end passkey flow against the REAL go-webauthn service using a software
// authenticator, so the browser wire format (clientDataJSON / authData /
// attestationObject / assertion signature) is exercised exactly as a device
// would produce it — a browserless regression net for registration + login.

const e2eOrigin = "https://app.example.test"
const e2eRPID = "app.example.test"

type softwareAuthenticator struct {
	key        *ecdsa.PrivateKey
	credential []byte
	signCount  uint32
}

func newSoftwareAuthenticator(t *testing.T) *softwareAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		t.Fatal(err)
	}
	return &softwareAuthenticator{key: key, credential: credential}
}

func b64u(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func (a *softwareAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))
	encoded, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (a *softwareAuthenticator) authData(t *testing.T, flags byte, includeAttested bool) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(e2eRPID))
	out := append([]byte{}, rpHash[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.signCount)
	if !includeAttested {
		return out
	}
	out = append(out, make([]byte, 16)...) // AAGUID
	out = binary.BigEndian.AppendUint16(out, uint16(len(a.credential)))
	out = append(out, a.credential...)
	out = append(out, a.coseKey(t)...)
	return out
}

func clientData(t *testing.T, ceremony, challenge string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": ceremony, "challenge": challenge, "origin": e2eOrigin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// registrationResponse produces the exact JSON a browser posts to finish.
func (a *softwareAuthenticator) registrationResponse(t *testing.T, challenge string) []byte {
	t.Helper()
	attestation, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": a.authData(t, 0x45, true), // UP | UV | AT
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"id":    b64u(a.credential),
		"rawId": b64u(a.credential),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64u(clientData(t, "webauthn.create", challenge)),
			"attestationObject": b64u(attestation),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (a *softwareAuthenticator) assertionResponse(t *testing.T, challenge, userHandle string) []byte {
	t.Helper()
	a.signCount++
	authenticatorData := a.authData(t, 0x05, false) // UP | UV
	clientJSON := clientData(t, "webauthn.get", challenge)
	digest := sha256.Sum256(clientJSON)
	signed := sha256.Sum256(append(append([]byte{}, authenticatorData...), digest[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"id":    b64u(a.credential),
		"rawId": b64u(a.credential),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64u(clientJSON),
			"authenticatorData": b64u(authenticatorData),
			"signature":         b64u(signature),
			"userHandle":        b64u([]byte(userHandle)),
		},
		"clientExtensionResults": map[string]any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}


func TestPasskeyEndToEndRegistrationAndLogin(t *testing.T) {
	d, _ := newPasskeyDeps(t)
	d.Passkeys = NewPasskeyService("Aivory")
	user := insertTestUser(t, d, "pk-e2e", "e2e@example.test", false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/me/passkeys/begin", bytes.NewReader([]byte(`{"name":"e2e device"}`)))
	req.Host = e2eRPID
	req.Header.Set("Origin", e2eOrigin)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	passkeyRegisterBeginHandler(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("begin: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &creation); err != nil {
		t.Fatal(err)
	}
	if creation.PublicKey.Challenge == "" || creation.PublicKey.User.ID == "" {
		t.Fatalf("incomplete creation options: %s", rec.Body.String())
	}

	authenticator := newSoftwareAuthenticator(t)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/me/passkeys/finish",
		bytes.NewReader(authenticator.registrationResponse(t, creation.PublicKey.Challenge)))
	req.Host = e2eRPID
	req.Header.Set("Origin", e2eOrigin)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	passkeyRegisterFinishHandler(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("finish: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := store.ListPasskeys(context.Background(), d.DB, "pk-e2e")
	if err != nil || len(rows) != 1 || rows[0].Name != "e2e device" {
		t.Fatalf("stored passkeys=%+v err=%v", rows, err)
	}

	// Login: discoverable assertion from the same software authenticator.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/passkey/begin", bytes.NewReader([]byte(`{}`)))
	req.Host = e2eRPID
	req.Header.Set("Origin", e2eOrigin)
	passkeyLoginBeginHandler(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("login begin: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var begin passkeyLoginBeginResp
	if err := json.Unmarshal(rec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	var assertionOptions struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(begin.Options, &assertionOptions); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"ticket":   begin.Ticket,
		"response": json.RawMessage(authenticator.assertionResponse(t, assertionOptions.PublicKey.Challenge, "pk-e2e")),
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/passkey/verify", bytes.NewReader(body))
	req.Host = e2eRPID
	req.Header.Set("Origin", e2eOrigin)
	passkeyLoginVerifyHandler(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("verify: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp authResp
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp.AccessToken == "" || resp.User == nil || resp.User.ID != "pk-e2e" {
		t.Fatalf("session response: %s", rec.Body.String())
	}
}