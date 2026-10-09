package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrackedToolNormalizesAndCachesReadOnlyRequest(t *testing.T) {
	tc := &ToolContext{}
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		executions.Add(1)
		return "result", []Citation{{URL: "https://example.test/article#section"}}, nil
	}

	output, _, err := tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"query":"  Example   Query "}`), execute,
	)
	if err != nil || output != "result" {
		t.Fatalf("first result = %q, %v", output, err)
	}
	output, _, err = tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"queries":["example query"]}`), execute,
	)
	var progressErr *ErrToolNoProgress
	if !errors.As(err, &progressErr) || progressErr.Kind != "duplicate_request" {
		t.Fatalf("cached error = %#v (%v)", progressErr, err)
	}
	if output != "result" {
		t.Fatalf("cached output = %q", output)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}
}

func TestSearchRequestKeyIncludesFiltersAndPage(t *testing.T) {
	base := normalizedToolRequestKey("aivory_web_search", []byte(`{"query":"news"}`))
	for _, input := range []string{
		`{"query":"news","categories":["news"]}`, `{"query":"news","time_range":"day"}`,
		`{"query":"news","language":"zh-CN"}`, `{"query":"news","pageno":2}`,
		`{"query":"news","engines":["bing"]}`, `{"query":"news","safesearch":0}`,
	} {
		if got := normalizedToolRequestKey("aivory_web_search", []byte(input)); got == base {
			t.Errorf("filter refinement was treated as a duplicate: %s", input)
		}
	}
	defaultInput := `{"queries":[" NEWS "],"categories":[],"time_range":"","language":null,"pageno":1,"safesearch":1}`
	if got := normalizedToolRequestKey("aivory_web_search", []byte(defaultInput)); got != base {
		t.Errorf("explicit defaults changed the request key")
	}
	first := `{"query":"news","categories":["News","general"],"language":"ZH_cn","engines":["bing","wikipedia"]}`
	second := `{"queries":["news"],"categories":["general"," news ","news"],"language":"zh-CN","engines":["Wikipedia","BING"]}`
	if normalizedToolRequestKey("aivory_web_search", []byte(first)) != normalizedToolRequestKey("aivory_web_search", []byte(second)) {
		t.Errorf("equivalent filters did not share a request key")
	}
}

func TestTrackedSearchCanRefineFiltersAndReadNextPage(t *testing.T) {
	tc := &ToolContext{}
	var calls int
	for _, input := range []string{
		`{"query":"news","categories":["news"],"time_range":"day"}`,
		`{"query":"news","categories":["general"],"time_range":"day"}`,
		`{"query":"news","categories":["general"],"time_range":"day","pageno":2}`,
	} {
		_, _, err := tc.executeTrackedTool(context.Background(), "aivory_web_search", []byte(input), func() (string, []Citation, error) {
			calls++
			return input, nil, nil // distinct evidence for each simulated page
		})
		if err != nil {
			t.Fatalf("fresh filter/page request was skipped: %s: %v", input, err)
		}
	}
	if calls != 3 {
		t.Fatalf("executed %d searches, want 3", calls)
	}
}

func TestImageGenerateRequestKeyIgnoresIrrelevantGenerateBaseIndex(t *testing.T) {
	withStaleIndex := normalizedToolRequestKey("image_generate", json.RawMessage(
		`{"prompt":"draw a poster","action":"generate","base_image":"none","base_image_index":1}`,
	))
	withoutIndex := normalizedToolRequestKey("image_generate", json.RawMessage(
		`{"base_image":"none","action":"generate","prompt":"draw a poster"}`,
	))
	if withStaleIndex != withoutIndex {
		t.Fatalf("equivalent generation requests produced different keys: %q != %q", withStaleIndex, withoutIndex)
	}
}

