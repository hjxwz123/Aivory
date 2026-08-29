package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"aivory/server/internal/envcfg"
)

// toolCallSpec is a provider-agnostic tool invocation.
type toolCallSpec struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// toolCallResult is the outcome of one invocation (order-preserving).
type toolCallResult struct {
	Output    string
	Citations []Citation
	Err       error
}

const (
	publicToolCanceledMessage   = "The operation was canceled."
	publicToolTimeoutMessage    = "The tool timed out. Please try again."
	publicToolFailureMessage    = "Tool execution failed. Please try again."
	publicToolBudgetMessage     = "已达到工具执行时间上限"
	publicToolNoProgressMessage = "工具调用未获得新的有效信息"

	toolBudgetExceededOutput   = "Tool execution budget exhausted. Do not call any more tools. Use the tool results already available to provide the best possible final answer."
	toolBudgetFinalInstruction = "The tool execution budget is exhausted. Do not call or request any tools. Based only on the conversation and tool results already available, provide the best possible final answer now. Do not discuss the tool budget unless it prevents you from answering."
	toolNoProgressOutput       = "This tool request was skipped because it duplicates an earlier request, repeats a failed path, or would add no new evidence. Do not repeat this request. Use the other results already available, and call a different tool only if decisive information is still missing."
	toolNoProgressInstruction  = "Further tool calls would not add new evidence. Do not call or request any tools. Based only on the conversation and tool results already available, provide the best possible final answer now. Do not discuss this internal stopping condition unless it prevents you from answering."
)

// ErrToolBudgetExceeded is the control-flow signal raised when a turn can no
// longer execute tools. Kind is deliberately open-ended: call-count limits use
// "total_calls" or "tool_calls" today, and a wall-clock budget can use "time"
// without changing provider loops. FinalizationErr records why the single
// tool-free closing request failed; it is not unwrapped so a provider deadline
// cannot be mistaken for an ordinary user cancellation by callers.
type ErrToolBudgetExceeded struct {
	Kind            string
	Tool            string
	Limit           int
	Duration        time.Duration
	FinalizationErr error
}

func (e *ErrToolBudgetExceeded) Error() string {
	if e == nil {
		return "tool execution budget exceeded"
	}
	detail := "tool execution budget exceeded"
	if e.Kind != "" {
		detail += " (" + e.Kind
		if e.Tool != "" {
			detail += ": " + e.Tool
		}
		if e.Limit > 0 {
			detail += fmt.Sprintf(", limit=%d", e.Limit)
		}
		if e.Duration > 0 {
			detail += ", duration=" + e.Duration.String()
		}
		detail += ")"
	}
	if e.FinalizationErr != nil {
		detail += ": tool-free finalization failed: " + e.FinalizationErr.Error()
	}
	return detail
}

// IsToolBudgetExceeded survives normal error wrapping and errors.Join.
func IsToolBudgetExceeded(err error) bool {
	var budgetErr *ErrToolBudgetExceeded
	return errors.As(err, &budgetErr)
}

// ToolBudgetExceededMessage is safe to expose in persisted messages and SSE.
func ToolBudgetExceededMessage() string { return publicToolBudgetMessage }

// ErrToolNoProgress stops a repetitive tool loop without pretending that a
// call-count or time budget was exhausted. The current provider batch still
// receives a result for every call; the next and only next model request runs
// without tools and must finish from evidence already collected.
type ErrToolNoProgress struct {
	Kind            string
	Tool            string
	RequestKey      string
	FinalizationErr error
}

func (e *ErrToolNoProgress) Error() string {
	if e == nil {
		return "tool execution made no progress"
	}
	detail := "tool execution made no progress"
	if e.Kind != "" {
		detail += " (" + e.Kind
		if e.Tool != "" {
			detail += ": " + e.Tool
		}
		detail += ")"
	}
	if e.FinalizationErr != nil {
		detail += ": tool-free finalization failed: " + e.FinalizationErr.Error()
	}
	return detail
}

// IsToolNoProgress survives normal error wrapping and errors.Join.
func IsToolNoProgress(err error) bool {
	var progressErr *ErrToolNoProgress
	return errors.As(err, &progressErr)
}

// ToolNoProgressMessage is safe to expose if the single closing request fails.
func ToolNoProgressMessage() string { return publicToolNoProgressMessage }

