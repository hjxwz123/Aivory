package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"aivory/server/internal/envcfg"
)

// These caps are consumed as int (clampString's max param and len() comparisons),
// so they are wired via envcfg.Int; defaults preserve prior hardcoded behaviour.
var (
	providerRequestBodyMaxBytes  = envcfg.Int("AIVORY_LLM_PROVIDER_REQUEST_BODY_MAX_BYTES", 128*1024)
	providerRequestValueMaxBytes = envcfg.Int("AIVORY_LLM_PROVIDER_REQUEST_VALUE_MAX_BYTES", 8*1024)
)

// maxProviderRequestSnapshots bounds the per-turn snapshot list (§B5-per-request
// usage rows). A native tool loop is capped at ~20 iterations and deep research
// at a few dozen provider calls; overflow requests still stream fine — they just
// lose their own usage row and their tokens fold into the last row's residual.
const maxProviderRequestSnapshots = 64

type providerRequestSnapshot struct {
	Method  string
	URL     string
	Header  string
	Body    string
	Attempt int
	// Estimates are internal billing fallbacks for a canceled stream whose
	// provider never delivered usage for this exact request. They are derived
	// from the sanitized request and emitted provider deltas, never exposed in
	// admin request logs.
	EstimatedInputTokens  int
	EstimatedOutputTokens int
	// Fallback identifies the channel credentials used for this exact upstream
	// request. A turn may switch after one or more successful primary tool rounds,
	// so attribution cannot be derived from a single turn-wide flag.
	Fallback bool
	// Usage of the stream this request produced, attached by the provider once
	// the response has been read (§B5-per-request usage rows). HasUsage marks a
	// provider-reported count. A failed stream without HasUsage can still produce
	// a conservative consumption row when emitted deltas prove output occurred.
	Usage    Usage
	HasUsage bool
	// Error is populated for any failed upstream attempt, whether a later channel
	// fallback recovered the turn or the whole turn failed. Keeping it on the
	// exact attempt preserves both channel outcomes for admin diagnostics.
	Error string
}

type providerRequestRecorder struct {
	mu      sync.Mutex
	last    providerRequestSnapshot
	all     []providerRequestSnapshot
	attempt int
	// provider is the normalized wire-family for this turn. Provider usage APIs
	// do not share one cache-token convention: Anthropic reports cache read/write
	// outside input_tokens, while OpenAI and Google report prompt/input totals
	// that already include cached portions.
	provider string
	// captureAll keeps sanitized request diagnostics on EVERY list entry (admin
	// enabled success-request logging). Off, successful entries keep only
	// method/URL/usage; attachFailure restores allowed diagnostics for errors.
	captureAll bool
	// captureBody is the independent privacy boundary for request bodies. When
	// disabled, method/URL/sanitized headers and errors remain available, but a
	// request body is never retained even for failed attempts.
	captureBody bool
}

type providerRequestRecorderKey struct{}

func newProviderRequestRecorder(provider ...string) *providerRequestRecorder {
	recorder := &providerRequestRecorder{captureBody: true}
	if len(provider) > 0 {
		recorder.provider = providerIDForChannelType(provider[0])
	}
	return recorder
}

func contextWithProviderRequestRecorder(ctx context.Context, rec *providerRequestRecorder) context.Context {
	if rec == nil {
		return ctx
	}
	return context.WithValue(ctx, providerRequestRecorderKey{}, rec)
}

// contextWithoutProviderRequestRecorder detaches any inherited recorder so a
// nested provider call (e.g. a task-model round issued mid chat turn, whose
// usage is logged separately as a task.* row) does NOT get captured into the
// outer chat turn's per-request recorder — otherwise it would surface as a
// phantom purpose="chat" usage row and inflate the billed total (§B5-per-request
// usage rows). Stores a typed-nil so the Value lookup + nil guards short-circuit.
func contextWithoutProviderRequestRecorder(ctx context.Context) context.Context {
	if ctx.Value(providerRequestRecorderKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, providerRequestRecorderKey{}, (*providerRequestRecorder)(nil))
}

func recordProviderRequest(ctx context.Context, req *http.Request) {
	recordProviderRequestAttempt(ctx, req, false)
}

func recordProviderRequestAttempt(ctx context.Context, req *http.Request, fallback bool) {
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil || req == nil {
		return
	}
	rec.record(req, fallback)
}

