package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	// Usage of the stream this request produced, attached by the provider once
	// the response has been read (§B5-per-request usage rows). HasUsage marks
	// requests that completed a stream — only those become usage rows.
	Usage    Usage
	HasUsage bool
}

type providerRequestRecorder struct {
	mu      sync.Mutex
	last    providerRequestSnapshot
	all     []providerRequestSnapshot
	attempt int
	// captureAll keeps the sanitized header/body on EVERY list entry (admin
	// enabled full success-request logging). Off, list entries keep only
	// method/URL/usage — `last` always keeps the full snapshot for the error row.
	captureAll bool
}

type providerRequestRecorderKey struct{}

func newProviderRequestRecorder() *providerRequestRecorder {
	return &providerRequestRecorder{}
}

func contextWithProviderRequestRecorder(ctx context.Context, rec *providerRequestRecorder) context.Context {
	if rec == nil {
		return ctx
	}
	return context.WithValue(ctx, providerRequestRecorderKey{}, rec)
}

func recordProviderRequest(ctx context.Context, req *http.Request) {
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil || req == nil {
		return
	}
	rec.record(req)
}

// attachProviderRequestUsage pins one stream's parsed usage onto the most
// recent recorded request (§B5-per-request usage rows). Providers call it right
// after reading each iteration's response stream; requests that never complete
// a stream (transport error, HTTP 4xx/5xx) stay usage-less and don't become
// success rows.
func attachProviderRequestUsage(ctx context.Context, u Usage) {
	rec, _ := ctx.Value(providerRequestRecorderKey{}).(*providerRequestRecorder)
	if rec == nil {
		return
	}
	rec.attachUsage(u)
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

func (r *providerRequestRecorder) record(req *http.Request) {
	if r == nil || req == nil {
		return
	}
	body := snapshotRequestBody(req)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt++
	r.last = providerRequestSnapshot{
		Method:  req.Method,
		URL:     sanitizeProviderRequestURL(req.URL),
		Header:  sanitizeProviderRequestHeaders(req.Header),
		Body:    sanitizeProviderRequestBody(body),
		Attempt: r.attempt,
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
	r.mu.Lock()
	defer r.mu.Unlock()
	// Attach only when the list's tail IS the latest request — after list
	// overflow the tail is an older request and must not absorb foreign usage
	// (the orchestrator's residual reconciliation keeps totals exact instead).
	if n := len(r.all); n > 0 && r.all[n-1].Attempt == r.attempt {
		r.all[n-1].Usage = u
		r.all[n-1].HasUsage = true
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
		return sanitizeProviderString(x)
	default:
		return v
	}
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