func toolBudgetErrorFromResults(results []toolCallResult) *ErrToolBudgetExceeded {
	for _, result := range results {
		var budgetErr *ErrToolBudgetExceeded
		if errors.As(result.Err, &budgetErr) {
			return budgetErr
		}
	}
	return nil
}

// toolFinalizationErrorFromResults gives hard budgets precedence when a
// concurrent batch contains both a budget error and a no-progress signal.
func toolFinalizationErrorFromResults(results []toolCallResult) error {
	if budgetErr := toolBudgetErrorFromResults(results); budgetErr != nil {
		return budgetErr
	}
	var noProgress error
	for _, result := range results {
		if result.Err == nil {
			// One useful result means the batch as a whole progressed. Repetitive
			// siblings were still skipped, but the model may still need a later,
			// distinct request before it has enough evidence to answer.
			return nil
		}
		var progressErr *ErrToolNoProgress
		if errors.As(result.Err, &progressErr) {
			noProgress = progressErr
		}
	}
	return noProgress
}

func toolFinalizationSignal(err error) error {
	var budgetErr *ErrToolBudgetExceeded
	if errors.As(err, &budgetErr) {
		return budgetErr
	}
	var progressErr *ErrToolNoProgress
	if errors.As(err, &progressErr) {
		return progressErr
	}
	return nil
}

func toolBudgetFinalizationError(budgetErr *ErrToolBudgetExceeded, cause error) *ErrToolBudgetExceeded {
	if budgetErr == nil {
		budgetErr = &ErrToolBudgetExceeded{Kind: "unknown"}
	}
	return &ErrToolBudgetExceeded{
		Kind:            budgetErr.Kind,
		Tool:            budgetErr.Tool,
		Limit:           budgetErr.Limit,
		Duration:        budgetErr.Duration,
		FinalizationErr: cause,
	}
}

func toolFinalizationError(signal error, cause error) error {
	var budgetErr *ErrToolBudgetExceeded
	if errors.As(signal, &budgetErr) {
		return toolBudgetFinalizationError(budgetErr, cause)
	}
	var progressErr *ErrToolNoProgress
	if errors.As(signal, &progressErr) {
		return &ErrToolNoProgress{
			Kind:            progressErr.Kind,
			Tool:            progressErr.Tool,
			RequestKey:      progressErr.RequestKey,
			FinalizationErr: cause,
		}
	}
	return toolBudgetFinalizationError(nil, cause)
}

func toolFinalizationInstruction(signal error) string {
	if IsToolNoProgress(signal) {
		return toolNoProgressInstruction
	}
	return toolBudgetFinalInstruction
}

func toolFinalizationStopReason(signal error) string {
	if IsToolNoProgress(signal) {
		return "tool_no_progress"
	}
	return "tool_budget_exceeded"
}

type toolBudgetFinalizationContextKey struct{}

func contextWithToolBudgetFinalization(ctx context.Context) context.Context {
	return contextWithToolFinalization(ctx, &ErrToolBudgetExceeded{Kind: "external_finalization"})
}

func contextWithToolFinalization(ctx context.Context, signal error) context.Context {
	return context.WithValue(ctx, toolBudgetFinalizationContextKey{}, signal)
}

func toolFinalizationFromContext(ctx context.Context) error {
	signal, _ := ctx.Value(toolBudgetFinalizationContextKey{}).(error)
	return signal
}

func isToolBudgetFinalization(ctx context.Context) bool {
	return toolFinalizationFromContext(ctx) != nil
}

// ToolUserError marks a validation error whose message is intentionally safe
// to return to the model and user. Unknown tool errors are never surfaced: HTTP
// clients commonly include the full request URL (and therefore private hosts,
// ports, paths, or query credentials) in err.Error().
type ToolUserError struct{ Message string }

func (e *ToolUserError) Error() string { return e.Message }

