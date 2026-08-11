package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

type registryMCPRequestRecord struct {
	Method        string
	RemoteName    string
	Authorization string
	Arguments     map[string]string
}

func TestRegistryListMCPMapsSnapshotWithoutUnsafeDefinitions(t *testing.T) {
	db := newMCPRegistryTestDB(t)
	remoteName := "  get tickets / 北京  "
	trimmedRemoteName := strings.TrimSpace(remoteName)

	createMCPRegistryServer(t, db, store.MCPServer{
		ID: "server-a", Name: "Alpha", Icon: "Train", Description: "Alpha service",
		URL: "https://alpha.example.test/mcp", Enabled: true,
		DiscoveredTools: json.RawMessage(`[
			{"name":"  get tickets / 北京  ","inputSchema":{"type":"object","properties":{"date":{"type":"string"}}}},
			{"name":"get tickets / 北京","description":"duplicate","inputSchema":{"type":"object"}},
			{"name":"","inputSchema":{"type":"object"}},
			{"name":"null-schema","inputSchema":null},
			{"name":"array-schema","inputSchema":[]},
			{"name":"scalar-schema","inputSchema":{"type":"string"}}
		]`),
	})
	createMCPRegistryServer(t, db, store.MCPServer{
		ID: "server-b", Name: "Beta", Icon: "Search", Description: "Beta service",
		URL: "https://beta.example.test/mcp", Enabled: true,
		DiscoveredTools: json.RawMessage(`[
			{"name":"get tickets / 北京","title":"Ticket title","inputSchema":{"properties":{}}}
		]`),
	})

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	initialCollision := mcpFunctionName("server-a", trimmedRemoteName)
	registry.Register(stubRegistryTool{name: initialCollision, output: "local collision"})

	definitions := registry.ListMCP("")
	if len(definitions) != 2 {
		t.Fatalf("ListMCP returned %d definitions, want 2: %#v", len(definitions), definitions)
	}
	if definitions[0].ServerID != "server-a" || definitions[1].ServerID != "server-b" {
		t.Fatalf("definition order/server ids = %#v", definitions)
	}
	if definitions[0].Name == initialCollision {
		t.Fatalf("MCP Function name collided with registered local tool %q", initialCollision)
	}
	if definitions[0].Name == definitions[1].Name {
		t.Fatalf("same remote method on different services shared Function name %q", definitions[0].Name)
	}
	if definitions[0].Description != "Use Alpha for this operation." {
		t.Fatalf("empty description fallback = %q", definitions[0].Description)
	}
	if definitions[1].Description != "Ticket title" {
		t.Fatalf("title description fallback = %q", definitions[1].Description)
	}
	providerName := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	for _, definition := range definitions {
		if !providerName.MatchString(definition.Name) {
			t.Errorf("provider-unsafe Function name %q", definition.Name)
		}
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil || schema == nil {
			t.Errorf("invalid retained schema %s: %v", definition.InputSchema, err)
		}
	}

	registry.mu.RLock()
	alphaBinding := registry.mcpBindings[definitions[0].Name]
	betaBinding := registry.mcpBindings[definitions[1].Name]
	registry.mu.RUnlock()
	if alphaBinding != (mcpBinding{ServerID: "server-a", RemoteName: trimmedRemoteName}) {
		t.Fatalf("alpha binding = %#v", alphaBinding)
	}
	if betaBinding != (mcpBinding{ServerID: "server-b", RemoteName: trimmedRemoteName}) {
		t.Fatalf("beta binding = %#v", betaBinding)
	}
}

