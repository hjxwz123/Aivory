package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
	toolregistry "aivory/server/internal/tools"
)

type userMCPFixture struct {
	db  *sql.DB
	mux *mux
}

func newUserMCPFixture(t *testing.T) userMCPFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-mcp.db"))
	t.Cleanup(func() { _ = db.Close() })
	// Handler tests exercise protocol and persistence against loopback fixtures.
	// Production leaves this injection nil and uses netsafe.UserMCPAllowedClient.
	d := Deps{DB: db, UserMCPHTTPClient: &http.Client{}}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/me/mcps", wrap(d, listMyMCPServersHandler))
	mx.handle(http.MethodPost, "/api/me/mcps", wrap(d, createMyMCPServerHandler))
	mx.handle(http.MethodPatch, "/api/me/mcps/:id", wrap(d, updateMyMCPServerHandler))
	mx.handle(http.MethodDelete, "/api/me/mcps/:id", wrap(d, deleteMyMCPServerHandler))
	mx.handle(http.MethodPost, "/api/me/mcps/:id/test", wrap(d, testMyMCPServerHandler))
	mx.handle(http.MethodPost, "/api/me/mcps/:id/sync", wrap(d, syncMyMCPServerHandler))
	return userMCPFixture{db: db, mux: mx}
}

func (fixture userMCPFixture) request(t *testing.T, method, path, body, userID string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	fixture.mux.ServeHTTP(recorder, libraryRequest(t, method, path, body, userID))
	return recorder
}

// startUserMCPBridge serves a minimal Streamable-HTTP MCP endpoint. When
// rejectWith is non-zero every request fails with an echoed Authorization
// header so tests can assert the redaction path.
func startUserMCPBridge(t *testing.T, authorization string, toolName string, rejectWith int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request mcpAdminRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if rejectWith != 0 {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(rejectWith)
			_, _ = w.Write([]byte("rejected Authorization " + r.Header.Get("Authorization")))
			return
		}
		if got := r.Header.Get("Authorization"); authorization != "" && got != authorization {
			t.Errorf("Authorization header = %q", got)
		}
		switch request.Method {
		case "server/discover":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "user-mcp-bridge"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name": toolName, "description": "Bridge tool",
					"inputSchema": map[string]any{"type": "object"},
				}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
}

func TestMCPDiscoveredToolsPresentRequiresCallableTool(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "malformed snapshot", raw: json.RawMessage(`{`), want: false},
		{name: "empty snapshot", raw: json.RawMessage(`[]`), want: false},
		{name: "blank name", raw: json.RawMessage(`[{"name":"  ","inputSchema":{"type":"object"}}]`), want: false},
		{name: "missing schema", raw: json.RawMessage(`[{"name":"lookup"}]`), want: false},
		{name: "array schema", raw: json.RawMessage(`[{"name":"lookup","inputSchema":[]}]`), want: false},
		{name: "non object type", raw: json.RawMessage(`[{"name":"lookup","inputSchema":{"type":"string"}}]`), want: false},
		{name: "schema without type", raw: json.RawMessage(`[{"name":"lookup","inputSchema":{"properties":{}}}]`), want: true},
		{name: "object schema", raw: json.RawMessage(`[{"name":"lookup","inputSchema":{"type":"object"}}]`), want: true},
		{name: "later callable tool", raw: json.RawMessage(`[{"name":"bad","inputSchema":{"type":"array"}},{"name":"good","inputSchema":{"type":"object"}}]`), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mcpDiscoveredToolsPresent(test.raw); got != test.want {
				t.Fatalf("mcpDiscoveredToolsPresent(%s) = %t, want %t", test.raw, got, test.want)
			}
		})
	}
}

