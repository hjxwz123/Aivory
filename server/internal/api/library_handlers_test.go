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

func TestAdminSkillDisplayDescriptionIsOptional(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "optional-skill-display-description.db"))
	defer db.Close()
	d := Deps{DB: db}

	rec := httptest.NewRecorder()
	createSkillAdmin(d, rec, httptest.NewRequest(http.MethodPost, "/api/admin/skills", strings.NewReader(
		`{"name":"meeting-notes","description":"Use when meeting notes need structure","instructions":"Extract decisions and owners."}`,
	)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created store.Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DisplayDescription != "" {
		t.Fatalf("display_description=%q want empty", created.DisplayDescription)
	}

	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/skills/:id", func(w http.ResponseWriter, r *http.Request) {
		updateSkillAdmin(d, w, r)
	})
	for _, displayDescription := range []string{"Structure meeting notes", ""} {
		rec = httptest.NewRecorder()
		body, err := json.Marshal(map[string]string{"display_description": displayDescription})
		if err != nil {
			t.Fatal(err)
		}
		mx.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/admin/skills/"+created.ID, strings.NewReader(string(body))))
		if rec.Code != http.StatusOK {
			t.Fatalf("update display_description=%q status=%d body=%s", displayDescription, rec.Code, rec.Body.String())
		}
		var updated store.Skill
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatal(err)
		}
		if updated.DisplayDescription != displayDescription {
			t.Fatalf("display_description=%q want=%q", updated.DisplayDescription, displayDescription)
		}
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

