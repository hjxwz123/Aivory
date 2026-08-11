package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/store"
)

type mcpAdminFixture struct {
	db  *sql.DB
	mux *mux
}

func newMCPAdminFixture(t *testing.T) mcpAdminFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "mcp-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/mcp", wrap(d, listMCPServersAdmin))
	mx.handle(http.MethodPost, "/api/admin/mcp", wrap(d, createMCPServerAdmin))
	mx.handle(http.MethodPatch, "/api/admin/mcp/:id", wrap(d, updateMCPServerAdmin))
	mx.handle(http.MethodDelete, "/api/admin/mcp/:id", wrap(d, deleteMCPServerAdmin))
	mx.handle(http.MethodPost, "/api/admin/mcp/:id/test", wrap(d, testMCPServerAdmin))
	mx.handle(http.MethodPost, "/api/admin/mcp/:id/sync", wrap(d, syncMCPServerAdmin))
	return mcpAdminFixture{db: db, mux: mx}
}

func (fixture mcpAdminFixture) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.mux.ServeHTTP(recorder, req)
	return recorder
}

func decodeMCPAdminResponse[T any](t *testing.T, recorder *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status=%d, want=%d, body=%s", recorder.Code, status, recorder.Body.String())
	}
	var response T
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func TestMCPAdminCRUDMasksAndPreservesHeaders(t *testing.T) {
	fixture := newMCPAdminFixture(t)
	const authorization = "Bearer admin-only-token"
	const tenantSecret = "tenant-secret"
	createdRecorder := fixture.request(t, http.MethodPost, "/api/admin/mcp", map[string]any{
		"name":        "12306 Rail",
		"icon":        "Train",
		"description": "Search Chinese railway schedules",
		"url":         "https://mcp.example.test/mcp",
		"headers": map[string]string{
			"authorization": authorization,
			"X-Tenant-Key":  tenantSecret,
		},
	})
	created := decodeMCPAdminResponse[adminMCPServerResponse](t, createdRecorder, http.StatusCreated)
	if created.ID == "" || created.Enabled || created.Headers["Authorization"] != mcpHeaderMask || created.Headers["X-Tenant-Key"] != mcpHeaderMask {
		t.Fatalf("create response mismatch: %+v", created)
	}
	if strings.Contains(createdRecorder.Body.String(), authorization) || strings.Contains(createdRecorder.Body.String(), tenantSecret) {
		t.Fatalf("create response leaked request headers: %s", createdRecorder.Body.String())
	}

	// The submitted header object is a replacement: Authorization remains via
	// its mask, X-Tenant-Key is omitted and removed, and X-Client is added.
	updatedRecorder := fixture.request(t, http.MethodPatch, "/api/admin/mcp/"+created.ID, map[string]any{
		"enabled": true,
		"headers": map[string]string{
			"AUTHORIZATION": mcpHeaderMask,
			"X-Client":      "desktop",
		},
	})
	updated := decodeMCPAdminResponse[adminMCPServerResponse](t, updatedRecorder, http.StatusOK)
	if !updated.Enabled || len(updated.Headers) != 2 || updated.Headers["Authorization"] != mcpHeaderMask || updated.Headers["X-Client"] != mcpHeaderMask {
		t.Fatalf("update response mismatch: %+v", updated)
	}
	if strings.Contains(updatedRecorder.Body.String(), authorization) || strings.Contains(updatedRecorder.Body.String(), "desktop") {
		t.Fatalf("update response leaked request headers: %s", updatedRecorder.Body.String())
	}
	stored, err := store.GetMCPServer(t.Context(), fixture.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Headers["Authorization"] != authorization || stored.Headers["X-Client"] != "desktop" {
		t.Fatalf("stored headers mismatch: %#v", stored.Headers)
	}
	if _, exists := stored.Headers["X-Tenant-Key"]; exists {
		t.Fatalf("removed header still persisted: %#v", stored.Headers)
	}

	// Empty values carry the same keep-existing semantics as the visual mask.
	decodeMCPAdminResponse[adminMCPServerResponse](t,
		fixture.request(t, http.MethodPatch, "/api/admin/mcp/"+created.ID, map[string]any{
			"headers": map[string]string{"Authorization": "", "X-Client": mcpHeaderMask},
		}), http.StatusOK)
	stored, err = store.GetMCPServer(t.Context(), fixture.db, created.ID)
	if err != nil || stored.Headers["Authorization"] != authorization || stored.Headers["X-Client"] != "desktop" {
		t.Fatalf("empty/masked update changed saved credentials: server=%+v err=%v", stored, err)
	}

	listedRecorder := fixture.request(t, http.MethodGet, "/api/admin/mcp", nil)
	listed := decodeMCPAdminResponse[[]adminMCPServerResponse](t, listedRecorder, http.StatusOK)
	if len(listed) != 1 || listed[0].ID != created.ID || strings.Contains(listedRecorder.Body.String(), authorization) {
		t.Fatalf("list response mismatch or leaked a secret: %s", listedRecorder.Body.String())
	}

	decodeMCPAdminResponse[map[string]bool](t,
		fixture.request(t, http.MethodDelete, "/api/admin/mcp/"+created.ID, nil), http.StatusOK)
	missing := fixture.request(t, http.MethodDelete, "/api/admin/mcp/"+created.ID, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("delete missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestMCPAdminValidatesURLHeadersAndDuplicateNames(t *testing.T) {
	fixture := newMCPAdminFixture(t)
	base := map[string]any{
		"name": "Papers", "icon": "BookOpen", "description": "Search scholarly literature", "url": "https://papers.example.test/mcp",
	}

	tests := []struct {
		name  string
		patch map[string]any
	}{
		{"non-http URL", map[string]any{"url": "file:///etc/passwd"}},
		{"credentials in URL", map[string]any{"url": "https://user:pass@example.test/mcp"}},
		{"URL fragment", map[string]any{"url": "https://example.test/mcp#fragment"}},
		{"invalid header name", map[string]any{"headers": map[string]string{"Bad Header": "value"}}},
		{"header newline", map[string]any{"headers": map[string]string{"X-Test": "a\r\nb"}}},
		{"new masked header", map[string]any{"headers": map[string]string{"Authorization": mcpHeaderMask}}},
		{"new empty header", map[string]any{"headers": map[string]string{"Authorization": ""}}},
		{"reserved header", map[string]any{"headers": map[string]string{"Mcp-Session-Id": "fixed"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{}
			for key, value := range base {
				payload[key] = value
			}
			for key, value := range test.patch {
				payload[key] = value
			}
			recorder := fixture.request(t, http.MethodPost, "/api/admin/mcp", payload)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	created := decodeMCPAdminResponse[adminMCPServerResponse](t,
		fixture.request(t, http.MethodPost, "/api/admin/mcp", base), http.StatusCreated)
	duplicate := map[string]any{
		"name": " papers ", "icon": "BookOpen", "description": "Duplicate", "url": "https://duplicate.example.test/mcp",
	}
	recorder := fixture.request(t, http.MethodPost, "/api/admin/mcp", duplicate)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), store.ErrMCPServerNameExists.Error()) {
		t.Fatalf("duplicate status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// An empty object intentionally clears all configured headers.
	decodeMCPAdminResponse[adminMCPServerResponse](t,
		fixture.request(t, http.MethodPatch, "/api/admin/mcp/"+created.ID, map[string]any{"headers": map[string]string{}}), http.StatusOK)
}

type mcpAdminRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

func writeMCPAdminRPCResult(t *testing.T, w http.ResponseWriter, id json.RawMessage, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Errorf("encode MCP response: %v", err)
	}
}

func TestMCPAdminTestPreservesSnapshotAndSyncUpdatesIt(t *testing.T) {
	fixture := newMCPAdminFixture(t)
	const authorization = "Bearer discovery-secret"
	var callsMu sync.Mutex
	calls := []string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != authorization {
			t.Errorf("Authorization header = %q", got)
		}
		var request mcpAdminRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		callsMu.Lock()
		calls = append(calls, request.Method)
		callsMu.Unlock()
		switch request.Method {
		case "server/discover":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "test-literature"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name": "search_papers", "description": "Search papers",
					"inputSchema": map[string]any{"type": "object"},
				}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(remote.Close)

	created := decodeMCPAdminResponse[adminMCPServerResponse](t,
		fixture.request(t, http.MethodPost, "/api/admin/mcp", map[string]any{
			"name": "Literature", "icon": "BookOpen", "description": "Search literature",
			"url": remote.URL, "headers": map[string]string{"Authorization": authorization},
		}), http.StatusCreated)
	oldSnapshot := json.RawMessage(`[{
		"name":"old_tool","description":"Previously discovered","inputSchema":{"type":"object"}
	}]`)
	if _, err := store.UpdateMCPServerSyncState(
		t.Context(), fixture.db, created.ID, oldSnapshot, "2025-03-26", "stale error", 101,
	); err != nil {
		t.Fatal(err)
	}

	testRecorder := fixture.request(t, http.MethodPost, "/api/admin/mcp/"+created.ID+"/test", nil)
	tested := decodeMCPAdminResponse[adminMCPServerResponse](t, testRecorder, http.StatusOK)
	if strings.Contains(testRecorder.Body.String(), authorization) || tested.Headers["Authorization"] != mcpHeaderMask {
		t.Fatalf("test response leaked header secret: %s", testRecorder.Body.String())
	}
	var testedTools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(tested.DiscoveredTools, &testedTools); err != nil {
		t.Fatal(err)
	}
	if len(testedTools) != 1 || testedTools[0].Name != "old_tool" {
		t.Fatalf("test changed saved snapshot: %s", tested.DiscoveredTools)
	}
	if tested.ProtocolVersion != "2026-07-28" || tested.LastError != "" || tested.LastSyncedAt <= 101 {
		t.Fatalf("test did not update connection status: %+v", tested)
	}

	syncRecorder := fixture.request(t, http.MethodPost, "/api/admin/mcp/"+created.ID+"/sync", nil)
	synced := decodeMCPAdminResponse[adminMCPServerResponse](t, syncRecorder, http.StatusOK)
	if strings.Contains(syncRecorder.Body.String(), authorization) || synced.Headers["Authorization"] != mcpHeaderMask {
		t.Fatalf("sync response leaked header secret: %s", syncRecorder.Body.String())
	}
	var syncedTools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(synced.DiscoveredTools, &syncedTools); err != nil {
		t.Fatal(err)
	}
	if len(syncedTools) != 1 || syncedTools[0].Name != "search_papers" {
		t.Fatalf("sync did not replace snapshot: %s", synced.DiscoveredTools)
	}
	stored, err := store.GetMCPServer(t.Context(), fixture.db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.LastError, authorization) || stored.LastError != "" || string(stored.DiscoveredTools) != string(synced.DiscoveredTools) {
		t.Fatalf("stored sync state mismatch: %+v", stored)
	}

	callsMu.Lock()
	defer callsMu.Unlock()
	if got := strings.Join(calls, ","); got != "server/discover,tools/list,server/discover,tools/list" {
		t.Fatalf("MCP methods = %q", got)
	}
}

func TestMCPAdminDiscoveryFailureRedactsSecretsAndPreservesSnapshot(t *testing.T) {
	fixture := newMCPAdminFixture(t)
	const authorization = "Bearer echoed-admin-secret"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != authorization {
			t.Errorf("Authorization header = %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected Authorization " + authorization))
	}))
	t.Cleanup(remote.Close)

	created := decodeMCPAdminResponse[adminMCPServerResponse](t,
		fixture.request(t, http.MethodPost, "/api/admin/mcp", map[string]any{
			"name": "Secret echo", "icon": "Shield", "description": "Failure test",
			"url": remote.URL, "headers": map[string]string{"Authorization": authorization},
		}), http.StatusCreated)
	oldSnapshot := json.RawMessage(`[{
		"name":"known_tool","description":"Last good snapshot","inputSchema":{"type":"object"}
	}]`)

	for _, action := range []string{"test", "sync"} {
		t.Run(action, func(t *testing.T) {
			if _, err := store.UpdateMCPServerSyncState(
				t.Context(), fixture.db, created.ID, oldSnapshot, "2025-03-26", "", 201,
			); err != nil {
				t.Fatal(err)
			}
			recorder := fixture.request(t, http.MethodPost, "/api/admin/mcp/"+created.ID+"/"+action, nil)
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), authorization) {
				t.Fatalf("failure response leaked header secret: %s", recorder.Body.String())
			}

			stored, err := store.GetMCPServer(t.Context(), fixture.db, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(stored.DiscoveredTools) != string(oldSnapshot) {
				t.Fatalf("%s failure changed last good snapshot: %s", action, stored.DiscoveredTools)
			}
			if stored.ProtocolVersion != "2025-03-26" {
				t.Fatalf("%s failure erased protocol version: %q", action, stored.ProtocolVersion)
			}
			if stored.LastError == "" || strings.Contains(stored.LastError, authorization) || !strings.Contains(stored.LastError, mcpHeaderMask) {
				t.Fatalf("stored last_error was not redacted: %q", stored.LastError)
			}

			listedRecorder := fixture.request(t, http.MethodGet, "/api/admin/mcp", nil)
			if listedRecorder.Code != http.StatusOK || strings.Contains(listedRecorder.Body.String(), authorization) {
				t.Fatalf("admin list leaked failure credential: %s", listedRecorder.Body.String())
			}
		})
	}
}

func TestMCPAdminRoutesRequireAuthentication(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "mcp-route-auth.db"))
	defer db.Close()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/admin/mcp", nil),
		httptest.NewRequest(http.MethodPost, "/api/admin/mcp", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPatch, "/api/admin/mcp/mcp_test", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/admin/mcp/mcp_test", nil),
		httptest.NewRequest(http.MethodPost, "/api/admin/mcp/mcp_test/test", nil),
		httptest.NewRequest(http.MethodPost, "/api/admin/mcp/mcp_test/sync", nil),
	} {
		recorder := httptest.NewRecorder()
		NewRouter(Deps{DB: db}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d body=%s", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
}