func TestUserMCPCreateSyncsMasksAndEnforcesScope(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `
		INSERT INTO users(id,email,password_hash,role,status) VALUES
			('u1','u1@example.test','h','user','active'),
			('u2','u2@example.test','h','user','active'),
			('u3','u3@example.test','h','user','active'),
			('u4','u4@example.test','h','user','active');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws1','u1','admin'),('ws1','u2','member'),('ws1','u3','guest')
	`)
	const authorization = "Bearer user-only-token"
	const clientTag = "desktop-client-tag"
	good := startUserMCPBridge(t, authorization, "search_papers", 0)
	t.Cleanup(good.Close)
	bad := startUserMCPBridge(t, "", "", http.StatusUnauthorized)
	t.Cleanup(bad.Close)

	createdRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps", `{
		"name":"My Papers","icon":"BookOpen","description":"Search my library",
		"url":"`+good.URL+`","headers":{"authorization":"`+authorization+`","X-Client":"`+clientTag+`"}
	}`, "u1")
	created := decodeMCPAdminResponse[userMCPServerResponse](t, createdRecorder, http.StatusCreated)
	if !strings.HasPrefix(created.ID, "umcp_") || !created.Enabled || !created.CanManage {
		t.Fatalf("create response mismatch: %+v", created)
	}
	if created.Headers["Authorization"] != mcpHeaderMask || created.Headers["X-Client"] != mcpHeaderMask {
		t.Fatalf("create did not mask header values: %+v", created.Headers)
	}
	if created.LastError != "" || !strings.Contains(string(created.DiscoveredTools), "search_papers") {
		t.Fatalf("create inline discovery not persisted: %+v", created)
	}
	if body := createdRecorder.Body.String(); strings.Contains(body, authorization) || strings.Contains(body, clientTag) {
		t.Fatalf("create response leaked header secrets: %s", body)
	}

	duplicateRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps", `{
		"name":" my papers ","icon":"BookOpen","description":"Duplicate","url":"https://duplicate.example.test/mcp"
	}`, "u1")
	if duplicateRecorder.Code != http.StatusConflict ||
		!strings.Contains(duplicateRecorder.Body.String(), store.ErrUserMCPNameExists.Error()) {
		t.Fatalf("duplicate status=%d body=%s", duplicateRecorder.Code, duplicateRecorder.Body.String())
	}

	for _, body := range []string{
		`{"name":"x","icon":"I","description":"d","url":"file:///etc/passwd"}`,
		`{"name":"x","icon":"I","description":"d","url":"https://u:p@example.test/mcp"}`,
		`{"name":"x","icon":"I","description":"d","url":"https://example.test/mcp?token=secret"}`,
		`{"name":"x","icon":"I","description":"d","url":"` + good.URL + `#frag"}`,
		`{"name":"x","icon":"I","description":"d","url":"` + good.URL + `","headers":{"Bad Header":"v"}}`,
		`{"name":"x","icon":"I","description":"d","url":"` + good.URL + `","headers":{"Authorization":"` + mcpHeaderMask + `"}}`,
	} {
		recorder := fixture.request(t, http.MethodPost, "/api/me/mcps", body, "u1")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d body=%s", body, recorder.Code, recorder.Body.String())
		}
	}

	// Masked-write replacement semantics, mirroring the admin editor.
	updatedRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{
		"headers":{"AUTHORIZATION":"`+mcpHeaderMask+`","X-Extra":"kept-tag"}
	}`, "u1")
	updated := decodeMCPAdminResponse[userMCPServerResponse](t, updatedRecorder, http.StatusOK)
	if len(updated.Headers) != 2 || updated.Headers["Authorization"] != mcpHeaderMask || updated.Headers["X-Extra"] != mcpHeaderMask {
		t.Fatalf("update headers mismatch: %+v", updated.Headers)
	}
	if body := updatedRecorder.Body.String(); strings.Contains(body, authorization) || strings.Contains(body, "kept-tag") {
		t.Fatalf("update response leaked header secrets: %s", body)
	}
	stored, err := store.GetUserMCPServerScoped(t.Context(), fixture.db, created.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Headers["Authorization"] != authorization || stored.Headers["X-Extra"] != "kept-tag" {
		t.Fatalf("stored headers mismatch: %#v", stored.Headers)
	}
	if _, stale := stored.Headers["X-Client"]; stale {
		t.Fatalf("replacement semantics kept an omitted header: %#v", stored.Headers)
	}

	// A moved endpoint drops the stale snapshot and redacts the echoed secret.
	movedRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"url":"`+bad.URL+`"}`, "u1")
	moved := decodeMCPAdminResponse[userMCPServerResponse](t, movedRecorder, http.StatusOK)
	if string(moved.DiscoveredTools) != "[]" || moved.LastError == "" {
		t.Fatalf("URL change kept stale snapshot or lost error: %+v", moved)
	}
	if strings.Contains(movedRecorder.Body.String(), authorization) || !strings.Contains(moved.LastError, mcpHeaderMask) {
		t.Fatalf("failed re-discovery leaked or was not redacted: %s", movedRecorder.Body.String())
	}
	restoredRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"url":"`+good.URL+`"}`, "u1")
	restored := decodeMCPAdminResponse[userMCPServerResponse](t, restoredRecorder, http.StatusOK)
	if restored.LastError != "" || !strings.Contains(string(restored.DiscoveredTools), "search_papers") {
		t.Fatalf("URL restore did not re-discover: %+v", restored)
	}

	// Disabling a server does not dial the endpoint. If its URL changes while it
	// is disabled, the old snapshot is cleared; re-enabling then performs a fresh
	// discovery instead of reviving stale remote methods.
	disabledRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"enabled":false}`, "u1")
	disabled := decodeMCPAdminResponse[userMCPServerResponse](t, disabledRecorder, http.StatusOK)
	if disabled.Enabled || !strings.Contains(string(disabled.DiscoveredTools), "search_papers") {
		t.Fatalf("disable unexpectedly changed snapshot: %+v", disabled)
	}
	movedWhileDisabledRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"url":"`+bad.URL+`"}`, "u1")
	movedWhileDisabled := decodeMCPAdminResponse[userMCPServerResponse](t, movedWhileDisabledRecorder, http.StatusOK)
	if movedWhileDisabled.Enabled || string(movedWhileDisabled.DiscoveredTools) != "[]" || movedWhileDisabled.LastError != "" {
		t.Fatalf("disabled URL change retained discovery state: %+v", movedWhileDisabled)
	}
	reenabledRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"enabled":true}`, "u1")
	reenabled := decodeMCPAdminResponse[userMCPServerResponse](t, reenabledRecorder, http.StatusOK)
	if !reenabled.Enabled || string(reenabled.DiscoveredTools) != "[]" || reenabled.LastError == "" {
		t.Fatalf("re-enable did not perform fresh discovery: %+v", reenabled)
	}
	// Restore the working endpoint so the remainder of the test continues with
	// a usable server and verifies the normal metadata-change discovery path.
	recoveredRecorder := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"url":"`+good.URL+`"}`, "u1")
	recovered := decodeMCPAdminResponse[userMCPServerResponse](t, recoveredRecorder, http.StatusOK)
	if recovered.LastError != "" || !strings.Contains(string(recovered.DiscoveredTools), "search_papers") {
		t.Fatalf("endpoint recovery did not re-discover: %+v", recovered)
	}

	// Cross-user and non-member isolation.
	if code := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+created.ID, `{"name":"stolen"}`, "u2").Code; code != http.StatusNotFound {
		t.Fatalf("cross-user patch status=%d", code)
	}
	if code := fixture.request(t, http.MethodDelete, "/api/me/mcps/"+created.ID, "", "u2").Code; code != http.StatusNotFound {
		t.Fatalf("cross-user delete status=%d", code)
	}
	emptyList := decodeMCPAdminResponse[[]userMCPServerResponse](t,
		fixture.request(t, http.MethodGet, "/api/me/mcps", "", "u2"), http.StatusOK)
	if len(emptyList) != 0 {
		t.Fatalf("outsider listed personal rows: %+v", emptyList)
	}
	if code := fixture.request(t, http.MethodPost, "/api/me/mcps",
		`{"workspace_id":"ws1","name":"Guest","icon":"I","description":"d","url":"https://example.test/mcp"}`, "u3").Code; code != http.StatusForbidden {
		t.Fatalf("guest workspace create status=%d", code)
	}
	if code := fixture.request(t, http.MethodGet, "/api/me/mcps?workspace_id=ws1", "", "u4").Code; code != http.StatusNotFound {
		t.Fatalf("non-member workspace list status=%d", code)
	}

	listedRecorder := fixture.request(t, http.MethodGet, "/api/me/mcps", "", "u1")
	listed := decodeMCPAdminResponse[[]userMCPServerResponse](t, listedRecorder, http.StatusOK)
	if len(listed) != 1 || listed[0].ID != created.ID || listed[0].Headers["Authorization"] != mcpHeaderMask {
		t.Fatalf("list mismatch: %+v", listed)
	}
	if body := listedRecorder.Body.String(); strings.Contains(body, authorization) {
		t.Fatalf("list response leaked header secret: %s", body)
	}

	deleted := decodeMCPAdminResponse[map[string]bool](t,
		fixture.request(t, http.MethodDelete, "/api/me/mcps/"+created.ID, "", "u1"), http.StatusOK)
	if !deleted["ok"] {
		t.Fatalf("delete response=%v", deleted)
	}
	if code := fixture.request(t, http.MethodDelete, "/api/me/mcps/"+created.ID, "", "u1").Code; code != http.StatusNotFound {
		t.Fatalf("delete missing status=%d", code)
	}
}

