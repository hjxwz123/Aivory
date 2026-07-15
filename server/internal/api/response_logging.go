package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	errorResponseCaptureLimit = 8 << 10
	errorReasonLogLimit       = 2048
	requestRouteLogLimit      = 512
)

// responseLogWriter observes the final status and a bounded error-response
// prefix without changing what is sent to the client. Successful response
// bodies (including SSE and file downloads) are never buffered.
type responseLogWriter struct {
	http.ResponseWriter
	status        int
	body          bytes.Buffer
	bodyTruncated bool
	errorReason   string
	routePattern  string
}

func (w *responseLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseLogWriter) Header() http.Header { return w.ResponseWriter.Header() }

func (w *responseLogWriter) WriteHeader(status int) {
	// net/http permits informational responses before the one final response.
	// A 101 is final because the connection switches protocols.
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseLogWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && w.status >= http.StatusBadRequest {
		w.capture(p[:n])
	}
	return n, err
}

func (w *responseLogWriter) finalStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *responseLogWriter) capture(p []byte) {
	remaining := errorResponseCaptureLimit - w.body.Len()
	if remaining <= 0 {
		w.bodyTruncated = true
		return
	}
	if len(p) > remaining {
		_, _ = w.body.Write(p[:remaining])
		w.bodyTruncated = true
		return
	}
	_, _ = w.body.Write(p)
}

func (w *responseLogWriter) recordError(err error) {
	if err != nil && w.errorReason == "" {
		w.errorReason = err.Error()
	}
}

func (w *responseLogWriter) recordRoute(pattern string) {
	if w.routePattern == "" {
		w.routePattern = pattern
	}
}

func (w *responseLogWriter) flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseLogWriter) hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func (w *responseLogWriter) push(target string, opts *http.PushOptions) error {
	return w.ResponseWriter.(http.Pusher).Push(target, opts)
}

func (w *responseLogWriter) readFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	// Keep the underlying fast path for successful files. Error responses must
	// flow through Write so their bounded prefix remains observable.
	if w.status < http.StatusBadRequest {
		return w.ResponseWriter.(io.ReaderFrom).ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

// The optional ResponseWriter interfaces must be preserved exactly. SSE checks
// Flusher directly, Gorilla WebSocket checks Hijacker directly, HTTP/2 can use
// Pusher, and large file responses benefit from ReaderFrom.
type responseLogF struct{ *responseLogWriter }

func (w *responseLogF) Flush() { w.flush() }

type responseLogH struct{ *responseLogWriter }

func (w *responseLogH) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }

type responseLogP struct{ *responseLogWriter }

func (w *responseLogP) Push(target string, opts *http.PushOptions) error { return w.push(target, opts) }

type responseLogFH struct{ *responseLogWriter }

func (w *responseLogFH) Flush()                                       { w.flush() }
func (w *responseLogFH) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }

type responseLogFP struct{ *responseLogWriter }

func (w *responseLogFP) Flush() { w.flush() }
func (w *responseLogFP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogHP struct{ *responseLogWriter }

func (w *responseLogHP) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }
func (w *responseLogHP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogFHP struct{ *responseLogWriter }

func (w *responseLogFHP) Flush()                                       { w.flush() }
func (w *responseLogFHP) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }
func (w *responseLogFHP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogR struct{ *responseLogWriter }

func (w *responseLogR) ReadFrom(r io.Reader) (int64, error) { return w.readFrom(r) }

type responseLogRF struct{ *responseLogR }

func (w *responseLogRF) Flush() { w.flush() }

type responseLogRH struct{ *responseLogR }

func (w *responseLogRH) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }

type responseLogRP struct{ *responseLogR }

func (w *responseLogRP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogRFH struct{ *responseLogR }

func (w *responseLogRFH) Flush()                                       { w.flush() }
func (w *responseLogRFH) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }

type responseLogRFP struct{ *responseLogR }

func (w *responseLogRFP) Flush() { w.flush() }
func (w *responseLogRFP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogRHP struct{ *responseLogR }

func (w *responseLogRHP) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }
func (w *responseLogRHP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

type responseLogRFHP struct{ *responseLogR }

func (w *responseLogRFHP) Flush()                                       { w.flush() }
func (w *responseLogRFHP) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijack() }
func (w *responseLogRFHP) Push(target string, opts *http.PushOptions) error {
	return w.push(target, opts)
}