// attachProviderRequestUsage pins one stream's parsed usage onto the most
// recent recorded request (§B5-per-request usage rows). Providers call it right
// after reading each iteration's response stream. Requests without parsed usage
// remain usage-less here; billing may still conservatively estimate a failed
// attempt when its recorded deltas prove that it emitted output.
func attachProviderRequestUsage(ctx context.Context, u Usage) {
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil {
		return
	}
	rec.attachUsage(u)
}

// providerRequestSnapshotUsage returns the conservative usage attributable to
// one upstream attempt. Providers normally attach terminal usage, but a stream
// can emit output and then fail before its terminal usage frame. In that case
// the recorder has an output estimate from the emitted deltas; count that
// attempt as consumed while using the request estimate only for the missing
// input field. A request with no output estimate remains non-billable, even if
// its body was sent, so plain HTTP/connection failures do not become phantom
// token charges.
func providerRequestSnapshotUsage(snapshot providerRequestSnapshot) (Usage, bool) {
	usage := snapshot.Usage
	if snapshot.HasUsage {
		// A provider's terminal usage is authoritative. Do not merge the rough
		// delta estimate into a populated field: doing so can double-count output
		// when a provider reports fewer tokens than the estimator. A partially read
		// stream can, however, have input/cache usage without a completion count;
		// fill only that missing output field from emitted deltas.
		if usage.OutputTokens == 0 && snapshot.EstimatedOutputTokens > 0 {
			usage.OutputTokens = snapshot.EstimatedOutputTokens
		}
		if usage.InputTokens == 0 && usage.OutputTokens > 0 {
			usage.InputTokens = snapshot.EstimatedInputTokens
		}
		return usage, usageHasValue(usage)
	}
	// A request without terminal usage is billable only when it emitted output.
	// This keeps pure HTTP/connection failures as zero-cost diagnostics.
	if snapshot.EstimatedOutputTokens > 0 {
		usage = Usage{OutputTokens: snapshot.EstimatedOutputTokens}
		if usage.InputTokens == 0 {
			usage.InputTokens = snapshot.EstimatedInputTokens
		}
		return usage, true
	}
	return Usage{}, false
}

// providerRequestBillingSnapshots returns successful and conservatively
// estimated consumed attempts in request order. Error text is cleared on the
// returned copies because the original failure snapshots are still emitted as
// separate zero-cost diagnostics.
func providerRequestBillingSnapshots(snapshots []providerRequestSnapshot) []providerRequestSnapshot {
	out := make([]providerRequestSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		usage, billable := providerRequestSnapshotUsage(snapshot)
		if !billable {
			continue
		}
		snapshot.Usage = usage
		snapshot.HasUsage = true
		snapshot.Error = ""
		out = append(out, snapshot)
	}
	return out
}

// providerRequestUsageTotal returns the upstream usage already attached to
// individual attempts plus conservative estimates for attempts that emitted
// output before failing. Failed attempts are included: a primary channel may
// consume tokens before a hidden task transparently succeeds on its fallback.
func providerRequestUsageTotal(snapshots []providerRequestSnapshot) Usage {
	total := Usage{}
	for _, snapshot := range providerRequestBillingSnapshots(snapshots) {
		total.InputTokens += snapshot.Usage.InputTokens
		total.OutputTokens += snapshot.Usage.OutputTokens
		total.CacheReadTokens += snapshot.Usage.CacheReadTokens
		total.CacheWriteTokens += snapshot.Usage.CacheWriteTokens
	}
	return total
}

func usageEqual(a, b Usage) bool {
	return a.InputTokens == b.InputTokens &&
		a.OutputTokens == b.OutputTokens &&
		a.CacheReadTokens == b.CacheReadTokens &&
		a.CacheWriteTokens == b.CacheWriteTokens
}

