package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func TestReadOpenAIChatStreamMultilineEvents(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, oneByte := range []bool{false, true} {
			name := "LF"
			if newline == "\r\n" {
				name = "CRLF"
			}
			if oneByte {
				name += "/one-byte-reads"
			}
			t.Run(name, func(t *testing.T) {
				stream := strings.Join([]string{
					": keepalive", "event: message", "id: ignored", "retry: 1000",
					`data: {"choices":[{"delta":`,
					`data: {"content":"你好"}}]}`, "",
					`data: {"choices":[{"delta":{"tool_calls":[`,
					`data: {"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`, "",
					`data:{"choices":[{"delta":{"tool_calls":[{"index":0,"function":`,
					`data: {"arguments":"\"docs\"}"}}]},"finish_reason":"tool_calls"}]}`, "",
					`data: {"choices":[],`,
					`data: "usage":{"prompt_tokens":5,"completion_tokens":2}}`, "",
					"data: [DONE]", "", "",
				}, newline)
				var reader io.Reader = strings.NewReader(stream)
				if oneByte {
					reader = iotest.OneByteReader(reader)
				}
				var events []SseEvent
				text, _, calls, finish, usage, err := readOpenAIChatStream(reader, func(e SseEvent) { events = append(events, e) })
				if err != nil {
					t.Fatal(err)
				}
				if text != "你好" || finish != "tool_calls" {
					t.Fatalf("text/finish = %q/%q", text, finish)
				}
				if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" || string(calls[0].Input) != `{"q":"docs"}` {
					t.Fatalf("calls = %+v", calls)
				}
				if usage.InputTokens != 5 || usage.OutputTokens != 2 {
					t.Fatalf("usage = %+v", usage)
				}
				if len(events) != 4 || events[0].Type != "text_delta" || events[0].Text != text || events[1].Type != "tool_start" || events[1].ID != "call_1" || events[2].PartialJson+events[3].PartialJson != `{"q":"docs"}` {
					t.Fatalf("events = %+v", events)
				}
			})
		}
	}
}

func TestReadOpenAIChatStreamMultilineUsageAfterFinish(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\n: keepalive\ndata: \"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3}}\n\n" +
		"data: {not-json}\n\ndata: {\"error\":{\"message\":\"late noise\"}}\n\ndata: [DONE]\n\n"
	text, _, _, finish, usage, err := readOpenAIChatStream(iotest.OneByteReader(strings.NewReader(stream)), func(SseEvent) {})
	if err != nil || text != "done" || finish != "stop" || usage.InputTokens != 9 || usage.OutputTokens != 3 {
		t.Fatalf("text=%q finish=%q usage=%+v err=%v", text, finish, usage, err)
	}
}

func TestReadOpenAIChatStreamBoundsMultilineEvent(t *testing.T) {
	large := strings.Repeat("data: "+strings.Repeat(" ", 1024)+"\n", 1025) + "\n"
	for _, terminal := range []bool{false, true} {
		prefix := "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}"
		if terminal {
			prefix += ",\"finish_reason\":\"stop\""
		}
		prefix += "}]}\n\n"
		text, _, _, _, _, err := readOpenAIChatStream(strings.NewReader(prefix+large), func(SseEvent) {})
		if text != "visible" {
			t.Fatalf("lost prefix: %q", text)
		}
		if terminal && err != nil {
			t.Fatalf("terminal response overturned: %v", err)
		}
		if !terminal && (err == nil || !strings.Contains(err.Error(), "event exceeds")) {
			t.Fatalf("unbounded event accepted: %v", err)
		}
	}
}

func TestReadOpenAIChatStreamSingleLineLimit(t *testing.T) {
	prefix := "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n"
	oversized := "data: " + strings.Repeat("x", readOpenAIChatStreamBufMax) + "\n\n"
	text, _, _, _, _, err := readOpenAIChatStream(strings.NewReader(prefix+oversized), func(SseEvent) {})
	if text != "visible" || err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("text=%q err=%v, want preserved prefix and scanner limit", text, err)
	}
}

func TestOpenAIChatMultilineCancellationDoesNotReplay(t *testing.T) {
	var primaryHits, fallbackHits atomic.Int32
	closed := make(chan struct{})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"visible\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(fallback.Close)
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	committed := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(timeoutCtx, committed)
	var visible strings.Builder
	_, err := (&OpenAIProvider{}).Stream(ctx, UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "test-model", BaseURL: primary.URL, APIKey: "test-key", Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "test-fallback"}},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, observeProviderVisibleOutput(func(e SseEvent) {
		if e.Type == "text_delta" {
			visible.WriteString(e.Text)
			cancel()
		}
	}, committed))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want cancellation", err)
	}
	if visible.String() != "visible" || primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("text=%q requests=%d/%d", visible.String(), primaryHits.Load(), fallbackHits.Load())
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request remained open after cancellation")
	}
}

func TestOpenAIChatMultilineErrorDoesNotReplay(t *testing.T) {
	var primaryHits, fallbackHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"visible\"}}]}\n\n"+
			"data: {\"error\":\ndata: {\"message\":\"upstream interrupted\"}}\n\n")
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"replayed\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(fallback.Close)
	committed := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), committed)
	var visible strings.Builder
	result, err := (&OpenAIProvider{}).Stream(ctx, UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "test-model", BaseURL: primary.URL, APIKey: "test-key", Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "test-fallback"}},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}, nil, observeProviderVisibleOutput(func(e SseEvent) {
		if e.Type == "text_delta" {
			visible.WriteString(e.Text)
		}
	}, committed))
	if err == nil || !strings.Contains(err.Error(), "upstream interrupted") {
		t.Fatalf("err = %v", err)
	}
	if result == nil || result.StopReason != "error" || visible.String() != "visible" || primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("result=%+v text=%q requests=%d/%d", result, visible.String(), primaryHits.Load(), fallbackHits.Load())
	}
}

type chatSSEInterruptedReader struct{}

func (chatSSEInterruptedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadOpenAIChatStreamEventTermination(t *testing.T) {
	prefix := "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"prefix\"}}]}\n\n"
	for _, tc := range []struct {
		name, tail, wantErr string
		interrupted         bool
	}{
		{"premature EOF", "", "before a terminal event", false},
		{"read error", "", "unexpected EOF", true},
		{"malformed event", "data: {\ndata: invalid}\n\n", "invalid JSON", false},
		{"DONE ignores later data", "data: [DONE]\n\ndata: {not-json}\n\n", "", false},
		{"finish without final blank line", "data: {\"choices\":[{\"delta\":{},\ndata: \"finish_reason\":\"stop\"}]}", "", false},
		{"finish ignores later read error", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(prefix + tc.tail)
			if tc.interrupted {
				reader = io.MultiReader(reader, chatSSEInterruptedReader{})
			}
			var deltas strings.Builder
			text, _, _, _, _, err := readOpenAIChatStream(reader, func(e SseEvent) {
				if e.Type == "text_delta" {
					deltas.WriteString(e.Text)
				}
			})
			if text != "prefix" || deltas.String() != text {
				t.Fatalf("text/deltas = %q/%q", text, deltas.String())
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
