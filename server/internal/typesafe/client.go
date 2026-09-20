package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Record contains metadata only: no state, question text, answers, credentials
// or upstream error bodies. UsageKnown=false means consumption is unknown,
// not that the provider necessarily charged zero tokens.
type Record struct {
	Metadata
	RequestedModel string        `json:"requested_model"`
	ServedModel    string        `json:"served_model,omitempty"`
	RequestID      string        `json:"request_id,omitempty"`
	Questions      int           `json:"questions"`
	Attempts       int           `json:"attempts"`
	Duration       time.Duration `json:"duration_ns"`
	StatusCode     int           `json:"status_code"`
	ErrorKind      ErrorKind     `json:"error_kind,omitempty"`
	Usage          Usage         `json:"usage"`
	UsageKnown     bool          `json:"usage_known"`
}

// Recorder must be concurrency-safe and respect its context. A recording error
// is returned alongside any response, never retried as a provider request.
type Recorder func(context.Context, Record) error

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
	// MaxRetries is the number of additional HTTP attempts; zero disables them.
	MaxRetries       int
	RetryBaseDelay   time.Duration
	MaxResponseBytes int64
	RecordTimeout    time.Duration
	HTTPClient       *http.Client
	Logger           *log.Logger
	Recorder         Recorder
}

type Stats struct {
	Calls        uint64
	Failures     uint64
	Attempts     uint64
	InputTokens  uint64
	OutputTokens uint64
	UnknownUsage uint64
}

// Client is safe for concurrent evaluations. Configuration is immutable after
// New; callers must not mutate request maps while Evaluate snapshots them.
type Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
	mu       sync.Mutex
	stats    Stats
}

func New(cfg Config) (*Client, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, failure(ErrDisabled, "API key is not configured")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, failure(ErrConfiguration, "invalid API key")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, failure(ErrConfiguration, "base URL must be an HTTP(S) API root without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.Path += "/systemone"
	u.RawPath = ""
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.RetryBaseDelay == 0 {
		cfg.RetryBaseDelay = 250 * time.Millisecond
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 8 << 20
	}
	if cfg.RecordTimeout == 0 {
		cfg.RecordTimeout = 3 * time.Second
	}
	if cfg.Timeout < 0 || cfg.RetryBaseDelay < 0 || cfg.MaxRetries < 0 || cfg.MaxRetries > 5 || cfg.MaxResponseBytes < 1 || cfg.MaxResponseBytes > 64<<20 || cfg.RecordTimeout < 0 {
		return nil, failure(ErrConfiguration, "invalid timeout, retry or response-size setting")
	}
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		*hc = *cfg.HTTPClient
	}
	// Never redirect a credential-bearing POST, including same-host redirects.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, endpoint: u.String(), http: hc}, nil
}