// publicToolErrorOutput is the single user/model-facing boundary for local,
// managed, and MCP tool failures. The original error remains available to
// server-side logging and usage diagnostics; only explicit public error types
// cross this boundary.
func publicToolErrorOutput(err error) string {
	if err == nil {
		return ""
	}
	if IsToolBudgetExceeded(err) {
		return toolBudgetExceededOutput
	}
	if IsToolNoProgress(err) {
		return toolNoProgressOutput
	}
	if errors.Is(err, context.Canceled) {
		return publicToolCanceledMessage
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return publicToolTimeoutMessage
	}
	var refusal *ToolRefusalError
	if errors.As(err, &refusal) && strings.TrimSpace(refusal.Message) != "" {
		return strings.TrimSpace(refusal.Message)
	}
	var userErr *ToolUserError
	if errors.As(err, &userErr) && strings.TrimSpace(userErr.Message) != "" {
		return strings.TrimSpace(userErr.Message)
	}
	return publicToolFailureMessage
}

// toolExecutionState belongs to one user message. It deliberately does not
// outlive the request, so cached web content cannot become stale across turns.
// requests also reserves mutating calls: an identical side-effecting call is
// never executed twice, but its result is not replayed as a successful result.
type toolExecutionState struct {
	mu           sync.Mutex
	requests     map[string]*toolExecutionEntry
	evidence     map[string]struct{}
	failedRoutes map[string]string
}

type toolExecutionEntry struct {
	done   chan struct{}
	output string
	err    error
}

func newToolExecutionState() *toolExecutionState {
	return &toolExecutionState{
		requests:     make(map[string]*toolExecutionEntry),
		evidence:     make(map[string]struct{}),
		failedRoutes: make(map[string]string),
	}
}

func isRequestCachedTool(name string) bool {
	switch name {
	case "aivory_web_search", "web_fetch":
		return true
	default:
		return false
	}
}

// executeTrackedTool provides request deduplication, read-only singleflight,
// result caching, evidence-gain detection, and repeated-error circuit breaking.
// Unknown/MCP and side-effecting tools are never result-cached, but an exact
// duplicate is still suppressed so one model turn cannot repeat a mutation.
func (tc *ToolContext) executeTrackedTool(
	ctx context.Context,
	name string,
	input []byte,
	execute func() (string, []Citation, error),
) (string, []Citation, error) {
	if tc == nil {
		return execute()
	}
	state := tc.requestToolExecutionState()
	requestKey := normalizedToolRequestKey(name, input)
	readOnly := isRequestCachedTool(name)

	state.mu.Lock()
	if failedSignature, failed := state.failedRoutes[requestKey]; failed {
		state.mu.Unlock()
		return "", nil, &ErrToolNoProgress{
			Kind: "repeated_error", Tool: name,
			RequestKey: shortToolRequestKey(requestKey + failedSignature),
		}
	}
	if existing := state.requests[requestKey]; existing != nil {
		if !readOnly {
			state.mu.Unlock()
			return "", nil, &ErrToolNoProgress{
				Kind: "duplicate_request", Tool: name, RequestKey: shortToolRequestKey(requestKey),
			}
		}
		done := existing.done
		state.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
		state.mu.Lock()
		output := existing.output
		existingErr := existing.err
		state.mu.Unlock()
		kind := "duplicate_request"
		if existingErr != nil {
			kind = "repeated_error"
		}
		// The cached output is returned for diagnostics/tests, while providers use
		// the control error's compact result text to avoid duplicating context.
		return output, nil, &ErrToolNoProgress{
			Kind: kind, Tool: name, RequestKey: shortToolRequestKey(requestKey),
		}
	}
	entry := &toolExecutionEntry{done: make(chan struct{})}
	state.requests[requestKey] = entry
	state.mu.Unlock()

	var (
		output    string
		citations []Citation
		err       error
	)
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("tool %q panicked: %v", name, recovered)
			}
		}()
		output, citations, err = execute()
	}()
	if err == nil && readOnly && !state.recordNewEvidence(name, input, output, citations) {
		err = &ErrToolNoProgress{
			Kind: "no_new_evidence", Tool: name, RequestKey: shortToolRequestKey(requestKey),
		}
	}
	errSig := stableToolErrorSignature(err)
	state.mu.Lock()
	entry.output = output
	entry.err = err
	if errSig != "" && !IsToolBudgetExceeded(err) && !IsToolNoProgress(err) {
		state.failedRoutes[requestKey] = errSig
	}
	close(entry.done)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Request cancellation and transport deadlines are transient rather than
		// stable failed routes. Release current waiters, then allow a later call
		// (if the parent turn is still alive) to make one fresh attempt.
		delete(state.requests, requestKey)
	}
	state.mu.Unlock()
	return output, citations, err
}

