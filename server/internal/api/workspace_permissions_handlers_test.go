package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func openWorkspacePermissionHTTPTest(t *testing.T) (*store.User, *store.User, Deps, string, string) {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-permissions-http.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('permission-owner','permission-owner@example.test','Owner','h','user','active'),
		('permission-member','permission-member@example.test','Member','h','user','active')`)
	owner, err := store.FindUserByID(context.Background(), db, "permission-owner")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.FindUserByID(context.Background(), db, "permission-member")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := store.CreateWorkspace(context.Background(), db, owner.ID, "Permissions")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.JoinWorkspace(context.Background(), db, workspace.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('permission-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('permission-embedding','permission-channel','embedding','embed','Embedding',1,8)`)
	kb, err := store.CreateKB(context.Background(), db, store.KnowledgeBase{
		UserID: member.ID, WorkspaceID: workspace.ID, Name: "Workspace library",
		EmbeddingModelID: "permission-embedding", EmbeddingDim: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner, member, Deps{DB: db}, workspace.ID, kb.ID
}

func workspacePermissionRequest(
	t *testing.T,
	method, target string,
	user *store.User,
	path map[string]string,
	body any,
) *http.Request {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	ctx := context.WithValue(req.Context(), userCtxKey{}, user)
	ctx = context.WithValue(ctx, pathCtxKey{}, path)
	return req.WithContext(ctx)
}

func TestHTTPWorkspaceOwnerUpdatesMemberTotalPermissions(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	body := store.WorkspaceMemberPermissions{}

	denied := httptest.NewRecorder()
	updateWorkspaceMemberPermissionsHandler(deps, denied, workspacePermissionRequest(
		t, http.MethodPatch, "/api/workspaces/x/members/y/permissions", member,
		map[string]string{"id": workspaceID, "uid": member.ID}, body,
	))
	if denied.Code != http.StatusNotFound {
		t.Fatalf("non-owner status=%d body=%s", denied.Code, denied.Body.String())
	}

	updated := httptest.NewRecorder()
	updateWorkspaceMemberPermissionsHandler(deps, updated, workspacePermissionRequest(
		t, http.MethodPatch, "/api/workspaces/x/members/y/permissions", owner,
		map[string]string{"id": workspaceID, "uid": member.ID}, body,
	))
	if updated.Code != http.StatusOK {
		t.Fatalf("owner status=%d body=%s", updated.Code, updated.Body.String())
	}
	var response store.WorkspaceMember
	if err := json.Unmarshal(updated.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CanCreateProjects || response.CanPrivateConversations || response.CanCreateKB ||
		response.CanAddKBFiles || response.CanDeleteKBContent {
		t.Fatalf("response permissions=%+v", response)
	}
}

func TestHTTPWorkspaceLegacyPermissionPatchDoesNotBroadenNewCapabilities(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	initial := store.WorkspaceMemberPermissions{
		CanCreateProjects: true, CanPrivateConversations: true,
		CanCreatePrompts: true, CanCreateSkills: true, CanCreateMCP: false,
		CanUsePrompts: true, CanUseSkills: false, CanUseMCP: false,
		CanCreateKB: true, CanAddKBFiles: true, CanDeleteKBContent: true,
		CanDeleteConversations: false,
	}
	if _, err := store.UpdateWorkspaceMemberPermissions(
		context.Background(), deps.DB, workspaceID, owner.ID, member.ID, initial,
	); err != nil {
		t.Fatalf("seed granular permissions: %v", err)
	}

	// The retired aggregate is false in the old client's GET response because
	// one granular creation field is disabled. Saving another old control must
	// not turn omitted MCP/use/deletion fields back on.
	legacyBody := json.RawMessage(`{
		"can_create_projects":false,
		"can_private_conversations":true,
		"can_create_skills_prompts":false,
		"can_create_kb":true,
		"can_add_kb_files":true,
		"can_delete_kb_content":true
	}`)
	recorder := httptest.NewRecorder()
	updateWorkspaceMemberPermissionsHandler(deps, recorder, workspacePermissionRequest(
		t, http.MethodPatch, "/api/workspaces/x/members/y/permissions", owner,
		map[string]string{"id": workspaceID, "uid": member.ID}, legacyBody,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("legacy patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var updated store.WorkspaceMember
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.CanCreateProjects || !updated.CanCreatePrompts || !updated.CanCreateSkills || updated.CanCreateMCP {
		t.Fatalf("legacy patch rewrote creation permissions: %+v", updated)
	}
	if !updated.CanUsePrompts || updated.CanUseSkills || updated.CanUseMCP || updated.CanDeleteConversations {
		t.Fatalf("legacy patch broadened omitted permissions: %+v", updated)
	}
}

func TestHTTPWorkspacePolicyModelCatalogIsManagerScopedAndGroupIndependent(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,fast) VALUES
		('policy-chat','permission-channel','chat','chat','Policy Chat',1,0),
		('policy-image','permission-channel','image','image','Policy Image',1,0),
		('policy-fast','permission-channel','chat','fast','Hidden Fast',1,1),
		('policy-disabled','permission-channel','image','disabled','Disabled Image',0,0)`)
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowDrawing = false
	rawPermissions, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, deps.DB, `UPDATE user_groups SET permissions=? WHERE id=?`, string(rawPermissions), store.DefaultGroupID)

	request := func(user *store.User) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		listWorkspacePolicyModelsHandler(deps, recorder, workspacePermissionRequest(
			t, http.MethodGet, "/api/workspaces/x/policy/models", user,
			map[string]string{"id": workspaceID}, nil,
		))
		return recorder
	}

	denied := request(member)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("ordinary member status=%d body=%s", denied.Code, denied.Body.String())
	}
	allowed := request(owner)
	if allowed.Code != http.StatusOK {
		t.Fatalf("workspace manager status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	var payload struct {
		Models []workspacePolicyModel `json:"models"`
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, model := range payload.Models {
		seen[model.ID] = true
	}
	if !seen["policy-chat"] || !seen["policy-image"] {
		t.Fatalf("manager policy catalog omitted enabled chat/image models: %+v", payload.Models)
	}
	if seen["policy-fast"] || seen["policy-disabled"] || seen["permission-embedding"] {
		t.Fatalf("manager policy catalog exposed hidden/non-selectable models: %+v", payload.Models)
	}
}

func TestHTTPWorkspaceKnowledgeBaseManagersUpdateOnlyLibraryLayer(t *testing.T) {
	owner, member, deps, _, kbID := openWorkspacePermissionHTTPTest(t)

	listed := httptest.NewRecorder()
	listWorkspaceKBMembersHandler(deps, listed, workspacePermissionRequest(
		t, http.MethodGet, "/api/kbs/x/workspace-members", member,
		map[string]string{"id": kbID}, nil,
	))
	if listed.Code != http.StatusOK {
		t.Fatalf("creator list status=%d body=%s", listed.Code, listed.Body.String())
	}

	updated := httptest.NewRecorder()
	updateWorkspaceKBMemberHandler(deps, updated, workspacePermissionRequest(
		t, http.MethodPatch, "/api/kbs/x/workspace-members/y", member,
		map[string]string{"id": kbID, "uid": owner.ID},
		map[string]bool{"can_add_files": false, "can_delete_content": false},
	))
	if updated.Code != http.StatusNotFound {
		t.Fatalf("creator changed locked owner: status=%d body=%s", updated.Code, updated.Body.String())
	}

	// The workspace owner may manage the KB too, but the KB creator remains a
	// locked principal at the library layer.
	lockedCreator := httptest.NewRecorder()
	updateWorkspaceKBMemberHandler(deps, lockedCreator, workspacePermissionRequest(
		t, http.MethodPatch, "/api/kbs/x/workspace-members/y", owner,
		map[string]string{"id": kbID, "uid": member.ID},
		map[string]bool{"can_add_files": false, "can_delete_content": false},
	))
	if lockedCreator.Code != http.StatusNotFound {
		t.Fatalf("owner changed locked creator: status=%d body=%s", lockedCreator.Code, lockedCreator.Body.String())
	}
}

func TestHTTPWorkspaceCreationPermissionsReturnExplicitForbiddenErrors(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	if _, err := store.UpdateWorkspaceMemberPermissions(
		context.Background(), deps.DB, workspaceID, owner.ID, member.ID, store.WorkspaceMemberPermissions{},
	); err != nil {
		t.Fatal(err)
	}

	project := httptest.NewRecorder()
	createProjectHandler(deps, project, workspacePermissionRequest(
		t, http.MethodPost, "/api/projects", member, nil,
		map[string]string{"name": "Denied project", "workspace_id": workspaceID},
	))
	if project.Code != http.StatusForbidden || !bytes.Contains(project.Body.Bytes(), []byte(errWorkspaceProjectCreationPermission.Error())) {
		t.Fatalf("project status=%d body=%s", project.Code, project.Body.String())
	}

	knowledgeBase := httptest.NewRecorder()
	createKBHandler(deps, knowledgeBase, workspacePermissionRequest(
		t, http.MethodPost, "/api/kbs", member, nil,
		map[string]string{"name": "Denied library", "workspace_id": workspaceID},
	))
	if knowledgeBase.Code != http.StatusForbidden || !bytes.Contains(knowledgeBase.Body.Bytes(), []byte(errWorkspaceKBCreationPermission.Error())) {
		t.Fatalf("knowledge base status=%d body=%s", knowledgeBase.Code, knowledgeBase.Body.String())
	}
}

func TestHTTPKnowledgeBaseDocumentRenameUsesCurrentMutationPermission(t *testing.T) {
	_, member, deps, _, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO documents(
		id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id
	) VALUES('rename-document',?,'before.txt','text/plain',1,'ready','',?)`, kbID, member.ID)

	renamed := httptest.NewRecorder()
	renameKBDocHandler(deps, renamed, workspacePermissionRequest(
		t, http.MethodPatch, "/api/kbs/x/documents/y", member,
		map[string]string{"id": kbID, "docId": "rename-document"},
		map[string]string{"filename": "after.txt"},
	))
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	var filename string
	if err := deps.DB.QueryRow(`SELECT filename FROM documents WHERE id='rename-document'`).Scan(&filename); err != nil {
		t.Fatal(err)
	}
	if filename != "after.txt" {
		t.Fatalf("filename=%q, want after.txt", filename)
	}

	invalid := httptest.NewRecorder()
	renameKBDocHandler(deps, invalid, workspacePermissionRequest(
		t, http.MethodPatch, "/api/kbs/x/documents/y", member,
		map[string]string{"id": kbID, "docId": "rename-document"},
		map[string]string{"filename": "../escape.txt"},
	))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid rename status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestHTTPProjectDocumentRenameRejectsUnsafeFilename(t *testing.T) {
	_, member, deps, workspaceID, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('rename-project',?,'Rename project',?,?)`, member.ID, kbID, workspaceID)
	mustExec(t, deps.DB, `INSERT INTO documents(
		id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id
	) VALUES('project-rename-document',?,'before.txt','text/plain',1,'ready','',?)`, kbID, member.ID)

	for _, filename := range []string{"../escape.txt", `folder\\escape.txt`, "control\nname.txt"} {
		recorder := httptest.NewRecorder()
		renameProjectDocHandler(deps, recorder, workspacePermissionRequest(
			t, http.MethodPatch, "/api/projects/x/documents/y", member,
			map[string]string{"id": "rename-project", "docId": "project-rename-document"},
			map[string]string{"filename": filename},
		))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("filename=%q status=%d body=%s, want 400", filename, recorder.Code, recorder.Body.String())
		}
	}

	var filename string
	if err := deps.DB.QueryRow(`SELECT filename FROM documents WHERE id='project-rename-document'`).Scan(&filename); err != nil {
		t.Fatal(err)
	}
	if filename != "before.txt" {
		t.Fatalf("unsafe rename persisted filename=%q", filename)
	}
}

func TestHTTPProjectDetailReturnsEffectiveWorkspaceLibraryPermissions(t *testing.T) {
	owner, member, deps, workspaceID, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('permission-project',?,'Permission project',?,?)`, member.ID, kbID, workspaceID)

	assertCapabilities := func(t *testing.T, user *store.User, upload, contentDelete, projectDelete bool) {
		t.Helper()
		recorder := httptest.NewRecorder()
		getProjectHandler(deps, recorder, workspacePermissionRequest(
			t, http.MethodGet, "/api/projects/permission-project", user,
			map[string]string{"id": "permission-project"}, nil,
		))
		if recorder.Code != http.StatusOK {
			t.Fatalf("detail status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Project projectResponse `json:"project"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Project.CanUploadFiles == nil || *body.Project.CanUploadFiles != upload ||
			body.Project.CanDeleteContent == nil || *body.Project.CanDeleteContent != contentDelete ||
			body.Project.CanDelete == nil || *body.Project.CanDelete != projectDelete {
			t.Fatalf("project capabilities=%+v, want upload=%v content-delete=%v project-delete=%v",
				body.Project, upload, contentDelete, projectDelete)
		}
	}

	// A current workspace member may use the project but cannot delete it. The
	// total member switches cap its per-library defaults immediately.
	assertCapabilities(t, owner, true, true, true)
	permissions := store.WorkspaceMemberPermissions{
		CanCreateProjects: true, CanPrivateConversations: true, CanCreateKB: true,
	}
	if _, err := store.UpdateWorkspaceMemberPermissions(
		context.Background(), deps.DB, workspaceID, owner.ID, member.ID, permissions,
	); err != nil {
		t.Fatal(err)
	}
	assertCapabilities(t, member, true, true, true)

	other := &store.User{ID: "permission-other", Role: "user", Status: "active", GroupID: store.DefaultGroupID}
	mustExec(t, deps.DB, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('permission-other','permission-other@example.test','Other','h','user','active')`)
	if err := store.JoinWorkspace(context.Background(), deps.DB, workspaceID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWorkspaceMemberPermissions(
		context.Background(), deps.DB, workspaceID, owner.ID, other.ID, permissions,
	); err != nil {
		t.Fatal(err)
	}
	assertCapabilities(t, other, false, false, false)
}

func TestHTTPProjectDetailGroupPermissionsCapLibraryCapabilities(t *testing.T) {
	_, member, deps, workspaceID, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('group-cap-project',?,'Group cap project',?,?)`, member.ID, kbID, workspaceID)
	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowFileUpload = false
	raw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, deps.DB, `INSERT INTO user_groups(id,name,features,permissions)
		VALUES('project-group-cap','Project group cap','[]',?)`, string(raw))
	mustExec(t, deps.DB, `UPDATE users SET group_id='project-group-cap' WHERE id=?`, member.ID)

	recorder := httptest.NewRecorder()
	getProjectHandler(deps, recorder, workspacePermissionRequest(
		t, http.MethodGet, "/api/projects/group-cap-project", member,
		map[string]string{"id": "group-cap-project"}, nil,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Project projectResponse `json:"project"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Project.CanUploadFiles == nil || *body.Project.CanUploadFiles ||
		body.Project.CanDeleteContent == nil || !*body.Project.CanDeleteContent {
		t.Fatalf("group-capped project upload=%v content-delete=%v",
			boolPointerValue(body.Project.CanUploadFiles), boolPointerValue(body.Project.CanDeleteContent))
	}
}

func TestHTTPProjectAutoAddRequiresEffectiveLibraryWritePermission(t *testing.T) {
	owner, _, deps, workspaceID, kbID := openWorkspacePermissionHTTPTest(t)
	mustExec(t, deps.DB, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id)
		VALUES('auto-add-project',?,'Auto add project',?,?)`, owner.ID, kbID, workspaceID)
	mustExec(t, deps.DB, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('auto-add-member','auto-add-member@example.test','Auto add member','h','user','active')`)
	member, err := store.FindUserByID(context.Background(), deps.DB, "auto-add-member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.JoinWorkspace(context.Background(), deps.DB, workspaceID, member.ID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, deps.DB, `INSERT INTO workspace_kb_member_permissions(
		kb_id,user_id,can_add_files,can_delete_content
	) VALUES(?,?,0,1)`, kbID, member.ID)

	recorder := httptest.NewRecorder()
	updateProjectHandler(deps, recorder, workspacePermissionRequest(
		t, http.MethodPatch, "/api/projects/auto-add-project", member,
		map[string]string{"id": "auto-add-project"}, map[string]bool{"auto_add_uploads": true},
	))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("auto-add status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var enabled int
	if err := deps.DB.QueryRow(`SELECT auto_add_uploads FROM projects WHERE id='auto-add-project'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("auto_add_uploads=%d after denied update", enabled)
	}
}

func boolPointerValue(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}