// mergeProviderRequestUsage reconciles the provider-level result with the
// recorder's per-attempt total. A transparent fallback can return only the
// fallback attempt's usage, while the recorder also contains consumed primary
// usage. Conversely, a provider loop can return a cumulative total that is
// already equal to the snapshots (or include an unretained residual). Prefer
// the exact snapshot sum when it differs in the presence of an error attempt;
// otherwise retain any larger provider-level residual without double-counting.
func mergeProviderRequestUsage(result Usage, snapshots []providerRequestSnapshot) Usage {
	requestUsage := providerRequestUsageTotal(snapshots)
	if !usageHasValue(requestUsage) {
		return result
	}
	if usageEqual(result, requestUsage) {
		return result
	}
	consumedFailedAttempt := false
	consumedFallbackAttempt := false
	for _, snapshot := range snapshots {
		_, billable := providerRequestSnapshotUsage(snapshot)
		if !billable {
			continue
		}
		if strings.TrimSpace(snapshot.Error) != "" {
			consumedFailedAttempt = true
		}
		if snapshot.Fallback {
			consumedFallbackAttempt = true
		}
	}
	if consumedFailedAttempt && consumedFallbackAttempt {
		return requestUsage
	}
	result.InputTokens = maxInt(result.InputTokens, requestUsage.InputTokens)
	result.OutputTokens = maxInt(result.OutputTokens, requestUsage.OutputTokens)
	result.CacheReadTokens = maxInt(result.CacheReadTokens, requestUsage.CacheReadTokens)
	result.CacheWriteTokens = maxInt(result.CacheWriteTokens, requestUsage.CacheWriteTokens)
	return result
}

func recordProviderRequestOutputEstimate(ctx context.Context, tokens int) {
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil {
		return
	}
	rec.attachOutputEstimate(tokens)
}

// recordProviderRequestFailure pins a non-cancellation transport, HTTP, or
// response-parsing failure to the most recent sent request attempt.
func recordProviderRequestFailure(ctx context.Context, fallback bool, err error) {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil {
		return
	}
	rec.attachFailure(fallback, truncErr(err.Error()))
}

// recordProviderRequestBuildFailure records an attempt that failed before an
// *http.Request existed. It must always create a new snapshot: attaching it to
// the recorder's last request would incorrectly turn a prior successful round
// into an error when a later tool-loop request cannot be constructed.
func recordProviderRequestBuildFailure(ctx context.Context, fallback bool, err error) {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil {
		return
	}
	rec.appendFailure(fallback, truncErr(err.Error()))
}

func (r *providerRequestRecorder) snapshot() providerRequestSnapshot {
	if r == nil {
		return providerRequestSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// snapshots returns a copy of the per-request list in request order.
func (r *providerRequestRecorder) snapshots() []providerRequestSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]providerRequestSnapshot, len(r.all))
	copy(out, r.all)
	return out
}

// maxContextTokens returns the largest prompt footprint of any successful
// upstream request in a turn. Tool loops report cumulative billing usage at the
// turn level, but compaction pressure is determined by one request at a time.
// Cache-token accounting is provider-specific. Anthropic's input_tokens
// excludes cache reads and newly-created cache entries, so both are added.
// OpenAI and Google input/prompt totals already include cached portions and
// must not have a cache detail added again.
func (r *providerRequestRecorder) maxContextTokens() int {
	maxTokens := 0
	for _, snapshot := range r.snapshots() {
		if strings.TrimSpace(snapshot.Error) != "" {
			continue
		}
		contextTokens := snapshot.EstimatedInputTokens
		if snapshot.HasUsage {
			contextTokens = snapshot.Usage.InputTokens
			if r.provider == "anthropic" {
				contextTokens += snapshot.Usage.CacheReadTokens + snapshot.Usage.CacheWriteTokens
			}
		}
		if contextTokens > maxTokens {
			maxTokens = contextTokens
		}
	}
	return maxTokens
}

