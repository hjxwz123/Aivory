package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestRegistryListMCPKeepsOfficialAndUserSourcesWithSameID(t *testing.T) {
	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	const serverID = "same-source-id"
	createMCPRegistryServer(t, db, store.MCPServer{
		ID: serverID, Name: "Official source", URL: "https://official.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot("shared/search"),
	})
	createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: serverID, UserID: "u1", Name: "Personal source", URL: "https://personal.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot("shared/search"),
	})

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	definitions := registry.ListMCP("model", "u1", "")
	if len(definitions) != 2 {
		t.Fatalf("definitions=%+v, want both official and user-owned tools", definitions)
	}
	seenSources := map[bool]llm.MCPToolDef{}
	for _, definition := range definitions {
		if definition.ServerID != serverID {
			t.Fatalf("unexpected server id in definition: %+v", definition)
		}
		if _, exists := seenSources[definition.UserOwned]; exists {
			t.Fatalf("duplicate source identity in definitions: %+v", definitions)
		}
		seenSources[definition.UserOwned] = definition
	}
	if !seenSources[false].UserOwned && seenSources[false].Name == "" {
		t.Fatalf("official definition missing: %+v", definitions)
	}
	if !seenSources[true].UserOwned || !seenSources[true].OwnerExempt {
		t.Fatalf("user-owned definition metadata=%+v", seenSources[true])
	}
	if seenSources[false].Name == seenSources[true].Name {
		t.Fatalf("official and user definitions reused Function name %q", seenSources[false].Name)
	}

	registry.mu.RLock()
	officialBinding, officialOK := registry.mcpBindings[seenSources[false].Name]
	userBinding, userOK := registry.mcpBindings[seenSources[true].Name]
	registry.mu.RUnlock()
	if !officialOK || officialBinding.UserOwned || officialBinding.ServerID != serverID {
		t.Fatalf("official binding=%+v, present=%v", officialBinding, officialOK)
	}
	if !userOK || !userBinding.UserOwned || userBinding.OwnerID != "u1" || userBinding.ServerID != serverID {
		t.Fatalf("user binding=%+v, present=%v", userBinding, userOK)
	}
}

func TestRegistryListMCPDoesNotRebindInFlightUserFunctionWhenOfficialSourceAppears(t *testing.T) {
	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	const serverID = "late-official-collision"
	const remoteName = "shared/search"
	createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: serverID, UserID: "u1", Name: "Personal source", URL: "https://personal.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot(remoteName),
	})

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	initial := registry.ListMCP("model", "u1", "")
	if len(initial) != 1 || !initial[0].UserOwned {
		t.Fatalf("initial definitions=%+v", initial)
	}
	inFlightFunction := initial[0].Name
	registry.mu.RLock()
	initialBinding := registry.mcpBindings[inFlightFunction]
	registry.mu.RUnlock()
	if !initialBinding.UserOwned || initialBinding.OwnerID != "u1" {
		t.Fatalf("initial binding=%+v", initialBinding)
	}

	// Simulate an administrator source being created while a model is deciding
	// whether to call the already-declared user Function.
	createMCPRegistryServer(t, db, store.MCPServer{
		ID: serverID, Name: "Official source", URL: "https://official.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot(remoteName),
	})
	refreshed := registry.ListMCP("model", "u1", "")
	if len(refreshed) != 2 {
		t.Fatalf("refreshed definitions=%+v", refreshed)
	}
	registry.mu.RLock()
	bindingAfterRefresh, present := registry.mcpBindings[inFlightFunction]
	registry.mu.RUnlock()
	if !present || !bindingAfterRefresh.UserOwned || bindingAfterRefresh.OwnerID != "u1" ||
		bindingAfterRefresh.ServerID != serverID || bindingAfterRefresh.RemoteName != remoteName {
		t.Fatalf("in-flight Function was rebound across MCP sources: present=%v binding=%+v", present, bindingAfterRefresh)
	}

	userFunctionStable := false
	for _, definition := range refreshed {
		if definition.UserOwned && definition.Name == inFlightFunction {
			userFunctionStable = true
		}
		if !definition.UserOwned && definition.Name == inFlightFunction {
			t.Fatalf("official source reused in-flight user Function %q", inFlightFunction)
		}
	}
	if !userFunctionStable {
		t.Fatalf("user Function changed after official source appeared: before=%q after=%+v", inFlightFunction, refreshed)
	}
}

