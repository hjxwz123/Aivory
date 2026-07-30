package tools

import (
	"context"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestSaveMemoryToolRespectsGlobalAndUserSettings(t *testing.T) {
	ctx := context.Background()
	db := openToolsTestDB(t)
	t.Cleanup(store.InvalidateConfig)
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role,settings) VALUES('u1','memory@example.com','h','user','{}')`); err != nil {
		t.Fatal(err)
	}
	tool := &saveMemoryTool{db: db}
	toolContext := &llm.ToolContext{DB: db, UserID: "u1"}
	input := []byte(`{"memory_text":"Prefers concise answers","slot":"response_style","value":"concise"}`)

	if err := store.SetSetting(db, "memory_enabled", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tool.Execute(ctx, input, toolContext); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Fatalf("global memory disable error = %v", err)
	}

	if err := store.SetSetting(db, "memory_enabled", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserSettings(ctx, db, "u1", map[string]any{"memory_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tool.Execute(ctx, input, toolContext); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Fatalf("user memory disable error = %v", err)
	}

	memories, err := store.ListMemories(ctx, db, "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 0 {
		t.Fatalf("disabled memory tool wrote rows: %+v", memories)
	}
}
