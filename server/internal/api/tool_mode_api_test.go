package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func toolModeTestRequest(t *testing.T, method, target, body string, user *store.User) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), userCtxKey{}, user)
	return req.WithContext(ctx)
}

func TestPostMessageRejectsInvalidExplicitToolModeBeforeStreaming(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tool-mode-message.db"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','tool@example.com','h','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := store.CreateConversation(context.Background(), db, store.Conversation{ID: "c1", UserID: "u1", Title: "test"}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	req := toolModeTestRequest(t, http.MethodPost, "/api/conversations/c1/messages", `{"text":"hello","tool_mode":"sometimes"}`, &store.User{ID: "u1", Role: "admin"})
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	postMessageHandler(Deps{DB: db}, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tool_mode must be one of") {
		t.Fatalf("response does not explain invalid tool mode: %s", rec.Body.String())
	}
}

func TestUpdateMeSettingsValidatesToolModeDefault(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tool-mode-settings.db"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','settings@example.com','h','user')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	user := &store.User{ID: "u1", Role: "user"}

	rec := httptest.NewRecorder()
	updateMeSettingsHandler(Deps{DB: db}, rec, toolModeTestRequest(t, http.MethodPatch, "/api/me/settings", `{"tool_mode_default":"official","official_tool_names_default":["web_search","image_generation","web_search"]}`, user))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var settings map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings["tool_mode_default"] != "official" {
		t.Fatalf("saved default = %#v, want official", settings["tool_mode_default"])
	}
	if settings["disable_tools_default"] != false {
		t.Fatalf("legacy default = %#v, want false for official fallback", settings["disable_tools_default"])
	}
	names, ok := settings["official_tool_names_default"].([]any)
	if !ok || len(names) != 2 || names[0] != "web_search" || names[1] != "image_generation" {
		t.Fatalf("saved official defaults = %#v, want deduplicated ordered names", settings["official_tool_names_default"])
	}

	for _, body := range []string{
		`{"tool_mode_default":"sometimes"}`,
		`{"tool_mode_default":true}`,
		`{"official_tool_names_default":"web_search"}`,
		`{"official_tool_names_default":[""]}`,
	} {
		t.Run(body, func(t *testing.T) {
			rec := httptest.NewRecorder()
			updateMeSettingsHandler(Deps{DB: db}, rec, toolModeTestRequest(t, http.MethodPatch, "/api/me/settings", body, user))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNormalizeToolModeSettingsPatchKeepsLegacyClientsCoherent(t *testing.T) {
	cases := []struct {
		name       string
		patch      map[string]any
		wantMode   string
		wantLegacy bool
		wantErr    bool
	}{
		{name: "new auto", patch: map[string]any{"tool_mode_default": "auto"}, wantMode: "auto", wantLegacy: false},
		{name: "new disabled", patch: map[string]any{"tool_mode_default": "disabled"}, wantMode: "disabled", wantLegacy: true},
		{name: "new enabled", patch: map[string]any{"tool_mode_default": "enabled"}, wantMode: "enabled", wantLegacy: false},
		{name: "new official", patch: map[string]any{"tool_mode_default": "official"}, wantMode: "official", wantLegacy: false},
		{name: "new wins over contradictory legacy", patch: map[string]any{"tool_mode_default": "auto", "disable_tools_default": true}, wantMode: "auto", wantLegacy: false},
		{name: "legacy true promotes", patch: map[string]any{"disable_tools_default": true}, wantMode: "disabled", wantLegacy: true},
		{name: "legacy false promotes", patch: map[string]any{"disable_tools_default": false}, wantMode: "enabled", wantLegacy: false},
		{name: "legacy invalid", patch: map[string]any{"disable_tools_default": "yes"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := normalizeToolModeSettingsPatch(tc.patch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.patch["tool_mode_default"] != tc.wantMode || tc.patch["disable_tools_default"] != tc.wantLegacy {
				t.Fatalf("normalized patch = %#v, want mode=%q legacy=%v", tc.patch, tc.wantMode, tc.wantLegacy)
			}
		})
	}
}
