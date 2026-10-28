package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/systemprompts"
	"aivory/server/internal/store"
)

func TestSystemPromptCatalogAndAdminBoundary(t *testing.T) {
	entries := map[string]systemprompts.Entry{}
	for _, e := range systemprompts.Catalog() {
		entries[e.ID] = e
		if e.Name == "" || e.Group == "" || e.Engine == "" || e.Format == "" {
			t.Fatalf("incomplete metadata for %s", e.ID)
		}
		for _, variable := range e.Variables {
			if !strings.Contains(e.Default, variable) {
				t.Fatalf("default %s lacks %s", e.ID, variable)
			}
		}
	}
	for _, id := range []string{
		"task.title", "task.tool_route", "decision.tool_scope.instructions", "decision.tool_scope.full_tools", "routing.tool_scope.input",
		"task.router", "decision.documents.instructions", "decision.documents.candidate", "rag.router.rules", "rag.evidence.input", "rag.map_reduce.input",
		"task.memory_extract", "task.memory_adjudicate", "task.memory_dedup", "decision.memory_adjudicate", "decision.memory_duplicate",
		"task.compact", "compaction.summary", "compaction.custom", "task.moderation", "task.moderation.categories", "decision.moderation", "verify",
		"task.research_plan", "task.research_read", "task.research_verify", "task.research_validate", "research.writer.common", "research.writer.no_evidence",
		"task.vision_caption", "task.image_intent", "task.image_prompt", "vision.input", "image.edit", "image.reference",
		"tools.protocol", "tools.protocol.retry", "tools.final.search", "docgen.recipes", "tools.description.python_execute",
	} {
		if _, ok := entries[id]; !ok {
			t.Errorf("missing runtime prompt: %s", id)
		}
	}
	for _, locale := range []string{"en", "zh", "zh-Hant", "fr", "ja"} {
		if entries["conversation.identity."+locale].Locale != locale {
			t.Errorf("missing locale %s", locale)
		}
	}
	t.Logf("catalog contains %d prompts", len(entries))
	db := openMigrated(t, filepath.Join(t.TempDir(), "prompts.db"))
	defer db.Close()
	for _, h := range []handler{adminSystemPromptsGet, adminSystemPromptsSet} {
		rec := httptest.NewRecorder()
		requireAdmin(Deps{DB: db}, h)(rec, httptest.NewRequest(http.MethodGet, "/api/admin/system-prompts", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous prompt access: %d", rec.Code)
		}
	}
}

func TestSystemPromptPatchFallbackValidationAndLegacySetting(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "prompts.db"))
	defer db.Close()
	d := Deps{DB: db}
	patch := func(values map[string]any, status int) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"overrides": values})
		rec := httptest.NewRecorder()
		adminSystemPromptsSet(d, rec, httptest.NewRequest(http.MethodPatch, "/api/admin/system-prompts", strings.NewReader(string(raw))))
		if rec.Code != status {
			t.Fatalf("status=%d expected=%d body=%s", rec.Code, status, rec.Body.String())
		}
	}
	patch(map[string]any{"task.title": "Custom title", "decision.tool_scope.instructions": "Custom decision"}, 200)
	patch(map[string]any{"task.title": "Updated title"}, 200)
	if got := systemprompts.Text(db, "decision.tool_scope.instructions"); got != "Custom decision" {
		t.Fatalf("unrelated override lost: %s", got)
	}
	for _, invalid := range []map[string]any{
		{"unknown.prompt": "x"}, {"task.title": nil}, {"task.title": 3},
		{"task.title": strings.Repeat("x", systemprompts.MaxPromptBytes+1)},
		{"task.title": "must not save", "conversation.identity.zh": "missing model placeholder"},
		{"compaction.custom": strings.Repeat("x", contextCompactionPromptMaxBytes+1)},
	} {
		patch(invalid, 400)
		if systemprompts.Text(db, "task.title") != "Updated title" {
			t.Fatal("invalid patch partially saved")
		}
	}
	patch(map[string]any{"task.title": " \n\t", "compaction.custom": "Preserve all paths"}, 200)
	if got := systemprompts.Load(db)["task.title"]; got != "" {
		t.Fatalf("blank override was persisted: %q", got)
	}
	if got := systemprompts.Text(db, "task.title"); !strings.Contains(got, "navigation title") {
		t.Fatalf("default fallback failed: %s", got)
	}
	raw, _ := store.GetSetting(db, "context_compaction_prompt")
	if string(raw) != `"Preserve all paths"` {
		t.Fatalf("legacy setting not synchronized: %s", raw)
	}
	if err := store.SetSetting(db, "context_compaction_prompt", "Updated on context page"); err != nil {
		t.Fatal(err)
	}
	if systemprompts.Text(db, "compaction.custom") != "Updated on context page" {
		t.Fatal("legacy page edit not visible")
	}
	patch(map[string]any{"compaction.custom": ""}, 200)
	if systemprompts.Text(db, "compaction.custom") != "" {
		t.Fatal("legacy default not restored")
	}
}

func TestSystemPromptSettingsAndBackupValidation(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "prompts.db"))
	defer db.Close()
	_, err := applyAdminSettingsPatch(t.Context(), Deps{DB: db}, map[string]json.RawMessage{
		systemprompts.SettingKey: json.RawMessage(`{"task.title":"Backup title"}`),
	}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if systemprompts.Text(db, "task.title") != "Backup title" {
		t.Fatal("generic settings path ignored override")
	}
	for _, value := range []string{`{"task.title":"ok"}`, `{"unknown":"bad"}`, `{"task.title":false}`} {
		encoded, _ := json.Marshal(value)
		row := map[string]json.RawMessage{"value": encoded}
		err := normalizeCompactionArchiveSetting(row, systemprompts.SettingKey)
		if (err == nil) != strings.Contains(value, `"ok"`) {
			t.Fatalf("backup validation %s: %v", value, err)
		}
	}
}