func TestRegistryListMCPDoesNotRebindInFlightFunctionAfterConfigurationChange(t *testing.T) {
	var newEndpointRequests atomic.Int64
	newEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newEndpointRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "new endpoint must not receive the old call",
			}}},
		})
	}))
	defer newEndpoint.Close()

	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	server := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "in-flight-config-change", UserID: "u1", Name: "Mutable source",
		URL: "https://old.example.test/mcp", Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("search"),
	})
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	registry.userMCPHTTPClient = newEndpoint.Client()

	initial := registry.ListMCP("model", "u1", "")
	if len(initial) != 1 {
		t.Fatalf("initial definitions=%+v", initial)
	}
	oldFunction := initial[0].Name

	// Model a configuration update from another process. Keeping the snapshot
	// present is deliberate: it proves a later ListMCP cannot transparently bind
	// an already-declared name to the new endpoint even before rediscovery.
	if _, err := store.UpdateUserMCPServer(context.Background(), db, server.ID, "u1", "",
		store.UserMCPServerPatch{URL: &newEndpoint.URL}); err != nil {
		t.Fatalf("update user MCP endpoint: %v", err)
	}
	refreshed := registry.ListMCP("model", "u1", "")
	if len(refreshed) != 1 {
		t.Fatalf("refreshed definitions=%+v", refreshed)
	}
	newFunction := refreshed[0].Name
	if newFunction == oldFunction {
		t.Fatalf("configuration change reused in-flight Function name %q", oldFunction)
	}
	registry.mu.RLock()
	_, oldBindingPresent := registry.mcpBindings[oldFunction]
	newBinding, newBindingPresent := registry.mcpBindings[newFunction]
	registry.mu.RUnlock()
	if oldBindingPresent {
		t.Fatalf("old in-flight Function %q retained a mutable binding", oldFunction)
	}
	if !newBindingPresent || newBinding.RuntimeFingerprint == "" {
		t.Fatalf("new binding missing runtime fingerprint: present=%v binding=%+v", newBindingPresent, newBinding)
	}

	_, _, err := registry.Run(context.Background(), oldFunction, json.RawMessage(`{"query":"old-schema"}`), &llm.ToolContext{
		UserID: "u1", BuiltinTools: map[string]bool{oldFunction: true},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("retired in-flight Function error=%v", err)
	}
	if newEndpointRequests.Load() != 0 {
		t.Fatalf("retired in-flight Function reached new endpoint %d times", newEndpointRequests.Load())
	}
}

func TestRegistryListUserMCPMergesConcurrentTenantBindings(t *testing.T) {
	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	personalU1 := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_personal_u1", UserID: "u1", Name: "U1 notes", URL: "https://u1.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot("search_u1"),
	})
	personalU2 := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_personal_u2", UserID: "u2", Name: "U2 notes", URL: "https://u2.example.test/mcp",
		Enabled: true, DiscoveredTools: registryUserMCPSnapshot("search_u2"),
	})
	sharedU2 := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_shared_u2", UserID: "u2", WorkspaceID: "ws1", Name: "Team notes",
		URL: "https://team.example.test/mcp", Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("search_team"),
	})

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	u1Defs := registry.ListMCP("model", "u1", "ws1")
	u2Defs := registry.ListMCP("model", "u2", "ws1")
	assertScopedUserMCPDefinition(t, u1Defs, personalU1.ID, true)
	assertScopedUserMCPDefinition(t, u1Defs, sharedU2.ID, false)
	assertMissingMCPDefinition(t, u1Defs, personalU2.ID)
	assertScopedUserMCPDefinition(t, u2Defs, personalU2.ID, true)
	assertScopedUserMCPDefinition(t, u2Defs, sharedU2.ID, true)
	assertMissingMCPDefinition(t, u2Defs, personalU1.ID)

	var wg sync.WaitGroup
	for index := 0; index < 24; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				registry.ListMCP("model", "u1", "ws1")
				return
			}
			registry.ListMCP("model", "u2", "ws1")
		}(index)
	}
	wg.Wait()

	registry.mu.RLock()
	boundServers := make(map[string]bool)
	for _, binding := range registry.mcpBindings {
		if binding.UserOwned {
			boundServers[binding.ServerID] = true
		}
	}
	registry.mu.RUnlock()
	for _, serverID := range []string{personalU1.ID, personalU2.ID, sharedU2.ID} {
		if !boundServers[serverID] {
			t.Fatalf("concurrent scoped listing lost binding for %s: %v", serverID, boundServers)
		}
	}

	registry.InvalidateMCPServer(sharedU2.ID)
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, binding := range registry.mcpBindings {
		if binding.ServerID == sharedU2.ID {
			t.Fatalf("InvalidateMCPServer retained binding: %+v", binding)
		}
	}
}