func (r *providerRequestRecorder) record(req *http.Request, fallbackAttempt ...bool) {
	if r == nil || req == nil {
		return
	}
	fallback := len(fallbackAttempt) > 0 && fallbackAttempt[0]
	body := snapshotRequestBody(req)
	sanitizedBody := sanitizeProviderRequestBodyForRequest(req, body)
	storedBody := sanitizedBody
	if !r.captureBody {
		storedBody = ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
	r.last = providerRequestSnapshot{
		Method:               req.Method,
		URL:                  sanitizeProviderRequestURL(req.URL),
		Header:               sanitizeProviderRequestHeaders(req.Header),
		Body:                 storedBody,
		Attempt:              r.attempt,
		Fallback:             fallback,
		EstimatedInputTokens: estimateTokens(sanitizedBody),
	}
	if len(r.all) < maxProviderRequestSnapshots {
		entry := r.last
		if !r.captureAll {
			entry.Header, entry.Body = "", ""
		}
		r.all = append(r.all, entry)
	}
}

func (r *providerRequestRecorder) attachUsage(u Usage) {
	if r == nil {
		return
	}
	// Providers attach right after reading the stream, BEFORE branching on the
	// read error — so a request that stalled at time-to-first-token and produced
	// zero bytes arrives here with all-zero usage. Skip it: attaching would mark
	// an empty request HasUsage, minting a phantom 0-token usage row (and, on a
	// credit-paid turn, a 0-credit split row that leaks the turn into the free
	// window's `credits<=0` count on reseed). A genuinely completed round always
	// carries input tokens.
	if u == (Usage{}) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Attach only when the list's tail IS the latest request — after list
	// overflow the tail is an older request and must not absorb foreign usage
	// (the orchestrator's residual reconciliation keeps totals exact instead).
	if n := len(r.all); n > 0 && r.all[n-1].Attempt == r.attempt {
		r.all[n-1].Usage = u
		r.all[n-1].HasUsage = true
		r.last.Usage = u
		r.last.HasUsage = true
	}
}

func (r *providerRequestRecorder) attachOutputEstimate(tokens int) {
	if r == nil || tokens <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last.Attempt != r.attempt {
		return
	}
	r.last.EstimatedOutputTokens = tokens
	if n := len(r.all); n > 0 && r.all[n-1].Attempt == r.attempt {
		r.all[n-1].EstimatedOutputTokens = tokens
	}
}

func (r *providerRequestRecorder) attachFailure(fallback bool, message string) {
	if r == nil || strings.TrimSpace(message) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// sendProviderRequest recorded this attempt immediately before the failure.
	// Restore the allowed sanitized request diagnostics onto the list entry even
	// when captureAll is off. The independent captureBody privacy boundary is
	// already reflected in r.last and is never bypassed here.
	if r.attempt > 0 && r.last.Attempt == r.attempt && r.last.Fallback == fallback && r.last.Error == "" {
		r.last.Error = message
		if n := len(r.all); n > 0 && r.all[n-1].Attempt == r.attempt {
			r.all[n-1] = r.last
		}
		return
	}

	// Preserve a failure even if the matching request snapshot was not retained.
	r.attempt++
	r.last = providerRequestSnapshot{Attempt: r.attempt, Fallback: fallback, Error: message}
	if len(r.all) < maxProviderRequestSnapshots {
		r.all = append(r.all, r.last)
	}
}

func (r *providerRequestRecorder) appendFailure(fallback bool, message string) {
	if r == nil || strings.TrimSpace(message) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
	r.last = providerRequestSnapshot{Attempt: r.attempt, Fallback: fallback, Error: message}
	if len(r.all) < maxProviderRequestSnapshots {
		r.all = append(r.all, r.last)
	}
}

func snapshotRequestBody(req *http.Request) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err == nil && rc != nil {
			defer rc.Close()
			body, _ := io.ReadAll(rc)
			return body
		}
	}
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func sanitizeProviderRequestURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clone := *u
	if clone.User != nil {
		clone.User = url.User("[redacted]")
	}
	q := clone.Query()
	for key := range q {
		if isSensitiveName(key) {
			q.Set(key, "[redacted]")
		}
	}
	clone.RawQuery = q.Encode()
	return clampString(clone.String(), providerRequestValueMaxBytes)
}

func sanitizeProviderRequestHeaders(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	out := map[string]any{}
	for k, vals := range h {
		name := http.CanonicalHeaderKey(k)
		if isSensitiveName(k) {
			out[name] = "[redacted]"
			continue
		}
		clean := make([]string, 0, len(vals))
		for _, v := range vals {
			clean = append(clean, clampString(v, providerRequestValueMaxBytes))
		}
		out[name] = clean
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return ""
	}
	return clampString(string(buf), providerRequestBodyMaxBytes)
}

func sanitizeProviderRequestBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		v = sanitizeProviderJSONValue("", v)
		buf, err := json.MarshalIndent(v, "", "  ")
		if err == nil {
			return clampString(string(buf), providerRequestBodyMaxBytes)
		}
	}
	return clampString(string(body), providerRequestBodyMaxBytes)
}

// ProviderRequestDiagnostics is the sanitized request metadata persisted on
// admin-visible usage rows. It is exported so non-chat provider integrations
// such as image generation can use the exact same redaction policy.
type ProviderRequestDiagnostics struct {
	Method  string
	URL     string
	Headers string
	Body    string
}

