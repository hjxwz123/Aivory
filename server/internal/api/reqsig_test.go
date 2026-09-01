package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

const requestProofTestDevice = "device-test-123456"
const requestProofTestKey = "session-request-proof-key"

func requestProofDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func signReq(requestKey, jwt string, ts int64, nonce, method, target, deviceID, payloadDigest string) string {
	base := hmac.New(sha256.New, []byte(requestKey))
	_, _ = base.Write([]byte(strconv.FormatInt(ts/3600, 10)))
	key := base.Sum(nil)
	message := strings.Join([]string{
		requestSignatureVersion,
		strconv.FormatInt(ts, 10),
		nonce,
		strings.ToUpper(method),
		target,
		deviceID,
		payloadDigest,
		requestAccessTokenDigest(jwt),
	}, "\x00")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func attachRequestProof(r *http.Request, jwt, requestKey string, ts int64, nonce, signedMethod, signedTarget, deviceID, payloadDigest string) {
	r.Header.Set("Authorization", "Bearer "+jwt)
	r.Header.Set("X-Device-Id", deviceID)
	r.Header.Set("X-Req-Ts", strconv.FormatInt(ts, 10))
	r.Header.Set("X-Req-Nonce", nonce)
	r.Header.Set("X-Req-Content-SHA256", payloadDigest)
	r.Header.Set("X-Req-Token", signReq(requestKey, jwt, ts, nonce, signedMethod, signedTarget, deviceID, payloadDigest))
}

func requestProofHandler(requestKey string, next handler) handler {
	return func(d Deps, w http.ResponseWriter, r *http.Request) {
		if err := verifyRequestSignature(d, readAccessToken(r), requestKey, "proof-user", "proof-session", r); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		next(d, w, r)
	}
}

func TestRequestSignatureV2BindsCompleteRequestAndRejectsReplay(t *testing.T) {
	const jwt = "test-jwt-token"
	body := []byte(`{"text":"signed body"}`)
	target := "/conversations/c1/messages?mode=tree&limit=20"
	url := "/api" + target
	timestamp := time.Now().Unix()
	d := Deps{Cache: cache.NewMemory()}

	called := false
	h := requestProofHandler(requestProofTestKey, func(_ Deps, w http.ResponseWriter, r *http.Request) {
		called = true
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("handler body = %q, err=%v; want %q", got, err, body)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	makeRequest := func(nonce string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		attachRequestProof(r, jwt, requestProofTestKey, timestamp, nonce, http.MethodPost, target, requestProofTestDevice, requestProofDigest(body))
		return r
	}

	req := makeRequest("nonce-valid-1234567890")
	rec := httptest.NewRecorder()
	h(d, rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("valid proof status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}

	called = false
	replay := makeRequest("nonce-valid-1234567890")
	replayRec := httptest.NewRecorder()
	h(d, replayRec, replay)
	if replayRec.Code != http.StatusForbidden || called || !strings.Contains(replayRec.Body.String(), "replayed") {
		t.Fatalf("replay status=%d called=%v body=%s", replayRec.Code, called, replayRec.Body.String())
	}
}

func TestRequestSignatureV2RejectsRequestMutation(t *testing.T) {
	const jwt = "test-jwt-token"
	timestamp := time.Now().Unix()
	originalBody := []byte(`{"text":"original"}`)
	originalTarget := "/conversations/c1/messages?mode=tree"

	tests := []struct {
		name         string
		actualMethod string
		actualURL    string
		actualBody   []byte
		deviceID     string
		signedMethod string
		signedTarget string
		signedBody   []byte
		signedDevice string
	}{
		{
			name: "method", actualMethod: http.MethodPatch, actualURL: "/api" + originalTarget,
			actualBody: originalBody, deviceID: requestProofTestDevice,
			signedMethod: http.MethodPost, signedTarget: originalTarget, signedBody: originalBody, signedDevice: requestProofTestDevice,
		},
		{
			name: "query", actualMethod: http.MethodPost, actualURL: "/api/conversations/c1/messages?mode=flat",
			actualBody: originalBody, deviceID: requestProofTestDevice,
			signedMethod: http.MethodPost, signedTarget: originalTarget, signedBody: originalBody, signedDevice: requestProofTestDevice,
		},
		{
			name: "body", actualMethod: http.MethodPost, actualURL: "/api" + originalTarget,
			actualBody: []byte(`{"text":"mutated"}`), deviceID: requestProofTestDevice,
			signedMethod: http.MethodPost, signedTarget: originalTarget, signedBody: originalBody, signedDevice: requestProofTestDevice,
		},
		{
			name: "device", actualMethod: http.MethodPost, actualURL: "/api" + originalTarget,
			actualBody: originalBody, deviceID: "device-other-123456",
			signedMethod: http.MethodPost, signedTarget: originalTarget, signedBody: originalBody, signedDevice: requestProofTestDevice,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := requestProofHandler(requestProofTestKey, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			nonce := "nonce-mutation-1234567" + strconv.Itoa(i)
			r := httptest.NewRequest(tc.actualMethod, tc.actualURL, bytes.NewReader(tc.actualBody))
			r.Header.Set("Content-Type", "application/json")
			digest := requestProofDigest(tc.signedBody)
			attachRequestProof(r, jwt, requestProofTestKey, timestamp, nonce, tc.signedMethod, tc.signedTarget, tc.signedDevice, digest)
			r.Header.Set("X-Device-Id", tc.deviceID)
			rec := httptest.NewRecorder()
			h(Deps{Cache: cache.NewMemory()}, rec, r)
			if rec.Code != http.StatusForbidden || called {
				t.Fatalf("status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
			}
		})
	}
}

func TestRequestSignatureV2RejectsBearerAsSigningKeyAndTokenSwap(t *testing.T) {
	const originalToken = "original-access-token"
	const swappedToken = "swapped-access-token"
	timestamp := time.Now().Unix()
	target := "/me/settings"
	payloadDigest := requestProofDigest(nil)

	tests := []struct {
		name       string
		headerJWT  string
		signedJWT  string
		signingKey string
		nonce      string
	}{
		{
			name: "bearer used as proof key", headerJWT: originalToken, signedJWT: originalToken,
			signingKey: originalToken, nonce: "nonce-bearer-key-123456",
		},
		{
			name: "proof moved to another access token", headerJWT: swappedToken, signedJWT: originalToken,
			signingKey: requestProofTestKey, nonce: "nonce-token-swap-123456",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api"+target, nil)
			attachRequestProof(
				r, tc.signedJWT, tc.signingKey, timestamp, tc.nonce, http.MethodGet,
				target, requestProofTestDevice, payloadDigest,
			)
			r.Header.Set("Authorization", "Bearer "+tc.headerJWT)
			rec := httptest.NewRecorder()
			requestProofHandler(requestProofTestKey, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})(Deps{Cache: cache.NewMemory()}, rec, r)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRequestSignatureV2MultipartMarkerIsNarrowlyScoped(t *testing.T) {
	const jwt = "test-jwt-token"
	timestamp := time.Now().Unix()
	target := "/files"

	do := func(contentType, nonce string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/files", strings.NewReader("multipart bytes"))
		r.Header.Set("Content-Type", contentType)
		attachRequestProof(r, jwt, requestProofTestKey, timestamp, nonce, http.MethodPost, target, requestProofTestDevice, requestSignatureUnsignedPayload)
		rec := httptest.NewRecorder()
		requestProofHandler(requestProofTestKey, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})(Deps{Cache: cache.NewMemory()}, rec, r)
		return rec.Code
	}

	if code := do("multipart/form-data; boundary=test", "nonce-multipart-123456"); code != http.StatusNoContent {
		t.Fatalf("multipart proof status=%d, want %d", code, http.StatusNoContent)
	}
	if code := do("application/json", "nonce-json-marker-123456"); code != http.StatusForbidden {
		t.Fatalf("JSON unsigned-payload status=%d, want %d", code, http.StatusForbidden)
	}
}

func TestRequireAuthEnforcesRequestSignatureWhenConfigured(t *testing.T) {
	d := newAuthSecurityDeps(t, "request-proof-auth.db")
	d.Config.RequestSignaturesRequired = true
	user, err := store.CreateUser(t.Context(), d.DB, "proof@example.test", "Proof", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := issueBoundTestAccessToken(t, d.DB, d.Auth, user)

	called := false
	h := requireAuth(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	unsigned := httptest.NewRequest(http.MethodGet, "/api/me?view=full", nil)
	unsigned.Header.Set("Authorization", "Bearer "+token)
	unsignedRec := httptest.NewRecorder()
	h.ServeHTTP(unsignedRec, unsigned)
	if unsignedRec.Code != http.StatusForbidden || called {
		t.Fatalf("unsigned status=%d called=%v body=%s", unsignedRec.Code, called, unsignedRec.Body.String())
	}

	signed := httptest.NewRequest(http.MethodGet, "/api/me?view=full", nil)
	requestKey, err := d.Auth.RequestSigningKeyForAccess(token)
	if err != nil {
		t.Fatalf("derive request signing key: %v", err)
	}
	attachRequestProof(
		signed, token, requestKey, time.Now().Unix(), "nonce-auth-route-123456", http.MethodGet,
		"/me?view=full", requestProofTestDevice, requestProofDigest(nil),
	)
	signedRec := httptest.NewRecorder()
	h.ServeHTTP(signedRec, signed)
	if signedRec.Code != http.StatusNoContent || !called {
		t.Fatalf("signed status=%d called=%v body=%s", signedRec.Code, called, signedRec.Body.String())
	}
}

func TestRequestSignatureExemptionsAreGETOnlyAndExplicit(t *testing.T) {
	tests := []struct {
		method  string
		target  string
		upgrade string
		want    bool
	}{
		{method: http.MethodGet, target: "/api/files/f1", want: true},
		{method: http.MethodGet, target: "/api/artifacts/a1", want: true},
		{method: http.MethodGet, target: "/api/documents/d1/content", want: true},
		{method: http.MethodGet, target: "/api/conversations/c1/sandbox/file", want: true},
		{method: http.MethodGet, target: "/api/audio/stream", upgrade: "websocket", want: true},
		{method: http.MethodGet, target: "/api/audio/stream", want: false},
		{method: http.MethodGet, target: "/api/me", want: false},
		{method: http.MethodGet, target: "/api/files/f1/metadata", want: false},
		{method: http.MethodGet, target: "/api/documents/d1/content/metadata", want: false},
		{method: http.MethodGet, target: "/api/conversations/c1/sandbox/files", want: false},
		{method: http.MethodPost, target: "/api/files/f1", want: false},
		{method: http.MethodDelete, target: "/api/documents/d1/content", want: false},
	}
	for _, tc := range tests {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if tc.upgrade != "" {
			r.Header.Set("Upgrade", tc.upgrade)
		}
		if got := requestSignatureExempt(r); got != tc.want {
			t.Errorf("requestSignatureExempt(%s %s, upgrade=%q)=%v, want %v", tc.method, tc.target, tc.upgrade, got, tc.want)
		}
	}
}