func TestLibraryCatalogExposesOnlyExplicitDisplayDescriptionWithoutPrivateContent(t *testing.T) {
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
	_, err = store.CreateSkill(ctx, db, store.Skill{
		Name: "legacy-skill", Description: "LEGACY_TRIGGER_SECRET",
		Icon: "WandSparkles", Instructions: "LEGACY_INSTRUCTION_SECRET", Enabled: true,
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
	for _, secret := range []string{"TRIGGER_SECRET", "LEGACY_TRIGGER_SECRET", "INSTRUCTION_SECRET", "LEGACY_INSTRUCTION_SECRET", "ASSET_SECRET", "PROMPT_CONTENT_SECRET", "storage_path", "instructions", "content"} {
		if strings.Contains(body, secret) {
			t.Fatalf("catalog leaked %q: %s", secret, body)
		}
	}
	var catalog struct {
		Skills []catalogSkill `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 {
		t.Fatalf("catalog skills=%+v", catalog.Skills)
	}
	byName := make(map[string]catalogSkill, len(catalog.Skills))
	for _, skill := range catalog.Skills {
		byName[skill.Name] = skill
	}
	configured := byName["catalog-skill"]
	if configured.DisplayDescription != "Public summary" || configured.Description != "Public summary" || configured.Icon != "Presentation" {
		t.Fatalf("configured catalog skill=%+v", configured)
	}
	legacy := byName["legacy-skill"]
	if legacy.DisplayDescription != "" || legacy.Description != "" || legacy.Icon != "WandSparkles" {
		t.Fatalf("legacy catalog skill=%+v", legacy)
	}
	if !strings.Contains(body, "Prompt summary") {
		t.Fatalf("catalog lost prompt display metadata: %s", body)
	}
}

func TestCatalogSkillCopyUsesDisplayDescriptionThenFallsBackToWhenToUse(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "catalog-skill-icon.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES('u1','u1@example.test','h')`)
	type copiedSource struct {
		source *store.Skill
		copy   store.UserSkill
	}
	copiedSources := make([]copiedSource, 0, 2)
	for _, tc := range []struct {
		name               string
		description        string
		displayDescription string
		wantDisplay        string
	}{
		{
			name: "slide-builder", description: "Use for slide requests", displayDescription: "Build presentation decks",
			wantDisplay: "Build presentation decks",
		},
		{
			name: "legacy-helper", description: "Use for legacy requests",
			wantDisplay: "Use for legacy requests",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := store.CreateSkill(t.Context(), db, store.Skill{
				Name: tc.name, Description: tc.description, DisplayDescription: tc.displayDescription,
				Icon: "Presentation", Instructions: "Create the requested result.", Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			copySkillFromCatalogHandler(Deps{DB: db}, rec, libraryRequest(t, http.MethodPost, "/api/me/skills/from-catalog",
				`{"source_id":"`+source.ID+`"}`, "u1"))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var copied store.UserSkill
			if err := json.Unmarshal(rec.Body.Bytes(), &copied); err != nil {
				t.Fatal(err)
			}
			if copied.Icon != "Presentation" || copied.Description != tc.description || copied.DisplayDescription != tc.wantDisplay || copied.SourceSkillID != source.ID {
				t.Fatalf("copied skill=%+v", copied)
			}
			stored, err := store.GetUserSkill(t.Context(), db, copied.ID, "u1")
			if err != nil || stored.Icon != "Presentation" || stored.Description != tc.description || stored.DisplayDescription != tc.wantDisplay {
				t.Fatalf("stored skill=%+v err=%v", stored, err)
			}
			copiedSources = append(copiedSources, copiedSource{source: source, copy: copied})
		})
	}

	personal, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "u1", Name: "personal-helper", Description: "Use my own helper", Instructions: "Help me.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if personal.DisplayDescription != "" {
		t.Fatalf("personal skill unexpectedly received display description: %+v", personal)
	}

	configuredSource := *copiedSources[0].source
	configuredSource.DisplayDescription = "Updated presentation summary"
	if _, err := store.UpdateSkill(t.Context(), db, configuredSource.ID, configuredSource); err != nil {
		t.Fatal(err)
	}
	legacySource := *copiedSources[1].source
	legacySource.Description = "Use for updated legacy requests"
	if _, err := store.UpdateSkill(t.Context(), db, legacySource.ID, legacySource); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	listMySkillsHandler(Deps{DB: db}, rec, libraryRequest(t, http.MethodGet, "/api/me/skills", "", "u1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed []store.UserSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]store.UserSkill, len(listed))
	for _, skill := range listed {
		byName[skill.Name] = skill
	}
	if got := byName[copiedSources[0].copy.Name]; got.Description != "Use for slide requests" || got.DisplayDescription != "Updated presentation summary" {
		t.Fatalf("configured listed skill=%+v", got)
	}
	if got := byName[copiedSources[1].copy.Name]; got.Description != "Use for legacy requests" || got.DisplayDescription != "Use for updated legacy requests" {
		t.Fatalf("legacy listed skill=%+v", got)
	}
	if got := byName[personal.Name]; got.Description != "Use my own helper" || got.DisplayDescription != "" {
		t.Fatalf("personal listed skill=%+v", got)
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

func TestPrivateLibraryPermissionRevocationDeniesDeleteWithoutDeletingData(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "library-permission-revocation.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u1@example.test','h','user','active')`)

	permissions := store.DefaultUserGroupPermissions()
	permissions.Skills = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone, IDs: []string{}}
	permissions.Prompts = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone, IDs: []string{}}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	permissionsRaw := json.RawMessage(raw)
	group, err := store.CreateUserGroupWithPermissions(
		t.Context(), db, store.UserGroup{ID: "ug-library-revoked", Name: "Library revoked"}, true, &permissionsRaw,
	)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE users SET group_id=? WHERE id='u1'`, group.ID)

	skill, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "u1", Name: "private-skill", Description: "Private", Instructions: "Keep this skill.",
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := store.CreateUserPrompt(t.Context(), db, store.UserPrompt{
		UserID: "u1", Name: "Private prompt", Description: "Private", Content: "Keep this prompt.",
	})
	if err != nil {
		t.Fatal(err)
	}

	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/me/skills/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteMySkillHandler(d, w, r)
	})
	mx.handle(http.MethodDelete, "/api/me/prompts/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteMyPromptHandler(d, w, r)
	})

	for _, tc := range []struct {
		name string
		path string
		code string
	}{
		{name: "skill", path: "/api/me/skills/" + skill.ID, code: errSkillGroupPermission.Error()},
		{name: "prompt", path: "/api/me/prompts/" + prompt.ID, code: errPromptGroupPermission.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mx.ServeHTTP(rec, libraryRequest(t, http.MethodDelete, tc.path, "", "u1"))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	if _, err := store.GetUserSkill(t.Context(), db, skill.ID, "u1"); err != nil {
		t.Fatalf("revocation deleted private skill: %v", err)
	}
	if _, err := store.GetUserPrompt(t.Context(), db, prompt.ID, "u1"); err != nil {
		t.Fatalf("revocation deleted private prompt: %v", err)
	}
}
