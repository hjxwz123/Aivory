// Package tooldiagnostics collects administrator-only evidence without changing
// tool results or consuming response bytes ahead of the caller.
package tooldiagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const BodyLimit = 64 * 1024
const maxRequests = 32

var diagnosticURLPattern = regexp.MustCompile(`https?://[^\s"<>\\]+`)
var diagnosticSecretPattern = regexp.MustCompile(`(?i)("(?:[^"\n]*(?:api[-_]?key|secret|password|authorization|cookie|credential|access[-_]?token|refresh[-_]?token|security[-_]?token|signature)[^"\n]*|[^"\n]*token|key)"\s*:\s*)("(?:\\.|[^"\\])*"?|[^,\s}\]]+)`)
var diagnosticBinaryPattern = regexp.MustCompile(`(?i)("(?:data|b64_json|b64json|[^"]*base64)"\s*:\s*)"[A-Za-z0-9+/_=-]{512,}"?`)
var diagnosticInlineDataPattern = regexp.MustCompile(`\bdata:[^\s"<>]+`)
var sensitiveNameReplacer = strings.NewReplacer("-", "", "_", "", " ", "")

type Request struct {
	Method            string `json:"method"`
	URL               string `json:"url"`
	StatusCode        int    `json:"status_code,omitempty"`
	DurationMS        int64  `json:"duration_ms"`
	RequestBody       string `json:"request_body,omitempty"`
	ResponseBody      string `json:"response_body,omitempty"`
	Error             string `json:"error,omitempty"`
	RequestTruncated  bool   `json:"request_truncated,omitempty"`
	ResponseTruncated bool   `json:"response_truncated,omitempty"`
}

type Issue struct {
	Scope string `json:"scope"`
	Error string `json:"error"`
}

type Collector struct {
	mu                               sync.Mutex
	bodies                           bool
	requests                         []*Request
	issues                           []Issue
	secrets                          []string
	secretSet                        map[string]struct{}
	ServerID, ServerName, RemoteName string
}

type collectorKey struct{}
type callIDKey struct{}

func Start(ctx context.Context, bodies bool) (context.Context, *Collector) {
	c := &Collector{bodies: bodies}
	return context.WithValue(ctx, collectorKey{}, c), c
}

func From(ctx context.Context) *Collector {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}

func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

func (c *Collector) AddSecrets(values ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, value := range values {
		if len(value) >= 4 {
			if c.secretSet == nil {
				c.secretSet = make(map[string]struct{})
			}
			for _, encoded := range []string{value, url.QueryEscape(value), url.PathEscape(value)} {
				if _, exists := c.secretSet[encoded]; !exists {
					c.secretSet[encoded] = struct{}{}
					c.secrets = append(c.secrets, encoded)
				}
			}
		}
	}
}

func (c *Collector) CaptureSecrets(raw string) {
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, child := range value {
				if sensitive(key) {
					if text, ok := child.(string); ok {
						c.AddSecrets(text)
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	var node any
	if json.Unmarshal([]byte(raw), &node) == nil {
		walk(node)
	}
}

func Note(ctx context.Context, scope string, err error) {
	if c := From(ctx); c != nil && err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.issues) < maxRequests {
			c.issues = append(c.issues, Issue{Scope: scope, Error: err.Error()})
		}
	}
}

func sensitive(key string) bool {
	key = strings.ToLower(sensitiveNameReplacer.Replace(key))
	return strings.Contains(key, "apikey") || strings.Contains(key, "secret") || strings.Contains(key, "password") ||
		strings.Contains(key, "authorization") || strings.Contains(key, "cookie") || strings.Contains(key, "credential") ||
		key == "token" || key == "key" || strings.Contains(key, "accesstoken") || strings.Contains(key, "refreshtoken") ||
		strings.Contains(key, "signature") || strings.HasSuffix(key, "token")
}

func sanitizeURL(raw string, bodies bool) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	u.User = nil
	u.Fragment = ""
	q := u.Query()
	for key := range q {
		if sensitive(key) || (!bodies && (key == "q" || key == "query" || key == "text" || key == "input")) {
			q.Set(key, "[redacted]")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func Limit(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return strings.ToValidUTF8(value, ""), false
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value, true
}

func (c *Collector) Sanitize(value string) string {
	c.mu.Lock()
	secrets := append([]string(nil), c.secrets...)
	c.mu.Unlock()
	var scrub func(any) any
	scrub = func(node any) any {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				if sensitive(key) {
					v[key] = "[redacted]"
				} else {
					v[key] = scrub(child)
				}
			}
		case []any:
			for i, child := range v {
				v[i] = scrub(child)
			}
		case string:
			if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
				return sanitizeURL(v, c.bodies)
			}
			if strings.HasPrefix(v, "data:") {
				return "[redacted inline data]"
			}
			if len(v) > 512 && strings.IndexFunc(v, func(r rune) bool {
				return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=')
			}) < 0 {
				return "[redacted binary data]"
			}
		}
		return node
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var node any
	if decoder.Decode(&node) == nil {
		var extra any
		if decoder.Decode(&extra) == io.EOF {
			if raw, err := json.Marshal(scrub(node)); err == nil {
				value = string(raw)
			}
		}
	}
	value = diagnosticSecretPattern.ReplaceAllString(value, `${1}"[redacted]"`)
	value = diagnosticBinaryPattern.ReplaceAllString(value, `${1}"[redacted binary data]"`)
	value = diagnosticInlineDataPattern.ReplaceAllString(value, "[redacted inline data]")
	value = diagnosticURLPattern.ReplaceAllStringFunc(value, func(raw string) string { return sanitizeURL(raw, c.bodies) })
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	return strings.ToValidUTF8(value, "")
}

func (c *Collector) HasErrors() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.issues) > 0 {
		return true
	}
	for _, request := range c.requests {
		if request.StatusCode >= 400 || request.Error != "" {
			return true
		}
	}
	return false
}