func TestRegistryListMCPAppliesWorkspaceOfficialMCPAllowlist(t *testing.T) {
	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	createMCPRegistryServer(t, db, store.MCPServer{
		ID: "official-allowed", Name: "Official allowed", Icon: "Blocks",
		Description: "allowed", URL: "https://official-allowed.example.test/mcp", Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("official/search"),
	})
	createMCPRegistryServer(t, db, store.MCPServer{
		ID: "official-denied", Name: "Official denied", Icon: "Blocks",
		Description: "denied", URL: "https://official-denied.example.test/mcp", Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("denied/search"),
	})
	createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "workspace-user-mcp", UserID: "u1", WorkspaceID: "ws1", Name: "Workspace own",
		URL: "https://workspace-user.example.test/mcp", Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("workspace/search"),
	})
	allowed := []string{"mcp:official-allowed"}
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowedMCPServerIDs: &allowed,
	}); err != nil {
		t.Fatalf("set workspace MCP allowlist: %v", err)
	}
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	definitions := registry.ListMCP("model", "u1", "ws1")
	seen := map[string]bool{}
	for _, definition := range definitions {
		seen[definition.ServerID] = true
	}
	if !seen["official-allowed"] {
		t.Fatalf("workspace allowlisted official MCP missing: %+v", definitions)
	}
	if seen["official-denied"] {
		t.Fatalf("workspace-denied official MCP leaked into registry: %+v", definitions)
	}
	if !seen["workspace-user-mcp"] {
		t.Fatalf("workspace user MCP unexpectedly filtered by official allowlist: %+v", definitions)
	}
}

