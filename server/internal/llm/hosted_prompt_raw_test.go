package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type hostedPromptRawToolRunner struct{}

func (hostedPromptRawToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	return "local result", nil, nil
}

func TestPromptHostedRawEnvelopeExtractsCompleteProviderResults(t *testing.T) {
	const evidence = "HOSTED_MIDDLE_EVIDENCE accession=PMC123 decision=approved"
	complete := "head " + strings.Repeat("provider filler ", 500) + evidence + strings.Repeat(" tail filler", 500)
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{
			name: "openai function output",
			raw: json.RawMessage(`[{"type":"function_call_output","call_id":"call_1","output":` +
				mustMarshal(complete) + `}]`),
		},
		{
			name: "openai hosted call output",
			raw: json.RawMessage(`[{"type":"code_interpreter_call","id":"ci_1","outputs":[{"type":"logs","logs":` +
				mustMarshal(complete) + `}]}]`),
		},
		{
			name: "anthropic hosted result",
			raw: json.RawMessage(`[{"role":"assistant","content":[{"type":"web_search_tool_result","tool_use_id":"srv_1",` +
				`"content":[{"type":"web_search_result","title":` + mustMarshal(complete) + `,"url":"https://example.test"}]}]}]`),
		},
		{
			name: "gemini function response",
			raw: json.RawMessage(`[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"content":` +
				mustMarshal(complete) + `}}}]}]`),
		},
		{
			name: "gemini hosted code result",
			raw: json.RawMessage(`[{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":` +
				mustMarshal(complete) + `}}]}]`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, blocks, _, _, _, raw, err := RunPromptToolLoopWithRaw(
				context.Background(), "system", nil, nil,
				func(context.Context, []UnifiedMessage, string) (PromptToolRound, error) {
					return PromptToolRound{Text: "done", Raw: test.raw}, nil
				},
				nil,
				func(SseEvent) {},
			)
			if err != nil {
				t.Fatal(err)
			}
			envelope, ok := parsePromptToolRawEnvelope(raw)
			if !ok || len(envelope.Outputs) != 1 || !strings.Contains(envelope.Outputs[0].Output, evidence) {
				t.Fatalf("prompt hosted envelope = %+v ok=%v", envelope, ok)
			}

			blocksJSON, err := json.Marshal(blocks)
			if err != nil {
				t.Fatal(err)
			}
			var source strings.Builder
			if err := appendCompactionSourceChecked(&source, []store.Message{{
				ID: "hosted-result", Role: "assistant", Blocks: blocksJSON, Raw: raw,
			}}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(source.String(), evidence) {
				t.Fatalf("compaction source lost complete hosted result: %s", source.String())
			}
			if strings.Contains(source.String(), promptToolRawEnvelopeType) {
				t.Fatalf("compaction source exposed the internal envelope: %s", source.String())
			}
		})
	}
}