func TestMCPFunctionNameIsStableBoundedAndServiceScoped(t *testing.T) {
	remoteName := strings.Repeat("非常长的工具名称/with spaces!", 20)
	first := mcpFunctionName("server-a", remoteName)
	if again := mcpFunctionName("server-a", remoteName); again != first {
		t.Fatalf("Function name is not stable: %q != %q", first, again)
	}
	if otherServer := mcpFunctionName("server-b", remoteName); otherServer == first {
		t.Fatalf("Function name is not service scoped: %q", first)
	}
	if len(first) > 64 {
		t.Fatalf("Function name length = %d, want <=64: %q", len(first), first)
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9_-]+$`, first); !ok {
		t.Fatalf("Function name contains provider-unsafe bytes: %q", first)
	}
	used := map[string]struct{}{first: {}}
	reserved, ok := reserveMCPFunctionName("server-a", remoteName, used)
	if !ok || reserved == first {
		t.Fatalf("collision reservation = %q, ok=%v", reserved, ok)
	}
}

func TestRegistryRunMCPRoutesRemoteMethodAndRechecksEnabledState(t *testing.T) {
	var mu sync.Mutex
	records := []registryMCPRequestRecord{}

	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			JSONRPC string                     `json:"jsonrpc"`
			ID      json.RawMessage            `json:"id"`
			Method  string                     `json:"method"`
			Params  map[string]json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		record := registryMCPRequestRecord{Method: request.Method, Authorization: r.Header.Get("Authorization")}
		if request.Method == "tools/call" {
			_ = json.Unmarshal(request.Params["name"], &record.RemoteName)
			_ = json.Unmarshal(request.Params["arguments"], &record.Arguments)
			if got := r.Header.Get("Mcp-Name"); got != record.RemoteName {
				t.Errorf("Mcp-Name = %q, remote name = %q", got, record.RemoteName)
			}
		}
		mu.Lock()
		records = append(records, record)
		mu.Unlock()

		switch request.Method {
		case "server/discover":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "runtime-test"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/call":
			if record.Arguments["query"] == "fail" {
				writeRegistryMCPResult(t, w, request.ID, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "remote denied Bearer configured"}},
					"isError": true,
				})
				return
			}
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "ticket result Bearer configured"},
					map[string]any{
						"type": "resource_link", "uri": "https://rail.example.test/G1",
						"name": "G1 schedule", "text": "direct train Bearer configured",
					},
					map[string]any{"type": "resource", "resource": map[string]any{
						"uri": "https://docs.example.test/G1", "mimeType": "text/plain", "text": "details",
					}},
					map[string]any{"type": "resource_link", "uri": "https://rail.example.test/?token=Bearer configured", "name": "secret URL"},
					map[string]any{"type": "resource_link", "uri": "file:///private/result", "name": "blocked"},
				},
				"structuredContent": map[string]any{"train": "G1"},
				"isError":           false,
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer mcpServer.Close()

	db := newMCPRegistryTestDB(t)
	remoteName := "rail/search"
	created := createMCPRegistryServer(t, db, store.MCPServer{
		ID: "runtime-server", Name: "Rail Search", Icon: "Train", Description: "Search rail data",
		URL: mcpServer.URL, Headers: map[string]string{"Authorization": "Bearer configured"}, Enabled: true,
		DiscoveredTools: json.RawMessage(`[
			{"name":"rail/search","description":"Search trains","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}
		]`),
	})
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	localCollision := mcpFunctionName(created.ID, remoteName)
	registry.Register(stubRegistryTool{name: localCollision, output: "local collision"})
	definitions := registry.ListMCP("")
	if len(definitions) != 1 {
		t.Fatalf("ListMCP definitions = %#v", definitions)
	}
	functionName := definitions[0].Name
	if functionName == localCollision {
		t.Fatalf("runtime MCP name collided with local tool %q", localCollision)
	}

	deniedContext := &llm.ToolContext{BuiltinTools: map[string]bool{}}
	if _, _, err := registry.Run(context.Background(), functionName, json.RawMessage(`{"query":"G1"}`), deniedContext); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disallowed Run error = %v", err)
	}
	if countRegistryMCPMethod(recordsSnapshot(&mu, records), "tools/call") != 0 {
		t.Fatal("disallowed tool reached the MCP endpoint")
	}

	toolContext := &llm.ToolContext{BuiltinTools: map[string]bool{functionName: true}}
	text, citations, err := registry.Run(
		context.Background(), functionName, json.RawMessage(`{"query":"G1"}`), toolContext,
	)
	if err != nil {
		t.Fatalf("Run MCP tool: %v", err)
	}
	if text != "ticket result [redacted]\ndetails" {
		t.Fatalf("MCP text = %q", text)
	}
	if len(citations) != 2 {
		t.Fatalf("MCP citations = %#v", citations)
	}
	if citations[0].URL != "https://rail.example.test/G1" || citations[0].Title != "G1 schedule" ||
		citations[0].Snippet != "direct train [redacted]" {
		t.Fatalf("direct resource citation = %#v", citations[0])
	}
	if citations[1].URL != "https://docs.example.test/G1" || citations[1].Title != "docs.example.test" ||
		citations[1].Snippet != "details" {
		t.Fatalf("embedded resource citation = %#v", citations[1])
	}

	_, _, err = registry.Run(
		context.Background(), functionName, json.RawMessage(`{"query":"fail"}`), toolContext,
	)
	if err == nil || err.Error() != "remote denied [redacted]" {
		t.Fatalf("remote isError result = %v", err)
	}
	recorded := recordsSnapshot(&mu, records)
	if countRegistryMCPMethod(recorded, "server/discover") != 1 || countRegistryMCPMethod(recorded, "tools/call") != 2 {
		t.Fatalf("unexpected MCP request reuse: %#v", recorded)
	}
	for _, record := range recorded {
		if record.Authorization != "Bearer configured" {
			t.Fatalf("configured header missing from %s: %#v", record.Method, record)
		}
		if record.Method == "tools/call" && record.RemoteName != remoteName {
			t.Fatalf("synthetic name leaked into remote route: %#v", record)
		}
	}

	disabled := false
	if _, err := store.UpdateMCPServer(context.Background(), db, created.ID, store.MCPServerPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable MCP server: %v", err)
	}
	beforeDisabledCall := len(recordsSnapshot(&mu, records))
	_, _, err = registry.Run(
		context.Background(), functionName, json.RawMessage(`{"query":"G2"}`), toolContext,
	)
	if err == nil || !strings.Contains(err.Error(), "MCP service is disabled") {
		t.Fatalf("disabled MCP Run error = %v", err)
	}
	if after := len(recordsSnapshot(&mu, records)); after != beforeDisabledCall {
		t.Fatalf("disabled MCP service made %d extra HTTP requests", after-beforeDisabledCall)
	}

	localText, _, err := registry.Run(
		context.Background(), localCollision, nil,
		&llm.ToolContext{BuiltinTools: map[string]bool{localCollision: true}},
	)
	if err != nil || localText != "local collision" {
		t.Fatalf("registered local collision route = %q, %v", localText, err)
	}
}

func newMCPRegistryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "registry-mcp.db"))
	if err != nil {
		t.Fatalf("open MCP registry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate MCP registry database: %v", err)
	}
	return db
}

func createMCPRegistryServer(t *testing.T, db *sql.DB, server store.MCPServer) *store.MCPServer {
	t.Helper()
	created, err := store.CreateMCPServer(context.Background(), db, server)
	if err != nil {
		t.Fatalf("create MCP registry server %q: %v", server.Name, err)
	}
	return created
}

type stubRegistryTool struct {
	name   string
	output string
}

func (t stubRegistryTool) Name() string                 { return t.name }
func (t stubRegistryTool) Description() string          { return "registry test tool" }
func (t stubRegistryTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t stubRegistryTool) Execute(context.Context, []byte, *llm.ToolContext) (string, []llm.Citation, error) {
	return t.output, nil, nil
}

func writeRegistryMCPResult(t *testing.T, w http.ResponseWriter, id json.RawMessage, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Errorf("write MCP result: %v", err)
	}
}

func recordsSnapshot(mu *sync.Mutex, records []registryMCPRequestRecord) []registryMCPRequestRecord {
	mu.Lock()
	defer mu.Unlock()
	return append([]registryMCPRequestRecord(nil), records...)
}

func countRegistryMCPMethod(records []registryMCPRequestRecord, method string) int {
	count := 0
	for _, record := range records {
		if record.Method == method {
			count++
		}
	}
	return count
}

var _ Tool = stubRegistryTool{}
