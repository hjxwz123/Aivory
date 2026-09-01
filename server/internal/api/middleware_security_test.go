package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func csrfCookieRequest(target, origin string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: "access"})
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestCSRFSameOriginRequiresMatchingSchemeHostAndPort(t *testing.T) {
	tests := []struct {
		name   string
		target string
		origin string
		want   bool
	}{
		{name: "exact https origin", target: "https://app.example.test/api/me", origin: "https://app.example.test", want: true},
		{name: "cross scheme", target: "https://app.example.test/api/me", origin: "http://app.example.test", want: false},
		{name: "cross host", target: "https://app.example.test/api/me", origin: "https://evil.example.test", want: false},
		{name: "cross port", target: "https://app.example.test:8443/api/me", origin: "https://app.example.test", want: false},
		{name: "credentialed origin", target: "https://app.example.test/api/me", origin: "https://user@app.example.test", want: false},
		{name: "origin with path", target: "https://app.example.test/api/me", origin: "https://app.example.test/path", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := csrfOK(nil, csrfCookieRequest(tc.target, tc.origin)); got != tc.want {
				t.Fatalf("csrfOK() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCSRFUsesTrustedForwardedRequestOrigin(t *testing.T) {
	req := csrfCookieRequest("http://api.internal/api/me", "https://app.example.test")
	req.RemoteAddr = "10.0.0.5:4321"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "app.example.test")
	if !csrfOK(nil, req) {
		t.Fatal("trusted proxy origin was rejected")
	}

	req.RemoteAddr = "203.0.113.10:4321"
	if csrfOK(nil, req) {
		t.Fatal("public peer was allowed to spoof the request origin")
	}
}

func TestCSRFRejectsCrossSiteFetchWithoutOrigin(t *testing.T) {
	req := csrfCookieRequest("https://app.example.test/api/me", "")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if csrfOK(nil, req) {
		t.Fatal("cross-site browser request without Origin was accepted")
	}
}

func TestCSRFAllowsExplicitCrossOriginFrontend(t *testing.T) {
	req := csrfCookieRequest("https://api.example.test/api/me", "https://app.example.test")
	if !csrfOK([]string{"https://app.example.test"}, req) {
		t.Fatal("configured frontend origin was rejected")
	}
}

func TestWebSocketOriginRequiresMatchingSchemeHostAndPort(t *testing.T) {
	request := func(target, origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Origin", origin)
		return r
	}
	if !sameOriginWS(request("https://app.example.test/api/audio/stream", "https://app.example.test")) {
		t.Fatal("exact WebSocket origin was rejected")
	}
	for _, origin := range []string{
		"http://app.example.test",
		"https://app.example.test:8443",
		"https://evil.example.test",
		"https://app.example.test/path",
	} {
		if sameOriginWS(request("https://app.example.test/api/audio/stream", origin)) {
			t.Errorf("mismatched WebSocket origin %q was accepted", origin)
		}
	}
}
