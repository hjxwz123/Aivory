package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	UserHandle  []byte // credential's original WebAuthn identity when an account id was remapped
	Email       string
	DisplayName string
	Credentials []PasskeyCredential
}

func (u *PasskeyUser) WebAuthnID() []byte {
	if len(u.UserHandle) != 0 {
		return u.UserHandle
	}
	return []byte(u.ID)
}
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
	CredentialID       []byte
	PublicKey          []byte
	SignCount          uint32
	AuthenticatorFlags uint8
	FlagsKnown         bool
}

func (c *PasskeyCredential) toWebAuthn() webauthn.Credential {
	credential := webauthn.Credential{ID: c.CredentialID, PublicKey: c.PublicKey, Authenticator: webauthn.Authenticator{SignCount: c.SignCount}}
	if c.FlagsKnown {
		credential.Flags = webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(c.AuthenticatorFlags))
	}
	return credential
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

// Sentinel origin problems surfaced as explicit 400s (never a bare 500):
// the browser refuses WebAuthn outside a secure context, and go-webauthn
// refuses IP-address RP IDs entirely (localhost is the one exception).
var (
	ErrPasskeyInsecureOrigin  = errors.New("passkey_insecure_origin")
	ErrPasskeyHostUnsupported = errors.New("passkey_host_unsupported")
)

// rp constructs a per-request verifier bound to the external origin. With the
// single-container same-origin deployment this makes the RP ID the hostname
// the browser actually visits (dev: localhost via the Vite proxy; prod: the
// configured public host), so WebAuthn origin checks match the real client.
func (s *webauthnPasskeyService) rp(origin string) (*webauthn.WebAuthn, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(origin), "/"))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%w: invalid relying party origin %q", ErrPasskeyHostUnsupported, origin)
	}
	host := strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil {
		return nil, fmt.Errorf("%w: %q is an IP address; passkeys require a domain name (http://localhost is supported)", ErrPasskeyHostUnsupported, host)
	}
	if parsed.Scheme != "https" && host != "localhost" {
		return nil, fmt.Errorf("%w: passkeys require HTTPS or http://localhost, got origin %q", ErrPasskeyInsecureOrigin, origin)
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: s.displayName,
		RPID:          host,
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
	return passkeyCredentialFromWebAuthn(credential), nil
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
	parsed, err := protocol.ParseCredentialRequestResponseBytes(responseJSON)
	if err != nil {
		return nil, err
	}
	_, credential, err := w.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		user, lookupErr := lookup(rawID, userHandle)
		if lookupErr != nil || user == nil {
			return user, lookupErr
		}
		// Releases before authenticator_flags was persisted have no trustworthy
		// stored BE value. Bootstrap only the credential used by this assertion
		// from its signed authenticator data; ValidatePasskeyLogin still verifies
		// the signature, challenge, origin, RP ID and user handle before callers
		// persist the value. Known credentials always retain the immutable BE
		// comparison enforced by go-webauthn.
		for i := range user.Credentials {
			candidate := &user.Credentials[i]
			if !candidate.FlagsKnown && bytes.Equal(candidate.CredentialID, rawID) {
				candidate.AuthenticatorFlags = uint8(parsed.Response.AuthenticatorData.Flags)
				candidate.FlagsKnown = true
				break
			}
		}
		return user, nil
	}, *session, parsed)
	if err != nil {
		return nil, err
	}
	if credential == nil || len(credential.ID) == 0 {
		return nil, errors.New("empty credential")
	}
	return passkeyCredentialFromWebAuthn(credential), nil
}

func passkeyCredentialFromWebAuthn(credential *webauthn.Credential) *PasskeyCredential {
	if credential == nil {
		return nil
	}
	return &PasskeyCredential{
		CredentialID:       append([]byte(nil), credential.ID...),
		PublicKey:          append([]byte(nil), credential.PublicKey...),
		SignCount:          credential.Authenticator.SignCount,
		AuthenticatorFlags: uint8(credential.Flags.ProtocolValue()),
		FlagsKnown:         true,
	}
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
