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
