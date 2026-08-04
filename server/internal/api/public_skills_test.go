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

func TestPublicSkillsExposeOnlyDisplayMetadata(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "public-skills-redaction.db"))
	defer db.Close()

	created, err := store.CreateSkill(context.Background(), db, store.Skill{
		Name:               "report-builder",
		Description:        "TRIGGER_SECRET",
		DisplayDescription: "  Build polished reports  ",
		Icon:               "FileChartColumn",
		Instructions:       "INSTRUCTION_SECRET",
		Assets:             json.RawMessage(`[{"filename":"template.md","storage_path":"ASSET_PATH_SECRET"}]`),
		Enabled:            true,
		SortOrder:          7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSkill(context.Background(), db, store.Skill{
		Name: "legacy-skill", Description: "LEGACY_TRIGGER_SECRET", Instructions: "LEGACY_INSTRUCTION_SECRET",
		Enabled: true, SortOrder: 8,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSkill(context.Background(), db, store.Skill{
		Name: "disabled-skill", Description: "DISABLED_TRIGGER_SECRET", Instructions: "DISABLED_INSTRUCTION_SECRET",
		Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	listSkillsPublicHandler(Deps{DB: db}, rec, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, secret := range []string{
		"TRIGGER_SECRET", "INSTRUCTION_SECRET", "ASSET_PATH_SECRET",
		"LEGACY_TRIGGER_SECRET", "LEGACY_INSTRUCTION_SECRET",
		"DISABLED_TRIGGER_SECRET", "DISABLED_INSTRUCTION_SECRET", "storage_path",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("public skills leaked %q: %s", secret, body)
		}
	}

	var rawRows []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawRows); err != nil {
		t.Fatal(err)
	}
	if len(rawRows) != 2 {
		t.Fatalf("rows=%s want two enabled skills", body)
	}
	allowed := map[string]bool{
		"id": true, "name": true, "display_description": true,
		"icon": true, "enabled": true, "sort_order": true,
	}
	for _, row := range rawRows {
		for key := range row {
			if !allowed[key] {
				t.Fatalf("public skills exposed unexpected field %q: %s", key, body)
			}
		}
		for key := range allowed {
			if _, ok := row[key]; !ok {
				t.Fatalf("public skills omitted display field %q: %s", key, body)
			}
		}
	}

	var skills []publicSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &skills); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]publicSkill, len(skills))
	for _, skill := range skills {
		byName[skill.Name] = skill
	}
	got := byName["report-builder"]
	if got.ID != created.ID || got.DisplayDescription != "Build polished reports" || got.Icon != "FileChartColumn" ||
		!got.Enabled || got.SortOrder != 7 {
		t.Fatalf("public display metadata=%+v", got)
	}
	if legacy := byName["legacy-skill"]; legacy.DisplayDescription != "" {
		t.Fatalf("legacy skill exposed a derived description: %+v", legacy)
	}
}
