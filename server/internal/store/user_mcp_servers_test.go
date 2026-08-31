package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// openUserMCPTestDB mirrors openLibraryTestDB: two personal users plus ws1
// owned by u1 with a managing member (u2), a guest (u3), and an outsider (u4).
func openUserMCPTestDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "user-mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO users(id,email,password_hash) VALUES
			('u1','u1@example.test','h'),('u2','u2@example.test','h'),
			('u3','u3@example.test','h'),('u4','u4@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role,can_create_skills_prompts) VALUES
			('ws1','u1','admin',1),('ws1','u2','member',1),('ws1','u3','guest',1)
	`); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}

func TestUserMCPServerPersonalAndWorkspaceScopesAreIndependent(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)

	personal, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "Weather", Icon: "Cloud", Description: "personal copy",
		URL: "https://weather.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer personal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if personal.ID == "" || personal.WorkspaceID != "" || !personal.CanManage ||
		personal.Enabled || personal.Headers["Authorization"] != "Bearer personal" ||
		string(personal.DiscoveredTools) != "[]" || personal.CreatedAt <= 0 {
		t.Fatalf("created personal user MCP server has invalid defaults: %+v", personal)
	}

	// The same display name is legal in the shared workspace namespace.
	shared, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", WorkspaceID: "ws1", Name: " weather ", Description: "workspace copy",
		URL: "https://weather.example.test/ws-mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if shared.WorkspaceID != "ws1" || shared.Name != "weather" {
		t.Fatalf("workspace row mismatch: %+v", shared)
	}
	// ... and in another user's personal space.
	if _, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u2", Name: "WEATHER", URL: "https://other.example.test/mcp",
	}); err != nil {
		t.Fatalf("same name across personal owners rejected: %v", err)
	}

	// Duplicate inside the same scope is translated regardless of case/spacing.
	if _, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: " weather ", URL: "https://dup.example.test/mcp",
	}); !errors.Is(err, ErrUserMCPNameExists) {
		t.Fatalf("personal duplicate err=%v, want ErrUserMCPNameExists", err)
	}
	if _, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u2", WorkspaceID: "ws1", Name: "Weather", URL: "https://dup.example.test/mcp",
	}); !errors.Is(err, ErrUserMCPNameExists) {
		t.Fatalf("workspace duplicate by another member err=%v, want ErrUserMCPNameExists", err)
	}

	personalRows, err := ListUserMCPServersScoped(ctx, db, "u1", "")
	if err != nil || len(personalRows) != 1 || personalRows[0].ID != personal.ID {
		t.Fatalf("personal rows=%+v err=%v", personalRows, err)
	}
	workspaceRows, err := ListUserMCPServersScoped(ctx, db, "u1", "ws1")
	if err != nil || len(workspaceRows) != 1 || workspaceRows[0].ID != shared.ID {
		t.Fatalf("workspace rows=%+v err=%v", workspaceRows, err)
	}
	if _, err := GetUserMCPServerScoped(ctx, db, personal.ID, "u1", "ws1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal row visible in workspace scope err=%v", err)
	}
	if _, err := GetUserMCPServerScoped(ctx, db, shared.ID, "u1", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace row visible in personal scope err=%v", err)
	}
	outsider, err := ListUserMCPServersScoped(ctx, db, "u4", "ws1")
	if err != nil || len(outsider) != 0 {
		t.Fatalf("outsider workspace rows=%+v err=%v", outsider, err)
	}
	if err := DeleteUserMCPServer(ctx, db, personal.ID, "u2", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user personal delete err=%v", err)
	}
}

func TestUserMCPServerWorkspaceCanManageIsRoleAware(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)

	ownerRow, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", WorkspaceID: "ws1", Name: "owner-mcp", URL: "https://a.example.test/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	memberRow, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u2", WorkspaceID: "ws1", Name: "member-mcp", URL: "https://b.example.test/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The workspace admin (owner) manages every shared row.
	adminRows, err := ListUserMCPServersScoped(ctx, db, "u1", "ws1")
	if err != nil || len(adminRows) != 2 {
		t.Fatalf("admin rows=%+v err=%v", adminRows, err)
	}
	for _, row := range adminRows {
		if !row.CanManage {
			t.Fatalf("workspace admin can_manage=false for %s", row.ID)
		}
	}
	// A member manages only their own row.
	memberRows, err := ListUserMCPServersScoped(ctx, db, "u2", "ws1")
	if err != nil || len(memberRows) != 2 {
		t.Fatalf("member rows=%+v err=%v", memberRows, err)
	}
	for _, row := range memberRows {
		if want := row.ID == memberRow.ID; row.CanManage != want {
			t.Fatalf("member can_manage for %s=%v want=%v", row.ID, row.CanManage, want)
		}
	}
	// A guest reads the shared library but never manages it, and cannot create.
	guestRows, err := ListUserMCPServersScoped(ctx, db, "u3", "ws1")
	if err != nil || len(guestRows) != 2 {
		t.Fatalf("guest rows=%+v err=%v", guestRows, err)
	}
	for _, row := range guestRows {
		if row.CanManage {
			t.Fatalf("guest unexpectedly can manage %s", row.ID)
		}
	}
	if _, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u3", WorkspaceID: "ws1", Name: "guest-mcp", URL: "https://c.example.test/mcp",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest workspace create err=%v, want ErrNotFound", err)
	}
	if _, err := UpdateUserMCPServer(ctx, db, memberRow.ID, "u2", "ws1",
		UserMCPServerPatch{Description: ptr("creator edit")}); err != nil {
		t.Fatalf("member could not update own row: %v", err)
	}
	if _, err := UpdateUserMCPServer(ctx, db, ownerRow.ID, "u2", "ws1",
		UserMCPServerPatch{Description: ptr("stolen edit")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member edited admin-owned row err=%v", err)
	}
	if err := DeleteUserMCPServer(ctx, db, ownerRow.ID, "u3", "ws1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guest deleted workspace row err=%v", err)
	}
}

func TestUserMCPServerUpdateMergesPartiallyAndSyncStateKeepsSnapshot(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)
	created, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "Maps", Description: "original", URL: "https://maps.example.test/mcp",
		Headers: map[string]string{"api-key": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	name := "  City Maps  "
	enabled := true
	updated, err := UpdateUserMCPServer(ctx, db, created.ID, "u1", "", UserMCPServerPatch{Name: &name, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "City Maps" || !updated.Enabled ||
		updated.Description != "original" || updated.URL != "https://maps.example.test/mcp" ||
		updated.Headers["api-key"] != "secret" {
		t.Fatalf("partial update mismatch: %+v", updated)
	}
	if _, err := UpdateUserMCPServer(ctx, db, created.ID, "u1", "", UserMCPServerPatch{}); err != nil {
		t.Fatalf("empty patch should still load the row: %v", err)
	}
	// Renaming onto a name already taken in the same scope is rejected.
	if _, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "Transit", URL: "https://transit.example.test/mcp",
	}); err != nil {
		t.Fatal(err)
	}
	dup := "transit"
	if _, err := UpdateUserMCPServer(ctx, db, created.ID, "u1", "", UserMCPServerPatch{Name: &dup}); !errors.Is(err, ErrUserMCPNameExists) {
		t.Fatalf("duplicate rename err=%v, want ErrUserMCPNameExists", err)
	}

	tools := json.RawMessage(`[{"name":"geocode","description":"Geocode an address"}]`)
	synced, err := UpdateUserMCPServerSyncState(ctx, db, created.ID, "u1", "", tools, "2025-03-26", "", 1234)
	if err != nil {
		t.Fatal(err)
	}
	if synced.ProtocolVersion != "2025-03-26" || synced.LastSyncedAt != 1234 || string(synced.DiscoveredTools) == "[]" {
		t.Fatalf("sync state mismatch: %+v", synced)
	}
	failed, err := UpdateUserMCPServerSyncState(ctx, db, created.ID, "u1", "", nil, synced.ProtocolVersion, "connection failed", 1235)
	if err != nil {
		t.Fatal(err)
	}
	if string(failed.DiscoveredTools) != string(synced.DiscoveredTools) || failed.LastError != "connection failed" {
		t.Fatalf("sync failure removed last good snapshot: %+v", failed)
	}
	// Sync state is tenant-scoped: strangers cannot write through it.
	if _, err := UpdateUserMCPServerSyncState(ctx, db, created.ID, "u2", "", tools, "", "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user sync state err=%v, want ErrNotFound", err)
	}
	if _, err := UpdateUserMCPServerSyncState(ctx, db, created.ID, "u1", "ws1", tools, "", "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal row sync via workspace scope err=%v, want ErrNotFound", err)
	}
}

func TestUserMCPServerDiscoveryCASRejectsNewerResult(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)
	created, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "CAS user", Description: "compare and swap",
		URL: "https://cas-user.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer cas-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if created, err = UpdateUserMCPServer(ctx, db, created.ID, "u1", "", UserMCPServerPatch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	first, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *created, "u1",
		json.RawMessage(`[{"name":"first","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 10)
	if err != nil {
		t.Fatalf("first CAS sync: %v", err)
	}
	if _, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *created, "u1",
		json.RawMessage(`[{"name":"stale","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 11); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("stale CAS error=%v want ErrMCPDiscoveryStateChanged", err)
	}
	latest, err := GetUserMCPServerScoped(ctx, db, created.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(latest.DiscoveredTools, []byte(`"first"`)) || bytes.Contains(latest.DiscoveredTools, []byte(`"stale"`)) {
		t.Fatalf("stale CAS replaced snapshot: %s", latest.DiscoveredTools)
	}
	url := "https://cas-user-new.example.test/mcp"
	if _, err := UpdateUserMCPServer(ctx, db, created.ID, "u1", "", UserMCPServerPatch{URL: &url}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *first, "u1",
		json.RawMessage(`[{"name":"old-endpoint","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 12); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("metadata-stale CAS error=%v want ErrMCPDiscoveryStateChanged", err)
	}

	// The same CAS must work through the workspace manage predicates when an
	// administrator discovers a teammate-owned endpoint.
	shared, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u2", WorkspaceID: "ws1", Name: "CAS shared", Description: "shared",
		URL: "https://cas-shared.example.test/mcp", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedFirst, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *shared, "u1",
		json.RawMessage(`[{"name":"shared-first","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 20)
	if err != nil {
		t.Fatalf("workspace CAS sync: %v", err)
	}
	if _, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *shared, "u1",
		json.RawMessage(`[{"name":"shared-stale","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 21); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("workspace stale CAS error=%v want ErrMCPDiscoveryStateChanged", err)
	}
	if !bytes.Contains(sharedFirst.DiscoveredTools, []byte(`"shared-first"`)) {
		t.Fatalf("workspace CAS snapshot=%s", sharedFirst.DiscoveredTools)
	}
}

func TestUserMCPServerMetadataResetIsAtomicAndVersioned(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)
	created, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "Atomic user", URL: "https://old-user.example.test/mcp", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer old-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *created, "u1",
		json.RawMessage(`[{"name":"old_tool","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "old error", 10)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := UpdateUserMCPServerSyncStateIfCurrent(ctx, db, *first, "u1",
		json.RawMessage(`[{"name":"newer_tool","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 11)
	if err != nil {
		t.Fatal(err)
	}

	newURL := "https://new-user.example.test/mcp"
	patch := UserMCPServerPatch{URL: &newURL, ResetDiscovery: true}
	if _, err := UpdateUserMCPServerIfCurrent(ctx, db, *first, "u1", patch); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("stale metadata update error=%v, want ErrMCPDiscoveryStateChanged", err)
	}
	unchanged, err := GetUserMCPServerScoped(ctx, db, created.ID, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.URL != newer.URL || !bytes.Contains(unchanged.DiscoveredTools, []byte(`"newer_tool"`)) {
		t.Fatalf("stale metadata update changed authoritative row: %+v", unchanged)
	}

	reset, err := UpdateUserMCPServerIfCurrent(ctx, db, *newer, "u1", patch)
	if err != nil {
		t.Fatal(err)
	}
	if reset.URL != newURL || string(reset.DiscoveredTools) != "[]" ||
		reset.ProtocolVersion != "" || reset.LastError != "" || reset.LastSyncedAt != 0 {
		t.Fatalf("metadata and discovery state were not reset together: %+v", reset)
	}
}

func TestUserMCPServerDeleteAndBackupRoundTrip(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)
	created, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", WorkspaceID: "ws1", Name: "Feeds", Enabled: true,
		URL: "https://feeds.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer shared"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var dump bytes.Buffer
	if count, err := ExportTable(ctx, db, "user_mcp_servers", &dump); err != nil || count != 1 {
		t.Fatalf("export user MCP servers: count=%d err=%v", count, err)
	}
	// The workspace admin may delete a member-managed namespace row.
	if err := DeleteUserMCPServer(ctx, db, created.ID, "u2", "ws1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("creator delete by non-manager err=%v, want ErrNotFound", err)
	}
	if err := DeleteUserMCPServer(ctx, db, created.ID, "u1", "ws1"); err != nil {
		t.Fatalf("delete user MCP server: %v", err)
	}
	if _, err := GetUserMCPServerScoped(ctx, db, created.ID, "u1", "ws1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row survived delete err=%v", err)
	}
	if count, err := RestoreTable(ctx, db, "user_mcp_servers", bytes.NewReader(dump.Bytes())); err != nil || count != 1 {
		t.Fatalf("restore user MCP servers: count=%d err=%v", count, err)
	}
	restored, err := GetUserMCPServerScoped(ctx, db, created.ID, "u1", "ws1")
	if err != nil || !restored.Enabled || restored.Headers["Authorization"] != "Bearer shared" {
		t.Fatalf("restored row mismatch: %+v err=%v", restored, err)
	}

	// User data belongs to the full backup only, never the admin config archive.
	assertTableOrder(t, BackupTableOrder(), "user_mcp_servers", true)
	assertTableOrder(t, ConfigTableOrder(), "user_mcp_servers", false)
}

func TestDeleteWorkspaceRemovesScopedUserMCPServers(t *testing.T) {
	db, ctx := openUserMCPTestDB(t)
	personal, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", Name: "Personal", Enabled: true,
		URL: "https://personal.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer personal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceServer, err := CreateUserMCPServer(ctx, db, UserMCPServer{
		UserID: "u1", WorkspaceID: "ws1", Name: "Workspace", Enabled: true,
		URL: "https://workspace.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := UserMCPServerIDsForWorkspace(ctx, db, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != workspaceServer.ID {
		t.Fatalf("workspace MCP invalidation worklist = %v, want [%s]", ids, workspaceServer.ID)
	}

	if err := MarkWorkspaceDeleting(ctx, db, "ws1", "u1"); err != nil {
		t.Fatalf("mark workspace deleting: %v", err)
	}
	if err := DeleteWorkspaceRow(ctx, db, "ws1", "u1"); err != nil {
		t.Fatalf("delete workspace row: %v", err)
	}

	var workspaceRows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_mcp_servers WHERE workspace_id=?`, "ws1",
	).Scan(&workspaceRows); err != nil {
		t.Fatal(err)
	}
	if workspaceRows != 0 {
		t.Fatalf("workspace MCP row %q survived workspace deletion", workspaceServer.ID)
	}
	restoredPersonal, err := GetUserMCPServerScoped(ctx, db, personal.ID, "u1", "")
	if err != nil || restoredPersonal.Headers["Authorization"] != "Bearer personal" {
		t.Fatalf("personal MCP row changed during workspace deletion: row=%+v err=%v", restoredPersonal, err)
	}
}

func assertTableOrder(t *testing.T, tables []string, table string, want bool) {
	t.Helper()
	for _, candidate := range tables {
		if candidate == table {
			if !want {
				t.Fatalf("%q unexpectedly present in backup table order", table)
			}
			return
		}
	}
	if want {
		t.Fatalf("%q missing from backup table order: %v", table, tables)
	}
}

func ptr[T any](value T) *T { return &value }
