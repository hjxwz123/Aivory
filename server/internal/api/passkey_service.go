package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// PasskeyService abstracts the WebAuthn ceremony state machine so handlers can
// be tested with a fake and the go-webauthn dependency stays in one file.
// Options/session are exchanged as JSON; `origin` is the caller-visible scheme
// + host (+ port) that binds RP ID verification to the deployment.
type PasskeyService interface {
	// BeginRegistration returns (creationOptionsJSON, sessionJSON).
	BeginRegistration(origin string, user *PasskeyUser) ([]byte, []byte, error)
	// FinishRegistration verifies the attestation response against the session
	// and returns the credential record to persist.
	FinishRegistration(origin string, user *PasskeyUser, sessionJSON, responseJSON []byte) (*PasskeyCredential, error)
	// BeginLogin starts a discoverable (usernameless) assertion: empty
	// allowList, user verification required.
	BeginLogin(origin string) ([]byte, []byte, error)
	// FinishLogin verifies the assertion; lookup resolves the credential owner
	// from the raw credential id / user handle and must return the user with
	// ALL of their stored credentials so the library can match and verify.
	FinishLogin(origin string, sessionJSON, responseJSON []byte, lookup func(rawID, userHandle []byte) (*PasskeyUser, error)) (*PasskeyCredential, error)
}

// PasskeyUser adapts an Aivory account to the go-webauthn user contract.
// Credentials stay in this package's own shape so handlers never touch
// go-webauthn types.
type PasskeyUser struct {
	ID          string
	Email       string
	DisplayName string
	Credentials []PasskeyCredential
}

func (u *PasskeyUser) WebAuthnID() []byte          { return []byte(u.ID) }
func (u *PasskeyUser) WebAuthnName() string        { return u.Email }
func (u *PasskeyUser) WebAuthnDisplayName() string { return firstNonEmpty(u.DisplayName, u.Email) }
func (u *PasskeyUser) WebAuthnCredentials() []webauthn.Credential {
	creds := make([]webauthn.Credential, 0, len(u.Credentials))
	for _, c := range u.Credentials {
		creds = append(creds, c.toWebAuthn())
	}
	return creds
}

// PasskeyCredential is the persistable result of a ceremony.
type PasskeyCredential struct {
	CredentialID []byte
	PublicKey    []byte
	SignCount    uint32
}

func (c *PasskeyCredential) toWebAuthn() webauthn.Credential {
	return webauthn.Credential{ID: c.CredentialID, PublicKey: c.PublicKey, Authenticator: webauthn.Authenticator{SignCount: c.SignCount}}
}

type webauthnPasskeyService struct {
	displayName string
}

// NewPasskeyService builds the production go-webauthn-backed service.
func NewPasskeyService(displayName string) PasskeyService {
	if strings.TrimSpace(displayName) == "" {
		displayName = "Aivory"
	}
	return &webauthnPasskeyService{displayName: displayName}
}

// rp constructs a per-request verifier bound to the external origin. With the
// single-container same-origin deployment this makes the RP ID the hostname
// the browser actually visits (dev: localhost via the Vite proxy; prod: the
// configured public host), so WebAuthn origin checks match the real client.
func (s *webauthnPasskeyService) rp(origin string) (*webauthn.WebAuthn, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid relying party origin %q", origin)
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("insecure relying party origin %q", origin)
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: s.displayName,
		RPID:          strings.ToLower(parsed.Hostname()),
		RPOrigins:     []string{fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)},
	})
}

func webAuthnUser(user *PasskeyUser) *PasskeyUser {
	if user == nil {
		return &PasskeyUser{}
	}
	return user
}

func (s *webauthnPasskeyService) BeginRegistration(origin string, user *PasskeyUser) ([]byte, []byte, error) {
	w, err := s.rp(origin)
	if err != nil {
		return nil, nil, err
	}
	u := webAuthnUser(user)
	creation, session, err := w.BeginRegistration(
		u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		// Exclude the user's existing credentials so a device never stores
		// two passkeys for the same account under one credential id.
		webauthn.WithExclusions(webauthn.Credentials(u.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		return nil, nil, err
	}
	return passkeyJSON(creation), passkeyJSON(session), nil
}

func (s *webauthnPasskeyService) FinishRegistration(origin string, user *PasskeyUser, sessionJSON, responseJSON []byte) (*PasskeyCredential, error) {
	w, err := s.rp(origin)
	if err != nil {
		return nil, err
	}
	session, err := decodePasskeySession(sessionJSON)
	if err != nil {
		return nil, err
	}
	credential, err := w.FinishRegistration(webAuthnUser(user), *session, jsonAssertionRequest(responseJSON))
	if err != nil {
		return nil, err
	}
	if credential == nil || len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
		return nil, errors.New("empty credential")
	}
	return &PasskeyCredential{CredentialID: append([]byte(nil), credential.ID...), PublicKey: append([]byte(nil), credential.PublicKey...), SignCount: credential.Authenticator.SignCount}, nil
}

func (s *webauthnPasskeyService) BeginLogin(origin string) ([]byte, []byte, error) {
	w, err := s.rp(origin)
	if err != nil {
		return nil, nil, err
	}
	assertion, session, err := w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, nil, err
	}
	return passkeyJSON(assertion), passkeyJSON(session), nil
}

func (s *webauthnPasskeyService) FinishLogin(origin string, sessionJSON, responseJSON []byte, lookup func(rawID, userHandle []byte) (*PasskeyUser, error)) (*PasskeyCredential, error) {
	w, err := s.rp(origin)
	if err != nil {
		return nil, err
	}
	session, err := decodePasskeySession(sessionJSON)
	if err != nil {
		return nil, err
	}
	if lookup == nil {
		return nil, errors.New("passkey lookup required")
	}
	_, credential, err := w.FinishPasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		return lookup(rawID, userHandle)
	}, *session, jsonAssertionRequest(responseJSON))
	if err != nil {
		return nil, err
	}
	if credential == nil || len(credential.ID) == 0 {
		return nil, errors.New("empty credential")
	}
	return &PasskeyCredential{CredentialID: append([]byte(nil), credential.ID...), PublicKey: append([]byte(nil), credential.PublicKey...), SignCount: credential.Authenticator.SignCount}, nil
}

func decodePasskeySession(raw []byte) (*webauthn.SessionData, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	if session.Challenge == "" {
		return nil, errors.New("expired passkey challenge")
	}
	return &session, nil
}

// jsonAssertionRequest replays a stored JSON body through the *http.Request
// shape go-webauthn's Finish* methods expect.
func jsonAssertionRequest(body []byte) *http.Request {
	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if err != nil {
		panic("passkey: cannot build synthetic request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func passkeyJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("passkey: cannot marshal options: " + err.Error())
	}
	return raw
}
