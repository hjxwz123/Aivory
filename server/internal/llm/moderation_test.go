package llm

import (
	"errors"
	"fmt"
	"testing"
)

// An upstream provider whose own content filter trips (e.g. a relay returning
// `sensitive_words_detected` in the error body) must be classified as content
// moderation — a rephrase-and-retry refusal — not a transient provider error.
func TestIsUpstreamModerationError(t *testing.T) {
	moderation := []error{
		errors.New("openai 400: {\"error\":{\"code\":\"sensitive_words_detected\"}}"),
		fmt.Errorf("anthropic 400: %s", `{"message":"SENSITIVE_WORDS_DETECTED"}`), // case-insensitive
		errors.New("upstream: request blocked — sensitive_words_detected"),
	}
	for _, e := range moderation {
		if !isUpstreamModerationError(e) {
			t.Errorf("isUpstreamModerationError(%v) = false, want true", e)
		}
	}

	notModeration := []error{
		nil,
		errors.New("openai 500: rate limited"),
		errors.New("anthropic 529: overloaded"),
		errors.New("dial tcp: connection refused"),
		errors.New("context canceled"),
	}
	for _, e := range notModeration {
		if isUpstreamModerationError(e) {
			t.Errorf("isUpstreamModerationError(%v) = true, want false", e)
		}
	}
}