func TestRegistryRunUserMCPRevalidatesTenantStateBeforeDial(t *testing.T) {
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "server/discover":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "user-runtime"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/call":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "private result"}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer bridge.Close()

	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	server := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_runtime_u1", UserID: "u1", Name: "Private runtime", URL: bridge.URL,
		Headers: map[string]string{"Authorization": "Bearer private"}, Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("private/search"),
	})
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	// Loopback is forbidden in production. The runtime behavior test injects the
	// fixture transport; netsafe has a separate test proving the default blocks it.
	registry.userMCPHTTPClient = bridge.Client()
	definitions := registry.ListMCP("model", "u1", "")
	if len(definitions) != 1 || definitions[0].ServerID != server.ID {
		t.Fatalf("scoped definitions=%+v", definitions)
	}
	functionName := definitions[0].Name

	before := requests.Load()
	_, _, err := registry.Run(context.Background(), functionName, json.RawMessage(`{"query":"secret"}`), &llm.ToolContext{
		UserID: "u2", BuiltinTools: map[string]bool{functionName: true},
	})
	if err == nil || !strings.Contains(err.Error(), "unavailable for this user or workspace") {
		t.Fatalf("cross-tenant user MCP error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("cross-tenant user MCP reached the endpoint")
	}

	text, _, err := registry.Run(context.Background(), functionName, json.RawMessage(`{"query":"mine"}`), &llm.ToolContext{
		UserID: "u1", BuiltinTools: map[string]bool{functionName: true},
	})
	if err != nil || text != "private result" {
		t.Fatalf("owner user MCP result=%q err=%v", text, err)
	}
	if requests.Load() != before+2 {
		t.Fatalf("owner execution request count=%d want=%d", requests.Load(), before+2)
	}

	registry.mu.RLock()
	if len(registry.mcpClients) != 1 {
		registry.mu.RUnlock()
		t.Fatalf("cached user MCP clients=%d want=1", len(registry.mcpClients))
	}
	for _, cached := range registry.mcpClients {
		if !cached.userOwned || cached.serverID != server.ID || cached.userID != "u1" {
			registry.mu.RUnlock()
			t.Fatalf("cached user MCP identity=%+v", cached)
		}
	}
	registry.mu.RUnlock()

	registry.InvalidateMCPServer(server.ID)
	registry.mu.RLock()
	bindingsAfterInvalidate, clientsAfterInvalidate := len(registry.mcpBindings), len(registry.mcpClients)
	registry.mu.RUnlock()
	if bindingsAfterInvalidate != 0 || clientsAfterInvalidate != 0 {
		t.Fatalf("invalidation left bindings=%d clients=%d", bindingsAfterInvalidate, clientsAfterInvalidate)
	}

	definitions = registry.ListMCP("model", "u1", "")
	functionName = definitions[0].Name
	disabled := false
	if _, err := store.UpdateUserMCPServer(context.Background(), db, server.ID, "u1", "", store.UserMCPServerPatch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	before = requests.Load()
	_, _, err = registry.Run(context.Background(), functionName, nil, &llm.ToolContext{
		UserID: "u1", BuiltinTools: map[string]bool{functionName: true},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled user MCP error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("disabled user MCP reached the endpoint")
	}

	enabled := true
	if _, err := store.UpdateUserMCPServer(context.Background(), db, server.ID, "u1", "", store.UserMCPServerPatch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserMCPServerSyncState(
		context.Background(), db, server.ID, "u1", "", json.RawMessage(`[]`), "", "", 0,
	); err != nil {
		t.Fatal(err)
	}
	_, _, err = registry.Run(context.Background(), functionName, nil, &llm.ToolContext{
		UserID: "u1", BuiltinTools: map[string]bool{functionName: true},
	})
	if err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("removed user MCP method error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("removed user MCP method reached the endpoint")
	}
}

func TestRegistryRunUserMCPHonorsWorkspaceCapabilitiesBeforeDial(t *testing.T) {
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "server/discover":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "workspace-runtime"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/call":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "workspace result"}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer bridge.Close()

	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	server := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_workspace_guard", UserID: "u1", WorkspaceID: "ws1", Name: "Guarded workspace MCP",
		URL: bridge.URL, Enabled: true, DiscoveredTools: registryUserMCPSnapshot("workspace/search"),
	})
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	registry.userMCPHTTPClient = bridge.Client()
	definitions := registry.ListMCP("model", "u1", "ws1")
	if len(definitions) != 1 || definitions[0].ServerID != server.ID {
		t.Fatalf("workspace definitions=%+v", definitions)
	}
	functionName := definitions[0].Name
	toolContext := &llm.ToolContext{
		UserID: "u1", WorkspaceID: "ws1",
		BuiltinTools: map[string]bool{functionName: true},
	}

	// AllowedMCPServerIDs is the administrator-owned MCP allowlist. It must not
	// silently turn into a denylist for user-owned workspace servers. Exercise a
	// teammate (OwnerExempt=false), so success here comes from ordinary group-all
	// permission rather than the creator exemption.
	officialOnly := []string{"mcp:official-only"}
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowedMCPServerIDs: &officialOnly,
	}); err != nil {
		t.Fatalf("set official-only workspace MCP allowlist: %v", err)
	}
	teammateDefinitions := registry.ListMCP("model", "u2", "ws1")
	teammateFunction := ""
	for _, definition := range teammateDefinitions {
		if definition.ServerID == server.ID {
			if !definition.UserOwned || definition.OwnerExempt {
				t.Fatalf("teammate definition identity=%+v", definition)
			}
			teammateFunction = definition.Name
			break
		}
	}
	if teammateFunction == "" {
		t.Fatalf("official-only allowlist removed teammate user MCP: %+v", teammateDefinitions)
	}
	before := requests.Load()
	text, _, err := registry.Run(context.Background(), teammateFunction, nil, &llm.ToolContext{
		UserID: "u2", WorkspaceID: "ws1",
		BuiltinTools: map[string]bool{teammateFunction: true},
	})
	if err != nil || text != "workspace result" {
		t.Fatalf("teammate user MCP under official allowlist result=%q err=%v", text, err)
	}
	if requests.Load() != before+2 {
		t.Fatalf("teammate execution request count=%d want=%d", requests.Load(), before+2)
	}

	allowMCP := false
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowMCP: &allowMCP,
	}); err != nil {
		t.Fatalf("disable workspace MCP: %v", err)
	}
	before = requests.Load()
	if _, _, err := registry.Run(context.Background(), functionName, nil, toolContext); err == nil || !strings.Contains(err.Error(), "MCP is disabled") {
		t.Fatalf("workspace MCP-disabled error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("workspace MCP-disabled call reached the endpoint")
	}

	allowMCP = true
	allowTools := false
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowMCP: &allowMCP, AllowToolCalling: &allowTools,
	}); err != nil {
		t.Fatalf("disable workspace tools: %v", err)
	}
	before = requests.Load()
	if _, _, err := registry.Run(context.Background(), functionName, nil, toolContext); err == nil || !strings.Contains(err.Error(), "tool calling is disabled") {
		t.Fatalf("workspace tool-disabled error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("workspace tool-disabled call reached the endpoint")
	}

	allowTools = true
	if _, err := store.UpdateWorkspacePolicy(context.Background(), db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowMCP: &allowMCP, AllowToolCalling: &allowTools,
	}); err != nil {
		t.Fatalf("re-enable workspace tools: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET can_use_mcp=0 WHERE workspace_id='ws1' AND user_id='u1'`); err != nil {
		t.Fatalf("disable member MCP use: %v", err)
	}
	// u1 is the workspace owner and therefore intentionally keeps the admin
	// capability. Verify the independent member-level gate with a teammate who
	// can see the shared row but did not create it.
	if _, err := db.Exec(`UPDATE workspace_members SET can_use_mcp=0 WHERE workspace_id='ws1' AND user_id='u2'`); err != nil {
		t.Fatalf("disable teammate MCP use: %v", err)
	}
	toolContext = &llm.ToolContext{
		UserID: "u2", WorkspaceID: "ws1",
		BuiltinTools: map[string]bool{functionName: true},
	}
	before = requests.Load()
	if _, _, err := registry.Run(context.Background(), functionName, nil, toolContext); err == nil || !strings.Contains(err.Error(), "MCP use is not allowed") {
		t.Fatalf("member MCP-disabled error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("member MCP-disabled call reached the endpoint")
	}
}

func TestRegistryUserMCPOwnerExemptionCannotBypassWorkspaceOrMemberDenies(t *testing.T) {
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "server/discover":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "owner-exempt-runtime"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/call":
			writeRegistryMCPResult(t, w, request.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "owner result"}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer bridge.Close()

	ctx := context.Background()
	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	server := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "umcp_member_owner_exempt", UserID: "u2", WorkspaceID: "ws1",
		Name: "Member-owned MCP", URL: bridge.URL, Enabled: true,
		DiscoveredTools: registryUserMCPSnapshot("owner/search"),
	})

	// Force the user's group tool list to none. The successful first call proves
	// this test is exercising the resource-owner exemption, not ordinary group
	// access; all workspace/member switches remain hard ceilings above it.
	permissions := store.DefaultUserGroupPermissions()
	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,permissions) VALUES('owner-exempt-none','Owner exempt none',?);
		UPDATE users SET group_id='owner-exempt-none' WHERE id='u2'`, string(raw)); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	registry.userMCPHTTPClient = bridge.Client()
	definitions := registry.ListMCP("model", "u2", "ws1")
	functionName := ""
	for _, definition := range definitions {
		if definition.ServerID == server.ID {
			if !definition.UserOwned || !definition.OwnerExempt {
				t.Fatalf("owner definition identity=%+v", definition)
			}
			functionName = definition.Name
			break
		}
	}
	if functionName == "" {
		t.Fatalf("owner-exempt definition missing: %+v", definitions)
	}
	toolContext := &llm.ToolContext{
		UserID: "u2", WorkspaceID: "ws1",
		BuiltinTools: map[string]bool{functionName: true},
	}
	before := requests.Load()
	text, _, err := registry.Run(ctx, functionName, nil, toolContext)
	if err != nil || text != "owner result" {
		t.Fatalf("owner exemption did not bypass group-none list: text=%q err=%v", text, err)
	}
	if requests.Load() != before+2 {
		t.Fatalf("owner execution request count=%d want=%d", requests.Load(), before+2)
	}

	assertDeniedBeforeDial := func(wantError string) {
		t.Helper()
		before = requests.Load()
		_, _, runErr := registry.Run(ctx, functionName, nil, toolContext)
		if runErr == nil || !strings.Contains(runErr.Error(), wantError) {
			t.Fatalf("hard-denied owner call error=%v want substring %q", runErr, wantError)
		}
		if requests.Load() != before {
			t.Fatalf("hard-denied owner call reached endpoint: before=%d after=%d", before, requests.Load())
		}
	}

	allowTools := false
	if _, err := store.UpdateWorkspacePolicy(ctx, db, "ws1", "u1", store.WorkspacePolicyPatch{AllowToolCalling: &allowTools}); err != nil {
		t.Fatal(err)
	}
	assertDeniedBeforeDial("tool calling is disabled")
	allowTools = true
	allowMCP := false
	if _, err := store.UpdateWorkspacePolicy(ctx, db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowToolCalling: &allowTools, AllowMCP: &allowMCP,
	}); err != nil {
		t.Fatal(err)
	}
	assertDeniedBeforeDial("MCP is disabled")
	allowMCP = true
	if _, err := store.UpdateWorkspacePolicy(ctx, db, "ws1", "u1", store.WorkspacePolicyPatch{AllowMCP: &allowMCP}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET can_use_mcp=0 WHERE workspace_id='ws1' AND user_id='u2'`); err != nil {
		t.Fatal(err)
	}
	assertDeniedBeforeDial("MCP use is not allowed")
}

