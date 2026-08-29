package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

type blockingTimeBudgetRegistry struct{}

func (blockingTimeBudgetRegistry) List(string) []ToolDef { return nil }

func (blockingTimeBudgetRegistry) Run(ctx context.Context, _ string, _ []byte, _ *ToolContext) (string, []Citation, error) {
	<-ctx.Done()
	return "", nil, ctx.Err()
}

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

func TestToolContextChargeReturnsTypedBudgetErrors(t *testing.T) {
	total := &ToolContext{counts: map[string]int{"__total__": maxToolCallsPerTurn}}
	err := total.charge("unlimited-test-tool")
	var totalBudget *ErrToolBudgetExceeded
	if !errors.As(err, &totalBudget) || totalBudget.Kind != "total_calls" || totalBudget.Limit != maxToolCallsPerTurn {
		t.Fatalf("total budget error = %#v (%v)", totalBudget, err)
	}

	toolName := "aivory_web_search"
	limit := perTurnToolLimits[toolName]
	perTool := &ToolContext{counts: map[string]int{toolName: limit}}
	err = perTool.charge(toolName)
	var toolBudget *ErrToolBudgetExceeded
	if !errors.As(err, &toolBudget) || toolBudget.Kind != "tool_calls" || toolBudget.Tool != toolName || toolBudget.Limit != limit {
		t.Fatalf("per-tool budget error = %#v (%v)", toolBudget, err)
	}

	if maxToolTimePerTurn > 0 {
		timed := &ToolContext{
			counts:              map[string]int{},
			toolBudgetStartedAt: time.Now().Add(-maxToolTimePerTurn),
		}
		err = timed.charge("unlimited-test-tool")
		var timeBudget *ErrToolBudgetExceeded
		if !errors.As(err, &timeBudget) || timeBudget.Kind != "time" || timeBudget.Duration != maxToolTimePerTurn {
			t.Fatalf("time budget error = %#v (%v)", timeBudget, err)
		}
		if remaining, limited := timed.toolTimeRemaining(); !limited || remaining != 0 {
			t.Fatalf("exhausted time remaining = %v/%v, want 0/true", remaining, limited)
		}
	}
}

func TestToolContextBudgetsOnlyAivorySystemTools(t *testing.T) {
	tc := &ToolContext{
		SystemTools: map[string]bool{"aivory_web_search": true},
		counts:      map[string]int{"__total__": maxToolCallsPerTurn},
	}
	if err := tc.charge("mcp_remote_lookup"); err != nil {
		t.Fatalf("MCP call consumed the system-tool budget: %v", err)
	}
	if got := tc.counts["__total__"]; got != maxToolCallsPerTurn {
		t.Fatalf("MCP call changed total system-tool count to %d", got)
	}
	if err := tc.charge("aivory_web_search"); !IsToolBudgetExceeded(err) {
		t.Fatalf("system tool did not enforce the total budget: %v", err)
	}
}

func TestFastToolBudgetsUseIndependentEnvironmentSettings(t *testing.T) {
	for _, key := range []string{
		"AIVORY_LLM_FAST_TOOL_LIMITS_WEB_SEARCH",
		"AIVORY_LLM_FAST_TOOL_LIMITS_WEB_FETCH",
		"AIVORY_LLM_FAST_TOOL_LIMITS_FETCH_IMAGE",
		"AIVORY_LLM_FAST_TOOL_LIMITS_IMAGE_GENERATE",
		"AIVORY_LLM_FAST_TOOL_LIMITS_PYTHON_EXECUTE",
		"AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN_FAST",
	} {
		t.Setenv(key, "")
	}
	// Changing normal-mode limits must not change fast-mode defaults.
	t.Setenv("AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_SEARCH", "400")
	t.Setenv("AIVORY_LLM_PER_TURN_TOOL_LIMITS_WEB_FETCH", "300")
	t.Setenv("AIVORY_LLM_PER_TURN_TOOL_LIMITS_FETCH_IMAGE", "200")
	t.Setenv("AIVORY_LLM_PER_TURN_TOOL_LIMITS_PYTHON_EXECUTE", "100")
	t.Setenv("AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN", "120")

	limits := configuredFastToolLimits()
	wantDefaults := map[string]int{
		"aivory_web_search": 4,
		"web_fetch":         3,
		"fetch_image":       0,
		"image_generate":    2,
		"python_execute":    0,
	}
	for name, want := range wantDefaults {
		if got := limits[name]; got != want {
			t.Fatalf("default fast %s limit=%d, want %d", name, got, want)
		}
	}
	if got := configuredFastMaxToolCallsPerTurn(); got != 12 {
		t.Fatalf("default fast total limit=%d, want 12", got)
	}

	t.Setenv("AIVORY_LLM_FAST_TOOL_LIMITS_WEB_SEARCH", "7")
	t.Setenv("AIVORY_LLM_FAST_TOOL_LIMITS_WEB_FETCH", "5")
	t.Setenv("AIVORY_LLM_FAST_TOOL_LIMITS_FETCH_IMAGE", "2")
	t.Setenv("AIVORY_LLM_FAST_TOOL_LIMITS_IMAGE_GENERATE", "3")
	t.Setenv("AIVORY_LLM_FAST_TOOL_LIMITS_PYTHON_EXECUTE", "1")
	t.Setenv("AIVORY_LLM_MAX_TOOL_CALLS_PER_TURN_FAST", "19")
	limits = configuredFastToolLimits()
	wantOverrides := map[string]int{
		"aivory_web_search": 7,
		"web_fetch":         5,
		"fetch_image":       2,
		"image_generate":    3,
		"python_execute":    1,
	}
	for name, want := range wantOverrides {
		if got := limits[name]; got != want {
			t.Fatalf("configured fast %s limit=%d, want %d", name, got, want)
		}
	}
	if got := configuredFastMaxToolCallsPerTurn(); got != 19 {
		t.Fatalf("configured fast total limit=%d, want 19", got)
	}
}

func TestToolBudgetFinalizationFailureDoesNotBecomeCancellation(t *testing.T) {
	err := toolBudgetFinalizationError(
		&ErrToolBudgetExceeded{Kind: "total_calls", Limit: 1},
		context.DeadlineExceeded,
	)
	if !IsToolBudgetExceeded(err) {
		t.Fatalf("finalization error = %v, want budget error", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("finalization deadline leaked through as a normal stopped-turn deadline")
	}
}

func TestOrchestratorToolRunnerCapsCallAtRemainingTurnTime(t *testing.T) {
	if maxToolTimePerTurn <= 0 {
		t.Skip("turn tool-time budget disabled")
	}
	tc := &ToolContext{
		counts:              map[string]int{},
		toolBudgetStartedAt: time.Now().Add(-maxToolTimePerTurn + 20*time.Millisecond),
	}
	runner := &orchToolRunner{
		orch: &Orchestrator{tools: blockingTimeBudgetRegistry{}},
		ctx:  tc,
	}
	started := time.Now()
	_, _, err := runner.Run(context.Background(), "blocking-test-tool", json.RawMessage(`{}`))
	var budgetErr *ErrToolBudgetExceeded
	if !errors.As(err, &budgetErr) || budgetErr.Kind != "time" || budgetErr.Duration != maxToolTimePerTurn {
		t.Fatalf("runner error = %#v (%v)", budgetErr, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("runner ignored remaining turn time: %v", elapsed)
	}
}
