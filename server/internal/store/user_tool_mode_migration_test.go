package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMigrateRemovesLegacyUserToolModeSettings(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-tool-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Install the current schema first so the legacy rows exist before the
	// one-time migration runs.
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role,settings) VALUES
		('legacy','legacy@example.test','h','user',?),
		('clean','clean@example.test','h','user',?)`,
		`{"tool_mode_default":"disabled","disable_tools_default":true,"official_tool_names_default":["web_search"],"persona_nickname":"Keep me"}`,
		`{"persona_nickname":"Already clean"}`,
	); err != nil {
		t.Fatalf("users: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var raw string
	if err := db.QueryRow(`SELECT settings FROM users WHERE id='legacy'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	for _, key := range legacyUserToolModeSettingKeys {
		if _, exists := settings[key]; exists {
			t.Fatalf("legacy setting %q was not removed: %s", key, raw)
		}
	}
	if settings["persona_nickname"] != "Keep me" {
		t.Fatalf("unrelated setting was changed: %s", raw)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if _, err := UpdateUserSettings(t.Context(), db, "legacy", map[string]any{
		"tool_mode_default":     "enabled",
		"disable_tools_default": false,
		"theme":                 "dark",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if err := db.QueryRow(`SELECT settings FROM users WHERE id='legacy'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("settings unexpectedly empty")
	}
	settings = map[string]any{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	for _, key := range legacyUserToolModeSettingKeys {
		if _, exists := settings[key]; exists {
			t.Fatalf("legacy setting %q was reintroduced: %s", key, raw)
		}
	}
	if settings["theme"] != "dark" || settings["persona_nickname"] != "Keep me" {
		t.Fatalf("valid settings were not preserved: %s", raw)
	}
}