func TestTrackedToolCoalescesConcurrentReadOnlyRequests(t *testing.T) {
	tc := &ToolContext{}
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		if executions.Add(1) == 1 {
			close(started)
		}
		<-release
		return "page body", nil, nil
	}

	type result struct {
		output string
		err    error
	}
	results := make(chan result, 2)
	go func() {
		output, _, err := tc.executeTrackedTool(
			context.Background(), "web_fetch", json.RawMessage(`{"url":"HTTPS://Example.test:443/page#top"}`), execute,
		)
		results <- result{output: output, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first execution did not start")
	}
	go func() {
		output, _, err := tc.executeTrackedTool(
			context.Background(), "web_fetch", json.RawMessage(`{"urls":["https://example.test/page"]}`), execute,
		)
		results <- result{output: output, err: err}
	}()
	close(release)

	var successes, noProgress int
	for range 2 {
		select {
		case got := <-results:
			if got.err == nil {
				successes++
			} else if IsToolNoProgress(got.err) {
				noProgress++
			} else {
				t.Fatalf("unexpected error: %v", got.err)
			}
			if got.output != "page body" {
				t.Fatalf("output = %q", got.output)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent execution did not finish")
		}
	}
	if executions.Load() != 1 || successes != 1 || noProgress != 1 {
		t.Fatalf("executions/success/no-progress = %d/%d/%d", executions.Load(), successes, noProgress)
	}
}

func TestTrackedToolStopsWhenDifferentRequestAddsNoEvidence(t *testing.T) {
	tc := &ToolContext{}
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		executions.Add(1)
		return "same source", []Citation{{URL: "https://example.test/article?b=2&a=1"}}, nil
	}
	if _, _, err := tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"query":"first"}`), execute,
	); err != nil {
		t.Fatal(err)
	}
	_, _, err := tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"query":"second"}`), execute,
	)
	var progressErr *ErrToolNoProgress
	if !errors.As(err, &progressErr) || progressErr.Kind != "no_new_evidence" {
		t.Fatalf("second error = %#v (%v)", progressErr, err)
	}
	if executions.Load() != 2 {
		t.Fatalf("executions = %d, want 2 distinct requests", executions.Load())
	}
}

func TestTrackedImageSearchRecognizesAnotherImageOnTheSamePage(t *testing.T) {
	tc := &ToolContext{}
	for index, image := range []string{"one", "two", "two"} {
		_, _, err := tc.executeTrackedTool(context.Background(), "aivory_web_search", []byte(`{"query":"`+image+`"}`), func() (string, []Citation, error) {
			return "same page", []Citation{{URL: "https://source.test/gallery", ImageURL: "https://images.test/" + image + ".jpg"}}, nil
		})
		if index < 2 && err != nil {
			t.Fatalf("distinct image was not progress: %v", err)
		}
		if index == 2 && !IsToolNoProgress(err) {
			t.Fatalf("identical image search was not stopped: %v", err)
		}
	}
}

func TestImageDisplayOptionsHaveDistinctButNormalizedRequests(t *testing.T) {
	for _, test := range []struct {
		name, first, second string
		equal               bool
	}{
		{"search display change", `{"query":"Kyoto"}`, `{"query":"Kyoto","show_images":true}`, false},
		{"search default false", `{"query":"Kyoto"}`, `{"query":"Kyoto","show_images":false}`, true},
		{"image category default true", `{"query":"Kyoto","categories":["images"]}`, `{"query":"Kyoto","categories":["images"],"show_images":true}`, true},
		{"fetch display change", `{"url":"https://example.test/page"}`, `{"url":"https://example.test/page","show_images":true}`, false},
		{"fetch default false", `{"url":"https://example.test/page"}`, `{"url":"https://example.test/page","show_images":false}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := "aivory_web_search"
			if strings.HasPrefix(test.name, "fetch") {
				name = "web_fetch"
			}
			first, second := canonicalToolInput(name, []byte(test.first)), canonicalToolInput(name, []byte(test.second))
			if (string(first) == string(second)) != test.equal {
				t.Fatalf("canonical requests: %s / %s", first, second)
			}
		})
	}
}

func TestShowingPreviouslyRetainedImagesCountsAsToolProgress(t *testing.T) {
	tc := &ToolContext{}
	for index, show := range []bool{false, true} {
		input := []byte(fmt.Sprintf(`{"query":"Kyoto","show_images":%t}`, show))
		_, _, err := tc.executeTrackedTool(context.Background(), "aivory_web_search", input, func() (string, []Citation, error) {
			return "same image", []Citation{{URL: "https://source.test/page", ImageURL: "https://images.test/photo.jpg", ImageDisplay: &show}}, nil
		})
		if err != nil {
			t.Fatalf("display step %d was blocked as duplicate evidence: %v", index, err)
		}
	}
}

func TestTrackedWebFetchTreatsDifferentSourceAsNewEvidence(t *testing.T) {
	tc := &ToolContext{}
	execute := func() (string, []Citation, error) {
		return "identical syndicated body", nil, nil
	}
	if _, _, err := tc.executeTrackedTool(
		context.Background(), "web_fetch", json.RawMessage(`{"url":"https://first.example.test/article"}`), execute,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tc.executeTrackedTool(
		context.Background(), "web_fetch", json.RawMessage(`{"url":"https://second.example.test/article"}`), execute,
	); err != nil {
		t.Fatalf("different source was incorrectly classified as no progress: %v", err)
	}
}

func TestNormalizeToolURLDropsTrackingAndDefaultPort(t *testing.T) {
	first := normalizeToolURL("HTTPS://Example.test:443/path?utm_source=x&b=2&a=1#section")
	second := normalizeToolURL("https://example.test/path?a=1&b=2")
	if first != second {
		t.Fatalf("normalized URLs differ: %q != %q", first, second)
	}
}

func TestTrackedToolFusesRepeatedErrorPath(t *testing.T) {
	tc := &ToolContext{}
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		executions.Add(1)
		return "", nil, errors.New("remote operation rejected")
	}
	if _, _, err := tc.executeTrackedTool(
		context.Background(), "python_execute", json.RawMessage(`{"code":"x","timeout":10}`), execute,
	); err == nil || IsToolNoProgress(err) {
		t.Fatalf("first error = %v", err)
	}
	_, _, err := tc.executeTrackedTool(
		context.Background(), "python_execute", json.RawMessage(`{"timeout":10,"code":"x"}`), execute,
	)
	var progressErr *ErrToolNoProgress
	if !errors.As(err, &progressErr) || progressErr.Kind != "repeated_error" {
		t.Fatalf("repeated error = %#v (%v)", progressErr, err)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}
}

func TestTrackedToolDoesNotFuseCanceledAttempt(t *testing.T) {
	tc := &ToolContext{}
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		if executions.Add(1) == 1 {
			return "", nil, context.DeadlineExceeded
		}
		return "fresh result", []Citation{{URL: "https://example.test/fresh"}}, nil
	}
	if _, _, err := tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"query":"retryable"}`), execute,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first error = %v", err)
	}
	output, _, err := tc.executeTrackedTool(
		context.Background(), "aivory_web_search", json.RawMessage(`{"query":"retryable"}`), execute,
	)
	if err != nil || output != "fresh result" {
		t.Fatalf("retry result = %q, %v", output, err)
	}
	if executions.Load() != 2 {
		t.Fatalf("executions = %d, want 2", executions.Load())
	}
}

