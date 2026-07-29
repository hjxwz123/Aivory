package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
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
// A configured channel fallback may retry one complete upstream response before
// that response's buffered events are committed to the client. It never replays
// a tool that has already run: providers retry the failed HTTP/SSE round in
// place, then keep the fallback channel sticky for the rest of the turn.
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
	resp, err := sendProviderRequest(ctx, primaryReq, false)
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
	// Release the primary connection now that we're committed to the retry.
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	resp2, err2 := sendProviderRequest(ctx, fbReq, true)
	// The fallback endpoint served the (final) response — mark the turn fallback
	// whether or not it ultimately succeeded, so an error row is still attributed
	// to the fallback channel.
	if fallbackUsed != nil {
		fallbackUsed.Store(true)
	}
	return resp2, err2
}

// doProviderParsedRequest owns one complete upstream HTTP response, including
// status validation and body/SSE parsing performed by consume. With a configured
// fallback channel, primary events are buffered until consume succeeds. If ANY
// non-cancellation failure occurs (transport, HTTP status, malformed protocol,
// explicit SSE error, or interrupted body), the buffered events are discarded
// and the same round is attempted once against the fallback credentials.
//
// A successful switch is sticky for the rest of the provider's tool loop: later
// rounds go directly to the fallback. This avoids mixing provider-side state or
// signed reasoning blocks between channels. The fallback attempt itself streams
// live because there is no third attempt that could require another rollback.
// Caller cancellation/deadline is not a channel failure and is never replayed;
// buffered partial events are flushed so stop semantics remain unchanged.
func doProviderParsedRequest(
	ctx context.Context,
	m ModelInfo,
	fallbackUsed *atomic.Bool,
	build func(baseURL, apiKey string) (*http.Request, error),
	consume func(resp *http.Response, onEvent func(SseEvent)) error,
	onEvent func(SseEvent),
) error {
	if onEvent == nil {
		onEvent = func(SseEvent) {}
	}

	consumeAttempt := func(req *http.Request, fallback bool, emit func(SseEvent)) error {
		resp, err := sendProviderRequest(ctx, req, fallback)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			recordProviderRequestFailure(ctx, fallback, err)
			return err
		}
		if resp == nil {
			err = errors.New("provider returned no HTTP response")
			recordProviderRequestFailure(ctx, fallback, err)
			return err
		}
		if resp.Body != nil {
			defer resp.Body.Close()
		}
		err = consume(resp, emit)
		if err != nil {
			recordProviderRequestFailure(ctx, fallback, err)
		}
		return err
	}

	// Once a prior round switched channels, do not probe the failed primary
	// again. FallbackUsed is turn-scoped and shared by every provider iteration.
	useStickyFallback := m.Fallback != nil && (m.APIKey == "" || (fallbackUsed != nil && fallbackUsed.Load()))
	if useStickyFallback {
		fbReq, err := build(m.Fallback.BaseURL, m.Fallback.APIKey)
		if err != nil {
			recordProviderRequestBuildFailure(ctx, true, err)
			return err
		}
		if fallbackUsed != nil {
			fallbackUsed.Store(true)
		}
		return consumeAttempt(fbReq, true, onEvent)
	}

	primaryReq, err := build(m.BaseURL, m.APIKey)
	if err != nil && m.Fallback == nil {
		recordProviderRequestBuildFailure(ctx, false, err)
		return err
	}
	if m.Fallback == nil {
		return consumeAttempt(primaryReq, false, onEvent)
	}

	buffered := make([]SseEvent, 0, 32)
	emitBuffered := func(ev SseEvent) { buffered = append(buffered, ev) }
	primaryErr := err
	if primaryErr != nil {
		recordProviderRequestBuildFailure(ctx, false, primaryErr)
	}
	if primaryErr == nil {
		primaryErr = consumeAttempt(primaryReq, false, emitBuffered)
	}
	if primaryErr == nil {
		for _, ev := range buffered {
			onEvent(ev)
		}
		return nil
	}
	if !fallbackAllowedAfter(ctx, primaryErr) {
		for _, ev := range buffered {
			onEvent(ev)
		}
		return primaryErr
	}

	// Build before discarding the primary's partial events. If the configured
	// fallback URL itself is invalid, preserve the primary result/error exactly.
	fbReq, buildErr := build(m.Fallback.BaseURL, m.Fallback.APIKey)
	if buildErr != nil {
		recordProviderRequestBuildFailure(ctx, true, buildErr)
		for _, ev := range buffered {
			onEvent(ev)
		}
		return primaryErr
	}
	if fallbackUsed != nil {
		fallbackUsed.Store(true)
	}
	return consumeAttempt(fbReq, true, onEvent)
}

func sendProviderRequest(ctx context.Context, req *http.Request, fallback bool) (*http.Response, error) {
	recordProviderRequestAttempt(ctx, req, fallback)
	armProviderTTFTWatchdog(ctx)
	resp, err := providerHTTPClient.Do(req)
	wrapFirstByteBody(ctx, resp)
	return resp, err
}

func fallbackAllowedAfter(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// providerStatusError keeps the numeric status available to provider-specific
// compatibility paths (notably Anthropic's one-time thinking-strip retry) while
// preserving the existing admin-visible error text.
type providerStatusError struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *providerStatusError) Error() string {
	return fmt.Sprintf("%s %d: %s", e.Provider, e.StatusCode, e.Body)
}

func requireProviderSuccess(resp *http.Response, provider string) error {
	if resp != nil && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if resp == nil {
		return errors.New("provider returned no HTTP response")
	}
	b, _ := io.ReadAll(resp.Body)
	return &providerStatusError{Provider: provider, StatusCode: resp.StatusCode, Body: string(b)}
}

func providerEventError(provider string, event map[string]any) error {
	raw, ok := event["error"]
	if !ok || raw == nil {
		typeName, _ := event["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(typeName), "error") {
			return nil
		}
		raw = event
	}
	message := "upstream reported an error"
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			message = strings.TrimSpace(value)
		}
	case map[string]any:
		for _, key := range []string{"message", "detail", "type", "code", "status"} {
			if text, _ := value[key].(string); strings.TrimSpace(text) != "" {
				message = strings.TrimSpace(text)
				break
			}
		}
	}
	return fmt.Errorf("%s stream error: %s", provider, message)
}

func invalidProviderStream(provider, detail string) error {
	return fmt.Errorf("%s stream protocol error: %s", provider, detail)
}

// wrapFirstByteBody wraps resp.Body (if present) so the TTFT watchdog disarms
// on the first byte actually read from the upstream — see the doc comment on
// providerTTFTWatchdog for why "first byte", not "first parsed content event".
// A no-op when resp/resp.Body is nil (transport error) or no watchdog is armed
// for this ctx (fallback_ttft_sec disabled).
func wrapFirstByteBody(ctx context.Context, resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	resp.Body = &firstByteBody{ReadCloser: resp.Body, ctx: ctx}
}

type firstByteBody struct {
	io.ReadCloser
	ctx  context.Context
	once sync.Once
}

func (b *firstByteBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { markProviderTTFTFirstByte(b.ctx) })
	}
	return n, err
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
	return resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices
}