// CaptureProviderRequestDiagnostics snapshots a provider request without
// consuming its body. includeBody is the administrator-controlled privacy
// boundary; method, URL and sanitized headers remain available when it is false.
func CaptureProviderRequestDiagnostics(req *http.Request, includeBody bool) ProviderRequestDiagnostics {
	if req == nil {
		return ProviderRequestDiagnostics{}
	}
	diagnostics := ProviderRequestDiagnostics{
		Method:  req.Method,
		URL:     sanitizeProviderRequestURL(req.URL),
		Headers: sanitizeProviderRequestHeaders(req.Header),
	}
	if includeBody {
		diagnostics.Body = sanitizeProviderRequestBodyForRequest(req, snapshotRequestBody(req))
	}
	return diagnostics
}

func sanitizeProviderRequestBodyForRequest(req *http.Request, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if req != nil {
		mediaType, params, err := mime.ParseMediaType(req.Header.Get("content-type"))
		if err == nil && strings.EqualFold(mediaType, "multipart/form-data") && params["boundary"] != "" {
			if sanitized := sanitizeProviderMultipartBody(body, params["boundary"]); sanitized != "" {
				return sanitized
			}
		}
	}
	return sanitizeProviderRequestBody(body)
}

func sanitizeProviderMultipartBody(body []byte, boundary string) string {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	fields := map[string]any{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ""
		}
		name := strings.TrimSpace(part.FormName())
		if name == "" {
			name = "field"
		}
		if filename := strings.TrimSpace(part.FileName()); filename != "" {
			n, _ := io.Copy(io.Discard, part)
			appendProviderMultipartField(fields, name, map[string]any{
				"filename":     clampString(filename, providerRequestValueMaxBytes),
				"content_type": clampString(part.Header.Get("content-type"), providerRequestValueMaxBytes),
				"bytes":        n,
				"content":      "[redacted binary]",
			})
			continue
		}
		value, _ := io.ReadAll(io.LimitReader(part, int64(providerRequestValueMaxBytes)+1))
		appendProviderMultipartField(fields, name, sanitizeProviderJSONValue(name, clampString(string(value), providerRequestValueMaxBytes)))
	}
	buf, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return ""
	}
	return clampString(string(buf), providerRequestBodyMaxBytes)
}

func appendProviderMultipartField(fields map[string]any, name string, value any) {
	if existing, ok := fields[name]; ok {
		switch current := existing.(type) {
		case []any:
			fields[name] = append(current, value)
		default:
			fields[name] = []any{current, value}
		}
		return
	}
	fields[name] = value
}

func sanitizeProviderJSONValue(key string, v any) any {
	if key != "" && !isProviderJSONTokenCountName(key) && isSensitiveName(key) {
		return "[redacted]"
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = sanitizeProviderJSONValue(k, child)
		}
		return out
	case []any:
		for i := range x {
			x[i] = sanitizeProviderJSONValue("", x[i])
		}
		return x
	case string:
		return sanitizeProviderStringForKey(key, x)
	default:
		return v
	}
}

func sanitizeProviderStringForKey(key, value string) string {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if (normalized == "data" || normalized == "b64_json" || normalized == "b64json") && looksLikeLargeBase64(value) {
		return "[redacted base64 " + decimalString(len(value)) + " chars]"
	}
	return sanitizeProviderString(value)
}

func looksLikeLargeBase64(value string) bool {
	if len(value) < 1024 || len(value)%4 != 0 {
		return false
	}
	for _, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '+' || char == '/' || char == '=' ||
			char == '-' || char == '_' || char == '\n' || char == '\r' {
			continue
		}
		return false
	}
	return true
}

func isProviderJSONTokenCountName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "max_tokens", "max_completion_tokens", "budget_tokens":
		return true
	default:
		return false
	}
}

func sanitizeProviderString(s string) string {
	if idx := strings.Index(s, ";base64,"); strings.HasPrefix(s, "data:") && idx >= 0 {
		prefix := s[:idx+len(";base64,")]
		return prefix + "[redacted base64 " + decimalString(len(s)-len(prefix)) + " chars]"
	}
	if len(s) > providerRequestValueMaxBytes {
		return clampString(s, providerRequestValueMaxBytes)
	}
	return s
}

func isSensitiveName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, part := range []string{"authorization", "api-key", "apikey", "x-api-key", "x-goog-api-key", "token", "secret", "password", "credential", "cookie"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	if n == "key" || strings.HasSuffix(n, "_key") || strings.HasSuffix(n, "-key") {
		return true
	}
	return false
}

func clampString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max < len("...[truncated]") {
		return s[:max]
	}
	cut := max - len("...[truncated]")
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut] + "...[truncated]"
}

func decimalString(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
