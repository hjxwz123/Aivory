package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// providerBaseURL trims a channel base URL and substitutes the vendor default
// when it is empty. Used inside the doProviderRequest build closures so the
// fallback endpoint gets the SAME defaulting the primary does.
func providerBaseURL(baseURL, vendorDefault string) string {
	if b := strings.TrimRight(baseURL, "/"); b != "" {
		return b
	}
	return vendorDefault
}

// providerHTTPClient is the shared client for all upstream model-provider calls
// (§B2). It deliberately has NO overall Timeout — generation responses stream
// for a long time and the request *context* bounds the total. Instead it bounds
// the parts that would otherwise hang forever before the request reaches the
// provider: TCP dial and TLS handshake. Do NOT set ResponseHeaderTimeout here:
// reasoning/tool-heavy streaming calls can legitimately take more than two
// minutes before the first SSE frame. The request context plus the provider
// TTFT watchdog/admin generation cap are the right owners of that decision.
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
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          50,
	},
}

// doProviderRequest issues one upstream call against the model's PRIMARY channel.
// If that fails (transport error, or ANY non-2xx HTTP status — see
// retryableUpstreamFailure for why 4xx is included) AND the model has a fallback
// channel, it rebuilds the request against the fallback creds and retries ONCE,
// flagging req.FallbackUsed so the whole turn is marked fallback (§fallback channel).
//
// build MUST create a fresh *http.Request each call — a request body Reader is
// consumed once and can't be rewound for the retry. A caller cancellation
// (ctx.Canceled / DeadlineExceeded — the stop button or the TTFT watchdog) is
// NOT a failure we retry: that would defeat the cancel and, for the watchdog,
// double-generate. On fallback, the primary response body is drained/closed
// before the retry so the connection is released.
//
// The retry covers only request ESTABLISHMENT (dial/TLS/headers/status). A
// stream that breaks mid-body after a 200 is not retried — replaying after
// partially-streamed tokens/tool-calls is unsafe (see the client note above).
func doProviderRequest(
	ctx context.Context,
	m ModelInfo,
	fallbackUsed *atomic.Bool,
	build func(baseURL, apiKey string) (*http.Request, error),
) (*http.Response, error) {
	primaryReq, err := build(m.BaseURL, m.APIKey)
	if err != nil {
		return nil, err
	}
	recordProviderRequest(ctx, primaryReq)
	armProviderTTFTWatchdog(ctx)
	resp, err := providerHTTPClient.Do(primaryReq)
	if m.Fallback == nil || !retryableUpstreamFailure(resp, err) {
		return resp, err
	}
	// Build the retry BEFORE releasing the primary response: if the fallback
	// request can't be constructed (e.g. an unparseable fallback base URL), we
	// return the primary response UNTOUCHED so the caller can still read its error
	// body — closing it first would surface an empty upstream message.
	fbReq, berr := build(m.Fallback.BaseURL, m.Fallback.APIKey)
	if berr != nil {
		return resp, err // keep the original (unclosed) failure; couldn't build the retry
	}
	recordProviderRequest(ctx, fbReq)
	// Release the primary connection now that we're committed to the retry.
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	armProviderTTFTWatchdog(ctx)
	resp2, err2 := providerHTTPClient.Do(fbReq)
	// The fallback endpoint served the (final) response — mark the turn fallback
	// whether or not it ultimately succeeded, so an error row is still attributed
	// to the fallback channel.
	if fallbackUsed != nil {
		fallbackUsed.Store(true)
	}
	return resp2, err2
}

// retryableUpstreamFailure reports whether a primary provider call failed in a
// way the fallback channel should absorb. A caller cancellation or deadline is
// intentional and never retried; everything else — transport errors and ANY
// non-2xx status — retries once on the backup.
//
// 4xx used to be excluded on the theory "our payload is malformed, a different
// endpoint fails identically". In practice relay/proxy channels answer 400/402/
// 404 for CHANNEL-side conditions (quota exhausted, model not enabled on this
// relay, region blocks), which a backup relay serves fine — a user who
// configured a fallback expects exactly that. The cost of a wasted retry on a
// genuinely malformed payload is one extra failed call; the cost of NOT
// retrying a relay-side 400 is a user-visible error with a healthy backup
// sitting idle.
func retryableUpstreamFailure(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		return true // dial / TLS / connection-reset / header-timeout
	}
	if resp == nil {
		return true
	}
	return resp.StatusCode >= 400
}
