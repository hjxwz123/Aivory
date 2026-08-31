package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestMCPServerStoreCRUDSyncAndBackup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mcp-servers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	created, err := CreateMCPServer(ctx, db, MCPServer{
		Name: "12306", Icon: "Train", Description: "Rail timetable search",
		URL:     "https://mcp.example.test/mcp",
		Headers: map[string]string{"Authorization": "Bearer private-token", "X-Tenant": "tenant-a"},
	})
	if err != nil {
		t.Fatalf("create MCP server: %v", err)
	}
	if created.ID == "" || created.Enabled || created.CreatedAt <= 0 || created.UpdatedAt <= 0 {
		t.Fatalf("created MCP server has invalid defaults: %+v", created)
	}
	if created.Headers["Authorization"] != "Bearer private-token" || string(created.DiscoveredTools) != "[]" {
		t.Fatalf("created MCP server lost configuration: %+v", created)
	}

	enabled := true
	name := "China Railway"
	updated, err := UpdateMCPServer(ctx, db, created.ID, MCPServerPatch{Name: &name, Enabled: &enabled})
	if err != nil {
		t.Fatalf("update MCP server: %v", err)
	}
	if updated.Name != name || !updated.Enabled || updated.Headers["Authorization"] != "Bearer private-token" {
		t.Fatalf("updated MCP server mismatch: %+v", updated)
	}

	tools := json.RawMessage(`[{
		"name":"get-tickets","description":"Find train tickets","inputSchema":{"type":"object"}
	}]`)
	synced, err := UpdateMCPServerSyncState(ctx, db, created.ID, tools, "2025-03-26", "", 1234)
	if err != nil {
		t.Fatalf("record discovery: %v", err)
	}
	if synced.ProtocolVersion != "2025-03-26" || synced.LastSyncedAt != 1234 || string(synced.DiscoveredTools) == "[]" {
		t.Fatalf("sync state mismatch: %+v", synced)
	}
	failed, err := UpdateMCPServerSyncState(ctx, db, created.ID, nil, synced.ProtocolVersion, "connection failed", 1235)
	if err != nil {
		t.Fatalf("record sync failure: %v", err)
	}
	if string(failed.DiscoveredTools) != string(synced.DiscoveredTools) || failed.LastError != "connection failed" {
		t.Fatalf("sync failure removed last good snapshot: %+v", failed)
	}

	listed, err := ListMCPServers(ctx, db, true)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("enabled list = %+v, err=%v", listed, err)
	}

	var dump bytes.Buffer
	if count, err := ExportTable(ctx, db, "mcp_servers", &dump); err != nil || count != 1 {
		t.Fatalf("export MCP servers: count=%d err=%v", count, err)
	}
	if err := DeleteMCPServer(ctx, db, created.ID); err != nil {
		t.Fatalf("delete MCP server: %v", err)
	}
	if count, err := RestoreTable(ctx, db, "mcp_servers", bytes.NewReader(dump.Bytes())); err != nil || count != 1 {
		t.Fatalf("restore MCP servers: count=%d err=%v", count, err)
	}
	restored, err := GetMCPServer(ctx, db, created.ID)
	if err != nil {
		t.Fatalf("load restored MCP server: %v", err)
	}
	if restored.Headers["Authorization"] != "Bearer private-token" || restored.LastError != "connection failed" {
		t.Fatalf("restored MCP server mismatch: %+v", restored)
	}

	if err := DeleteMCPServer(ctx, db, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing MCP server error = %v", err)
	}
}

func TestMCPServerStoreRejectsDuplicateNameAndInvalidDiscovery(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mcp-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := CreateMCPServer(ctx, db, MCPServer{
		Name: "Literature", Description: "Search papers", URL: "https://example.test/mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMCPServer(ctx, db, MCPServer{
		Name: " literature ", Description: "Duplicate", URL: "https://other.example.test/mcp",
	}); !errors.Is(err, ErrMCPServerNameExists) {
		t.Fatalf("duplicate name error = %v", err)
	}
	if _, err := UpdateMCPServerSyncState(ctx, db, first.ID, json.RawMessage(`{"not":"an array"}`), "", "", 0); err == nil {
		t.Fatal("invalid discovery snapshot was accepted")
	}
}