func TestTrackedToolSuppressesConcurrentDuplicateMutation(t *testing.T) {
	tc := &ToolContext{}
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		executions.Add(1)
		close(started)
		<-release
		return "saved", nil, nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, _ = tc.executeTrackedTool(
			context.Background(), "save_memory", json.RawMessage(`{"text":"remember"}`), execute,
		)
	}()
	<-started
	_, _, err := tc.executeTrackedTool(
		context.Background(), "save_memory", json.RawMessage(`{"text":"remember"}`), execute,
	)
	if !IsToolNoProgress(err) {
		t.Fatalf("duplicate mutation error = %v", err)
	}
	close(release)
	wg.Wait()
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}
}

func TestNoProgressFinalizationPreservesTypedFailure(t *testing.T) {
	signal := &ErrToolNoProgress{Kind: "duplicate_request", Tool: "web_fetch"}
	err := toolFinalizationError(signal, context.DeadlineExceeded)
	if !IsToolNoProgress(err) || IsToolBudgetExceeded(err) {
		t.Fatalf("finalization error = %T %v", err, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("finalization deadline leaked as ordinary cancellation")
	}
	if toolFinalizationStopReason(err) != "tool_no_progress" {
		t.Fatalf("stop reason = %q", toolFinalizationStopReason(err))
	}
}

func TestNoProgressFinalizesOnlyWhenWholeConcurrentBatchStalls(t *testing.T) {
	progressErr := &ErrToolNoProgress{Kind: "no_new_evidence", Tool: "aivory_web_search"}
	if signal := toolFinalizationErrorFromResults([]toolCallResult{
		{Output: "new source"},
		{Err: progressErr},
	}); signal != nil {
		t.Fatalf("mixed-progress batch signal = %v", signal)
	}
	if signal := toolFinalizationErrorFromResults([]toolCallResult{
		{Err: progressErr},
		{Err: &ErrToolNoProgress{Kind: "duplicate_request", Tool: "aivory_web_search"}},
	}); !IsToolNoProgress(signal) {
		t.Fatalf("stalled batch signal = %v", signal)
	}
}

func TestTrackedToolPanicReleasesConcurrentWaiters(t *testing.T) {
	tc := &ToolContext{}
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	execute := func() (string, []Citation, error) {
		executions.Add(1)
		close(started)
		<-release
		panic("broken tool")
	}

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := tc.executeTrackedTool(
			context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.test/panic"}`), execute,
		)
		firstDone <- err
	}()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := tc.executeTrackedTool(
			context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.test/panic"}`), execute,
		)
		waiterDone <- err
	}()
	close(release)

	select {
	case err := <-firstDone:
		if err == nil || IsToolNoProgress(err) {
			t.Fatalf("first panic error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("panicking execution did not finish")
	}
	select {
	case err := <-waiterDone:
		var progressErr *ErrToolNoProgress
		if !errors.As(err, &progressErr) || progressErr.Kind != "repeated_error" {
			t.Fatalf("waiter error = %#v (%v)", progressErr, err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter remained blocked after tool panic")
	}
	if executions.Load() != 1 {
		t.Fatalf("executions = %d, want 1", executions.Load())
	}
}
