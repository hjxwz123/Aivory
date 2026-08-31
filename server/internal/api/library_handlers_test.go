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

func libraryRequestWithPath(t *testing.T, method, path, body, userID, resourceID string) *http.Request {
	t.Helper()
	req := libraryRequest(t, method, path, body, userID)
	return req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": resourceID}))
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

func TestWorkspaceLibraryAllowsMemberWritesAndGuestReadsOnly(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-library-rbac.db"))
	defer db.Close()
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash) VALUES
			('u1','u1@example.test','h'),('u2','u2@example.test','h'),
			('u3','u3@example.test','h'),('u4','u4@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws1','Workspace one','u1','token-ws1');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws1','u1','admin'),('ws1','u2','member'),('ws1','u3','guest')
	`)
	d := Deps{DB: db}

	rec := httptest.NewRecorder()
	createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills",
		`{"name":"shared-skill","description":"Shared","instructions":"Use this skill.","workspace_id":"ws1"}`, "u2"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("member create status=%d body=%s", rec.Code, rec.Body.String())
	}
	mustExec(t, db, `UPDATE workspace_members
		SET can_create_skills_prompts=0, can_create_prompts=0, can_create_skills=0, can_create_mcp=0
		WHERE workspace_id='ws1' AND user_id='u2'`)

	rec = httptest.NewRecorder()
	createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills",
		`{"name":"revoked-skill","description":"Revoked","instructions":"Should fail.","workspace_id":"ws1"}`, "u2"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoked member create status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	listMySkillsHandler(d, rec, libraryRequest(t, http.MethodGet, "/api/me/skills?workspace_id=ws1", "", "u2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoked member list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var revokedList []store.UserSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &revokedList); err != nil {
		t.Fatal(err)
	}
	if len(revokedList) != 1 || !revokedList[0].CanManage {
		t.Fatalf("revoked member listed=%+v", revokedList)
	}
	// Creation rights are independent from management of an existing resource:
	// the creator can still edit/delete their own workspace row after the
	// administrator revokes new-resource creation.
	rec = httptest.NewRecorder()
	updateMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/skills/"+revokedList[0].ID+"?workspace_id=ws1",
		`{"instructions":"Updated after revoke."}`, "u2", revokedList[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoked member update status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	listMySkillsHandler(d, rec, libraryRequest(t, http.MethodGet, "/api/me/skills?workspace_id=ws1", "", "u3"))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed []store.UserSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].CanManage {
		t.Fatalf("guest listed=%+v", listed)
	}

	rec = httptest.NewRecorder()
	createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills",
		`{"name":"guest-skill","description":"Guest","instructions":"Should fail.","workspace_id":"ws1"}`, "u3"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest create status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	listMySkillsHandler(d, rec, libraryRequest(t, http.MethodGet, "/api/me/skills?workspace_id=ws1", "", "u4"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outsider list status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	deleteMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodDelete,
		"/api/me/skills/"+revokedList[0].ID+"?workspace_id=ws1", "", "u2", revokedList[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoked member delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	listMySkillsHandler(d, rec, libraryRequest(t, http.MethodGet, "/api/me/skills?workspace_id=ws1", "", "u3"))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest list after delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var afterDelete []store.UserSkill
	if err := json.Unmarshal(rec.Body.Bytes(), &afterDelete); err != nil {
		t.Fatal(err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("guest still sees deleted skill: %+v", afterDelete)
	}
}

func TestWorkspaceLibraryRedactsContentWithoutUsePermissionUnlessManageable(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-library-use-redaction.db"))
	defer db.Close()
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash) VALUES
			('owner','owner@example.test','h'),('admin','admin@example.test','h'),
			('creator','creator@example.test','h'),('viewer','viewer@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES
			('ws-redact','Redaction workspace','owner','token-redact');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws-redact','owner','admin'),('ws-redact','admin','admin'),
			('ws-redact','creator','member'),('ws-redact','viewer','member');
		UPDATE workspace_members
		   SET can_use_skills=0, can_use_prompts=0
		 WHERE workspace_id='ws-redact' AND user_id IN ('creator','viewer')
	`)

	skill, err := store.CreateUserSkill(t.Context(), db, store.UserSkill{
		UserID: "creator", WorkspaceID: "ws-redact", Name: "shared-skill",
		Description: "Visible skill metadata", Instructions: "SKILL_BODY_SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := store.CreateUserPrompt(t.Context(), db, store.UserPrompt{
		UserID: "creator", WorkspaceID: "ws-redact", Name: "Shared prompt",
		Description: "Visible prompt metadata", Content: "PROMPT_BODY_SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}

	for _, tc := range []struct {
		userID      string
		wantManage  bool
		wantContent bool
	}{
		{userID: "viewer", wantManage: false, wantContent: false},
		{userID: "creator", wantManage: true, wantContent: true},
		{userID: "admin", wantManage: true, wantContent: true},
		{userID: "owner", wantManage: true, wantContent: true},
	} {
		t.Run(tc.userID, func(t *testing.T) {
			rec := httptest.NewRecorder()
			listMySkillsHandler(d, rec, libraryRequest(t, http.MethodGet,
				"/api/me/skills?workspace_id=ws-redact", "", tc.userID))
			if rec.Code != http.StatusOK {
				t.Fatalf("skill list status=%d body=%s", rec.Code, rec.Body.String())
			}
			var skills []store.UserSkill
			if err := json.Unmarshal(rec.Body.Bytes(), &skills); err != nil {
				t.Fatal(err)
			}
			if len(skills) != 1 || skills[0].ID != skill.ID || skills[0].CanManage != tc.wantManage {
				t.Fatalf("skills=%+v", skills)
			}
			if skills[0].Description != "Visible skill metadata" {
				t.Fatalf("skill metadata was removed: %+v", skills[0])
			}
			if gotContent := skills[0].Instructions != ""; gotContent != tc.wantContent {
				t.Fatalf("skill instructions=%q wantContent=%v", skills[0].Instructions, tc.wantContent)
			}
			if tc.wantContent && skills[0].Instructions != "SKILL_BODY_SECRET" {
				t.Fatalf("skill instructions=%q", skills[0].Instructions)
			}

			rec = httptest.NewRecorder()
			listMyPromptsHandler(d, rec, libraryRequest(t, http.MethodGet,
				"/api/me/prompts?workspace_id=ws-redact", "", tc.userID))
			if rec.Code != http.StatusOK {
				t.Fatalf("prompt list status=%d body=%s", rec.Code, rec.Body.String())
			}
			var prompts []store.UserPrompt
			if err := json.Unmarshal(rec.Body.Bytes(), &prompts); err != nil {
				t.Fatal(err)
			}
			if len(prompts) != 1 || prompts[0].ID != prompt.ID || prompts[0].CanManage != tc.wantManage {
				t.Fatalf("prompts=%+v", prompts)
			}
			if prompts[0].Description != "Visible prompt metadata" {
				t.Fatalf("prompt metadata was removed: %+v", prompts[0])
			}
			if gotContent := prompts[0].Content != ""; gotContent != tc.wantContent {
				t.Fatalf("prompt content=%q wantContent=%v", prompts[0].Content, tc.wantContent)
			}
			if tc.wantContent && prompts[0].Content != "PROMPT_BODY_SECRET" {
				t.Fatalf("prompt content=%q", prompts[0].Content)
			}
		})
	}

	// Revoking use does not strand resources the member created earlier.
	rec := httptest.NewRecorder()
	updateMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/skills/"+skill.ID+"?workspace_id=ws-redact",
		`{"instructions":"Updated skill body."}`, "creator", skill.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("creator skill update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	updateMyPromptHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/prompts/"+prompt.ID+"?workspace_id=ws-redact",
		`{"content":"Updated prompt body."}`, "creator", prompt.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("creator prompt update status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWorkspaceCatalogCopyRequiresUsePermission(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-catalog-copy-use.db"))
	defer db.Close()
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash) VALUES
			('owner','owner@example.test','h'),('member','member@example.test','h');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES
			('ws-copy','Copy workspace','owner','token-copy');
		INSERT INTO workspace_members(workspace_id,user_id,role,can_use_skills,can_use_prompts)
			VALUES ('ws-copy','member','member',0,0)
	`)
	sourceSkill, err := store.CreateSkill(t.Context(), db, store.Skill{
		Name: "catalog-skill", Description: "Catalog skill", Instructions: "CATALOG_SKILL_SECRET", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourcePrompt, err := store.CreatePrompt(t.Context(), db, store.Prompt{
		Name: "Catalog prompt", Description: "Catalog prompt", Content: "CATALOG_PROMPT_SECRET", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db}

	for _, tc := range []struct {
		name string
		path string
		body string
		call func(*httptest.ResponseRecorder, *http.Request)
	}{
		{
			name: "skill", path: "/api/me/skills/from-catalog",
			body: `{"source_id":"` + sourceSkill.ID + `","workspace_id":"ws-copy"}`,
			call: func(rec *httptest.ResponseRecorder, req *http.Request) { copySkillFromCatalogHandler(d, rec, req) },
		},
		{
			name: "prompt", path: "/api/me/prompts/from-catalog",
			body: `{"source_id":"` + sourcePrompt.ID + `","workspace_id":"ws-copy"}`,
			call: func(rec *httptest.ResponseRecorder, req *http.Request) { copyPromptFromCatalogHandler(d, rec, req) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, libraryRequest(t, http.MethodPost, tc.path, tc.body, "member"))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "CATALOG_") {
				t.Fatalf("copy denial leaked catalog content: %s", rec.Body.String())
			}
		})
	}

	for _, table := range []string{"user_skills", "user_prompts"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE workspace_id=?", "ws-copy").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d want=0", table, count)
		}
	}
}

func TestWorkspaceLibraryIgnoresPersonalCatalogPolicyButKeepsItForPersonalRows(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-library-catalog-scope.db"))
	defer db.Close()

	permissions := store.DefaultUserGroupPermissions()
	permissions.Skills = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone, IDs: []string{}}
	permissions.Prompts = store.ResourceAccessPolicy{Mode: store.ResourceAccessNone, IDs: []string{}}
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	permissionsRaw := json.RawMessage(raw)
	if _, err := store.CreateUserGroupWithPermissions(
		t.Context(), db, store.UserGroup{ID: "ug-library-scope", Name: "No catalog"}, true, &permissionsRaw,
	); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES
			('owner','owner@example.test','h','user','active','ug-library-scope'),
			('member','member@example.test','h','user','active','ug-library-scope'),
			('other','other@example.test','h','user','active','ug-library-scope');
		INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES ('ws-scope','Scoped workspace','owner','token-scope');
		INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
			('ws-scope','owner','admin'),('ws-scope','member','member'),('ws-scope','other','member')
	`)
	d := Deps{DB: db}

	// Group-level catalog restrictions do not prevent a member from creating a
	// workspace-owned prompt/skill, nor from seeing the shared rows.
	for _, tc := range []struct {
		name string
		path string
		body string
		call func(*httptest.ResponseRecorder, *http.Request)
	}{
		{
			name: "skill", path: "/api/me/skills",
			body: `{"name":"workspace-skill","description":"Shared skill","instructions":"Use it."}`,
			call: func(rec *httptest.ResponseRecorder, req *http.Request) { createMySkillHandler(d, rec, req) },
		},
		{
			name: "prompt", path: "/api/me/prompts",
			body: `{"name":"Workspace prompt","description":"Shared prompt","content":"Use it."}`,
			call: func(rec *httptest.ResponseRecorder, req *http.Request) { createMyPromptHandler(d, rec, req) },
		},
	} {
		t.Run(tc.name+" workspace create", func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, libraryRequest(t, http.MethodPost, tc.path, withWorkspaceID(tc.body, "ws-scope"), "member"))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	workspaceSkills, err := store.ListUserSkillsScoped(t.Context(), db, "member", "ws-scope")
	if err != nil || len(workspaceSkills) != 1 {
		t.Fatalf("workspace skills=%+v err=%v", workspaceSkills, err)
	}
	// The personal catalog policy is intentionally restrictive in this fixture,
	// but a shared workspace skill is governed by the workspace/member gates
	// instead. This is the same selection path used by send/regenerate handlers.
	selected, normalized, err := resolvePermittedUserSkillSelection(
		t.Context(), db, "member", "ws-scope", []string{workspaceSkills[0].ID}, true, permissions.Skills,
	)
	if err != nil || len(selected) != 1 || len(normalized) != 1 || normalized[0] != workspaceSkills[0].ID {
		t.Fatalf("workspace skill selection was filtered by personal policy: skills=%+v ids=%v err=%v", selected, normalized, err)
	}
	workspacePrompts, err := store.ListUserPromptsScoped(t.Context(), db, "member", "ws-scope")
	if err != nil || len(workspacePrompts) != 1 {
		t.Fatalf("workspace prompts=%+v err=%v", workspacePrompts, err)
	}

	for _, tc := range []struct {
		name string
		path string
		call func(*httptest.ResponseRecorder, *http.Request)
	}{
		{name: "skill", path: "/api/me/skills?workspace_id=ws-scope", call: func(rec *httptest.ResponseRecorder, req *http.Request) { listMySkillsHandler(d, rec, req) }},
		{name: "prompt", path: "/api/me/prompts?workspace_id=ws-scope", call: func(rec *httptest.ResponseRecorder, req *http.Request) { listMyPromptsHandler(d, rec, req) }},
	} {
		t.Run(tc.name+" workspace list", func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, libraryRequest(t, http.MethodGet, tc.path, "", "other"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%v", rows)
			}
		})
	}

	// The same catalog restriction does not block the creator's workspace
	// update/delete operations, while another ordinary member cannot manage the
	// row merely because they can read it.
	rec := httptest.NewRecorder()
	updateMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/skills/"+workspaceSkills[0].ID+"?workspace_id=ws-scope",
		`{"instructions":"Updated shared skill."}`, "member", workspaceSkills[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace skill update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	updateMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/skills/"+workspaceSkills[0].ID+"?workspace_id=ws-scope",
		`{"instructions":"Should be denied."}`, "other", workspaceSkills[0].ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner workspace skill update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	updateMyPromptHandler(d, rec, libraryRequestWithPath(t, http.MethodPatch,
		"/api/me/prompts/"+workspacePrompts[0].ID+"?workspace_id=ws-scope",
		`{"content":"Updated shared prompt."}`, "member", workspacePrompts[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace prompt update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	deleteMySkillHandler(d, rec, libraryRequestWithPath(t, http.MethodDelete,
		"/api/me/skills/"+workspaceSkills[0].ID+"?workspace_id=ws-scope", "", "member", workspaceSkills[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace skill delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	deleteMyPromptHandler(d, rec, libraryRequestWithPath(t, http.MethodDelete,
		"/api/me/prompts/"+workspacePrompts[0].ID+"?workspace_id=ws-scope", "", "member", workspacePrompts[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace prompt delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Personal rows retain the old group policy and cannot be created by this
	// group, even though the same member can create workspace rows.
	personalSkill := httptest.NewRecorder()
	createMySkillHandler(d, personalSkill, libraryRequest(t, http.MethodPost, "/api/me/skills",
		`{"name":"personal-skill","description":"Private","instructions":"Nope."}`, "member"))
	if personalSkill.Code != http.StatusForbidden || !strings.Contains(personalSkill.Body.String(), errSkillGroupPermission.Error()) {
		t.Fatalf("personal skill status=%d body=%s", personalSkill.Code, personalSkill.Body.String())
	}
	personalPrompt := httptest.NewRecorder()
	createMyPromptHandler(d, personalPrompt, libraryRequest(t, http.MethodPost, "/api/me/prompts",
		`{"name":"Personal prompt","description":"Private","content":"Nope."}`, "member"))
	if personalPrompt.Code != http.StatusForbidden || !strings.Contains(personalPrompt.Body.String(), errPromptGroupPermission.Error()) {
		t.Fatalf("personal prompt status=%d body=%s", personalPrompt.Code, personalPrompt.Body.String())
	}

	// A granular creation denial still applies to workspace resources and is
	// enforced before the store write.
	mustExec(t, db, `UPDATE workspace_members SET can_create_skills=0 WHERE workspace_id='ws-scope' AND user_id='member'`)
	rec = httptest.NewRecorder()
	createMySkillHandler(d, rec, libraryRequest(t, http.MethodPost, "/api/me/skills",
		withWorkspaceID(`{"name":"blocked-skill","description":"Blocked","instructions":"Nope."}`, "ws-scope"), "member"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("granular denial status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func withWorkspaceID(body, workspaceID string) string {
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return body
	}
	fields["workspace_id"] = workspaceID
	out, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return string(out)
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
