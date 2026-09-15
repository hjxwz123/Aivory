package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func setIntersectionGroup(t *testing.T, d Deps, userID string, p store.UserGroupPermissions) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d.DB, `INSERT INTO user_groups(id,name,permissions) VALUES('intersection','Intersection',?)
		ON CONFLICT(id) DO UPDATE SET permissions=excluded.permissions`, string(raw))
	mustExec(t, d.DB, `UPDATE users SET group_id='intersection' WHERE id=?`, userID)
}

func TestLibraryFeatureSwitchesIntersectWorkspaceAndGroup(t *testing.T) {
	owner, member, d, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	for _, tc := range []struct {
		name             string
		group, workspace bool
		role             string
		want             int
	}{
		{"both allow", true, true, "user", http.StatusOK},
		{"group denies workspace owner", false, true, "user", http.StatusForbidden},
		{"workspace denies owner", true, false, "user", http.StatusForbidden},
		{"site admin bypasses group", false, true, "admin", http.StatusOK},
		{"site admin obeys workspace", true, false, "admin", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := store.DefaultUserGroupPermissions()
			p.AllowSkills, p.AllowPrompts = tc.group, tc.group
			setIntersectionGroup(t, d, owner.ID, p)
			mustExec(t, d.DB, `UPDATE users SET role=? WHERE id=?`, tc.role, owner.ID)
			actor := *owner
			actor.Role = tc.role
			if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspaceID, owner.ID, store.WorkspacePolicyPatch{
				AllowSkills: &tc.workspace, AllowPrompts: &tc.workspace,
			}); err != nil {
				t.Fatal(err)
			}
			for _, h := range []handler{listMySkillsHandler, listMyPromptsHandler} {
				rec := httptest.NewRecorder()
				h(d, rec, userGroupPermissionRequest(http.MethodGet, "/?workspace_id="+workspaceID, &actor, nil, ""))
				if rec.Code != tc.want {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
				}
			}
		})
	}
	// An ordinary resource creator cannot escape a group-wide shutdown either.
	p := store.DefaultUserGroupPermissions()
	p.AllowSkills, p.AllowPrompts = false, false
	setIntersectionGroup(t, d, member.ID, p)
	enabled := true
	if _, err := store.UpdateWorkspacePolicy(t.Context(), d.DB, workspaceID, owner.ID, store.WorkspacePolicyPatch{
		AllowSkills: &enabled, AllowPrompts: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	skill, err := store.CreateUserSkill(t.Context(), d.DB, store.UserSkill{
		UserID: member.ID, WorkspaceID: workspaceID, Name: "group-test", Description: "Test", Instructions: "secret instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := store.CreateUserPrompt(t.Context(), d.DB, store.UserPrompt{
		UserID: member.ID, WorkspaceID: workspaceID, Name: "Group test", Description: "Test", Content: "secret prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		h        handler
		id, body string
	}{
		{createMySkillHandler, "", `{"name":"new-skill","description":"Test","instructions":"secret","workspace_id":"` + workspaceID + `"}`},
		{createMyPromptHandler, "", `{"name":"New prompt","description":"Test","content":"secret","workspace_id":"` + workspaceID + `"}`},
		{updateMySkillHandler, skill.ID, `{"description":"updated"}`},
		{updateMyPromptHandler, prompt.ID, `{"description":"updated"}`},
		{deleteMySkillHandler, skill.ID, ""},
		{deleteMyPromptHandler, prompt.ID, ""},
	} {
		rec := httptest.NewRecorder()
		tc.h(d, rec, userGroupPermissionRequest(http.MethodPost, "/?workspace_id="+workspaceID, member, map[string]string{"id": tc.id}, tc.body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("group denied mutation status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if _, _, err := resolvePermittedUserSkillSelection(t.Context(), d.DB, member.ID, workspaceID, []string{skill.ID}, true, p.Skills); !errors.Is(err, errSkillGroupPermission) {
		t.Fatalf("selected workspace skill escaped feature denial: %v", err)
	}
	if _, _, err := resolvePermittedUserSkillSelection(t.Context(), d.DB, member.ID, workspaceID, nil, true, p.Skills); err != nil {
		t.Fatalf("ordinary chat should remain usable: %v", err)
	}
	for _, scope := range []string{"", workspaceID} {
		rec := httptest.NewRecorder()
		listLibraryCatalogHandler(d, rec, userGroupPermissionRequest(http.MethodGet, "/?workspace_id="+scope, member, nil, ""))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"skills":[]`) || !strings.Contains(rec.Body.String(), `"prompts":[]`) {
			t.Fatalf("disabled catalog response: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestWorkspaceDeletionHasIndependentGroupPermission(t *testing.T) {
	owner, member, d, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	d.Cache = cache.NewMemory()
	p := store.DefaultUserGroupPermissions()
	p.AllowWorkspaceDeletion = false
	setIntersectionGroup(t, d, owner.ID, p)
	rec := httptest.NewRecorder()
	deleteWorkspaceHandler(d, rec, userGroupPermissionRequest(http.MethodDelete, "/", owner, map[string]string{"id": workspaceID}, ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetWorkspaceForMember(t.Context(), d.DB, workspaceID, owner.ID); err != nil {
		t.Fatalf("denied delete changed workspace: %v", err)
	}
	p.AllowWorkspaceDeletion, p.AllowConversationDeletion = true, false
	setIntersectionGroup(t, d, owner.ID, p)
	rec = httptest.NewRecorder()
	deleteWorkspaceHandler(d, rec, userGroupPermissionRequest(http.MethodDelete, "/", member, map[string]string{"id": workspaceID}, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-owner deletion status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	deleteWorkspaceHandler(d, rec, userGroupPermissionRequest(http.MethodDelete, "/", owner, map[string]string{"id": workspaceID}, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("independent workspace delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetWorkspaceForMember(t.Context(), d.DB, workspaceID, owner.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace still exists: %v", err)
	}
}

func TestUserMCPGroupDenialBlocksDiscoveryIncludingCreation(t *testing.T) {
	fixture := newUserMCPFixture(t)
	mustExec(t, fixture.db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','group-mcp@example.test','h','user','active')`)
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(remote.Close)
	for _, mode := range []string{store.ResourceAccessNone, store.ResourceAccessSelected} {
		p := store.DefaultUserGroupPermissions()
		p.Tools = store.ResourceAccessPolicy{Mode: mode, IDs: []string{"builtin:web_fetch"}}
		setIntersectionGroup(t, Deps{DB: fixture.db}, "u1", p)
		created := decodeMCPAdminResponse[userMCPServerResponse](t, fixture.request(t, http.MethodPost, "/api/me/mcps",
			`{"name":"`+mode+`","icon":"Blocks","description":"Group restrictions","url":"`+remote.URL+`"}`, "u1"), http.StatusCreated)
		for _, action := range []string{"test", "sync"} {
			rec := fixture.request(t, http.MethodPost, "/api/me/mcps/"+created.ID+"/"+action, "", "u1")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("mode=%s action=%s status=%d body=%s", mode, action, rec.Code, rec.Body.String())
			}
		}
		if requests.Load() != 0 {
			t.Fatalf("denied MCP reached remote: %d calls", requests.Load())
		}
	}
}
