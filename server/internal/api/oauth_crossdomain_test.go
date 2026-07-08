package api

import (
	"net/http/httptest"
	"testing"

	"aurelia/server/internal/config"
)

// TestAllowedReturnOrigin is the open-redirect guard for the cross-domain OAuth
// hand-off: only exact scheme://host matches from the configured allowlist may
// ever be a redirect target. A miss here means the flow refuses to bounce back.
func TestAllowedReturnOrigin(t *testing.T) {
	d := Deps{Config: config.Config{OAuthReturnOrigins: []string{
		"https://a.example.com", "https://b.example.com/",
	}}}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://a.example.com", true},
		{"https://b.example.com", true},           // trailing slash in config is normalised
		{"https://a.example.com/", true},          // trailing slash in input is normalised
		{"https://A.EXAMPLE.COM", true},           // host compare is case-insensitive
		{"https://evil.example.com", false},       // not listed
		{"http://a.example.com", false},           // scheme must match (no downgrade)
		{"https://a.example.com.evil.com", false}, // suffix attack
		{"https://a.example.com@evil.com", false}, // userinfo trick
		{"", false},
	}
	for _, c := range cases {
		if got := allowedReturnOrigin(d, c.origin); got != c.want {
			t.Errorf("allowedReturnOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// TestStartOrigin covers the decision made when a flow begins: return the request
// host only when it differs from the canonical callback host AND is allowlisted;
// otherwise "" (a same-host flow, no hand-off).
func TestStartOrigin(t *testing.T) {
	d := Deps{Config: config.Config{
		OAuthCallbackBaseURL: "https://a.example.com",
		OAuthReturnOrigins:   []string{"https://a.example.com", "https://b.example.com"},
	}}
	callbackBase := oauthCallbackBase(d, httptest.NewRequest("GET", "https://a.example.com/x", nil))
	if callbackBase != "https://a.example.com" {
		t.Fatalf("callbackBase = %q, want canonical", callbackBase)
	}

	// Started on B (allowlisted, != canonical) → hand-off back to B.
	req := httptest.NewRequest("GET", "https://b.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "https://b.example.com" {
		t.Errorf("startOrigin(B) = %q, want https://b.example.com", got)
	}
	// Started on the canonical host itself → no hand-off.
	req = httptest.NewRequest("GET", "https://a.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "" {
		t.Errorf("startOrigin(A) = %q, want empty", got)
	}
	// Started on an UN-allowlisted domain → no hand-off (falls back to canonical,
	// never trusts an arbitrary host as a redirect target).
	req = httptest.NewRequest("GET", "https://evil.example.com/api/auth/oauth/g/start", nil)
	if got := startOrigin(d, req, callbackBase); got != "" {
		t.Errorf("startOrigin(evil) = %q, want empty", got)
	}
}

// TestOAuthCallbackBaseFallback: with no canonical host configured the callback
// base derives from the request host (single-domain deployments — unchanged).
func TestOAuthCallbackBaseFallback(t *testing.T) {
	d := Deps{Config: config.Config{}}
	req := httptest.NewRequest("GET", "https://only.example.com/x", nil)
	if got := oauthCallbackBase(d, req); got != "https://only.example.com" {
		t.Errorf("oauthCallbackBase fallback = %q, want request host", got)
	}
}
