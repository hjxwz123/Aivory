package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// signReq mirrors the FRONTEND's _sign (client.ts): derive an hourly key from
// the jwt, then HMAC ts\x00nonce\x00<signedPath> — where signedPath is whatever
// string the client chose to bind (the tests vary it on purpose).
func signReq(jwt string, ts int64, nonce, signedPath string) string {
	base := hmac.New(sha256.New, []byte(jwt))
	base.Write([]byte(strconv.FormatInt(ts/3600, 10)))
	key := base.Sum(nil)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "\x00" + nonce + "\x00" + signedPath))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// TestRequireReqSigPathContract pins the client↔server signing contract: the
// client signs the LOGICAL path — no "/api" mount prefix (its fetch prepends
// API_BASE after signing) and no query string — so the middleware must
// canonicalise r.URL.Path the same way. The tree fetch (?mode=tree) 403'd with
// "invalid request signature" from day one because the server hashed the raw
// "/api/...(?query)" while the client hashed "/conversations/...".
func TestRequireReqSigPathContract(t *testing.T) {
	const jwt = "test-jwt-token"
	called := false
	h := requireReqSig(func(d Deps, w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	do := func(url, signedPath string) int {
		called = false
		ts := time.Now().Unix()
		nonce := "n0nce"
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set("Authorization", "Bearer "+jwt)
		r.Header.Set("X-Req-Ts", strconv.FormatInt(ts, 10))
		r.Header.Set("X-Req-Nonce", nonce)
		r.Header.Set("X-Req-Token", signReq(jwt, ts, nonce, signedPath))
		w := httptest.NewRecorder()
		h(Deps{}, w, r)
		return w.Code
	}

	// The real client: signs the logical path, requests with /api prefix + query.
	if code := do("/api/conversations/c1/messages?mode=tree", "/conversations/c1/messages"); code != 200 || !called {
		t.Fatalf("logical-path signature must pass: code=%d called=%v", code, called)
	}
	// Same without a query string (plain path fetch).
	if code := do("/api/conversations/c1/messages", "/conversations/c1/messages"); code != 200 || !called {
		t.Fatalf("logical-path signature (no query) must pass: code=%d called=%v", code, called)
	}
	// A reverse proxy that strips /api: TrimPrefix is a no-op and it still works.
	if code := do("/conversations/c1/messages?mode=tree", "/conversations/c1/messages"); code != 200 || !called {
		t.Fatalf("proxy-stripped prefix must pass: code=%d called=%v", code, called)
	}
	// Signing the RAW path (old server expectation) must NOT verify…
	if code := do("/api/conversations/c1/messages?mode=tree", "/api/conversations/c1/messages"); code != 403 || called {
		t.Fatalf("raw /api path signature must fail: code=%d called=%v", code, called)
	}
	// …and neither must a signature that still binds the query string.
	if code := do("/api/conversations/c1/messages?mode=tree", "/conversations/c1/messages?mode=tree"); code != 403 || called {
		t.Fatalf("query-bound signature must fail: code=%d called=%v", code, called)
	}
	// A token minted for one endpoint is invalid on another (path binding).
	if code := do("/api/conversations/c1/messages", "/conversations/OTHER/messages"); code != 403 || called {
		t.Fatalf("cross-path signature must fail: code=%d called=%v", code, called)
	}
}
