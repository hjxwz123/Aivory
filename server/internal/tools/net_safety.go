package tools

import (
	"net"
	"net/http"
	"time"

	"aurelia/server/internal/envcfg"
	"aurelia/server/internal/netsafe"
)

// Env-overridable tool HTTP client tunables (envcfg). Defaults preserve prior
// hardcoded behaviour; overrides are read once at process start.
var (
	ssrfSafeClientTimeout               = envcfg.Dur("AURELIA_TOOLS_SSRFSAFECLIENT_OVERALL_TIMEOUT_WEB_FETCH", 25*time.Second)
	toolHTTPClientDialTimeout           = envcfg.Dur("AURELIA_TOOLS_TOOLHTTPCLIENT_DIAL_TIMEOUT", 10*time.Second)
	toolHTTPClientTLSHandshakeTimeout   = envcfg.Dur("AURELIA_TOOLS_TOOLHTTPCLIENT_TLS_HANDSHAKE_TIMEOUT", 10*time.Second)
	toolHTTPClientResponseHeaderTimeout = envcfg.Dur("AURELIA_TOOLS_TOOLHTTPCLIENT_RESPONSE_HEADER_TIMEOUT", 600*time.Second)
	toolHTTPClientIdleConnTimeout       = envcfg.Dur("AURELIA_TOOLS_TOOLHTTPCLIENT_IDLE_CONN_TIMEOUT", 90*time.Second)
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