func TestRegistryUserMCPBindingCannotCrossPersonalAndWorkspaceScopes(t *testing.T) {
	var requests atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "must not execute"}}},
		})
	}))
	defer bridge.Close()

	db := newMCPRegistryTestDB(t)
	seedRegistryUserMCPPrincipals(t, db)
	personal := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "scope-personal", UserID: "u1", Name: "Personal scope",
		URL: bridge.URL, Enabled: true, DiscoveredTools: registryUserMCPSnapshot("scope/search"),
	})
	workspace := createRegistryUserMCPServer(t, db, store.UserMCPServer{
		ID: "scope-workspace", UserID: "u1", WorkspaceID: "ws1", Name: "Workspace scope",
		URL: bridge.URL, Enabled: true, DiscoveredTools: registryUserMCPSnapshot("scope/search"),
	})
	registry := NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	registry.userMCPHTTPClient = bridge.Client()
	definitions := registry.ListMCP("model", "u1", "ws1")
	var workspaceFunction string
	for _, definition := range definitions {
		if definition.ServerID == workspace.ID {
			workspaceFunction = definition.Name
			break
		}
	}
	if workspaceFunction == "" {
		t.Fatalf("workspace MCP definition missing: %+v", definitions)
	}

	// Simulate a stale/imported binding whose server id was reused by a personal
	// row while its captured scope still says workspace. Runtime must query the
	// binding's scope and reject it, rather than personal-first fallback.
	registry.mu.Lock()
	binding := registry.mcpBindings[workspaceFunction]
	binding.ServerID = personal.ID
	registry.mcpBindings[workspaceFunction] = binding
	registry.mu.Unlock()
	before := requests.Load()
	_, _, err := registry.Run(context.Background(), workspaceFunction, nil, &llm.ToolContext{
		UserID: "u1", WorkspaceID: "ws1",
		BuiltinTools: map[string]bool{workspaceFunction: true},
	})
	if err == nil || !strings.Contains(err.Error(), "unavailable for this workspace") {
		t.Fatalf("cross-scope execution error=%v", err)
	}
	if requests.Load() != before {
		t.Fatal("cross-scope binding reached the personal endpoint")
	}
}

