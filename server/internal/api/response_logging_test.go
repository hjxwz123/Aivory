package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serveWithErrorResponseLogger(req *http.Request, next http.Handler) (*httptest.ResponseRecorder, string) {
	var logs bytes.Buffer
	rec := httptest.NewRecorder()
	logger := log.New(&logs, "", 0)
	errorResponseLoggingMiddleware(logger, next).ServeHTTP(rec, req)
	return rec, logs.String()
}

func TestErrorResponseLoggingUsesRouteTemplateAndJSONReasonWithoutSecrets(t *testing.T) {
	mx := newMux()
	mx.handle(http.MethodPost, "/api/workspaces/join/:token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":        "spreadsheet parse failed",
			"access_token": "response-secret",
		})
	}))
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/workspaces/join/path-secret?access_token=query-secret",
		strings.NewReader("request-body-secret"),
	)
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "auth_token=cookie-secret")

	rec, got := serveWithErrorResponseLogger(req, mx)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rec.Body.String(), "response-secret") {
		t.Fatalf("client response was changed: %s", rec.Body.String())
	}
	for _, want := range []string{
		`method="POST"`,
		`route="/api/workspaces/join/:token"`,
		`status=422`,
		`reason="spreadsheet parse failed"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q does not contain %q", got, want)
		}
	}
	for _, secret := range []string{
		"path-secret",
		"query-secret",
		"response-secret",
		"request-body-secret",
		"header-secret",
		"cookie-secret",
	} {
		if strings.Contains(got, secret) {
			t.Errorf("log leaked %q: %s", secret, got)
		}
	}
}

func TestErrorResponseLoggingCoversUnmatchedMuxRoute(t *testing.T) {
	mx := newMux()
	req := httptest.NewRequest(http.MethodDelete, "/api/missing-route?token=query-secret", nil)

	rec, got := serveWithErrorResponseLogger(req, mx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	for _, want := range []string{
		`method="DELETE"`,
		`route="/api/missing-route"`,
		`status=404`,
		`reason="not found"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "query-secret") {
		t.Fatalf("unmatched-route log leaked query data: %s", got)
	}
}

func TestErrorResponseLoggingSkipsAllSuccessfulResponses(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{
			name: "implicit 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "created",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "no content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name: "successful protocol upgrade",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusSwitchingProtocols)
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		{
			name: "later error status is ignored",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "still ok")
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			rec, got := serveWithErrorResponseLogger(req, tc.handler)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got != "" {
				t.Fatalf("successful response was logged: %q", got)
			}
		})
	}
}