func (c *Collector) Snapshot() ([]Request, []Issue, bool) {
	c.mu.Lock()
	requests := make([]Request, 0, len(c.requests))
	for _, request := range c.requests {
		requests = append(requests, *request)
	}
	issues := append([]Issue(nil), c.issues...)
	c.mu.Unlock()
	hasErrors := len(issues) > 0
	for i := range requests {
		r := &requests[i]
		r.URL = c.Sanitize(r.URL)
		r.Error, _ = Limit(c.Sanitize(r.Error), BodyLimit)
		var requestTruncated, responseTruncated bool
		r.RequestBody, requestTruncated = Limit(c.Sanitize(r.RequestBody), BodyLimit)
		r.ResponseBody, responseTruncated = Limit(c.Sanitize(r.ResponseBody), BodyLimit)
		r.RequestTruncated = r.RequestTruncated || requestTruncated
		r.ResponseTruncated = r.ResponseTruncated || responseTruncated
		hasErrors = hasErrors || r.StatusCode >= 400 || r.Error != ""
	}
	for i := range issues {
		if c.bodies {
			issues[i].Scope = c.Sanitize(issues[i].Scope)
		} else {
			issues[i].Scope = ""
		}
		issues[i].Error, _ = Limit(c.Sanitize(issues[i].Error), BodyLimit)
	}
	return requests, issues, hasErrors
}

// Do observes only bytes the tool itself reads; binary image/file bodies are
// excluded, and each request has a bounded diagnostic buffer.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	c := From(req.Context())
	if c == nil {
		return client.Do(req)
	}
	for key, values := range req.Header {
		if sensitive(key) {
			for _, value := range values {
				c.AddSecrets(value)
				if parts := strings.Fields(value); len(parts) == 2 {
					c.AddSecrets(parts[1])
				}
			}
		}
	}
	if req.URL.User != nil {
		password, _ := req.URL.User.Password()
		c.AddSecrets(password)
	}
	for key, values := range req.URL.Query() {
		if sensitive(key) {
			c.AddSecrets(values...)
		}
	}
	r := &Request{Method: req.Method, URL: sanitizeURL(req.URL.String(), c.bodies)}
	if c.bodies && req.GetBody != nil && strings.Contains(req.Header.Get("Content-Type"), "json") {
		if body, err := req.GetBody(); err == nil {
			data, _ := io.ReadAll(io.LimitReader(body, BodyLimit+1))
			body.Close()
			r.RequestBody, r.RequestTruncated = Limit(string(data), BodyLimit)
			c.CaptureSecrets(string(data))
		}
	}
	c.mu.Lock()
	if len(c.requests) >= maxRequests {
		c.mu.Unlock()
		return client.Do(req)
	}
	c.requests = append(c.requests, r)
	c.mu.Unlock()
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		c.mu.Lock()
		r.Error, r.DurationMS = err.Error(), time.Since(started).Milliseconds()
		c.mu.Unlock()
		return resp, err
	}
	c.mu.Lock()
	r.StatusCode, r.DurationMS = resp.StatusCode, time.Since(started).Milliseconds()
	c.mu.Unlock()
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	capture := c.bodies && (strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") || strings.Contains(contentType, "xml") || contentType == "")
	resp.Body = &observedBody{ReadCloser: resp.Body, collector: c, request: r, started: started, capture: capture}
	return resp, nil
}

type observedBody struct {
	io.ReadCloser
	collector *Collector
	request   *Request
	started   time.Time
	capture   bool
	buf       bytes.Buffer
	truncated bool
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.capture {
		remaining := BodyLimit - b.buf.Len()
		if n > remaining {
			b.truncated = true
		}
		if remaining > n {
			remaining = n
		}
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
	}
	if err != nil && err != io.EOF {
		b.collector.mu.Lock()
		b.request.Error = err.Error()
		b.collector.mu.Unlock()
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.ReadCloser.Close()
	b.collector.mu.Lock()
	b.request.DurationMS = time.Since(b.started).Milliseconds()
	b.request.ResponseBody = strings.ToValidUTF8(b.buf.String(), "")
	b.request.ResponseTruncated = b.truncated
	if err != nil && b.request.Error == "" {
		b.request.Error = err.Error()
	}
	b.collector.mu.Unlock()
	return err
}
