package tools

import (
	"aurelia/server/internal/netsafe"
	"net"
	"net/http"
	"time"
)

// Low-level HTTP-client network tunables for tool fetches — dial / TLS / idle /
// overall timeouts. Hardcoded (not operator-facing knobs).
var (
	ssrfSafeClientTimeout               = 25 * time.Second
	toolHTTPClientDialTimeout           = 10 * time.Second
	toolHTTPClientTLSHandshakeTimeout   = 10 * time.Second
	toolHTTPClientResponseHeaderTimeout = 600 * time.Second
	toolHTTPClientIdleConnTimeout       = 90 * time.Second
)

// isPublicIP rejects loopback/private/link-local/unspecified/multicast plus the
// CGNAT/NAT64/TEST-NET ranges Go doesn't classify (§F4). Delegates to the shared
// netsafe package so the tools and rag SSRF guards stay in lockstep.
func isPublicIP(ip net.IP) bool { return netsafe.IsPublicIP(ip) }

// ssrfSafeClient returns an http.Client that only connects to ports 80/443 and
// validates the resolved IP at dial time (defeats DNS-rebinding + redirects).
// Use for MODEL-controlled URLs (web_fetch).
func ssrfSafeClient() *http.Client { return netsafe.SafeClient(ssrfSafeClientTimeout) }

// toolHTTPClient is for ADMIN-configured tool endpoints (web search backends,
// image-generation gateways). Like the LLM provider client (§B2/§F3) it bounds
// connect/TLS/response-header time but not the overall body (image gen is slow),
// and — unlike ssrfSafeClient — it does NOT block private IPs, because these base
// URLs are admin-set and legitimately point at internal gateways. The query /
// prompt that the model controls is request DATA, not the destination URL.
var toolHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: toolHTTPClientDialTimeout}).DialContext,
		TLSHandshakeTimeout:   toolHTTPClientTLSHandshakeTimeout,
		ResponseHeaderTimeout: toolHTTPClientResponseHeaderTimeout, // image gen can be slow; per-tool ctx is the real bound
		IdleConnTimeout:       toolHTTPClientIdleConnTimeout,
	},
}