func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *Client) Evaluate(ctx context.Context, req Request, opts Options) (result *Response, returnErr error) {
	if c == nil {
		return nil, failure(ErrDisabled, "decision client is not configured")
	}
	started := time.Now()
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		req.Model = c.cfg.Model
	}
	record := Record{Metadata: opts.Metadata, RequestedModel: req.Model, Questions: len(req.Questions)}
	defer func() {
		record.Duration = time.Since(started)
		record.ErrorKind = KindOf(returnErr)
		if record.Attempts > 0 && c.cfg.Recorder != nil {
			// A canceled inference must not discard already-reported consumption.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.RecordTimeout)
			err := c.cfg.Recorder(rctx, record)
			cancel()
			if err != nil {
				returnErr = errors.Join(returnErr, &Error{Kind: ErrRecording, Message: "decision accounting failed", Cause: err})
				record.ErrorKind = ErrRecording
			}
		}
		c.mu.Lock()
		c.stats.Calls++
		c.stats.Attempts += uint64(record.Attempts)
		if returnErr != nil {
			c.stats.Failures++
		}
		if record.UsageKnown {
			c.stats.InputTokens += uint64(record.Usage.InputTokens)
			c.stats.OutputTokens += uint64(record.Usage.OutputTokens)
		} else if record.Attempts > 0 {
			c.stats.UnknownUsage++
		}
		c.mu.Unlock()
		c.log("completed", record)
	}()
	if opts.Timeout < 0 {
		return nil, failure(ErrValidation, "timeout must not be negative")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = c.cfg.Timeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, snapshot, err := prepare(req)
	if err != nil {
		return nil, err
	}
	expected := strings.TrimSpace(opts.ExpectedModel)
	if req.Model != "jev-latest" && req.Model != "jev-preview" {
		if expected != "" && expected != req.Model {
			return nil, failure(ErrValidation, "expected release conflicts with pinned request model")
		}
		expected = req.Model
	}
	for attempt := 0; ; attempt++ {
		if err := callCtx.Err(); err != nil {
			return nil, contextError(err)
		}
		hreq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, failure(ErrConfiguration, "cannot construct request")
		}
		hreq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", "application/json")
		record.Attempts++
		hresp, err := c.http.Do(hreq)
		if err != nil {
			// Transport failures may already have consumed tokens; do not replay.
			return nil, contextError(err)
		}
		record.StatusCode = hresp.StatusCode
		record.RequestID = safeRequestID(hresp.Header.Get("X-Request-ID"))
		if hresp.StatusCode != http.StatusOK {
			_ = hresp.Body.Close()
			apiErr := httpError(hresp.StatusCode, record.RequestID)
			if retryable(hresp.StatusCode) && attempt < c.cfg.MaxRetries {
				retryRecord := record
				retryRecord.ErrorKind = apiErr.Kind
				c.log("retry", retryRecord)
				if err := waitRetry(callCtx, retryDelay(hresp.Header.Get("Retry-After"), attempt, c.cfg.RetryBaseDelay)); err != nil {
					return nil, contextError(err)
				}
				continue
			}
			return nil, apiErr
		}
		raw, readErr := io.ReadAll(io.LimitReader(hresp.Body, c.cfg.MaxResponseBytes+1))
		_ = hresp.Body.Close()
		if readErr != nil {
			if callCtx.Err() != nil {
				return nil, contextError(callCtx.Err())
			}
			return nil, contextError(readErr)
		}
		if int64(len(raw)) > c.cfg.MaxResponseBytes {
			return nil, failure(ErrResponse, "response exceeds byte limit")
		}
		// Decode envelope/usage first so invalid answers do not erase known costs.
		var envelope struct {
			Model   string          `json:"model"`
			Answers json.RawMessage `json:"answers"`
			Usage   *struct {
				Input  *int `json:"input_tokens"`
				Output *int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return nil, failure(ErrResponse, "response is not valid JSON")
		}
		record.ServedModel = envelope.Model
		result = &Response{Model: envelope.Model, RequestedModel: req.Model, RequestID: record.RequestID}
		if envelope.Usage == nil || envelope.Usage.Input == nil || envelope.Usage.Output == nil || *envelope.Usage.Input < 0 || *envelope.Usage.Output < 0 {
			return result, failure(ErrResponse, "missing or invalid token usage")
		}
		result.Usage = Usage{InputTokens: *envelope.Usage.Input, OutputTokens: *envelope.Usage.Output}
		record.Usage, record.UsageKnown = result.Usage, true
		if json.Unmarshal(envelope.Answers, &result.Answers) != nil {
			result.Answers = nil
			return result, failure(ErrResponse, "invalid answers JSON")
		}
		if err := validateResponse(snapshot, result, expected); err != nil {
			// Preserve usage/version for accounting, but never expose invalid judgments.
			result.Answers = nil
			return result, err
		}
		return result, nil
	}
}

func (c *Client) log(event string, record Record) {
	if c.cfg.Logger != nil {
		body, _ := json.Marshal(record)
		c.cfg.Logger.Printf("typesafe event=%s %s", event, body)
	}
}

func safeRequestID(id string) string {
	if len(id) > 128 {
		return ""
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return ""
		}
	}
	return id
}

func contextError(err error) *Error {
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: ErrCanceled, Message: "request canceled", Cause: context.Canceled}
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return &Error{Kind: ErrTimeout, Message: "request deadline exceeded", Cause: context.DeadlineExceeded}
	}
	return failure(ErrTransport, "request transport or response read failed")
}

func httpError(status int, requestID string) *Error {
	kind := ErrHTTP
	switch status {
	case 401, 403:
		kind = ErrAuthentication
	case 400, 422:
		kind = ErrValidation
	case 429:
		kind = ErrRateLimit
	case 529:
		kind = ErrOverloaded
	}
	return &Error{Kind: kind, StatusCode: status, RequestID: requestID, Message: "upstream rejected request"}
}

func retryable(status int) bool {
	return status == 429 || status == 529 || status == 502 || status == 503 || status == 504
}

func retryDelay(header string, attempt int, base time.Duration) time.Duration {
	backoff := min(base, 5*time.Second) * time.Duration(1<<attempt)
	backoff = min(backoff, 5*time.Second)
	backoff += time.Duration(rand.Float64() * float64(backoff) * 0.25)
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds >= 0 {
		// A huge server delay must exhaust the deadline, never overflow to a retry.
		return max(backoff, time.Duration(min(seconds, int64(24*60*60)))*time.Second)
	}
	if date, err := http.ParseTime(header); err == nil {
		return max(backoff, time.Until(date))
	}
	return backoff
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