func TestMCPServerDiscoveryCASRejectsNewerResult(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mcp-discovery-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := CreateMCPServer(ctx, db, MCPServer{
		Name: "CAS", Icon: "Blocks", Description: "compare and swap",
		URL: "https://cas.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer cas"},
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if created, err = UpdateMCPServer(ctx, db, created.ID, MCPServerPatch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	first, err := UpdateMCPServerSyncStateIfCurrent(ctx, db, *created,
		json.RawMessage(`[{"name":"first","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 10)
	if err != nil {
		t.Fatalf("first CAS sync: %v", err)
	}
	if first.LastSyncedAt != 10 {
		t.Fatalf("first sync timestamp=%d want=10", first.LastSyncedAt)
	}
	if _, err := UpdateMCPServerSyncStateIfCurrent(ctx, db, *created,
		json.RawMessage(`[{"name":"stale","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 11); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("stale CAS error=%v want ErrMCPDiscoveryStateChanged", err)
	}
	latest, err := GetMCPServer(ctx, db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(latest.DiscoveredTools, []byte(`"first"`)) || bytes.Contains(latest.DiscoveredTools, []byte(`"stale"`)) {
		t.Fatalf("stale CAS replaced snapshot: %s", latest.DiscoveredTools)
	}
	// A metadata edit also invalidates a result even if no newer discovery has
	// completed yet.
	url := "https://cas-new.example.test/mcp"
	if _, err := UpdateMCPServer(ctx, db, created.ID, MCPServerPatch{URL: &url}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMCPServerSyncStateIfCurrent(ctx, db, *first,
		json.RawMessage(`[{"name":"old-endpoint","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 12); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("metadata-stale CAS error=%v want ErrMCPDiscoveryStateChanged", err)
	}
}

func TestMCPServerMetadataResetIsAtomicAndVersioned(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mcp-metadata-reset.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := CreateMCPServer(ctx, db, MCPServer{
		Name: "Atomic", URL: "https://old.example.test/mcp", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := UpdateMCPServerSyncStateIfCurrent(ctx, db, *created,
		json.RawMessage(`[{"name":"old_tool","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "old error", 10)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := UpdateMCPServerSyncStateIfCurrent(ctx, db, *first,
		json.RawMessage(`[{"name":"newer_tool","inputSchema":{"type":"object"}}]`),
		"2026-07-28", "", 11)
	if err != nil {
		t.Fatal(err)
	}

	newURL := "https://new.example.test/mcp"
	patch := MCPServerPatch{URL: &newURL, ResetDiscovery: true}
	if _, err := UpdateMCPServerIfCurrent(ctx, db, *first, patch); !errors.Is(err, ErrMCPDiscoveryStateChanged) {
		t.Fatalf("stale metadata update error=%v, want ErrMCPDiscoveryStateChanged", err)
	}
	unchanged, err := GetMCPServer(ctx, db, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.URL != newer.URL || !bytes.Contains(unchanged.DiscoveredTools, []byte(`"newer_tool"`)) {
		t.Fatalf("stale metadata update changed authoritative row: %+v", unchanged)
	}

	reset, err := UpdateMCPServerIfCurrent(ctx, db, *newer, patch)
	if err != nil {
		t.Fatal(err)
	}
	if reset.URL != newURL || string(reset.DiscoveredTools) != "[]" ||
		reset.ProtocolVersion != "" || reset.LastError != "" || reset.LastSyncedAt != 0 {
		t.Fatalf("metadata and discovery state were not reset together: %+v", reset)
	}
}

func TestMCPServerIncludedInFullAndConfigBackups(t *testing.T) {
	for name, tables := range map[string][]string{
		"full":   BackupTableOrder(),
		"config": ConfigTableOrder(),
	} {
		found := false
		for _, table := range tables {
			if table == "mcp_servers" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s backup does not include mcp_servers: %v", name, tables)
		}
	}
}
