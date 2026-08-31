package netsafe

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsUserMCPDialAllowed(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		allowed bool
	}{
		{name: "public IPv4", ip: "8.8.8.8", allowed: true},
		{name: "private LAN IPv4", ip: "10.42.0.5", allowed: true},
		{name: "private LAN IPv6", ip: "fd00::25", allowed: true},
		{name: "loopback IPv4", ip: "127.0.0.1", allowed: false},
		{name: "loopback IPv6", ip: "::1", allowed: false},
		{name: "cloud metadata", ip: "169.254.169.254", allowed: false},
		{name: "IPv4-mapped cloud metadata", ip: "::ffff:169.254.169.254", allowed: false},
		{name: "link-local IPv6", ip: "fe80::1", allowed: false},
		{name: "unspecified", ip: "0.0.0.0", allowed: false},
		{name: "this network range", ip: "0.0.0.1", allowed: false},
		{name: "multicast", ip: "224.0.0.1", allowed: false},
		{name: "reserved IPv4", ip: "240.0.0.1", allowed: false},
		{name: "limited broadcast", ip: "255.255.255.255", allowed: false},
		{name: "carrier NAT", ip: "100.64.0.1", allowed: false},
		{name: "NAT64 metadata-capable prefix", ip: "64:ff9b::a9fe:a9fe", allowed: false},
		{name: "local NAT64 metadata-capable prefix", ip: "64:ff9b:1::a9fe:a9fe", allowed: false},
		{name: "AWS IPv6 metadata", ip: "fd00:ec2::254", allowed: false},
		{name: "ordinary private LAN IPv6 remains allowed", ip: "fd00:ec2::255", allowed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsUserMCPDialAllowed(net.ParseIP(test.ip)); got != test.allowed {
				t.Fatalf("IsUserMCPDialAllowed(%s)=%v want=%v", test.ip, got, test.allowed)
			}
		})
	}
	if IsUserMCPDialAllowed(nil) {
		t.Fatal("nil IP was allowed")
	}
}

func TestUserMCPAllowedClientBlocksLoopbackBeforeHTTP(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := UserMCPAllowedClient(2 * time.Second).Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "blocked private/loopback host") {
		t.Fatalf("loopback request error=%v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("blocked loopback endpoint received %d request(s)", requests.Load())
	}
}

func TestUserMCPAllowedClientBlocksCrossOriginRedirectHeaders(t *testing.T) {
	var leakedAuthorization atomic.Value
	var leakedAPIKey atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedAuthorization.Store(r.Header.Get("Authorization"))
		leakedAPIKey.Store(r.Header.Get("X-Api-Key"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	// The production dial gate intentionally rejects httptest loopback hosts.
	// Replacing only the transport keeps the redirect policy under test while
	// allowing these two local fixtures to exchange requests.
	client := UserMCPAllowedClient(2 * time.Second)
	client.Transport = http.DefaultTransport
	request, err := http.NewRequest(http.MethodPost, source.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer redirect-secret")
	request.Header.Set("X-Api-Key", "redirect-api-key")
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "cross-origin MCP redirect blocked") {
		t.Fatalf("cross-origin redirect error=%v", err)
	}
	if value, _ := leakedAuthorization.Load().(string); value != "" {
		t.Fatalf("cross-origin redirect leaked Authorization header %q", value)
	}
	if value, _ := leakedAPIKey.Load().(string); value != "" {
		t.Fatalf("cross-origin redirect leaked API key header %q", value)
	}
}

func TestUserMCPAllowedClientPreservesHeadersOnSameOriginRedirect(t *testing.T) {
	var received atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		received.Store(r.Header.Get("X-Api-Key"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := UserMCPAllowedClient(2 * time.Second)
	client.Transport = http.DefaultTransport
	request, err := http.NewRequest(http.MethodPost, server.URL+"/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", "same-origin-secret")
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	if value, _ := received.Load().(string); value != "same-origin-secret" {
		t.Fatalf("same-origin redirect header=%q want secret", value)
	}
}

func TestUserMCPRedirectChecksImmediatePreviousHop(t *testing.T) {
	request := func(rawURL string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	via := []*http.Request{
		request("http://mcp.example.test/start"),
		request("https://mcp.example.test/secure"),
	}
	err := checkUserMCPRedirect(request("http://mcp.example.test/final"), via)
	if err == nil || !strings.Contains(err.Error(), "insecure MCP redirect blocked") {
		t.Fatalf("chained HTTPS downgrade error=%v", err)
	}
}
