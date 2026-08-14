package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

func assertNoPermissionEvent(t *testing.T, conn *eventsConn) {
	t.Helper()
	select {
	case payload := <-conn.ch:
		t.Fatalf("unexpected permission event: %s", payload)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestAdminSettingsOnlyRevokesGlobalCapabilitiesOnSemanticChange(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "global-capability-events.db"))
	t.Cleanup(func() { _ = db.Close() })
	if err := store.SetSetting(db, "disabled_tools", []string{"python_execute", "aivory_web_search"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "memory_enabled", true); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Cache: eventsTestCache}
	connA := eventsHub.registerForGroup("global-semantic-a", "group-a")
	t.Cleanup(func() { eventsHub.unregister("global-semantic-a", connA) })
	connB := eventsHub.registerForGroup("global-semantic-b", "group-b")
	t.Cleanup(func() { eventsHub.unregister("global-semantic-b", connB) })

	before := permissionGenerationEpoch(d, "global", "")
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"disabled_tools":[" web_search ","python_execute","web_search"],
		"memory_enabled":true
	}`))
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-value status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "global", ""); got != before {
		t.Fatalf("same-value update changed global generation epoch from %q to %q", before, got)
	}
	assertNoPermissionEvent(t, connA)
	assertNoPermissionEvent(t, connB)

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"disabled_tools":["python_execute"],
		"memory_enabled":false
	}`))
	rec = httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changed-value status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "global", ""); got == before {
		t.Fatalf("changed-value update did not advance global generation epoch %q", got)
	}
	for label, conn := range map[string]*eventsConn{"a": connA, "b": connB} {
		var event map[string]string
		if err := json.Unmarshal([]byte(waitEvent(t, conn.ch, 2*time.Second)), &event); err != nil {
			t.Fatalf("decode event for %s: %v", label, err)
		}
		if event["type"] != "account.permissions_updated" {
			t.Fatalf("event for %s = %v", label, event)
		}
	}
}

func TestMCPAvailabilityOnlyRevokesGlobalCapabilitiesOnSemanticChange(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "mcp-capability-events.db"))
	t.Cleanup(func() { _ = db.Close() })
	server, err := store.CreateMCPServer(t.Context(), db, store.MCPServer{
		Name: "Runtime MCP", Icon: "Blocks", Description: "Runtime capability",
		URL: "https://mcp.example.test/mcp", Enabled: true,
		DiscoveredTools: json.RawMessage(`[{
			"name":"lookup","description":"Look up records","inputSchema":{"type":"object"}
		}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Cache: eventsTestCache}
	conn := eventsHub.registerForGroup("mcp-capability-target", "group-a")
	t.Cleanup(func() { eventsHub.unregister("mcp-capability-target", conn) })
	revoked, unsubscribe := eventsTestCache.Subscribe(globalCapabilityRevocationTopic)
	t.Cleanup(unsubscribe)
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/mcp/:id", wrap(d, updateMCPServerAdmin))

	before := permissionGenerationEpoch(d, "global", "")
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/mcp/"+server.ID, strings.NewReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-value status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "global", ""); got != before {
		t.Fatalf("same-value update changed global generation epoch from %q to %q", before, got)
	}
	assertNoPermissionEvent(t, conn)
	select {
	case payload := <-revoked:
		t.Fatalf("same-value update published revocation: %q", payload)
	case <-time.After(150 * time.Millisecond):
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/admin/mcp/"+server.ID, strings.NewReader(`{"enabled":false}`))
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "global", ""); got == before {
		t.Fatalf("disable did not advance global generation epoch %q", got)
	}
	select {
	case <-revoked:
	case <-time.After(2 * time.Second):
		t.Fatal("disable did not publish generation revocation")
	}
	var event map[string]string
	if err := json.Unmarshal([]byte(waitEvent(t, conn.ch, 2*time.Second)), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "account.permissions_updated" {
		t.Fatalf("event = %v", event)
	}

	beforeEnable := permissionGenerationEpoch(d, "global", "")
	req = httptest.NewRequest(http.MethodPatch, "/api/admin/mcp/"+server.ID, strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "global", ""); got != beforeEnable {
		t.Fatalf("enable changed global generation epoch from %q to %q", beforeEnable, got)
	}
	select {
	case payload := <-revoked:
		t.Fatalf("enable published generation revocation: %q", payload)
	case <-time.After(150 * time.Millisecond):
	}
	if err := json.Unmarshal([]byte(waitEvent(t, conn.ch, 2*time.Second)), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "account.permissions_updated" {
		t.Fatalf("enable event = %v", event)
	}
}

func TestUserGroupUpdateOnlyRevokesPermissionsOnSemanticChange(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "group-permission-events.db"))
	t.Cleanup(func() { _ = db.Close() })
	permissions := store.DefaultUserGroupPermissions()
	permissions.Prompts = store.ResourceAccessPolicy{Mode: store.ResourceAccessSelected, IDs: []string{"prompt-a", "prompt-b"}}
	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessSelected, IDs: []string{"builtin:web_fetch", "mcp:docs"}}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	permissionsRaw := json.RawMessage(raw)
	group, err := store.CreateUserGroupWithPermissions(context.Background(), db, store.UserGroup{ID: "ug-semantic", Name: "Semantic"}, true, &permissionsRaw)
	if err != nil {
		t.Fatal(err)
	}

	d := Deps{DB: db, Cache: eventsTestCache}
	target := eventsHub.registerForGroup("group-semantic-target", group.ID)
	t.Cleanup(func() { eventsHub.unregister("group-semantic-target", target) })
	other := eventsHub.registerForGroup("group-semantic-other", "ug-other")
	t.Cleanup(func() { eventsHub.unregister("group-semantic-other", other) })
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/user-groups/:id", wrap(d, updateUserGroupAdmin))

	reordered := permissions
	reordered.Prompts.IDs = []string{" prompt-b ", "prompt-a", "prompt-b"}
	reordered.Tools.IDs = []string{"mcp:docs", "builtin:web_fetch"}
	requestBody, err := json.Marshal(map[string]any{"permissions": reordered})
	if err != nil {
		t.Fatal(err)
	}
	before := permissionGenerationEpoch(d, "group", group.ID)
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/user-groups/"+group.ID, strings.NewReader(string(requestBody)))
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-value status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "group", group.ID); got != before {
		t.Fatalf("same-value update changed group generation epoch from %q to %q", before, got)
	}
	assertNoPermissionEvent(t, target)
	assertNoPermissionEvent(t, other)

	permissions.AllowDrawing = false
	requestBody, err = json.Marshal(map[string]any{"permissions": permissions})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPatch, "/api/admin/user-groups/"+group.ID, strings.NewReader(string(requestBody)))
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("changed-value status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := permissionGenerationEpoch(d, "group", group.ID); got == before {
		t.Fatalf("changed-value update did not advance group generation epoch %q", got)
	}
	var event map[string]string
	if err := json.Unmarshal([]byte(waitEvent(t, target.ch, 2*time.Second)), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "account.permissions_updated" {
		t.Fatalf("target event = %v", event)
	}
	assertNoPermissionEvent(t, other)
}