func TestPromptHostedRawEnvelopeDeduplicatesPauseResumeByStableID(t *testing.T) {
	const evidence = "FINAL_HOSTED_EVIDENCE after resume"
	round := 0
	_, _, _, _, _, raw, err := RunPromptToolLoopWithRaw(
		context.Background(), "system", nil,
		[]ToolDef{{Name: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		func(context.Context, []UnifiedMessage, string) (PromptToolRound, error) {
			round++
			if round == 1 {
				return PromptToolRound{
					Text: `<tool_call>{"name":"local_lookup","arguments":{}}</tool_call>`,
					Raw:  json.RawMessage(`[{"type":"function_call_output","call_id":"hosted_1","output":"partial hosted result"}]`),
				}, nil
			}
			return PromptToolRound{
				Text: "done",
				Raw: json.RawMessage(`[{"type":"function_call_output","call_id":"hosted_1","output":"` +
					evidence + `"}]`),
			}, nil
		},
		hostedPromptRawToolRunner{},
		func(SseEvent) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := parsePromptToolRawEnvelope(raw)
	if !ok {
		t.Fatalf("raw is not a prompt envelope: %s", raw)
	}
	hostedCount := 0
	for _, output := range envelope.Outputs {
		if output.ID != "hosted_1" {
			continue
		}
		hostedCount++
		if output.Output != evidence {
			t.Fatalf("stable hosted result was not updated after resume: %+v", output)
		}
	}
	if hostedCount != 1 {
		t.Fatalf("stable hosted result count = %d, envelope=%+v", hostedCount, envelope)
	}
}

func TestPromptHostedRawEnvelopeKeepsDistinctNoIDResults(t *testing.T) {
	round := 0
	_, _, _, _, _, raw, err := RunPromptToolLoopWithRaw(
		context.Background(), "system", nil,
		[]ToolDef{{Name: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		func(context.Context, []UnifiedMessage, string) (PromptToolRound, error) {
			round++
			if round == 1 {
				return PromptToolRound{
					Text: `<tool_call>{"name":"local_lookup","arguments":{}}</tool_call>`,
					Raw:  json.RawMessage(`[{"role":"model","parts":[{"codeExecutionResult":{"output":"first result"}}]}]`),
				}, nil
			}
			return PromptToolRound{
				Text: "done",
				Raw:  json.RawMessage(`[{"role":"model","parts":[{"codeExecutionResult":{"output":"second result"}}]}]`),
			}, nil
		},
		hostedPromptRawToolRunner{},
		func(SseEvent) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := parsePromptToolRawEnvelope(raw)
	if !ok {
		t.Fatalf("raw is not a prompt envelope: %s", raw)
	}
	codeResults := []string{}
	for _, output := range envelope.Outputs {
		if output.Name == "code_execution" && output.ID == "" {
			codeResults = append(codeResults, output.Output)
		}
	}
	if len(codeResults) != 2 || codeResults[0] != "first result" || codeResults[1] != "second result" {
		t.Fatalf("distinct no-id hosted results = %#v, envelope=%+v", codeResults, envelope)
	}
}

func TestPromptHostedRawEnvelopeDeduplicatesRepeatedNoIDResult(t *testing.T) {
	raw := json.RawMessage(`[{"role":"model","parts":[{"codeExecutionResult":{"output":"same result"}}]}]`)
	outputs := appendPromptToolRawOutputs(nil, raw)
	outputs = appendPromptToolRawOutputs(outputs, raw)
	if len(outputs) != 1 || outputs[0].Name != "code_execution" || outputs[0].Output != "same result" {
		t.Fatalf("repeated no-id hosted result was not deduplicated: %+v", outputs)
	}
}

func TestPromptHostedRawEnvelopeStableIDKeepsKnownMetadata(t *testing.T) {
	outputs := []promptToolRawOutput{{
		Name: "code_interpreter", ID: "stable_1", Output: "partial", Status: "complete",
	}}
	outputs = appendPromptToolRawOutput(outputs, promptToolRawOutput{
		ID: "stable_1", Output: "final",
	})
	if len(outputs) != 1 || outputs[0].Name != "code_interpreter" ||
		outputs[0].Status != "complete" || outputs[0].Output != "final" {
		t.Fatalf("stable-id update discarded known metadata: %+v", outputs)
	}
}

func TestPromptHostedRawEnvelopeSurvivesProviderError(t *testing.T) {
	const evidence = "HOSTED_RESULT_BEFORE_PROVIDER_ERROR"
	_, _, _, _, _, raw, err := RunPromptToolLoopWithRaw(
		context.Background(), "system", nil, nil,
		func(context.Context, []UnifiedMessage, string) (PromptToolRound, error) {
			return PromptToolRound{
				Text: "partial answer",
				Raw: json.RawMessage(`[{"role":"assistant","content":[{"type":"web_search_tool_result",` +
					`"tool_use_id":"srv_error","content":` + mustMarshal(evidence) + `}]}]`),
			}, errors.New("provider disconnected after hosted result")
		},
		nil,
		func(SseEvent) {},
	)
	if err == nil || !strings.Contains(err.Error(), "provider disconnected") {
		t.Fatalf("error = %v", err)
	}
	envelope, ok := parsePromptToolRawEnvelope(raw)
	if !ok || len(envelope.Outputs) != 1 || !strings.Contains(envelope.Outputs[0].Output, evidence) {
		t.Fatalf("hosted result was dropped on provider error: raw=%s envelope=%+v", raw, envelope)
	}
}

func TestOpenAIPromptHostedRawEnvelopeSurvivesErrorAfterCompletedResult(t *testing.T) {
	const evidence = "OPENAI_HOSTED_RESULT_BEFORE_STREAM_ERROR"
	stream := strings.Join([]string{
		// Some compatible relays omit output_item.added. A completed hosted
		// result must still make the partial result durable on its own.
		`data: {"type":"response.output_item.done","item":{"id":"ci_error","type":"code_interpreter_call","status":"completed","outputs":[{"type":"logs","logs":"` + evidence + `"}]}}`,
		`data: {"type":"response.failed","response":{"error":{"message":"relay disconnected"}}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: "responses"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run code"}}}},
		Tools:          []ToolDef{{Name: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`),
		},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "relay disconnected") {
		t.Fatalf("error = %v, want hosted round failure", err)
	}
	if result == nil {
		t.Fatal("partial prompt result is nil")
	}
	envelope, ok := parsePromptToolRawEnvelope(result.Raw)
	if !ok || len(envelope.Outputs) != 1 || !strings.Contains(envelope.Outputs[0].Output, evidence) {
		t.Fatalf("completed hosted result was dropped after provider error: raw=%s envelope=%+v", result.Raw, envelope)
	}
}

func TestGeminiPromptHostedRawEnvelopeSurvivesErrorAfterCompletedResult(t *testing.T) {
	const evidence = "GEMINI_HOSTED_RESULT_BEFORE_STREAM_ERROR"
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"` + evidence + `"}}]}}]}`,
		`data: {"error":{"message":"relay disconnected"}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&GoogleProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run code"}}}},
		Tools:          []ToolDef{{Name: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"codeExecution":{}}]}`),
		},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "relay disconnected") {
		t.Fatalf("error = %v, want hosted round failure", err)
	}
	if result == nil {
		t.Fatal("partial prompt result is nil")
	}
	envelope, ok := parsePromptToolRawEnvelope(result.Raw)
	if !ok || len(envelope.Outputs) != 1 || !strings.Contains(envelope.Outputs[0].Output, evidence) {
		t.Fatalf("completed Gemini hosted result was dropped after provider error: raw=%s envelope=%+v", result.Raw, envelope)
	}
}

func TestAnthropicPromptHostedRawEnvelopeSurvivesErrorAfterCompletedResult(t *testing.T) {
	const evidence = "ANTHROPIC_HOSTED_RESULT_BEFORE_STREAM_ERROR"
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_error","content":"` + evidence + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"error","error":{"message":"relay disconnected"}}`,
		``,
	}, "\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(server.Close)

	result, err := (&AnthropicProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "claude-test", BaseURL: server.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search"}}}},
		Tools:          []ToolDef{{Name: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolModePrompt: true,
		OfficialToolRequests: []json.RawMessage{
			json.RawMessage(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		},
	}, nil, func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "relay disconnected") {
		t.Fatalf("error = %v, want hosted round failure", err)
	}
	if result == nil {
		t.Fatal("partial prompt result is nil")
	}
	envelope, ok := parsePromptToolRawEnvelope(result.Raw)
	if !ok || len(envelope.Outputs) != 1 || !strings.Contains(envelope.Outputs[0].Output, evidence) {
		t.Fatalf("completed Anthropic hosted result was dropped after provider error: raw=%s envelope=%+v", result.Raw, envelope)
	}
}

func TestHostedPromptRunnersReturnProviderRaw(t *testing.T) {
	const evidence = "HOSTED_RUNNER_COMPLETE_EVIDENCE"
	tests := []struct {
		name     string
		response string
		request  func(string) UnifiedChatRequest
		runner   func(UnifiedChatRequest) PromptToolRunner
	}{
		{
			name: "openai responses",
			response: strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"id":"ci_1","type":"code_interpreter_call"}}`,
				`data: {"type":"response.output_item.done","item":{"id":"ci_1","type":"code_interpreter_call","status":"completed","outputs":[{"type":"logs","logs":"` + evidence + `"}]}}`,
				`data: {"type":"response.output_text.delta","delta":"done"}`,
				`data: {"type":"response.completed","response":{"output":[{"id":"ci_1","type":"code_interpreter_call","status":"completed","outputs":[{"type":"logs","logs":"` + evidence + `"}]}]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n"),
			request: func(baseURL string) UnifiedChatRequest {
				return UnifiedChatRequest{
					Model:   ModelInfo{RequestID: "gpt-test", BaseURL: baseURL, APIKey: "k", APIFormat: "responses"},
					History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run code"}}}},
					OfficialToolRequests: []json.RawMessage{
						json.RawMessage(`{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`),
					},
				}
			},
			runner: func(request UnifiedChatRequest) PromptToolRunner {
				return (&OpenAIProvider{}).promptResponsesRunOnce(request)
			},
		},
		{
			name: "anthropic",
			response: strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":"` + evidence + `"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":"done"}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"),
			request: func(baseURL string) UnifiedChatRequest {
				return UnifiedChatRequest{
					Model:   ModelInfo{RequestID: "claude-test", BaseURL: baseURL, APIKey: "k"},
					History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "search"}}}},
					OfficialToolRequests: []json.RawMessage{
						json.RawMessage(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
					},
				}
			},
			runner: func(request UnifiedChatRequest) PromptToolRunner {
				return (&AnthropicProvider{}).promptRunOnce(request)
			},
		},
		{
			name: "gemini",
			response: `data: {"candidates":[{"content":{"role":"model","parts":[` +
				`{"executableCode":{"language":"PYTHON","code":"print(1)"}},` +
				`{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"` + evidence + `"}},` +
				`{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}` + "\n\n",
			request: func(baseURL string) UnifiedChatRequest {
				return UnifiedChatRequest{
					Model:   ModelInfo{RequestID: "gemini-test", BaseURL: baseURL, APIKey: "k"},
					History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "run code"}}}},
					OfficialToolRequests: []json.RawMessage{
						json.RawMessage(`{"tools":[{"codeExecution":{}}]}`),
					},
				}
			},
			runner: func(request UnifiedChatRequest) PromptToolRunner {
				return (&GoogleProvider{}).promptRunOnce(request)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, test.response)
			}))
			t.Cleanup(server.Close)

			request := test.request(server.URL)
			round, err := test.runner(request)(context.Background(), request.History, "system")
			if err != nil {
				t.Fatal(err)
			}
			if len(round.Raw) == 0 {
				t.Fatal("hosted prompt runner dropped provider Raw")
			}
			outputs := extractCompactionRawToolOutputs(round.Raw)
			if len(outputs) != 1 || !strings.Contains(outputs[0].Text, evidence) {
				t.Fatalf("hosted prompt runner Raw did not retain result: raw=%s outputs=%+v", round.Raw, outputs)
			}
		})
	}
}

func TestPromptRawEnvelopeNeverReplaysAsProviderHistory(t *testing.T) {
	const secret = "INTERNAL_COMPLETE_TOOL_RESULT_MUST_NOT_REPLAY"
	raw := marshalPromptToolRawEnvelope([]promptToolRawOutput{{
		Name: "paper_lookup", ID: "pt_0", Output: secret, Status: "complete",
	}})
	history := []UnifiedMessage{{
		Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "visible answer"}}, Raw: raw,
	}}

	assertCanonical := func(t *testing.T, provider string, value any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body := string(encoded)
		if strings.Contains(body, secret) || strings.Contains(body, promptToolRawEnvelopeType) {
			t.Fatalf("%s replayed internal prompt envelope: %s", provider, body)
		}
		if !strings.Contains(body, "visible answer") {
			t.Fatalf("%s dropped canonical visible history: %s", provider, body)
		}
	}
	assertCanonical(t, "anthropic", historyToAnthropic(history, false))
	assertCanonical(t, "gemini", historyToGemini(history, false))

	tests := []struct {
		name       string
		apiFormat  string
		response   string
		bodyMarker string
	}{
		{
			name:       "openai chat",
			response:   `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
			bodyMarker: `"messages"`,
		},
		{
			name:      "openai responses",
			apiFormat: "responses",
			response: strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"ok"}`,
				`data: {"type":"response.completed","response":{"output":[]}}`,
				``,
			}, "\n\n"),
			bodyMarker: `"input"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requestBody, _ = io.ReadAll(request.Body)
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, test.response)
			}))
			t.Cleanup(server.Close)

			_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
				Model:   ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "k", APIFormat: test.apiFormat},
				History: history,
			}, nil, func(SseEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			body := string(requestBody)
			if !strings.Contains(body, test.bodyMarker) || !strings.Contains(body, "visible answer") {
				t.Fatalf("OpenAI request dropped canonical history: %s", body)
			}
			if strings.Contains(body, secret) || strings.Contains(body, promptToolRawEnvelopeType) {
				t.Fatalf("OpenAI request replayed internal prompt envelope: %s", body)
			}
		})
	}
}
