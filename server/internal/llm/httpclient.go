package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aivory/server/internal/requestheaders"
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

// OpenAIBaseURL returns the configured upstream API root exactly as supplied.
// Host-only legacy rows still receive /v1 for backward compatibility, while
// explicit /v2, /v3, and vendor-specific paths are preserved.
func OpenAIBaseURL(baseURL string) string {
	base := providerBaseURL(baseURL, "https://api.openai.com/v1")
	parsed, err := url.Parse(base)
	if err == nil && (parsed.Path == "" || parsed.Path == "/") {
		return strings.TrimRight(base, "/") + "/v1"
	}
	return base
}

// VendorAPIBaseURL accepts both legacy host roots and explicit version roots.
func VendorAPIBaseURL(baseURL, vendorDefault, version string) string {
	base := providerBaseURL(baseURL, vendorDefault)
	parsed, err := url.Parse(base)
	if err == nil {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		last := segments[len(segments)-1]
		if len(last) > 1 && last[0] == 'v' && last[1] >= '0' && last[1] <= '9' {
			return base
		}
	}
	return base + "/" + version
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
// Channel failover retries the failed HTTP/SSE round before its user-visible
// output is committed, then keeps the successful channel for the rest of the
// turn. Completed tools are never replayed.
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

// doProviderRequest advances through the priority-ordered channel queue on a
// transport error or non-200 response. Every retry builds a fresh request with
// that channel's credentials and headers.
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
	candidates := providerChannelCandidates(m)
	start := providerChannelIndex(m, fallbackUsed)
	var lastResp *http.Response
	var lastErr error
	for index := start; index < len(candidates); index++ {
		setProviderChannelIndex(m, index, fallbackUsed)
		candidate := candidates[index]
		attemptCtx := contextWithProviderRequestChannel(ctx, m.ID, candidate.ID, index > 0)
		req, err := buildProviderChannelRequest(m, index, build)
		if err != nil {
			lastErr = err
			if index+1 < len(candidates) && fallbackAllowedAfter(ctx, err) {
				continue
			}
			return nil, err
		}
		resp, err := sendProviderRequest(attemptCtx, req, index > 0)
		lastResp, lastErr = resp, err
		if !retryableUpstreamFailure(resp, err) || index+1 == len(candidates) {
			return resp, err
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return lastResp, lastErr
}

// doProviderParsedRequest owns HTTP status validation and body/SSE parsing.
// It advances through the ordered channel queue only before this response
// commits visible output, buffering metadata until that point. Later tool
// rounds continue on the successful channel without replaying completed tools.
// Hidden calls buffer a response while another candidate remains; the final
// candidate and single-channel requests stream directly. Cancellation never
// advances the queue, and flushes partial events to preserve stop semantics.
func doProviderParsedRequest(
	ctx context.Context,
	m ModelInfo,
	fallbackUsed *atomic.Bool,
	build func(baseURL, apiKey string) (*http.Request, error),
	consume func(resp *http.Response, onEvent func(SseEvent)) error,
	onEvent func(SseEvent),
) error {
	return doProviderParsedRequestWithRepair(ctx, m, fallbackUsed, build, consume, onEvent, nil)
}

// providerRequestNoFallbackError marks a request validation error that must be
// repaired on the current channel rather than sent to a fallback channel.
type providerRequestNoFallbackError struct{ error }

func (e *providerRequestNoFallbackError) Unwrap() error { return e.error }

// repair runs before failure recording or fallback selection, at most once per
// channel attempt and only before any output. It updates the caller's payload;
// build then recreates the request with the SAME channel credentials.
func doProviderParsedRequestWithRepair(
	ctx context.Context,
	m ModelInfo,
	fallbackUsed *atomic.Bool,
	build func(baseURL, apiKey string) (*http.Request, error),
	consume func(resp *http.Response, onEvent func(SseEvent)) error,
	onEvent func(SseEvent),
	repair func(error) bool,
) error {
	if onEvent == nil {
		onEvent = func(SseEvent) {}
	}
	visibleOutput := providerVisibleOutputFromContext(ctx)

	consumeAttempt := func(req *http.Request, channelIndex int, emit func(SseEvent)) error {
		candidate := providerChannelCandidates(m)[channelIndex]
		attemptCtx := contextWithProviderRequestChannel(ctx, m.ID, candidate.ID, channelIndex > 0)
		for attempt := 0; ; attempt++ {
			resp, err := sendProviderRequest(attemptCtx, req, channelIndex > 0)
			if err != nil {
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				recordProviderRequestFailure(attemptCtx, channelIndex > 0, err)
				return err
			}
			if resp == nil {
				err = errors.New("provider returned no HTTP response")
				recordProviderRequestFailure(attemptCtx, channelIndex > 0, err)
				return err
			}
			if resp.Body != nil {
				defer resp.Body.Close()
			}
			emitted := false
			var generated strings.Builder
			trackGenerated := func(ev SseEvent) {
				emitted = true
				switch ev.Type {
				case "text_delta", "thinking_delta":
					generated.WriteString(ev.Text)
				case "tool_start":
					generated.WriteString(ev.Name)
				case "tool_input":
					generated.WriteString(ev.PartialJson)
				}
				emit(ev)
			}
			err = consume(resp, trackGenerated)
			recordProviderRequestOutputEstimate(attemptCtx, estimateTokens(generated.String()))
			if err != nil && attempt == 0 && !emitted && ctx.Err() == nil && repair != nil && repair(err) {
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
				req, err = buildProviderChannelRequest(m, channelIndex, build)
				if err != nil {
					recordProviderRequestBuildFailure(attemptCtx, channelIndex > 0, err)
					return err
				}
				continue
			}
			if err != nil {
				recordProviderRequestFailure(attemptCtx, channelIndex > 0, err)
			}
			return err
		}
	}

	candidates := providerChannelCandidates(m)
	start := providerChannelIndex(m, fallbackUsed)
	var lastErr error
	for index := start; index < len(candidates); index++ {
		setProviderChannelIndex(m, index, fallbackUsed)
		candidate := candidates[index]
		attemptCtx := contextWithProviderRequestChannel(ctx, m.ID, candidate.ID, index > 0)
		req, err := buildProviderChannelRequest(m, index, build)
		if err != nil {
			recordProviderRequestBuildFailure(attemptCtx, index > 0, err)
			lastErr = err
			if index+1 < len(candidates) && fallbackAllowedAfter(ctx, err) {
				continue
			}
			return err
		}
		if index+1 == len(candidates) {
			return consumeAttempt(req, index, onEvent)
		}
		buffered := make([]SseEvent, 0, 32)
		committed := false
		flushBuffered := func() {
			for _, ev := range buffered {
				onEvent(ev)
			}
			buffered = buffered[:0]
		}
		emitBuffered := func(ev SseEvent) {
			if visibleOutput == nil {
				buffered = append(buffered, ev)
				return
			}
			if committed {
				flushBuffered()
				onEvent(ev)
				return
			}
			if !providerEventCommitsVisibleOutputInContext(ctx, ev) {
				buffered = append(buffered, ev)
				return
			}
			committed = true
			flushBuffered()
			onEvent(ev)
		}
		lastErr = consumeAttempt(req, index, emitBuffered)
		if lastErr == nil {
			flushBuffered()
			return nil
		}
		if !fallbackAllowedAfter(ctx, lastErr) || committed {
			flushBuffered()
			return lastErr
		}
	}
	return lastErr
}

func providerChannelCandidates(m ModelInfo) []ChannelCreds {
	if len(m.ChannelCandidates) > 0 {
		return m.ChannelCandidates
	}
	candidates := []ChannelCreds{{ID: m.ChannelID, BaseURL: m.BaseURL, APIKey: m.APIKey, Headers: m.Headers}}
	if m.Fallback != nil {
		candidates = append(candidates, ChannelCreds{ID: m.FallbackChannelID, BaseURL: m.Fallback.BaseURL, APIKey: m.Fallback.APIKey, Headers: m.Fallback.Headers})
	}
	return candidates
}

func providerChannelIndex(m ModelInfo, fallbackUsed *atomic.Bool) int {
	candidates := providerChannelCandidates(m)
	if m.ChannelIndex != nil {
		index := int(m.ChannelIndex.Load())
		if index >= 0 && index < len(candidates) {
			if index+1 < len(candidates) && strings.TrimSpace(candidates[index].APIKey) == "" {
				return index + 1
			}
			return index
		}
	}
	// Older callers only carried the sticky fallback bit. Preserve that
	// compatibility while the new request path also carries ChannelIndex.
	if fallbackUsed != nil && fallbackUsed.Load() && len(candidates) > 1 {
		return 1
	}
	if len(candidates) > 1 && strings.TrimSpace(candidates[0].APIKey) == "" {
		return 1
	}
	return 0
}

func setProviderChannelIndex(m ModelInfo, index int, fallbackUsed *atomic.Bool) {
	if m.ChannelIndex != nil {
		m.ChannelIndex.Store(int64(index))
	}
	if index > 0 && fallbackUsed != nil {
		fallbackUsed.Store(true)
	}
}

func buildProviderChannelRequest(m ModelInfo, index int, build func(string, string) (*http.Request, error)) (*http.Request, error) {
	candidates := providerChannelCandidates(m)
	if index < 0 || index >= len(candidates) {
		return nil, errors.New("provider channel is unavailable")
	}
	candidate := candidates[index]
	req, err := build(candidate.BaseURL, candidate.APIKey)
	if err == nil {
		requestheaders.Apply(req, candidate.Headers)
	}
	return req, err
}

func sendProviderRequest(ctx context.Context, req *http.Request, fallback bool) (*http.Response, error) {
	timing := recordProviderRequestAttempt(ctx, req, fallback)
	armProviderTTFTWatchdog(ctx)
	if timing != nil {
		timing.startedAt = time.Now()
	}
	resp, err := providerHTTPClient.Do(req)
	if timing != nil && (err != nil || resp == nil) {
		timing.finish()
	}
	wrapFirstByteBody(ctx, resp, timing)
	return resp, err
}

func fallbackAllowedAfter(ctx context.Context, err error) bool {
	var noFallback *providerRequestNoFallbackError
	if err == nil || ctx.Err() != nil || errors.As(err, &noFallback) {
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
	if resp != nil && resp.StatusCode == http.StatusOK {
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

// wrapFirstByteBody observes the first byte and end of the upstream response
// body for both the TTFT watchdog and request timing. It passes bytes through
// unchanged; timing is a no-op when this request has no recorder.
func wrapFirstByteBody(ctx context.Context, resp *http.Response, timing *providerRequestTiming) {
	if resp == nil || resp.Body == nil {
		if timing != nil {
			timing.finish()
		}
		return
	}
	resp.Body = &firstByteBody{ReadCloser: resp.Body, ctx: ctx, timing: timing}
}

type firstByteBody struct {
	io.ReadCloser
	ctx       context.Context
	timing    *providerRequestTiming
	firstOnce sync.Once
	endOnce   sync.Once
}

func (b *firstByteBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.firstOnce.Do(func() {
			markProviderTTFTFirstByte(b.ctx)
			b.timing.markFirstByte()
		})
	}
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *firstByteBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	return err
}

func (b *firstByteBody) finish() {
	b.endOnce.Do(func() { b.timing.finish() })
}

// retryableUpstreamFailure reports whether a primary provider call failed in a
// way the fallback channel should absorb. A caller cancellation or deadline is
// intentional and never retried; everything else — transport errors and ANY
// status other than 200 — retries once on the backup.
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
	return resp.StatusCode != http.StatusOK
}
