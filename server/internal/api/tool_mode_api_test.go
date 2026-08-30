package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
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

func TestUpdateMeSettingsIgnoresUserGlobalToolMode(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tool-mode-settings.db"))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','settings@example.com','h','user')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	user := &store.User{ID: "u1", Role: "user"}

	rec := httptest.NewRecorder()
	updateMeSettingsHandler(Deps{DB: db}, rec, toolModeTestRequest(t, http.MethodPatch, "/api/me/settings", `{"tool_mode_default":"disabled","disable_tools_default":true,"persona_nickname":"kept"}`, user))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var settings map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings["persona_nickname"] != "kept" {
		t.Fatalf("allowed setting was not saved: %#v", settings)
	}
	if _, exists := settings["tool_mode_default"]; exists {
		t.Fatalf("user global tool mode was persisted: %#v", settings)
	}
	if _, exists := settings["disable_tools_default"]; exists {
		t.Fatalf("legacy user global tool mode was persisted: %#v", settings)
	}
}

func TestEffectiveDefaultToolModeUsesDeploymentOnly(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "tool-mode-default.db"))
	defer db.Close()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	if got := effectiveDefaultToolMode(db); got != llm.ToolModeAuto {
		t.Fatalf("missing deployment default = %q, want auto", got)
	}
	if err := store.SetSetting(db, "tool_mode_default", llm.ToolModeDisabled); err != nil {
		t.Fatal(err)
	}
	if got := effectiveDefaultToolMode(db); got != llm.ToolModeDisabled {
		t.Fatalf("deployment default = %q, want disabled", got)
	}
}