func TestRegistryUserMCPClientCacheIncludesWorkspaceScope(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	server := &store.MCPServer{ID: "same-server", URL: "https://mcp.example.test/mcp"}
	personalBinding := mcpBinding{ServerID: server.ID, UserOwned: true, OwnerID: "u1"}
	workspaceBinding := mcpBinding{ServerID: server.ID, UserOwned: true, WorkspaceID: "ws1", OwnerID: "u1"}
	personal, _, err := registry.mcpClient(server, personalBinding, "u1", "")
	if err != nil {
		t.Fatalf("personal MCP client: %v", err)
	}
	personalWS1, _, err := registry.mcpClient(server, personalBinding, "u1", "ws1")
	if err != nil {
		t.Fatalf("personal MCP client in workspace 1: %v", err)
	}
	personalWS2, _, err := registry.mcpClient(server, personalBinding, "u1", "ws2")
	if err != nil {
		t.Fatalf("personal MCP client in workspace 2: %v", err)
	}
	personalWS1Again, _, err := registry.mcpClient(server, personalBinding, "u1", "ws1")
	if err != nil {
		t.Fatalf("repeat personal MCP client in workspace 1: %v", err)
	}
	workspace, _, err := registry.mcpClient(server, workspaceBinding, "u1", "ws1")
	if err != nil {
		t.Fatalf("workspace MCP client: %v", err)
	}
	if personal == personalWS1 || personal == personalWS2 || personalWS1 == personalWS2 {
		t.Fatal("personal MCP execution scopes reused a client across workspaces")
	}
	if personalWS1Again != personalWS1 {
		t.Fatal("same personal MCP execution scope did not reuse its client")
	}
	if personalWS1 == workspace {
		t.Fatal("personal and workspace MCP resource scopes reused the same client")
	}
	registry.mu.RLock()
	clientCount := len(registry.mcpClients)
	registry.mu.RUnlock()
	if clientCount != 4 {
		t.Fatalf("MCP client cache entries=%d, want 4", clientCount)
	}
}

