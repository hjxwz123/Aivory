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

func libraryRequest(t *testing.T, method, path, body, userID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"}))
}

func TestCreatePromptAdminPreservesExplicitDisabledAndDefaultsOmittedEnabled(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "prompt-enabled.db"))
	defer db.Close()
	d := Deps{DB: db}

	for _, tc := range []struct {
		name        string
		enabledJSON string
		wantEnabled bool
	}{
		{name: "omitted", wantEnabled: true},
		{name: "explicit-false", enabledJSON: `,"enabled":false`, wantEnabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			body := `{"name":"` + tc.name + `","description":"Shown","content":"Template"` + tc.enabledJSON + `}`
			createPromptAdmin(d, rec, httptest.NewRequest(http.MethodPost, "/api/admin/prompts", strings.NewReader(body)))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var prompt store.Prompt
			if err := json.Unmarshal(rec.Body.Bytes(), &prompt); err != nil {
				t.Fatal(err)
			}
			if prompt.Enabled != tc.wantEnabled {
				t.Fatalf("enabled=%v want=%v", prompt.Enabled, tc.wantEnabled)
			}
		})
	}
}

func TestPrivateSkillRejectsFileAndPathFieldsRegardlessOfCaseOrNesting(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "private-skill-fields.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h')`)
	d := Deps{DB: db}

	cases := []string{
		`{"name":"safe-skill","description":"Safe","instructions":"Do work","assets":[]}`,
		`{"name":"safe-skill","description":"Safe","instructions":"Do work","ASSETS":[]}`,
		`{"name":"safe-skill","description":"Safe","instructions":"Do work","storagePath":"/tmp/x"}`,
		`{"name":"safe-skill","description":"Safe","instructions":{"text":"Do work","FiLeS":[]}}`,
		`{"name":"safe-skill","description":"Safe","instructions":"Do work","metadata":{"attachments":[]}}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills", body, "u1"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills",
		`{"name":"safe-skill","description":"Safe","instructions":"Do work"}`, "u1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLibraryCatalogNeverExposesSkillTriggerInstructionsAssetsOrPromptContent(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "catalog-redaction.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h')`)
	ctx := context.Background()
	_, err := store.CreateSkill(ctx, db, store.Skill{
		Name: "catalog-skill", Description: "TRIGGER_SECRET", DisplayDescription: "Public summary",
		Icon: "Presentation", Instructions: "INSTRUCTION_SECRET", Assets: json.RawMessage(`[{"storage_path":"ASSET_SECRET"}]`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrompt(ctx, db, store.Prompt{
		Name: "Catalog prompt", Description: "Prompt summary", Content: "PROMPT_CONTENT_SECRET", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	listLibraryCatalogHandler(Deps{DB: db}, rec, libraryRequest(t, http.MethodGet, "/api/library/catalog", "", "u1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"TRIGGER_SECRET", "INSTRUCTION_SECRET", "ASSET_SECRET", "PROMPT_CONTENT_SECRET", "storage_path", "instructions", "content"} {
		if strings.Contains(body, secret) {
			t.Fatalf("catalog leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "Public summary") || !strings.Contains(body, "Prompt summary") {
		t.Fatalf("catalog lost safe display metadata: %s", body)
	}
	var catalog struct {
		Skills []catalogSkill `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Icon != "Presentation" {
		t.Fatalf("catalog skill icon=%+v", catalog.Skills)
	}
}

func TestCatalogSkillCopyPreservesAdministratorIcon(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "catalog-skill-icon.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h')`)
	source, err := store.CreateSkill(t.Context(), db, store.Skill{
		Name: "slide-builder", Description: "Use for slide requests", DisplayDescription: "Build presentation decks",
		Icon: "Presentation", Instructions: "Create the requested deck.", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	copySkillFromCatalogHandler(Deps{DB: db}, rec, libraryRequest(t, http.MethodPost, "/api/me/skills/catalog",
		`{"source_id":"`+source.ID+`"}`, "u1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var copied store.UserSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	if copied.Icon != "Presentation" || copied.Description != "Build presentation decks" || copied.SourceSkillID != source.ID {
		t.Fatalf("copied skill=%+v", copied)
	}
	stored, err := store.GetUserSkill(t.Context(), db, copied.ID, "u1")
	if err != nil || stored.Icon != "Presentation" {
		t.Fatalf("stored skill=%+v err=%v", stored, err)
	}
}

func TestPrivateLibraryMutationCannotCrossUserBoundary(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "library-isolation.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h'),('u2','u2@example.test','h')`)
	skill, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "u1", Name: "owner-skill", Description: "Owner", Instructions: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/me/skills/:id", func(w http.ResponseWriter, r *http.Request) { updateMySkillHandler(d, w, r) })
	mx.handle(http.MethodDelete, "/api/me/skills/:id", func(w http.ResponseWriter, r *http.Request) { deleteMySkillHandler(d, w, r) })

	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, libraryRequest(t, http.MethodPatch, "/api/me/skills/"+skill.ID,
		`{"instructions":"stolen"}`, "u2"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, libraryRequest(t, http.MethodDelete, "/api/me/skills/"+skill.ID, "", "u2"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := store.GetUserSkill(t.Context(), db, skill.ID, "u1")
	if err != nil || stored.Instructions != "private" {
		t.Fatalf("owner row changed: %+v err=%v", stored, err)
	}
}
