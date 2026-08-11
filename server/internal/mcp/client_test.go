package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testRPCRequest struct {
	JSONRPC string                     `json:"jsonrpc"`
	ID      json.RawMessage            `json:"id"`
	Method  string                     `json:"method"`
	Params  map[string]json.RawMessage `json:"params"`
}

func TestModernJSONLifecycle(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var methods []string
	configuredHeaders := map[string]string{"Authorization": "Bearer admin-secret"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()

		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer admin-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != ModernProtocolVersion {
			t.Errorf("MCP-Protocol-Version = %q", got)
		}
		if got := r.Header.Get("Mcp-Method"); got != request.Method {
			t.Errorf("Mcp-Method = %q, method = %q", got, request.Method)
		}

		switch request.Method {
		case "server/discover":
			assertModernMeta(t, request.Params)
			writeTestResult(t, w, request.ID, map[string]any{
				"protocolVersion": ModernProtocolVersion,
				"serverInfo":      map[string]any{"name": "rail", "version": "2"},
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			})
		case "tools/list":
			assertModernMeta(t, request.Params)
			if _, secondPage := request.Params["cursor"]; !secondPage {
				writeTestResult(t, w, request.ID, map[string]any{
					"tools": []any{map[string]any{
						"name": "stations", "description": "Find stations",
						"inputSchema": map[string]any{"type": "object"},
					}},
					"nextCursor": "page-2",
				})
				return
			}
			writeTestResult(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name": "tickets", "description": "Find tickets",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"date": map[string]any{"type": "string"}},
					},
				}},
			})
		case "tools/call":
			assertModernMeta(t, request.Params)
			if got := r.Header.Get("Mcp-Name"); got != "tickets" {
				t.Errorf("Mcp-Name = %q", got)
			}
			var arguments map[string]string
			if err := json.Unmarshal(request.Params["arguments"], &arguments); err != nil {
				t.Errorf("decode arguments: %v", err)
			}
			if arguments["date"] != "2026-08-12" {
				t.Errorf("arguments = %#v", arguments)
			}
			writeTestResult(t, w, request.ID, map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "G1 available"},
					map[string]any{"type": "resource", "resource": map[string]any{
						"uri": "rail://G1", "mimeType": "text/plain", "text": "second class",
					}},
				},
				"structuredContent": map[string]any{"train": "G1", "available": true},
				"isError":           true,
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{URL: server.URL, Headers: configuredHeaders})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	configuredHeaders["Authorization"] = "Bearer mutated"

	discovery, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discovery.Mode != ModeModern || discovery.ServerInfo.Name != "rail" {
		t.Fatalf("discovery = %#v", discovery)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "stations" || tools[1].Name != "tickets" {
		t.Fatalf("tools = %#v", tools)
	}
	if string(tools[1].InputSchema) == "" {
		t.Fatal("input schema was not retained")
	}

	result, err := client.CallTool(context.Background(), "tickets", json.RawMessage(`{"date":"2026-08-12"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("isError was not retained")
	}
	if got := result.TextContent(); got != "G1 available\nsecond class" {
		t.Fatalf("TextContent = %q", got)
	}
	if !strings.Contains(string(result.StructuredContent), `"train":"G1"`) {
		t.Fatalf("StructuredContent = %s", result.StructuredContent)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "server/discover,tools/list,tools/list,tools/call" {
		t.Fatalf("method sequence = %q", got)
	}
}

func TestRequestScopedSSEResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		if request.Method == "server/discover" {
			writeTestResult(t, w, request.ID, map[string]any{
				"protocolVersion": ModernProtocolVersion,
				"serverInfo":      map[string]any{"name": "sse-server"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
			return
		}
		if request.Method != "tools/list" {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, ": keepalive\r\n")
		_, _ = io.WriteString(w, "event: message\r\n")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\r\n\r\n")
		_, _ = fmt.Fprintf(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":%s,\"result\":{\"tools\":[{\"name\":\"sse_tool\",\"inputSchema\":{\"type\":\"object\"}}]}}\r\n\r\n", request.ID)
		_, _ = io.WriteString(w, "data: [DONE]\r\n\r\n")
	}))
	defer server.Close()

	client, err := NewClient(Config{URL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "sse_tool" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestLegacyInitializeSessionLifecycle(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()

		switch request.Method {
		case "server/discover":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"Code":"InvalidArgument","Message":"request without mcp-session-id header should be mcp initialize request"}`)
		case "initialize":
			if got := r.Header.Get("MCP-Protocol-Version"); got != "2025-11-25" {
				t.Errorf("initialize protocol header = %q", got)
			}
			w.Header().Set("Mcp-Session-Id", "session-123")
			writeTestResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2025-03-26",
				"serverInfo":      map[string]any{"name": "legacy-server", "version": "1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "notifications/initialized":
			assertLegacyHeaders(t, r)
			if len(request.ID) != 0 {
				t.Errorf("notification id = %s", request.ID)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			assertLegacyHeaders(t, r)
			if got := r.Header.Get("Mcp-Method"); got != "" {
				t.Errorf("legacy Mcp-Method = %q", got)
			}
			writeTestResult(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name": "legacy_tool", "inputSchema": map[string]any{"type": "object"},
				}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{URL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "legacy_tool" {
		t.Fatalf("tools = %#v", tools)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "server/discover,initialize,notifications/initialized,tools/list" {
		t.Fatalf("method sequence = %q", got)
	}
}

func TestLegacyRetriesSupportedInitializeVersion(t *testing.T) {
	t.Parallel()

	initializeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		switch request.Method {
		case "server/discover":
			writeTestError(t, w, request.ID, -32601, "unsupported")
		case "initialize":
			initializeCalls++
			var version string
			_ = json.Unmarshal(request.Params["protocolVersion"], &version)
			if version != "2025-03-26" {
				writeTestError(t, w, request.ID, -32602, "unsupported version")
				return
			}
			writeTestResult(t, w, request.ID, map[string]any{
				"protocolVersion": version,
				"serverInfo":      map[string]any{"name": "strict-legacy"},
				"capabilities":    map[string]any{},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{URL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	discovery, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discovery.ProtocolVersion != "2025-03-26" || initializeCalls != 2 {
		t.Fatalf("discovery = %#v, initializeCalls = %d", discovery, initializeCalls)
	}
}

func TestStructuredOnlyResultAndRPCError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		switch request.Method {
		case "server/discover":
			writeTestResult(t, w, request.ID, map[string]any{
				"protocolVersion": ModernProtocolVersion,
				"serverInfo":      map[string]any{"name": "results"},
				"capabilities":    map[string]any{},
			})
		case "tools/call":
			var name string
			_ = json.Unmarshal(request.Params["name"], &name)
			if name == "broken" {
				writeTestError(t, w, request.ID, -32001, "remote failure")
				return
			}
			writeTestResult(t, w, request.ID, map[string]any{
				"structuredContent": map[string]any{"count": 3},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{URL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.CallTool(context.Background(), "structured", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := result.TextContent(); got != `{"count":3}` {
		t.Fatalf("TextContent = %q", got)
	}

	_, err = client.CallTool(context.Background(), "broken", json.RawMessage(`{}`))
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32001 {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseLimitAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("response limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("x", 129))
		}))
		defer server.Close()
		client, err := NewClient(Config{URL: server.URL, MaxResponseBytes: 128})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = client.Discover(context.Background())
		var sizeErr *ResponseTooLargeError
		if !errors.As(err, &sizeErr) || sizeErr.Limit != 128 {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		client, err := NewClient(Config{URL: server.URL, Timeout: 10 * time.Millisecond})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		_, err = client.Discover(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestConfigAndArgumentsValidation(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{},
		{URL: "ftp://example.com/mcp"},
		{URL: "https://user:pass@example.com/mcp"},
		{URL: "https://example.com/mcp#fragment"},
		{URL: "https://example.com/mcp", Headers: map[string]string{"Bad Header": "value"}},
		{URL: "https://example.com/mcp", Headers: map[string]string{"X-Header": "bad\nvalue"}},
		{URL: "https://example.com/mcp", Headers: map[string]string{"Mcp-Session-Id": "fixed"}},
	}
	for _, cfg := range tests {
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("NewClient(%#v) unexpectedly succeeded", cfg)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, ok := readTestRequest(t, w, r)
		if !ok {
			return
		}
		writeTestResult(t, w, request.ID, map[string]any{
			"protocolVersion": ModernProtocolVersion,
			"serverInfo":      map[string]any{"name": "validation"},
			"capabilities":    map[string]any{},
		})
	}))
	defer server.Close()
	client, err := NewClient(Config{URL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.CallTool(context.Background(), "", nil); err == nil {
		t.Fatal("empty tool name unexpectedly succeeded")
	}
	if _, err := client.CallTool(context.Background(), "tool", json.RawMessage(`[]`)); err == nil {
		t.Fatal("array arguments unexpectedly succeeded")
	}
	if _, err := client.CallTool(context.Background(), "tool", json.RawMessage(`{"x":`)); err == nil {
		t.Fatal("malformed arguments unexpectedly succeeded")
	}
}

func assertModernMeta(t *testing.T, params map[string]json.RawMessage) {
	t.Helper()
	var meta map[string]string
	if err := json.Unmarshal(params["_meta"], &meta); err != nil {
		t.Errorf("decode _meta: %v", err)
		return
	}
	if meta["protocolVersion"] != ModernProtocolVersion {
		t.Errorf("_meta = %#v", meta)
	}
}

func assertLegacyHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Mcp-Session-Id"); got != "session-123" {
		t.Errorf("Mcp-Session-Id = %q", got)
	}
	if got := r.Header.Get("MCP-Protocol-Version"); got != "2025-03-26" {
		t.Errorf("MCP-Protocol-Version = %q", got)
	}
}

func readTestRequest(t *testing.T, w http.ResponseWriter, r *http.Request) (testRPCRequest, bool) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("method = %s", r.Method)
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return testRPCRequest{}, false
	}
	defer r.Body.Close()
	var request testRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode request: %v", err)
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return testRPCRequest{}, false
	}
	if request.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", request.JSONRPC)
	}
	return request, true
}

func writeTestResult(t *testing.T, w http.ResponseWriter, id json.RawMessage, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Errorf("encode result: %v", err)
	}
}

func writeTestError(t *testing.T, w http.ResponseWriter, id json.RawMessage, code int, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}); err != nil {
		t.Errorf("encode error: %v", err)
	}
}