func TestUserMCPTestDoesNotPersistAndSyncUpdatesSnapshot(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u1@example.test','h','user','active')`)
	const authorization = "Bearer test-echo-secret"
	good := startUserMCPBridge(t, authorization, "search_papers", 0)
	t.Cleanup(good.Close)
	bad := startUserMCPBridge(t, "", "", http.StatusUnauthorized)
	t.Cleanup(bad.Close)

	ctx := t.Context()
	// Compact single-line JSON: the store normalizes snapshots, so byte
	// equality with the seeded value is only meaningful in compact form.
	oldSnapshot := json.RawMessage(`[{"name":"old_tool","description":"Previously discovered","inputSchema":{"type":"object"}}]`)
	server, err := store.CreateUserMCPServer(ctx, fixture.db, store.UserMCPServer{
		UserID: "u1", Name: "Bridge", Icon: "BookOpen", Description: "Test flow",
		URL: good.URL, Headers: map[string]string{"Authorization": authorization},
		Enabled: true, DiscoveredTools: oldSnapshot, ProtocolVersion: "2025-03-26",
		LastError: "stale error", LastSyncedAt: 101,
	})
	if err != nil {
		t.Fatal(err)
	}

	tested := decodeMCPAdminResponse[userMCPTestResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/test", "", "u1"), http.StatusOK)
	if !tested.OK || len(tested.Tools) != 1 || tested.Tools[0].Name != "search_papers" || tested.Error != "" {
		t.Fatalf("test response mismatch: %+v", tested)
	}
	stored, err := store.GetUserMCPServerScoped(ctx, fixture.db, server.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.DiscoveredTools) != string(oldSnapshot) || stored.LastError != "stale error" ||
		stored.LastSyncedAt != 101 || stored.ProtocolVersion != "2025-03-26" {
		t.Fatalf("test persisted discovery state: %+v", stored)
	}

	// A failing test still answers 200 with ok:false and a redacted message,
	// and must not touch the stored row.
	if _, err := store.UpdateUserMCPServer(ctx, fixture.db, server.ID, "u1", "",
		store.UserMCPServerPatch{URL: &bad.URL}); err != nil {
		t.Fatal(err)
	}
	failedRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/test", "", "u1")
	failed := decodeMCPAdminResponse[userMCPTestResponse](t, failedRecorder, http.StatusOK)
	if failed.OK || failed.Error == "" || strings.Contains(failedRecorder.Body.String(), authorization) {
		t.Fatalf("failed test response mismatch or leaked secret: %s", failedRecorder.Body.String())
	}
	stored, err = store.GetUserMCPServerScoped(ctx, fixture.db, server.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastError != "stale error" || string(stored.DiscoveredTools) != string(oldSnapshot) {
		t.Fatalf("failed test changed stored state: %+v", stored)
	}

	syncRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/sync", "", "u1")
	if syncRecorder.Code != http.StatusBadGateway {
		t.Fatalf("sync against failing endpoint status=%d body=%s", syncRecorder.Code, syncRecorder.Body.String())
	}
	if strings.Contains(syncRecorder.Body.String(), authorization) {
		t.Fatalf("sync failure response leaked header secret: %s", syncRecorder.Body.String())
	}
	stored, err = store.GetUserMCPServerScoped(ctx, fixture.db, server.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastError == "" || !strings.Contains(stored.LastError, mcpHeaderMask) ||
		string(stored.DiscoveredTools) != string(oldSnapshot) {
		t.Fatalf("sync failure did not persist a redacted error: %+v", stored)
	}

	if _, err := store.UpdateUserMCPServer(ctx, fixture.db, server.ID, "u1", "",
		store.UserMCPServerPatch{URL: &good.URL}); err != nil {
		t.Fatal(err)
	}
	synced := decodeMCPAdminResponse[userMCPServerResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/sync", "", "u1"), http.StatusOK)
	if synced.LastError != "" || !strings.Contains(string(synced.DiscoveredTools), "search_papers") ||
		synced.Headers["Authorization"] != mcpHeaderMask {
		t.Fatalf("sync did not update snapshot: %+v", synced)
	}
	if body := syncRecorder.Body.String(); strings.Contains(body, authorization) {
		t.Fatalf("sync response leaked header secret: %s", body)
	}
}

func TestUserMCPTestRedactsCredentialEchoedInToolName(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u1@example.test','h','user','active')`)
	const authorization = "Bearer name-echo-secret"
	remote := startUserMCPBridge(t, authorization, "lookup-"+authorization, 0)
	t.Cleanup(remote.Close)

	server, err := store.CreateUserMCPServer(t.Context(), fixture.db, store.UserMCPServer{
		UserID: "u1", Name: "Name echo", Icon: "Blocks", Description: "Test flow",
		URL: remote.URL, Headers: map[string]string{"Authorization": authorization}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	testedRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/test", "", "u1")
	tested := decodeMCPAdminResponse[userMCPTestResponse](t, testedRecorder, http.StatusOK)
	if !tested.OK || len(tested.Tools) != 1 {
		t.Fatalf("test response mismatch: %+v", tested)
	}
	if tested.Tools[0].Name != "lookup-"+mcpHeaderMask {
		t.Fatalf("tool name was not redacted at display boundary: %q", tested.Tools[0].Name)
	}
	if strings.Contains(testedRecorder.Body.String(), authorization) {
		t.Fatalf("test response leaked tool-name credential: %s", testedRecorder.Body.String())
	}
}

func TestSelectableToolsCatalogUserMCPSegmentAndOwnerExemption(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tools-catalog-user-mcp.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	permissions := store.DefaultUserGroupPermissions()
	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessSelected, IDs: []string{"builtin:web_fetch"}}
	permissionsRaw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,features,permissions) VALUES('g1','Restricted','[]',?)`, string(permissionsRaw))
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES
			('u1','u1@example.test','h','user','active','g1'),
			('u2','u2@example.test','h','user','active','g1');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES ('ws1','u1','admin'),('ws1','u2','member')
	`)
	channel, err := store.CreateChannel(ctx, db, "Catalog", "openai", "responses", "https://provider.example.test", "secret")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "user-mcp-catalog", Label: "User MCP catalog",
		Enabled: true, ToolMode: "native", BuiltinTools: json.RawMessage(`["web_fetch"]`),
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := json.RawMessage(`[{"name":"remote_tool","description":"Remote","inputSchema":{"type":"object"}}]`)
	createServer := func(userID, workspaceID, name string, enabled bool, tools json.RawMessage) string {
		t.Helper()
		server, err := store.CreateUserMCPServer(ctx, db, store.UserMCPServer{
			UserID: userID, WorkspaceID: workspaceID, Name: name, Icon: "Blocks",
			Description: "User server", URL: "https://private-user.example.test/mcp",
			Enabled: enabled, DiscoveredTools: tools,
		})
		if err != nil {
			t.Fatal(err)
		}
		return server.ID
	}
	ownSynced := createServer("u1", "", "Own synced", true, snapshot)
	ownDisabled := createServer("u1", "", "Own disabled", false, snapshot)
	ownNeverSynced := createServer("u1", "", "Own never synced", true, json.RawMessage(`[]`))
	teammatePersonal := createServer("u2", "", "Teammate personal", true, snapshot)
	sharedOwnedByTeammate := createServer("u2", "ws1", "Shared team server", true, snapshot)

	registry := toolregistry.NewRegistry(db, config.Config{}, log.New(io.Discard, "", 0))
	listFor := func(userID, query string) map[string]selectableToolResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/tools?model_id="+model.ID+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"}))
		recorder := httptest.NewRecorder()
		listSelectableToolsHandler(Deps{DB: db, Tools: registry}, recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("user=%s status=%d body=%s", userID, recorder.Code, recorder.Body.String())
		}
		var rows []selectableToolResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		byID := map[string]selectableToolResponse{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		if body := recorder.Body.String(); strings.Contains(body, "private-user.example.test") {
			t.Fatalf("catalog leaked user MCP URL: %s", body)
		}
		return byID
	}

	own := listFor("u1", "")
	row, ok := own["usermcp:"+ownSynced]
	if !ok || !row.Allowed || row.DefaultSelected || row.Icon != "Blocks" {
		t.Fatalf("owner row under mode=selected must be visible, allowed and not default-selected: %+v (ok=%v)", row, ok)
	}
	for _, hidden := range []string{
		"usermcp:" + teammatePersonal, "usermcp:" + ownDisabled, "usermcp:" + ownNeverSynced,
	} {
		if _, exists := own[hidden]; exists {
			t.Fatalf("catalog exposed a hidden server row: %s", hidden)
		}
	}
	ownWithWorkspace := listFor("u1", "&workspace_id=ws1")
	if _, exists := ownWithWorkspace["usermcp:"+ownSynced]; !exists {
		t.Fatalf("personal row disappeared inside a workspace scope")
	}
	shared, ok := ownWithWorkspace["usermcp:"+sharedOwnedByTeammate]
	if !ok {
		t.Fatalf("member-visible shared server missing from workspace catalog: %+v", ownWithWorkspace)
	}
	if shared.Allowed {
		t.Fatalf("non-owner member bypassed the selected Tools policy: %+v", shared)
	}
	teammateView := listFor("u2", "&workspace_id=ws1")
	if row, ok := teammateView["usermcp:"+teammatePersonal]; !ok || !row.Allowed {
		t.Fatalf("owner's personal server not exempt for u2: %+v (ok=%v)", row, ok)
	}
	if row, ok := teammateView["usermcp:"+sharedOwnedByTeammate]; !ok || !row.Allowed {
		t.Fatalf("owner's workspace server not exempt for u2: %+v (ok=%v)", row, ok)
	}
	if _, exists := teammateView["usermcp:"+ownSynced]; exists {
		t.Fatalf("u2 saw u1's personal server")
	}

	// The same exemption must survive turn selection filtering.
	kept, configured := applyTurnToolPermissions(permissions, []string{
		"usermcp:" + ownSynced, "usermcp:" + sharedOwnedByTeammate, "usermcp:" + teammatePersonal,
	}, true, toolPolicyScope{ctx: ctx, db: db, userID: "u1", workspaceID: "ws1"})
	if !configured || len(kept) != 1 || kept[0] != "usermcp:"+ownSynced {
		t.Fatalf("applyTurnToolPermissions kept=%v want only the owner-exempt id", kept)
	}

	// An official workspace allowlist must remain independent from the user's
	// scoped MCP namespace. In particular, group Tools=all must not be rewritten
	// to the official selected ids, or a teammate-owned shared MCP would be
	// incorrectly marked unavailable before the registry can enforce its scope.
	allPermissions := store.DefaultUserGroupPermissions()
	allPermissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessAll}
	allRaw, err := json.Marshal(allPermissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='g1'`, string(allRaw))
	officialOnly := []string{"mcp:official-only"}
	if _, err := store.UpdateWorkspacePolicy(ctx, db, "ws1", "u1", store.WorkspacePolicyPatch{
		AllowedMCPServerIDs: &officialOnly,
	}); err != nil {
		t.Fatal(err)
	}
	groupAllView := listFor("u1", "&workspace_id=ws1")
	shared, ok = groupAllView["usermcp:"+sharedOwnedByTeammate]
	if !ok || !shared.Allowed {
		t.Fatalf("official allowlist removed teammate user MCP under group-all policy: %+v (ok=%v)", shared, ok)
	}
}

func TestUserMCPRoutesRequireAuthentication(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-mcp-route-auth.db"))
	defer db.Close()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/me/mcps", nil),
		httptest.NewRequest(http.MethodPost, "/api/me/mcps", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPatch, "/api/me/mcps/umcp_test", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/me/mcps/umcp_test", nil),
		httptest.NewRequest(http.MethodPost, "/api/me/mcps/umcp_test/test", nil),
		httptest.NewRequest(http.MethodPost, "/api/me/mcps/umcp_test/sync", nil),
	} {
		recorder := httptest.NewRecorder()
		NewRouter(Deps{DB: db}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d body=%s", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestWorkspaceAdminManagesTeammateMCPWithActorScope(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `
		INSERT INTO users(id,email,password_hash,role,status) VALUES
			('u1','owner@example.test','h','user','active'),
			('u2','member@example.test','h','user','active'),
			('u3','outsider@example.test','h','user','active');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws1','u1','admin'),('ws1','u2','member')
	`)
	const authorization = "Bearer workspace-secret"
	bridge := startUserMCPBridge(t, authorization, "workspace_search", 0)
	t.Cleanup(bridge.Close)
	server, err := store.CreateUserMCPServer(t.Context(), fixture.db, store.UserMCPServer{
		ID: "umcp_workspace_actor", UserID: "u2", WorkspaceID: "ws1", Name: "Team server",
		Icon: "Blocks", Description: "Shared MCP", URL: "https://old.example.test/mcp",
		Headers: map[string]string{"Authorization": authorization}, Enabled: true,
		DiscoveredTools: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The workspace owner manages a member-owned row. Discovery and sync-state
	// persistence must use the authenticated actor (u1), while the row owner
	// remains u2.
	updatedRecorder := fixture.request(t, http.MethodPatch,
		"/api/me/mcps/"+server.ID+"?workspace_id=ws1",
		`{"url":"`+bridge.URL+`"}`, "u1")
	updated := decodeMCPAdminResponse[userMCPServerResponse](t, updatedRecorder, http.StatusOK)
	if !updated.CanManage || updated.WorkspaceID != "ws1" || updated.LastError != "" ||
		!strings.Contains(string(updated.DiscoveredTools), "workspace_search") {
		t.Fatalf("workspace admin update/discovery mismatch: %+v", updated)
	}
	if updated.Headers["Authorization"] != mcpHeaderMask || strings.Contains(updatedRecorder.Body.String(), authorization) {
		t.Fatalf("workspace admin response leaked header or lost mask: %s", updatedRecorder.Body.String())
	}

	// A masked header round-trip keeps the stored credential, even when the
	// workspace administrator uses a different header casing.
	maskedRecorder := fixture.request(t, http.MethodPatch,
		"/api/me/mcps/"+server.ID+"?workspace_id=ws1",
		`{"headers":{"authorization":"`+mcpHeaderMask+`"}}`, "u1")
	if maskedRecorder.Code != http.StatusOK {
		t.Fatalf("masked workspace header update status=%d body=%s", maskedRecorder.Code, maskedRecorder.Body.String())
	}
	stored, err := store.GetUserMCPServerScoped(t.Context(), fixture.db, server.ID, "u1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.UserID != "u2" || stored.Headers["Authorization"] != authorization {
		t.Fatalf("workspace actor/owner or header round-trip mismatch: %+v", stored)
	}

	// Personal scope must never resolve a workspace row, and a non-member cannot
	// read it even when they know the id.
	if code := fixture.request(t, http.MethodGet, "/api/me/mcps", "", "u1").Code; code != http.StatusOK {
		t.Fatalf("personal list status=%d", code)
	}
	if code := fixture.request(t, http.MethodPatch, "/api/me/mcps/"+server.ID,
		`{"name":"personal-scope-escape"}`, "u1").Code; code != http.StatusNotFound {
		t.Fatalf("workspace row leaked through personal scope: status=%d", code)
	}
	if code := fixture.request(t, http.MethodGet, "/api/me/mcps?workspace_id=ws1", "", "u3").Code; code != http.StatusNotFound {
		t.Fatalf("outsider workspace scope status=%d", code)
	}
}

func TestWorkspaceMCPListHidesConnectionDetailsWithoutUseOrManagePermission(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `
		INSERT INTO users(id,email,password_hash,role,status) VALUES
			('u1','owner@example.test','h','user','active'),
			('u2','creator@example.test','h','user','active'),
			('u3','viewer@example.test','h','user','active');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role,can_use_mcp) VALUES
			('ws1','u1','admin',0),('ws1','u2','member',0),('ws1','u3','member',0)
	`)
	const headerValue = "workspace-private-header"
	server, err := store.CreateUserMCPServer(t.Context(), fixture.db, store.UserMCPServer{
		ID: "umcp_workspace_private", UserID: "u2", WorkspaceID: "ws1", Name: "Shared private server",
		Icon: "Blocks", Description: "Visible list metadata", URL: "https://private.example.test/mcp",
		Headers: map[string]string{"X-Private-Route": headerValue}, Enabled: true,
		DiscoveredTools: json.RawMessage(`[{"name":"private_lookup","inputSchema":{"type":"object"}}]`),
		ProtocolVersion: "private-protocol", LastError: "private-diagnostic", LastSyncedAt: 123,
	})
	if err != nil {
		t.Fatal(err)
	}

	viewerRecorder := fixture.request(t, http.MethodGet, "/api/me/mcps?workspace_id=ws1", "", "u3")
	viewer := decodeMCPAdminResponse[[]userMCPServerResponse](t, viewerRecorder, http.StatusOK)
	if len(viewer) != 1 || viewer[0].ID != server.ID || viewer[0].CanManage ||
		viewer[0].Name != server.Name || viewer[0].Description != server.Description || !viewer[0].Enabled {
		t.Fatalf("restricted member list metadata mismatch: %+v", viewer)
	}
	if viewer[0].URL != "" || len(viewer[0].Headers) != 0 || string(viewer[0].DiscoveredTools) != "[]" ||
		viewer[0].ProtocolVersion != "" || viewer[0].LastError != "" {
		t.Fatalf("restricted member received executable MCP details: %+v", viewer[0])
	}
	for _, secret := range []string{server.URL, "X-Private-Route", headerValue, "private_lookup", "private-protocol", "private-diagnostic"} {
		if strings.Contains(viewerRecorder.Body.String(), secret) {
			t.Fatalf("restricted member response leaked %q: %s", secret, viewerRecorder.Body.String())
		}
	}

	// Creation and administration remain management capabilities even when the
	// same member is barred from selecting or dialing MCP tools.
	for _, actorID := range []string{"u1", "u2"} {
		listed := decodeMCPAdminResponse[[]userMCPServerResponse](t,
			fixture.request(t, http.MethodGet, "/api/me/mcps?workspace_id=ws1", "", actorID), http.StatusOK)
		if len(listed) != 1 || !listed[0].CanManage || listed[0].URL != server.URL ||
			listed[0].Headers["X-Private-Route"] != mcpHeaderMask ||
			!strings.Contains(string(listed[0].DiscoveredTools), "private_lookup") ||
			listed[0].ProtocolVersion != "private-protocol" || listed[0].LastError != "private-diagnostic" {
			t.Fatalf("manager %s lost MCP maintenance details: %+v", actorID, listed)
		}
	}
}

func TestWorkspaceMCPCreateManageAndUsePermissionsAreIndependent(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `
		INSERT INTO users(id,email,password_hash,role,status) VALUES
			('u1','owner@example.test','h','user','active'),
			('u2','member@example.test','h','user','active');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws1','u1','admin'),('ws1','u2','member')
	`)
	bridge := startUserMCPBridge(t, "", "workspace_lookup", 0)
	t.Cleanup(bridge.Close)
	server, err := store.CreateUserMCPServer(t.Context(), fixture.db, store.UserMCPServer{
		ID: "umcp_granular_permissions", UserID: "u2", WorkspaceID: "ws1", Name: "Member server",
		Icon: "Blocks", Description: "Shared MCP", URL: bridge.URL, Enabled: true,
		DiscoveredTools: json.RawMessage(`[{"name":"workspace_lookup","description":"Shared","inputSchema":{"type":"object"}}]`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Creation permission is an admission gate for new rows, not a lease on rows
	// the member already owns. Keep the legacy aggregate in sync to prove neither
	// store nor handler is accidentally consulting it as a management gate.
	mustExec(t, fixture.db, `UPDATE workspace_members
		SET can_create_mcp=0, can_create_skills_prompts=0, can_use_mcp=1
		WHERE workspace_id='ws1' AND user_id='u2'`)
	listed := decodeMCPAdminResponse[[]userMCPServerResponse](t,
		fixture.request(t, http.MethodGet, "/api/me/mcps?workspace_id=ws1", "", "u2"), http.StatusOK)
	if len(listed) != 1 || listed[0].ID != server.ID || !listed[0].CanManage {
		t.Fatalf("existing resource lost manage authority after create revoke: %+v", listed)
	}
	createRecorder := fixture.request(t, http.MethodPost, "/api/me/mcps", `{
		"workspace_id":"ws1","name":"Second server","icon":"Blocks",
		"description":"must be denied","url":"`+bridge.URL+`"
	}`, "u2")
	if createRecorder.Code != http.StatusForbidden {
		t.Fatalf("create without can_create_mcp status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}

	tested := decodeMCPAdminResponse[userMCPTestResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/test?workspace_id=ws1", "", "u2"), http.StatusOK)
	if !tested.OK || len(tested.Tools) != 1 || tested.Tools[0].Name != "workspace_lookup" {
		t.Fatalf("resource owner could not test existing MCP after create revoke: %+v", tested)
	}
	synced := decodeMCPAdminResponse[userMCPServerResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/sync?workspace_id=ws1", "", "u2"), http.StatusOK)
	if !synced.CanManage || !strings.Contains(string(synced.DiscoveredTools), "workspace_lookup") {
		t.Fatalf("resource owner could not sync existing MCP after create revoke: %+v", synced)
	}
	updated := decodeMCPAdminResponse[userMCPServerResponse](t,
		fixture.request(t, http.MethodPatch, "/api/me/mcps/"+server.ID+"?workspace_id=ws1", `{"name":"Managed after revoke"}`, "u2"), http.StatusOK)
	if updated.Name != "Managed after revoke" || !updated.CanManage {
		t.Fatalf("resource owner could not update existing MCP after create revoke: %+v", updated)
	}

	// Workspace administrators use the same manage path, while remote operations
	// still require the independent member-level use capability.
	adminTest := decodeMCPAdminResponse[userMCPTestResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+server.ID+"/test?workspace_id=ws1", "", "u1"), http.StatusOK)
	if !adminTest.OK {
		t.Fatalf("workspace owner could not test teammate MCP: %+v", adminTest)
	}
	mustExec(t, fixture.db, `UPDATE workspace_members SET can_use_mcp=0
		WHERE workspace_id='ws1' AND user_id='u2'`)
	for _, suffix := range []string{"test", "sync"} {
		recorder := fixture.request(t, http.MethodPost,
			"/api/me/mcps/"+server.ID+"/"+suffix+"?workspace_id=ws1", "", "u2")
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s without can_use_mcp status=%d body=%s", suffix, recorder.Code, recorder.Body.String())
		}
	}
	// Metadata maintenance is local and remains available even when the member
	// cannot dial or select this server.
	if recorder := fixture.request(t, http.MethodPatch,
		"/api/me/mcps/"+server.ID+"?workspace_id=ws1", `{"description":"Still manageable"}`, "u2"); recorder.Code != http.StatusOK {
		t.Fatalf("metadata update without use permission status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	allowMCP := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), fixture.db, "ws1", "u1", store.WorkspacePolicyPatch{AllowMCP: &allowMCP}); err != nil {
		t.Fatal(err)
	}
	if recorder := fixture.request(t, http.MethodPost,
		"/api/me/mcps/"+server.ID+"/test?workspace_id=ws1", "", "u1"); recorder.Code != http.StatusForbidden {
		t.Fatalf("workspace AllowMCP=false test status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	allowMCP = true
	if _, err := store.UpdateWorkspacePolicy(t.Context(), fixture.db, "ws1", "u1", store.WorkspacePolicyPatch{AllowMCP: &allowMCP}); err != nil {
		t.Fatal(err)
	}
	deleted := decodeMCPAdminResponse[map[string]bool](t,
		fixture.request(t, http.MethodDelete, "/api/me/mcps/"+server.ID+"?workspace_id=ws1", "", "u2"), http.StatusOK)
	if !deleted["ok"] {
		t.Fatalf("resource owner could not delete existing MCP after create revoke: %+v", deleted)
	}
}

func TestUserMCPDiscoveryRedactsHeaderSecretsFromToolMetadata(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u1@example.test','h','user','active')`)
	const authorization = "Bearer metadata-secret"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request mcpAdminRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "server/discover":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"protocolVersion": "2026-07-28",
				"serverInfo":      map[string]any{"name": "metadata-echo"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			writeMCPAdminRPCResult(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name":        "metadata_tool",
					"title":       "title=" + authorization,
					"description": "description=" + authorization,
					"inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{
							"token-" + authorization: map[string]any{"default": authorization},
						},
					},
					"outputSchema": map[string]any{"description": authorization},
					"annotations":  map[string]any{"note": authorization},
					"_meta":        map[string]any{"echo-" + authorization: authorization},
					"icons":        []any{map[string]any{"src": "https://icons.test/" + authorization, "sizes": []string{authorization}}},
				}},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(remote.Close)

	recorder := fixture.request(t, http.MethodPost, "/api/me/mcps", `{
		"name":"Metadata echo","icon":"Blocks","description":"Echo test","url":"`+remote.URL+`",
		"headers":{"Authorization":"`+authorization+`"}
	}`, "u1")
	created := decodeMCPAdminResponse[userMCPServerResponse](t, recorder, http.StatusCreated)
	if created.LastError != "" || !strings.Contains(string(created.DiscoveredTools), "metadata_tool") {
		t.Fatalf("metadata discovery failed: %+v", created)
	}
	if strings.Contains(recorder.Body.String(), authorization) || strings.Contains(string(created.DiscoveredTools), authorization) {
		t.Fatalf("create response leaked metadata secret: %s", recorder.Body.String())
	}
	if !strings.Contains(string(created.DiscoveredTools), mcpHeaderMask) {
		t.Fatalf("metadata secret was not redacted in snapshot: %s", created.DiscoveredTools)
	}
	if !strings.Contains(string(created.DiscoveredTools), "token-"+mcpHeaderMask) ||
		!strings.Contains(string(created.DiscoveredTools), "echo-"+mcpHeaderMask) {
		t.Fatalf("metadata JSON keys were not redacted in snapshot: %s", created.DiscoveredTools)
	}
	stored, err := store.GetUserMCPServerScoped(t.Context(), fixture.db, created.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.DiscoveredTools), authorization) {
		t.Fatalf("stored metadata snapshot leaked secret: %s", stored.DiscoveredTools)
	}

	tested := decodeMCPAdminResponse[userMCPTestResponse](t,
		fixture.request(t, http.MethodPost, "/api/me/mcps/"+created.ID+"/test", "", "u1"), http.StatusOK)
	if !tested.OK || len(tested.Tools) != 1 || tested.Tools[0].Name != "metadata_tool" {
		t.Fatalf("metadata test response mismatch: %+v", tested)
	}
}