func TestRegistryRunNilToolContextFailsClosed(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	registry.Register(stubRegistryTool{name: "nil-context-tool", output: "unexpected"})
	if _, _, err := registry.Run(context.Background(), "nil-context-tool", nil, nil); err == nil || !strings.Contains(err.Error(), "tool context unavailable") {
		t.Fatalf("nil ToolContext error=%v", err)
	}
}

func TestRegistryRunChecksWorkspaceAccessBeforeDirectImage(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	var checks atomic.Int64
	tc := &llm.ToolContext{
		UserID:          "u1",
		DirectImageTurn: true,
		BuiltinTools:    map[string]bool{"image_generate": true},
		WorkspaceAccessCheck: func(context.Context) error {
			checks.Add(1)
			return errors.New("workspace access revoked")
		},
	}
	if _, _, err := registry.Run(context.Background(), "image_generate", []byte(`{"prompt":"should not run"}`), tc); err == nil || !strings.Contains(err.Error(), "workspace access revoked before tool execution") {
		t.Fatalf("direct image access error=%v", err)
	}
	if checks.Load() != 1 {
		t.Fatalf("workspace access checks=%d, want 1", checks.Load())
	}
}

func TestRegistryRunNilContextFailsClosed(t *testing.T) {
	registry := NewRegistry(nil, config.Config{}, log.New(io.Discard, "", 0))
	registry.Register(stubRegistryTool{name: "nil-execution-context-tool", output: "unexpected"})
	tc := &llm.ToolContext{BuiltinTools: map[string]bool{"nil-execution-context-tool": true}}
	if _, _, err := registry.Run(nil, "nil-execution-context-tool", nil, tc); err == nil || !strings.Contains(err.Error(), "tool execution context unavailable") {
		t.Fatalf("nil execution context error=%v", err)
	}
}

func seedRegistryUserMCPPrincipals(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO users(id,email,password_hash) VALUES
			('u1','registry-u1@example.test','h'),
			('u2','registry-u2@example.test','h'),
			('u3','registry-u3@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token)
			VALUES ('ws1','Registry workspace','u1','registry-ws-token');
		INSERT INTO workspace_members(workspace_id,user_id,role,can_create_skills_prompts) VALUES
			('ws1','u1','admin',1),('ws1','u2','member',1)
	`); err != nil {
		t.Fatalf("seed user MCP principals: %v", err)
	}
}

func registryUserMCPSnapshot(remoteName string) json.RawMessage {
	raw, _ := json.Marshal([]map[string]any{{
		"name": remoteName, "description": "User MCP test method",
		"inputSchema": map[string]any{"type": "object"},
	}})
	return raw
}

func createRegistryUserMCPServer(t *testing.T, db *sql.DB, server store.UserMCPServer) *store.UserMCPServer {
	t.Helper()
	created, err := store.CreateUserMCPServer(context.Background(), db, server)
	if err != nil {
		t.Fatalf("create user MCP server %q: %v", server.Name, err)
	}
	return created
}

func assertScopedUserMCPDefinition(t *testing.T, definitions []llm.MCPToolDef, serverID string, ownerExempt bool) {
	t.Helper()
	for _, definition := range definitions {
		if definition.ServerID != serverID {
			continue
		}
		if !definition.UserOwned || definition.OwnerExempt != ownerExempt {
			t.Fatalf("definition for %s=%+v want user_owned=true owner_exempt=%v", serverID, definition, ownerExempt)
		}
		return
	}
	t.Fatalf("missing user MCP definition for %s: %+v", serverID, definitions)
}

func assertMissingMCPDefinition(t *testing.T, definitions []llm.MCPToolDef, serverID string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.ServerID == serverID {
			t.Fatalf("unexpected MCP definition for %s: %+v", serverID, definition)
		}
	}
}