func TestErrorResponseLoggingLogsRedirectWithoutLocationOrQuery(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.test/callback?token=location-secret")
		w.WriteHeader(http.StatusFound)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/github/start?token=query-secret", nil)

	rec, got := serveWithErrorResponseLogger(req, handler)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); !strings.Contains(location, "location-secret") {
		t.Fatalf("Location header was changed: %q", location)
	}
	for _, want := range []string{`route="/api/auth/oauth/github/start"`, `status=302`, `reason="Found"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q does not contain %q", got, want)
		}
	}
	for _, secret := range []string{"location-secret", "query-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("redirect log leaked %q: %s", secret, got)
		}
	}
}

func TestErrorResponseLoggingWriteErrorKeepsInternalReasonServerSide(t *testing.T) {
	underlying := errors.New(`insert message: messages_parent_id_fkey constraint failed`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, underlying)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/messages", nil)

	rec, got := serveWithErrorResponseLogger(req, handler)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), `"error":"internal server error"`) {
		t.Fatalf("client did not receive generic error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "messages_parent_id_fkey") {
		t.Fatalf("client response leaked internal error: %s", rec.Body.String())
	}
	if !strings.Contains(got, `reason="insert message: messages_parent_id_fkey constraint failed"`) {
		t.Fatalf("server log did not retain underlying error: %s", got)
	}
	if count := strings.Count(got, "http non-2xx:"); count != 1 {
		t.Fatalf("error was logged %d times, want once: %s", count, got)
	}
}

func TestErrorResponseLoggingPlainTextAndEmptyFallbacks(t *testing.T) {
	t.Run("plain text", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/upstream", nil)
		rec, got := serveWithErrorResponseLogger(req, handler)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
		}
		if !strings.Contains(got, `reason="upstream unavailable"`) {
			t.Fatalf("plain-text reason missing or not trimmed: %s", got)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/unavailable", nil)
		rec, got := serveWithErrorResponseLogger(req, handler)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
		if !strings.Contains(got, `reason="Service Unavailable"`) {
			t.Fatalf("status-text fallback missing: %s", got)
		}
	})
}

func TestErrorResponseLoggingBoundsLargeErrorBody(t *testing.T) {
	large := strings.Repeat("x", errorResponseCaptureLimit*2) + "tail-secret"
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, large)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/large-error", nil)

	rec, got := serveWithErrorResponseLogger(req, handler)
	if rec.Body.String() != large {
		t.Fatalf("large client response was changed: got %d bytes, want %d", rec.Body.Len(), len(large))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("bounded log is missing truncation marker: %s", got)
	}
	if strings.Contains(got, "tail-secret") {
		t.Fatalf("log captured data beyond the bounded prefix: %s", got)
	}
	if len(got) > errorReasonLogLimit+1024 {
		t.Fatalf("large response produced an unbounded log entry (%d bytes)", len(got))
	}
}

func TestErrorResponseLoggingWrapsRecoveryToLogPanics(t *testing.T) {
	panicHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	})
	wrapped := recoverMiddleware(panicHandler)
	req := httptest.NewRequest(http.MethodGet, "/api/panic", nil)

	rec, got := serveWithErrorResponseLogger(req, wrapped)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("panic response = %q", rec.Body.String())
	}
	if !strings.Contains(got, `status=500`) || !strings.Contains(got, `reason="internal server error"`) {
		t.Fatalf("recovered panic was not logged: %s", got)
	}
}

type responseInterfaceProbeWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseInterfaceProbeWriter() *responseInterfaceProbeWriter {
	return &responseInterfaceProbeWriter{header: make(http.Header)}
}

func (w *responseInterfaceProbeWriter) Header() http.Header { return w.header }

func (w *responseInterfaceProbeWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responseInterfaceProbeWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

type responseFlushProbe struct{ calls int }

func (p *responseFlushProbe) Flush() { p.calls++ }

var errResponseHijackProbe = errors.New("hijack probe")

type responseHijackProbe struct{ calls int }

func (p *responseHijackProbe) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	p.calls++
	return nil, nil, errResponseHijackProbe
}

var errResponsePushProbe = errors.New("push probe")

type responsePushProbe struct {
	calls  int
	target string
}

func (p *responsePushProbe) Push(target string, _ *http.PushOptions) error {
	p.calls++
	p.target = target
	return errResponsePushProbe
}

type responseReaderFromProbe struct {
	base  *responseInterfaceProbeWriter
	calls int
}

func (p *responseReaderFromProbe) ReadFrom(r io.Reader) (int64, error) {
	p.calls++
	return io.Copy(struct{ io.Writer }{p.base}, r)
}

type responseOptionalInterfaces struct {
	flusher    bool
	hijacker   bool
	pusher     bool
	readerFrom bool
}

func TestWrapResponseLogWriterPreservesOptionalInterfacesExactly(t *testing.T) {
	type testCase struct {
		name string
		want responseOptionalInterfaces
		make func(
			*responseInterfaceProbeWriter,
			*responseFlushProbe,
			*responseHijackProbe,
			*responsePushProbe,
			*responseReaderFromProbe,
		) http.ResponseWriter
	}
	tests := []testCase{
		{name: "none", make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, _ *responseHijackProbe, _ *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return b
		}},
		{name: "F", want: responseOptionalInterfaces{flusher: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, _ *responseHijackProbe, _ *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
			}{b, f}
		}},
		{name: "H", want: responseOptionalInterfaces{hijacker: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, h *responseHijackProbe, _ *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseHijackProbe
			}{b, h}
		}},
		{name: "P", want: responseOptionalInterfaces{pusher: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, _ *responseHijackProbe, p *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responsePushProbe
			}{b, p}
		}},
		{name: "R", want: responseOptionalInterfaces{readerFrom: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, _ *responseHijackProbe, _ *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseReaderFromProbe
			}{b, r}
		}},
		{name: "FH", want: responseOptionalInterfaces{flusher: true, hijacker: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, h *responseHijackProbe, _ *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responseHijackProbe
			}{b, f, h}
		}},
		{name: "FP", want: responseOptionalInterfaces{flusher: true, pusher: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, _ *responseHijackProbe, p *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responsePushProbe
			}{b, f, p}
		}},
		{name: "FR", want: responseOptionalInterfaces{flusher: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, _ *responseHijackProbe, _ *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responseReaderFromProbe
			}{b, f, r}
		}},
		{name: "HP", want: responseOptionalInterfaces{hijacker: true, pusher: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, h *responseHijackProbe, p *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseHijackProbe
				*responsePushProbe
			}{b, h, p}
		}},
		{name: "HR", want: responseOptionalInterfaces{hijacker: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, h *responseHijackProbe, _ *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseHijackProbe
				*responseReaderFromProbe
			}{b, h, r}
		}},
		{name: "PR", want: responseOptionalInterfaces{pusher: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, _ *responseHijackProbe, p *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responsePushProbe
				*responseReaderFromProbe
			}{b, p, r}
		}},
		{name: "FHP", want: responseOptionalInterfaces{flusher: true, hijacker: true, pusher: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, h *responseHijackProbe, p *responsePushProbe, _ *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responseHijackProbe
				*responsePushProbe
			}{b, f, h, p}
		}},
		{name: "FHR", want: responseOptionalInterfaces{flusher: true, hijacker: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, h *responseHijackProbe, _ *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responseHijackProbe
				*responseReaderFromProbe
			}{b, f, h, r}
		}},
		{name: "FPR", want: responseOptionalInterfaces{flusher: true, pusher: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, _ *responseHijackProbe, p *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responsePushProbe
				*responseReaderFromProbe
			}{b, f, p, r}
		}},
		{name: "HPR", want: responseOptionalInterfaces{hijacker: true, pusher: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, _ *responseFlushProbe, h *responseHijackProbe, p *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseHijackProbe
				*responsePushProbe
				*responseReaderFromProbe
			}{b, h, p, r}
		}},
		{name: "FHPR", want: responseOptionalInterfaces{flusher: true, hijacker: true, pusher: true, readerFrom: true}, make: func(b *responseInterfaceProbeWriter, f *responseFlushProbe, h *responseHijackProbe, p *responsePushProbe, r *responseReaderFromProbe) http.ResponseWriter {
			return &struct {
				*responseInterfaceProbeWriter
				*responseFlushProbe
				*responseHijackProbe
				*responsePushProbe
				*responseReaderFromProbe
			}{b, f, h, p, r}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := newResponseInterfaceProbeWriter()
			flushProbe := &responseFlushProbe{}
			hijackProbe := &responseHijackProbe{}
			pushProbe := &responsePushProbe{}
			readerProbe := &responseReaderFromProbe{base: base}
			underlying := tc.make(base, flushProbe, hijackProbe, pushProbe, readerProbe)
			observed := &responseLogWriter{ResponseWriter: underlying}
			wrapped := wrapResponseLogWriter(observed)

			_, gotF := wrapped.(http.Flusher)
			_, gotH := wrapped.(http.Hijacker)
			_, gotP := wrapped.(http.Pusher)
			_, gotR := wrapped.(io.ReaderFrom)
			got := responseOptionalInterfaces{flusher: gotF, hijacker: gotH, pusher: gotP, readerFrom: gotR}
			if got != tc.want {
				t.Fatalf("optional interfaces = %+v, want %+v", got, tc.want)
			}

			unwrapper, ok := wrapped.(interface{ Unwrap() http.ResponseWriter })
			if !ok {
				t.Fatal("wrapped writer does not expose Unwrap")
			}
			if unwrapped := unwrapper.Unwrap(); unwrapped != underlying {
				t.Fatalf("Unwrap() = %T, want original %T", unwrapped, underlying)
			}

			if tc.want.flusher {
				wrapped.(http.Flusher).Flush()
				if flushProbe.calls != 1 {
					t.Fatalf("Flush forwarded %d times, want once", flushProbe.calls)
				}
			}
			if tc.want.hijacker {
				_, _, err := wrapped.(http.Hijacker).Hijack()
				if !errors.Is(err, errResponseHijackProbe) || hijackProbe.calls != 1 {
					t.Fatalf("Hijack forwarding: calls=%d err=%v", hijackProbe.calls, err)
				}
			}
			if tc.want.pusher {
				err := wrapped.(http.Pusher).Push("/asset.js", nil)
				if !errors.Is(err, errResponsePushProbe) || pushProbe.calls != 1 || pushProbe.target != "/asset.js" {
					t.Fatalf("Push forwarding: calls=%d target=%q err=%v", pushProbe.calls, pushProbe.target, err)
				}
			}
			if tc.want.readerFrom {
				n, err := wrapped.(io.ReaderFrom).ReadFrom(strings.NewReader("reader data"))
				if err != nil || n != int64(len("reader data")) || readerProbe.calls != 1 {
					t.Fatalf("ReadFrom forwarding: calls=%d n=%d err=%v", readerProbe.calls, n, err)
				}
			}
		})
	}
}

type responseDeadlineProbeWriter struct {
	*responseInterfaceProbeWriter
	calls    int
	deadline time.Time
}

func (w *responseDeadlineProbeWriter) SetWriteDeadline(deadline time.Time) error {
	w.calls++
	w.deadline = deadline
	return nil
}

func TestWrapResponseLogWriterUnwrapSupportsResponseController(t *testing.T) {
	underlying := &responseDeadlineProbeWriter{responseInterfaceProbeWriter: newResponseInterfaceProbeWriter()}
	wrapped := wrapResponseLogWriter(&responseLogWriter{ResponseWriter: underlying})
	want := time.Unix(123, 456)

	if err := http.NewResponseController(wrapped).SetWriteDeadline(want); err != nil {
		t.Fatalf("SetWriteDeadline through wrapper: %v", err)
	}
	if underlying.calls != 1 || !underlying.deadline.Equal(want) {
		t.Fatalf("deadline forwarding: calls=%d deadline=%v, want %v", underlying.calls, underlying.deadline, want)
	}
}
