package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestMCPServerIDsPolicyDistinguishesDefaultAllFromExplicitNone(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(` `)} {
		ids, configured, err := ParseMCPServerIDs(raw)
		if err != nil || configured || ids != nil {
			t.Fatalf("default policy %q = ids=%v configured=%v err=%v", raw, ids, configured, err)
		}
	}

	normalized, err := NormalizeMCPServerIDs(json.RawMessage(`[]`))
	if err != nil || string(normalized) != "[]" {
		t.Fatalf("explicit none normalized to %q, err=%v", normalized, err)
	}
	ids, configured, err := ParseMCPServerIDs(normalized)
	if err != nil || !configured || len(ids) != 0 {
		t.Fatalf("explicit none = ids=%v configured=%v err=%v", ids, configured, err)
	}
}

func TestNormalizeMCPServerIDsCanonicalizesAndRetainsUnavailableIDs(t *testing.T) {
	normalized, err := NormalizeMCPServerIDs(json.RawMessage(`[" rail ","missing","rail"]`))
	if err != nil || string(normalized) != `["rail","missing"]` {
		t.Fatalf("normalized = %s, err=%v", normalized, err)
	}
	for _, raw := range []string{`{}`, `"rail"`, `[null]`, `[""]`, `["   "]`} {
		if _, err := NormalizeMCPServerIDs(json.RawMessage(raw)); !errors.Is(err, ErrMCPServerIDsInvalid) {
			t.Fatalf("NormalizeMCPServerIDs(%s) error = %v", raw, err)
		}
	}
}

func TestModelMCPServerIDsPersistenceKeepsNullablePolicy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "model-mcp-defaults.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	channel, err := CreateChannel(ctx, db, "main", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}

	defaultModel, err := CreateModel(ctx, db, Model{ChannelID: channel.ID, RequestID: "default-mcp", Label: "Default MCP"})
	if err != nil {
		t.Fatal(err)
	}
	if defaultModel.MCPServerIDs != nil {
		t.Fatalf("omitted mcp_server_ids = %s, want nil/default-all", defaultModel.MCPServerIDs)
	}
	var nullable sql.NullString
	if err := db.QueryRow(`SELECT mcp_server_ids FROM models WHERE id=?`, defaultModel.ID).Scan(&nullable); err != nil || nullable.Valid {
		t.Fatalf("default MCP policy persisted as %+v, err=%v", nullable, err)
	}

	configured, err := CreateModel(ctx, db, Model{
		ChannelID: channel.ID, RequestID: "configured-mcp", Label: "Configured MCP",
		MCPServerIDs: json.RawMessage(`[" rail ","offline","rail"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(configured.MCPServerIDs) != `["rail","offline"]` {
		t.Fatalf("configured policy read as %q", configured.MCPServerIDs)
	}
	if err := db.QueryRow(`SELECT mcp_server_ids FROM models WHERE id=?`, configured.ID).Scan(&nullable); err != nil || !nullable.Valid || nullable.String != `["rail","offline"]` {
		t.Fatalf("configured MCP policy persisted as %+v, err=%v", nullable, err)
	}

	configured.MCPServerIDs = json.RawMessage(`[]`)
	updated, err := UpdateModel(ctx, db, configured.ID, *configured)
	if err != nil || string(updated.MCPServerIDs) != "[]" {
		t.Fatalf("explicit none update = %s, err=%v", updated.MCPServerIDs, err)
	}
	configured.MCPServerIDs = nil
	updated, err = UpdateModel(ctx, db, configured.ID, *configured)
	if err != nil || updated.MCPServerIDs != nil {
		t.Fatalf("default-all update = %s, err=%v", updated.MCPServerIDs, err)
	}
}
