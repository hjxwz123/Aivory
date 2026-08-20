package llm

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestPublicToolErrorOutputDoesNotExposeUpstreamEndpoint(t *testing.T) {
	err := &url.Error{
		Op:  "Post",
		URL: "https://images.internal.example.test/v1/images/edits",
		Err: context.Canceled,
	}

	got := publicToolErrorOutput(err)
	if got != publicToolCanceledMessage {
		t.Fatalf("public cancellation = %q, want %q", got, publicToolCanceledMessage)
	}
	for _, secret := range []string{"images.internal.example.test", "/v1/images/edits", "https://"} {
		if strings.Contains(got, secret) {
			t.Fatalf("public cancellation exposed %q in %q", secret, got)
		}
	}
}

func TestPublicToolErrorOutputClassifiesOnlyExplicitPublicErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: publicToolTimeoutMessage},
		{name: "internal", err: errors.New("dial tcp 10.0.0.8:9000: connection refused"), want: publicToolFailureMessage},
		{name: "refusal", err: &ToolRefusalError{Message: "daily image limit reached"}, want: "daily image limit reached"},
		{name: "validation", err: &ToolUserError{Message: "prompt required"}, want: "prompt required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := publicToolErrorOutput(test.err); got != test.want {
				t.Fatalf("public error = %q, want %q", got, test.want)
			}
		})
	}
}
