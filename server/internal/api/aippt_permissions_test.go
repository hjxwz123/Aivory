package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aivory/server/internal/store"
)

func TestAiPPTPermissionIntersection(t *testing.T) {
	for _, test := range []struct {
		name                     string
		group, workspace, member bool
		role                     string
		siteAdmin                bool
		want                     int
	}{
		{"allowed", true, true, true, "member", false, http.StatusOK},
		{"group denies", false, true, true, "member", false, http.StatusForbidden},
		{"workspace denies", true, false, true, "member", false, http.StatusForbidden},
		{"member denies", true, true, false, "member", false, http.StatusForbidden},
		{"workspace admin still obeys group", false, true, true, "admin", false, http.StatusForbidden},
		{"site admin bypasses group", false, true, true, "member", true, http.StatusOK},
		{"site admin obeys workspace", false, false, true, "member", true, http.StatusForbidden},
		{"guest cannot use PPT", true, true, true, "guest", false, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := aipptTestDeps(t, 0, nil)
			workspace, err := store.CreateWorkspace(t.Context(), fixture.db, fixture.other.ID, "Slides")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.JoinWorkspace(t.Context(), fixture.db, workspace.ID, fixture.user.ID); err != nil {
				t.Fatal(err)
			}
			mustExec(t, fixture.db, `UPDATE workspace_members SET role=?,can_use_ai_ppt=? WHERE workspace_id=? AND user_id=?`, test.role, test.member, workspace.ID, fixture.user.ID)
			if !test.group {
				mustExec(t, fixture.db, `UPDATE user_groups SET permissions='{"allow_ai_ppt":false}' WHERE id='ug_free'`)
			}
			if test.siteAdmin {
				fixture.user.Role = "admin"
				mustExec(t, fixture.db, `UPDATE users SET role='admin' WHERE id=?`, fixture.user.ID)
			}
			if !test.workspace {
				if _, err := store.UpdateWorkspacePolicy(t.Context(), fixture.db, workspace.ID, fixture.other.ID, store.WorkspacePolicyPatch{AllowAiPPT: &test.workspace}); err != nil {
					t.Fatal(err)
				}
			}
			recorder, request := aipptReq(t, fixture, fixture.user, http.MethodGet, "/api/me/ppt/options?workspace_id="+workspace.ID, nil)
			aiPPTAuthorized(fixture.deps, func(_ Deps, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })(fixture.deps, recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d want=%d: %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestAiPPTMemberPermissionPatchPreservesLegacyOmission(t *testing.T) {
	fixture := aipptTestDeps(t, 0, nil)
	workspace, err := store.CreateWorkspace(t.Context(), fixture.db, fixture.other.ID, "Slides")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.JoinWorkspace(t.Context(), fixture.db, workspace.ID, fixture.user.ID); err != nil {
		t.Fatal(err)
	}
	patch := func(body string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := userGroupPermissionRequest(http.MethodPatch, "/api/workspaces/"+workspace.ID+"/members/"+fixture.user.ID+"/permissions", fixture.other, map[string]string{"id": workspace.ID, "uid": fixture.user.ID}, body)
		updateWorkspaceMemberPermissionsHandler(fixture.deps, recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("patch status=%d: %s", recorder.Code, recorder.Body.String())
		}
	}
	patch(`{"can_use_ai_ppt":false}`)
	patch(`{"can_create_projects":false}`)
	var allowed bool
	if err := fixture.db.QueryRow(`SELECT can_use_ai_ppt=1 FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace.ID, fixture.user.ID).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("legacy member edit re-enabled AI PPT")
	}
}

func TestAiPPTDeckScopeAndRevocation(t *testing.T) {
	fixture := aipptTestDeps(t, 0, nil)
	workspace, err := store.CreateWorkspace(t.Context(), fixture.db, fixture.user.ID, "Slides")
	if err != nil {
		t.Fatal(err)
	}
	deck, err := store.CreateAiPPTDeck(t.Context(), fixture.db, store.AiPPTDeck{UserID: fixture.user.ID, WorkspaceID: workspace.ID, Subject: "Quarterly review"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(path string, want int) {
		t.Helper()
		recorder, request := aipptReq(t, fixture, fixture.user, http.MethodGet, path, nil)
		request = withPathParam(request, "id", deck.ID)
		aiPPTAuthorized(fixture.deps, func(_ Deps, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })(fixture.deps, recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s: status=%d want=%d: %s", path, recorder.Code, want, recorder.Body.String())
		}
	}
	path := "/api/me/ppt/decks/" + deck.ID
	check(path, http.StatusNotFound)
	check(path+"?workspace_id="+workspace.ID, http.StatusOK)
	denied := false
	if _, err := store.UpdateWorkspacePolicy(t.Context(), fixture.db, workspace.ID, fixture.user.ID, store.WorkspacePolicyPatch{AllowAiPPT: &denied}); err != nil {
		t.Fatal(err)
	}
	check(path+"?workspace_id="+workspace.ID, http.StatusForbidden)
	personal, err := store.ListAiPPTDecks(t.Context(), fixture.db, fixture.user.ID, 10, 0)
	if err != nil || len(personal) != 0 {
		t.Fatalf("workspace deck leaked into personal list: %+v %v", personal, err)
	}
}

func TestAiPPTLegacyGroupPermissionDefaults(t *testing.T) {
	permissions, err := store.NormalizeUserGroupPermissions(json.RawMessage(`{"allow_sharing":false}`))
	if err != nil || !permissions.AllowAiPPT {
		t.Fatalf("legacy policy lost AI PPT: %+v %v", permissions, err)
	}
}

func TestAiPPTDemotedSiteAdminDoesNotKeepGroupBypass(t *testing.T) {
	fixture := aipptTestDeps(t, 0, nil)
	fixture.user.Role = "admin"
	mustExec(t, fixture.db, `UPDATE users SET role='user' WHERE id=?`, fixture.user.ID)
	mustExec(t, fixture.db, `UPDATE user_groups SET permissions='{"allow_ai_ppt":false}' WHERE id='ug_free'`)
	recorder, request := aipptReq(t, fixture, fixture.user, http.MethodGet, "/api/me/ppt/options", nil)
	aiPPTAuthorized(fixture.deps, func(_ Deps, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })(fixture.deps, recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("demoted admin status=%d: %s", recorder.Code, recorder.Body.String())
	}
}
