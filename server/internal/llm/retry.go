package llm

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// shouldRetryStatus returns true for HTTP status codes that warrant a retry
// per §4.10-D: 429 (rate limit) plus the 5xx family. We deliberately do NOT
// retry 4xx other than 429 — they're permanent and the model owes the user a
// real error message.
func shouldRetryStatus(status int) bool {
	if status == 429 {
		return true
	}
	if status >= 500 && status < 600 {
		return true
	}
	return false
}

// retryDelay computes the backoff wait between attempts. Honors Retry-After
// (seconds or HTTP date) when the upstream provides it; otherwise falls back
// to exponential with full jitter: rand([0, base * 2^attempt)).
func retryDelay(attempt int, h http.Header, base time.Duration) time.Duration {
	if ra := h.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(ra); err == nil {
			d := time.Until(t)
			if d > 0 {
				return d
			}
		}
	}
	if attempt < 0 {
		attempt = 0
	}
	maxExp := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if maxExp > 30*time.Second {
		maxExp = 30 * time.Second
	}
	return time.Duration(rand.Int63n(int64(maxExp) + 1))
}

// doRequestWithRetry runs `make()` once per attempt, retrying on a transient
// upstream failure (network error, 429, 5xx). The cap is 4 attempts (1 initial
// + 3 retries). Cancellation aborts immediately.
//
// `make` must return a fresh *http.Request each call because Go's http client
// consumes the request body. The caller is responsible for closing resp.Body
// on success.
func doRequestWithRetry(ctx context.Context, client *http.Client, make func() (*http.Request, error)) (*http.Response, error) {
	const maxAttempts = 4
	const baseDelay = 500 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := make()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = err
		} else if !shouldRetryStatus(resp.StatusCode) {
			return resp, nil
		} else {
			// Drain + close the body so the connection is reusable.
			_ = resp.Body.Close()
			lastErr = errFromStatus(resp.StatusCode)
			if attempt == maxAttempts-1 {
				return resp, lastErr
			}
		}
		// Last attempt — bubble the error up.
		if attempt == maxAttempts-1 {
			break
		}
		// Wait with backoff, but respect context cancellation.
		var hdr http.Header
		if resp != nil {
			hdr = resp.Header
		}
		wait := retryDelay(attempt, hdr, baseDelay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("upstream retries exhausted")
	}
	return nil, lastErr
}

type httpStatusError struct{ code int }

func (e httpStatusError) Error() string { return "upstream HTTP " + strconv.Itoa(e.code) }
func errFromStatus(code int) error      { return httpStatusError{code: code} }