func (s *toolExecutionState) recordNewEvidence(name string, input []byte, output string, citations []Citation) bool {
	keys := toolEvidenceKeys(name, input, output, citations)
	if len(keys) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, key := range keys {
		if _, exists := s.evidence[key]; exists {
			continue
		}
		s.evidence[key] = struct{}{}
		changed = true
	}
	return changed
}

func toolEvidenceKeys(name string, input []byte, output string, citations []Citation) []string {
	keys := make([]string, 0, len(citations)*2+2)
	seen := make(map[string]struct{}, len(citations)*2+2)
	appendKey := func(key string) {
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	appendContent := func(content string) {
		normalized := strings.Join(strings.Fields(content), " ")
		if normalized == "" {
			return
		}
		digest := sha256.Sum256([]byte(normalized))
		appendKey(fmt.Sprintf("content:%x", digest[:]))
	}
	for _, citation := range citations {
		normalized := normalizeToolURL(citation.URL)
		if normalized != "" {
			appendKey("url:" + normalized)
		} else if id := strings.TrimSpace(citation.ID); id != "" {
			appendKey("citation:" + strings.TrimSpace(citation.Source) + ":" + id)
		}
		appendContent(citation.Snippet)
	}
	if name == "web_fetch" {
		var canonical struct {
			URLs []string `json:"urls"`
		}
		if json.Unmarshal(canonicalFetchInputFromRaw(input), &canonical) == nil {
			for _, sourceURL := range canonical.URLs {
				appendKey("url:" + sourceURL)
			}
		}
	}
	// Search snippets are source-indexed: seeing the same source set through a
	// rewritten query is not progress. Fetch results have no citations, so each
	// fetched URL and normalized content hash becomes evidence identity.
	if len(citations) == 0 || len(keys) == 0 {
		contents := []string{output}
		if name == "aivory_web_search" || name == "web_fetch" {
			var batch struct {
				Items []struct {
					Content string `json:"content"`
				} `json:"items"`
			}
			if json.Unmarshal([]byte(output), &batch) == nil && len(batch.Items) > 0 {
				contents = contents[:0]
				for _, item := range batch.Items {
					contents = append(contents, item.Content)
				}
			}
		}
		for _, content := range contents {
			appendContent(content)
		}
	}
	return keys
}

func canonicalFetchInputFromRaw(input []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil {
		return nil
	}
	return canonicalFetchInput(value)
}

func stableToolErrorSignature(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		IsToolBudgetExceeded(err) || IsToolNoProgress(err) {
		return ""
	}
	message := strings.ToLower(strings.Join(strings.Fields(err.Error()), " "))
	digest := sha256.Sum256([]byte(fmt.Sprintf("%T\x00%s", err, message)))
	return fmt.Sprintf("%x", digest[:8])
}

func shortToolRequestKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", digest[:8])
}

func normalizedToolRequestKey(name string, input []byte) string {
	canonical := canonicalToolInput(name, input)
	digest := sha256.Sum256(canonical)
	return strings.TrimSpace(name) + "\x00" + fmt.Sprintf("%x", digest[:])
}

func canonicalToolInput(name string, input []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return bytes.TrimSpace(input)
	}
	if object, ok := value.(map[string]any); ok {
		switch name {
		case "aivory_web_search":
			return canonicalSearchInput(object)
		case "web_fetch":
			return canonicalFetchInput(object)
		case "image_generate":
			return canonicalImageGenerateInput(object)
		}
	}
	canonical, err := json.Marshal(normalizeJSONValue(value))
	if err != nil {
		return bytes.TrimSpace(input)
	}
	return canonical
}

func canonicalImageGenerateInput(object map[string]any) []byte {
	canonical, _ := normalizeJSONValue(object).(map[string]any)
	action, _ := canonical["action"].(string)
	baseImage, _ := canonical["base_image"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	baseImage = strings.ToLower(strings.TrimSpace(baseImage))
	canonical["action"] = action
	canonical["base_image"] = baseImage
	if action == "generate" || (action == "edit" && baseImage == "previous_generation") {
		delete(canonical, "base_image_index")
	}
	encoded, _ := json.Marshal(canonical)
	return encoded
}

func canonicalSearchInput(object map[string]any) []byte {
	queries := normalizedStringList(object["query"], object["queries"], func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	})
	canonical := map[string]any{"queries": queries}
	if topK, ok := object["top_k"]; ok {
		canonical["top_k"] = normalizeJSONValue(topK)
	}
	encoded, _ := json.Marshal(canonical)
	return encoded
}

