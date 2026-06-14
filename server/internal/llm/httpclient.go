package llm

import (
	"net"
	"net/http"
	"time"
)

// providerHTTPClient is the shared client for all upstream model-provider calls
// (§B2). It deliberately has NO overall Timeout — generation responses stream
// for a long time and the request *context* bounds the total. Instead it bounds
// the parts that would otherwise hang forever on a dead upstream: TCP dial, TLS
// handshake, and time-to-first-response-header.
//
// Retries are intentionally NOT applied to streaming generation: replaying after
// partial output (already-streamed tokens / tool calls) is unsafe. A transient
// upstream 429/5xx surfaces as a turn error and the user re-sends; the prior
// timeout-less http.DefaultClient could instead hang a goroutine indefinitely.
var providerHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          50,
	},
}