func wrapResponseLogWriter(base *responseLogWriter) http.ResponseWriter {
	_, f := base.ResponseWriter.(http.Flusher)
	_, h := base.ResponseWriter.(http.Hijacker)
	_, p := base.ResponseWriter.(http.Pusher)
	_, r := base.ResponseWriter.(io.ReaderFrom)

	if r {
		rw := &responseLogR{responseLogWriter: base}
		switch {
		case f && h && p:
			return &responseLogRFHP{responseLogR: rw}
		case f && h:
			return &responseLogRFH{responseLogR: rw}
		case f && p:
			return &responseLogRFP{responseLogR: rw}
		case h && p:
			return &responseLogRHP{responseLogR: rw}
		case f:
			return &responseLogRF{responseLogR: rw}
		case h:
			return &responseLogRH{responseLogR: rw}
		case p:
			return &responseLogRP{responseLogR: rw}
		default:
			return rw
		}
	}

	switch {
	case f && h && p:
		return &responseLogFHP{responseLogWriter: base}
	case f && h:
		return &responseLogFH{responseLogWriter: base}
	case f && p:
		return &responseLogFP{responseLogWriter: base}
	case h && p:
		return &responseLogHP{responseLogWriter: base}
	case f:
		return &responseLogF{responseLogWriter: base}
	case h:
		return &responseLogH{responseLogWriter: base}
	case p:
		return &responseLogP{responseLogWriter: base}
	default:
		return base
	}
}

func recordResponseError(w http.ResponseWriter, err error) bool {
	recorder, ok := w.(interface{ recordError(error) })
	if ok {
		recorder.recordError(err)
	}
	return ok
}

func recordResponseRoute(w http.ResponseWriter, pattern string) {
	if recorder, ok := w.(interface{ recordRoute(string) }); ok {
		recorder.recordRoute(pattern)
	}
}

func errorResponseLoggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		observed := &responseLogWriter{ResponseWriter: w}
		next.ServeHTTP(wrapResponseLogWriter(observed), r)

		status := observed.finalStatus()
		if status >= 200 && status < 300 || status == http.StatusSwitchingProtocols {
			return
		}
		route := observed.routePattern
		if route == "" {
			route = r.URL.Path
			if route == "" {
				route = "/"
			}
		}
		route = sanitizeLogValue(route, requestRouteLogLimit, false)
		reason := observed.errorReason
		bodyDerivedReason := false
		if reason == "" && status >= http.StatusBadRequest {
			reason = responseErrorReason(observed.Header().Get("Content-Type"), observed.body.Bytes())
			bodyDerivedReason = reason != ""
		}
		if reason == "" {
			reason = http.StatusText(status)
		}
		if reason == "" {
			reason = "non-2xx response"
		}
		reason = sanitizeLogValue(reason, errorReasonLogLimit, bodyDerivedReason && observed.bodyTruncated)
		if logger != nil {
			logger.Printf("http non-2xx: method=%q route=%q status=%d duration_ms=%d reason=%q",
				r.Method, route, status, time.Since(started).Milliseconds(), reason)
			return
		}
		slog.Error("http non-2xx", "method", r.Method, "route", route, "status", status,
			"duration_ms", time.Since(started).Milliseconds(), "reason", reason)
	})
}

func responseErrorReason(contentType string, body []byte) string {
	if strings.Contains(strings.ToLower(contentType), "json") {
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &payload) == nil {
			for _, key := range []string{"error", "message", "reason", "detail"} {
				if raw, ok := payload[key]; ok {
					if reason := jsonReasonString(raw); reason != "" {
						return reason
					}
				}
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/plain") {
		return string(body)
	}
	return ""
}

func jsonReasonString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(raw, &nested) != nil {
		return ""
	}
	for _, key := range []string{"message", "reason", "detail"} {
		if value, ok := nested[key]; ok && json.Unmarshal(value, &text) == nil && text != "" {
			return text
		}
	}
	return ""
}

func sanitizeLogValue(value string, maxRunes int, truncated bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value, clipped := truncateRunes(value, maxRunes)
	value = strings.Join(strings.Fields(value), " ")
	if clipped || truncated {
		value += " [truncated]"
	}
	return value
}

func truncateRunes(value string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", value != ""
	}
	count := 0
	for index := range value {
		if count == maxRunes {
			return value[:index], true
		}
		count++
	}
	return value, false
}
