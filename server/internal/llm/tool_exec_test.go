package llm

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestPythonConversationGateSerializesWithoutSpendingNextCallDeadline(t *testing.T) {
	releaseFirst, err := acquirePythonConversationGate(context.Background(), "conv-gated")
	if err != nil {
		t.Fatal(err)
	}

	type acquiredResult struct {
		release func()
		err     error
	}
	acquired := make(chan acquiredResult, 1)
	go func() {
		release, acquireErr := acquirePythonConversationGate(context.Background(), "conv-gated")
		acquired <- acquiredResult{release: release, err: acquireErr}
	}()

	select {
	case result := <-acquired:
		if result.release != nil {
			result.release()
		}
		t.Fatal("second Python call entered before the first released the conversation gate")
	case <-time.After(25 * time.Millisecond):
	}

	releaseFirst()
	select {
	case result := <-acquired:
		if result.err != nil {
			t.Fatalf("second acquire: %v", result.err)
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("second Python call did not enter after the first released the gate")
	}

	// A canceled waiter must drop its reference and leave the key reusable.
	releaseHeld, err := acquirePythonConversationGate(context.Background(), "conv-canceled")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquirePythonConversationGate(waitCtx, "conv-canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire error=%v", err)
	}
	releaseHeld()
	releaseAgain, err := acquirePythonConversationGate(context.Background(), "conv-canceled")
	if err != nil {
		t.Fatalf("gate was not reusable after canceled waiter: %v", err)
	}
	releaseAgain()
}

func TestPromptToolRetrySkipsDeterministicFailures(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		&ToolUserError{Message: "sandbox busy"},
		&ToolRefusalError{Message: "refused"},
	} {
		if promptToolErrorRetryable(err) {
			t.Errorf("promptToolErrorRetryable(%T) = true", err)
		}
	}
	if !promptToolErrorRetryable(errors.New("temporary upstream failure")) {
		t.Error("unknown operational failure should retain bounded prompt-tool retry")
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
