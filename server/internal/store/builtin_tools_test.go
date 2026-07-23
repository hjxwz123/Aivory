package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestBuiltinToolsPolicyDistinguishesDefaultAllFromExplicitNone(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(` `)} {
		names, configured, err := ParseBuiltinTools(raw)
		if err != nil || configured || names != nil {
			t.Fatalf("default policy %q = names=%v configured=%v err=%v", raw, names, configured, err)
		}
		if !BuiltinToolAllowed(raw, "future_tool") {
			t.Fatalf("default policy %q did not allow a future registered tool", raw)
		}
	}

	normalized, err := NormalizeBuiltinTools(json.RawMessage(`[]`))
	if err != nil || string(normalized) != "[]" {
		t.Fatalf("explicit none normalized to %q, err=%v", normalized, err)
	}
	names, configured, err := ParseBuiltinTools(normalized)
	if err != nil || !configured || len(names) != 0 || BuiltinToolAllowed(normalized, "web_search") {
		t.Fatalf("explicit none = names=%v configured=%v allowed=%v err=%v", names, configured, BuiltinToolAllowed(normalized, "web_search"), err)
	}
}

func TestNormalizeBuiltinToolsCanonicalizesAndRejectsInvalidValues(t *testing.T) {
	normalized, err := NormalizeBuiltinTools(json.RawMessage(`[" web_search ","python_execute","web_search"]`))
	if err != nil || string(normalized) != `["web_search","python_execute"]` {
		t.Fatalf("normalized = %s, err=%v", normalized, err)
	}
	if !BuiltinToolAllowed(normalized, "web_search") || BuiltinToolAllowed(normalized, "save_memory") {
		t.Fatalf("canonical policy allowed the wrong tools: %s", normalized)
	}
	for _, raw := range []string{`{}`, `"web_search"`, `[null]`, `[""]`, `["   "]`} {
		if _, err := NormalizeBuiltinTools(json.RawMessage(raw)); !errors.Is(err, ErrBuiltinToolsInvalid) {
			t.Fatalf("NormalizeBuiltinTools(%s) error = %v", raw, err)
		}
	}
}

func TestModelBuiltinToolsPersistenceKeepsNullablePolicy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "builtin-tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	channel, err := CreateChannel(ctx, db, "main", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	defaultModel, err := CreateModel(ctx, db, Model{ChannelID: channel.ID, RequestID: "default", Label: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	if defaultModel.BuiltinTools != nil {
		t.Fatalf("omitted builtin_tools = %s, want nil/default-all", defaultModel.BuiltinTools)
	}
	var nullable sql.NullString
	if err := db.QueryRow(`SELECT builtin_tools FROM models WHERE id=?`, defaultModel.ID).Scan(&nullable); err != nil || nullable.Valid {
		t.Fatalf("default policy persisted as %+v, err=%v", nullable, err)
	}

	noneModel, err := CreateModel(ctx, db, Model{ChannelID: channel.ID, RequestID: "none", Label: "None", BuiltinTools: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(noneModel.BuiltinTools) != "[]" {
		t.Fatalf("explicit none read as %q", noneModel.BuiltinTools)
	}
	if err := db.QueryRow(`SELECT builtin_tools FROM models WHERE id=?`, noneModel.ID).Scan(&nullable); err != nil || !nullable.Valid || nullable.String != "[]" {
		t.Fatalf("explicit none persisted as %+v, err=%v", nullable, err)
	}
}