func canonicalFetchInput(object map[string]any) []byte {
	urls := normalizedStringList(object["url"], object["urls"], normalizeToolURL)
	encoded, _ := json.Marshal(map[string]any{"urls": urls})
	return encoded
}

func normalizedStringList(single any, multiple any, normalize func(string) string) []string {
	values := make([]string, 0, 4)
	if text, ok := single.(string); ok {
		values = append(values, text)
	}
	if items, ok := multiple.([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalize(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeJSONValue(item)
		}
		return out
	default:
		return value
	}
}

func normalizeToolURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443" {
		parsed.Host = host
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]"
		}
	} else {
		parsed.Host = net.JoinHostPort(host, port)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_cid" || lower == "mc_eid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// maxConcurrentTools caps how many tools run at once within a single turn so a
// model can't fan out unbounded work (§4.3).
var maxConcurrentTools = envcfg.Int("AIVORY_LLM_MAX_CONCURRENT_TOOLS", 4)

type conversationToolGate struct {
	slot chan struct{}
	refs int
}

var pythonConversationGates = struct {
	sync.Mutex
	byConversation map[string]*conversationToolGate
}{byConversation: make(map[string]*conversationToolGate)}

// acquirePythonConversationGate serializes Python calls for one conversation.
// The provider can emit several tool calls in a single response, and separate
// requests can briefly overlap after a client cancellation. Both cases target
// the same persistent sandbox session. Waiting happens before orchToolRunner
// creates the per-call deadline, so a queued call still receives its full
// execution budget once it actually starts.
func acquirePythonConversationGate(ctx context.Context, conversationID string) (func(), error) {
	if strings.TrimSpace(conversationID) == "" {
		return func() {}, nil
	}

	pythonConversationGates.Lock()
	gate := pythonConversationGates.byConversation[conversationID]
	if gate == nil {
		gate = &conversationToolGate{slot: make(chan struct{}, 1)}
		pythonConversationGates.byConversation[conversationID] = gate
	}
	gate.refs++
	pythonConversationGates.Unlock()

	dropRef := func() {
		pythonConversationGates.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(pythonConversationGates.byConversation, conversationID)
		}
		pythonConversationGates.Unlock()
	}

	select {
	case gate.slot <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-gate.slot
				dropRef()
			})
		}, nil
	case <-ctx.Done():
		dropRef()
		return nil, ctx.Err()
	}
}

// runToolsConcurrent executes all tool calls in a turn concurrently (§4.2/§4.3)
// while preserving result order. tool_start events are emitted up-front from
// the caller's single goroutine; per-tool timeouts are enforced by the runner
// (orchToolRunner.Run wraps each call with a deadline).
func runToolsConcurrent(ctx context.Context, runner ToolRunner, calls []toolCallSpec, onEvent func(SseEvent)) []toolCallResult {
	results := make([]toolCallResult, len(calls))
	// Announce all calls first (serialised — SSE writer isn't concurrent-safe).
	for _, c := range calls {
		onEvent(SseEvent{Type: "tool_start", Name: c.Name, ID: c.ID, Input: c.Input})
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentTools)
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c toolCallSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A panic inside a tool's Execute (e.g. a nil deref while parsing an
			// adversary-influenced sandbox/tool response) unwinds out of THIS child
			// goroutine. The request-scoped recoverMiddleware can't catch it — a
			// recover() only fires for panics in the goroutine that deferred it — so
			// an unrecovered panic here would crash the whole API process and abort
			// every other in-flight generation. Contain it and surface it as a tool
			// error so the turn degrades instead of taking the server down.
			defer func() {
				if r := recover(); r != nil {
					results[i] = toolCallResult{Err: fmt.Errorf("tool %q panicked: %v", c.Name, r)}
				}
			}()
			out, cites, err := runner.Run(ctx, c.Name, c.Input)
			results[i] = toolCallResult{Output: out, Citations: cites, Err: err}
		}(i, c)
	}
	wg.Wait()
	return results
}
