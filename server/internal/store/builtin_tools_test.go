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
	if err != nil || !configured || len(names) != 0 || BuiltinToolAllowed(normalized, "aivory_web_search") {
		t.Fatalf("explicit none = names=%v configured=%v allowed=%v err=%v", names, configured, BuiltinToolAllowed(normalized, "aivory_web_search"), err)
	}
}

func TestNormalizeBuiltinToolsCanonicalizesAndRejectsInvalidValues(t *testing.T) {
	normalized, err := NormalizeBuiltinTools(json.RawMessage(`[" web_search ","search_knowledge_base","python_execute","web_search"]`))
	if err != nil || string(normalized) != `["aivory_web_search","python_execute"]` {
		t.Fatalf("normalized = %s, err=%v", normalized, err)
	}
	if !BuiltinToolAllowed(normalized, "aivory_web_search") || BuiltinToolAllowed(normalized, "web_search") || BuiltinToolAllowed(normalized, "save_memory") ||
		BuiltinToolAllowed(json.RawMessage(`["search_knowledge_base"]`), "search_knowledge_base") {
		t.Fatalf("canonical policy allowed the wrong tools: %s", normalized)
	}
	retiredOnly, err := NormalizeBuiltinTools(json.RawMessage(`["search_knowledge_base"]`))
	if err != nil || string(retiredOnly) != "[]" {
		t.Fatalf("retired-only policy = %s, err=%v; want explicit none", retiredOnly, err)
	}
	for _, raw := range []string{`{}`, `"web_search"`, `[null]`, `[""]`, `["   "]`} {
		if _, err := NormalizeBuiltinTools(json.RawMessage(raw)); !errors.Is(err, ErrBuiltinToolsInvalid) {
			t.Fatalf("NormalizeBuiltinTools(%s) error = %v", raw, err)
		}
	}
}

func TestMigrateNormalizesLegacyBuiltinToolPolicies(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "retired-kb-tool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','retired-tool@example.test','hash','user')`); err != nil {
		t.Fatal(err)
	}
	conversation, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "Legacy RAG"})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := CreateChannel(ctx, db, "main", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := CreateModel(ctx, db, Model{ChannelID: channel.ID, RequestID: "legacy", Label: "Legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversations SET rag_mode='tool' WHERE id=?`, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET builtin_tools=? WHERE id=?`, `[" web_search ","search_knowledge_base"]`, model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE models SET official_tools=? WHERE id=?`, `[{"name":"web_search","icon":"search","request":{"tools":[{"type":"web_search"}]}}]`, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(db, "disabled_tools", []string{"search_knowledge_base", "web_search", "python_execute"}); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var ragMode, builtinTools string
	if err := db.QueryRow(`SELECT rag_mode FROM conversations WHERE id=?`, conversation.ID).Scan(&ragMode); err != nil || ragMode != "auto" {
		t.Fatalf("migrated rag_mode=%q err=%v", ragMode, err)
	}
	if err := db.QueryRow(`SELECT builtin_tools FROM models WHERE id=?`, model.ID).Scan(&builtinTools); err != nil || builtinTools != `["aivory_web_search"]` {
		t.Fatalf("migrated builtin_tools=%q err=%v", builtinTools, err)
	}
	var officialToolsRaw string
	if err := db.QueryRow(`SELECT official_tools FROM models WHERE id=?`, model.ID).Scan(&officialToolsRaw); err != nil {
		t.Fatal(err)
	}
	officialTools, err := ParseOfficialTools(json.RawMessage(officialToolsRaw))
	if err != nil || len(officialTools) != 1 || officialTools[0].Name != "web_search" {
		t.Fatalf("migrated official_tools=%q parsed=%+v err=%v; provider name must remain official", officialToolsRaw, officialTools, err)
	}
	raw, err := GetSetting(db, "disabled_tools")
	if err != nil {
		t.Fatal(err)
	}
	var disabled []string
	if err := json.Unmarshal(raw, &disabled); err != nil || len(disabled) != 2 || disabled[0] != "aivory_web_search" || disabled[1] != "python_execute" {
		t.Fatalf("migrated disabled_tools=%s decoded=%v err=%v", raw, disabled, err)
	}

	legacyMode := "tool"
	updated, err := UpdateConversation(ctx, db, conversation.ID, "u1", ConversationPatch{RAGMode: &legacyMode})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RAGMode != "auto" {
		t.Fatalf("legacy patch mode=%q", updated.RAGMode)
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
