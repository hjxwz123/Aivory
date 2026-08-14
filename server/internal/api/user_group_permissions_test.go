package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func userGroupPermissionRequest(method, target string, user *store.User, path map[string]string, body string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("content-type", "application/json")
	ctx := context.WithValue(req.Context(), userCtxKey{}, user)
	if path != nil {
		ctx = context.WithValue(ctx, pathCtxKey{}, path)
	}
	return req.WithContext(ctx)
}

func TestProjectKnowledgeSurfacesHonorCurrentGroupPermission(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "project-group-permission.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	allowed := store.DefaultUserGroupPermissions()
	allowedRaw, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,features,permissions) VALUES('project_group','Projects','[]',?)`, string(allowedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id) VALUES
		('project_member','project-member@example.test','Member','h','user','active','project_group')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('project_channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('project_embedding','project_channel','embedding','embed','Embedding',1,8)`)
	if err := store.SetSetting(db, "embedding_model_id", "project_embedding"); err != nil {
		t.Fatal(err)
	}
	user := &store.User{ID: "project_member", Role: "user", Status: "active", GroupID: "project_group"}
	deps := Deps{DB: db}

	created := httptest.NewRecorder()
	createProjectHandler(deps, created, userGroupPermissionRequest(http.MethodPost, "/api/projects", user, nil, `{"name":"Allowed project"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("allowed create status=%d body=%s", created.Code, created.Body.String())
	}
	var project projectResponse
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || project.KBID == "" {
		t.Fatalf("created project=%+v", project)
	}
	doc, err := store.CreateDocumentForUser(ctx, db, store.Document{
		KBID: project.KBID, Filename: "private-notes.txt", MimeType: "text/plain", Status: "ready",
	}, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	restricted := allowed
	restricted.AllowKnowledgeBases = false
	restrictedRaw, err := json.Marshal(restricted)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='project_group'`, string(restrictedRaw))

	deniedCreate := httptest.NewRecorder()
	createProjectHandler(deps, deniedCreate, userGroupPermissionRequest(http.MethodPost, "/api/projects", user, nil, `{"name":"Denied project"}`))
	if deniedCreate.Code != http.StatusForbidden || !strings.Contains(deniedCreate.Body.String(), errKnowledgeBaseGroupPermission.Error()) {
		t.Fatalf("restricted create status=%d body=%s", deniedCreate.Code, deniedCreate.Body.String())
	}

	detail := httptest.NewRecorder()
	getProjectHandler(deps, detail, userGroupPermissionRequest(http.MethodGet, "/api/projects/"+project.ID, user, map[string]string{"id": project.ID}, ""))
	if detail.Code != http.StatusOK {
		t.Fatalf("restricted detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Documents []store.Document `json:"documents"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	if len(detailBody.Documents) != 0 || strings.Contains(detail.Body.String(), doc.Filename) {
		t.Fatalf("restricted project leaked documents: %s", detail.Body.String())
	}

	autoAdd := httptest.NewRecorder()
	updateProjectHandler(deps, autoAdd, userGroupPermissionRequest(http.MethodPatch, "/api/projects/"+project.ID, user, map[string]string{"id": project.ID}, `{"auto_add_uploads":true}`))
	if autoAdd.Code != http.StatusForbidden || !strings.Contains(autoAdd.Body.String(), errKnowledgeBaseGroupPermission.Error()) {
		t.Fatalf("restricted auto-add status=%d body=%s", autoAdd.Code, autoAdd.Body.String())
	}
}

func TestKnowledgeBaseGroupPermissionOverridesExistingShareImmediately(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-group-permission.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	allowedRaw, err := json.Marshal(store.DefaultUserGroupPermissions())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,features,permissions) VALUES('group_member','Member','[]',?)`, string(allowedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id) VALUES
		('owner','owner@example.test','Owner','h','user','active','ug_free'),
		('member','member@example.test','Member','h','user','active','group_member')`)

	channel, err := store.CreateChannel(ctx, db, "Embedding", "openai", "chat", "https://provider.example.test/v1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "embedding", RequestID: "embed", Label: "Embedding", Enabled: true, Dim: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	kb, err := store.CreateKB(ctx, db, store.KnowledgeBase{
		UserID: "owner", Name: "Shared library", EmbeddingModelID: model.ID, EmbeddingDim: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertKnowledgeBaseShare(ctx, db, kb.ID, "owner", "member", "read"); err != nil {
		t.Fatal(err)
	}

	userSnapshot := &store.User{ID: "member", Role: "user", Status: "active", GroupID: "group_member"}
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/kbs", nil)
		return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, userSnapshot))
	}
	handler := requireKnowledgeBaseHandler(listKBsHandler)

	allowed := httptest.NewRecorder()
	handler(Deps{DB: db}, allowed, request())
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), kb.ID) {
		t.Fatalf("shared KB was not initially visible: status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	restricted := store.DefaultUserGroupPermissions()
	restricted.AllowKnowledgeBases = false
	restrictedRaw, err := json.Marshal(restricted)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='group_member'`, string(restrictedRaw))

	denied := httptest.NewRecorder()
	handler(Deps{DB: db}, denied, request())
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), errKnowledgeBaseGroupPermission.Error()) {
		t.Fatalf("share bypassed current group permission: status=%d body=%s", denied.Code, denied.Body.String())
	}
	var shareCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_base_shares WHERE kb_id=? AND user_id='member'`, kb.ID).Scan(&shareCount); err != nil {
		t.Fatal(err)
	}
	if shareCount != 1 {
		t.Fatalf("test lost the share row; count=%d", shareCount)
	}
}

func TestKnowledgeBaseUploadPermissionErrorPrecedesQuotaError(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-upload-permission-order.db"))
	t.Cleanup(func() { _ = db.Close() })
	restricted := store.DefaultUserGroupPermissions()
	restricted.AllowFileUpload = false
	restrictedRaw, err := json.Marshal(restricted)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,permissions)
		VALUES('restricted-upload','Restricted',1,?)`, string(restrictedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id)
		VALUES('restricted-user','restricted@example.test','Restricted','h','user','active','restricted-upload')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('permission-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim)
		VALUES('permission-embedding','permission-channel','embedding','embed','Embedding',1,8)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim)
		VALUES('permission-kb','restricted-user','Library','permission-embedding',8)`)
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id)
		VALUES('quota-full','permission-kb','full.txt','text/plain',1048576,'ready','/quota/full','restricted-user')`)

	user := &store.User{ID: "restricted-user", Role: "user", Status: "active", GroupID: "restricted-upload"}
	req := userGroupPermissionRequest(
		http.MethodPost, "/api/kbs/permission-kb/documents", user,
		map[string]string{"id": "permission-kb"}, `{"filename":"more.txt","content":"x","mime_type":"text/plain"}`,
	)
	recorder := httptest.NewRecorder()
	uploadKBDocHandler(Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), MaxUploadBytes: 1 << 20},
	}, recorder, req)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), errFileUploadGroupPermission.Error()) {
		t.Fatalf("status=%d body=%s, want upload permission error before quota", recorder.Code, recorder.Body.String())
	}
}

func TestPostMessageRejectsDraftAttachmentsWithoutUploadPermission(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-upload-permission.db"))
	t.Cleanup(func() { _ = db.Close() })
	restricted := store.DefaultUserGroupPermissions()
	restricted.AllowFileUpload = false
	restrictedRaw, err := json.Marshal(restricted)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions)
		VALUES('message-upload-group','No uploads',?)`, string(restrictedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id)
		VALUES('message-upload-user','message-upload@example.test','No uploads','h','user','active','message-upload-group')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title)
		VALUES('message-upload-conversation','message-upload-user','Draft attachment')`)

	user := &store.User{
		ID: "message-upload-user", Role: "user", Status: "active", GroupID: "message-upload-group",
	}
	req := userGroupPermissionRequest(
		http.MethodPost,
		"/api/conversations/message-upload-conversation/messages",
		user,
		map[string]string{"id": "message-upload-conversation"},
		`{"text":"use this file","attachments":[{"id":"draft-file","filename":"draft.txt","kind":"doc"}]}`,
	)
	recorder := httptest.NewRecorder()
	postMessageHandler(Deps{DB: db}, recorder, req)

	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), errFileUploadGroupPermission.Error()) {
		t.Fatalf("status=%d body=%s, want upload permission denial", recorder.Code, recorder.Body.String())
	}
	var messageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='message-upload-conversation'`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("persisted messages=%d, want none", messageCount)
	}
}

func TestTurnUsesKnowledgeBaseCoversPersistedExplicitAndProjectSelections(t *testing.T) {
	if !turnUsesKnowledgeBase(&store.Conversation{KBIDs: json.RawMessage(`["kb_old"]`)}, nil) {
		t.Fatal("persisted knowledge-base selection was not detected")
	}
	if turnUsesKnowledgeBase(&store.Conversation{KBIDs: json.RawMessage(`["kb_old"]`)}, json.RawMessage(`[]`)) {
		t.Fatal("explicit empty turn selection did not override persisted knowledge bases")
	}
	if !turnUsesKnowledgeBase(&store.Conversation{ProjectID: "project_1"}, json.RawMessage(`[]`)) {
		t.Fatal("project knowledge base was not detected")
	}
}

func TestApplyTurnToolPermissionsPreservesModelDefaultsButFiltersExplicitSelection(t *testing.T) {
	permissions := store.DefaultUserGroupPermissions()
	permissions.Tools = store.ResourceAccessPolicy{Mode: store.ResourceAccessSelected, IDs: []string{"builtin:web_fetch", "builtin:image_generate", "builtin:save_memory"}}
	permissions.AllowDrawing = false
	permissions.AllowMemory = false

	ids, configured := applyTurnToolPermissions(permissions, nil, false)
	if configured || len(ids) != 0 {
		t.Fatalf("omitted model defaults changed to %#v configured=%v", ids, configured)
	}

	ids, configured = applyTurnToolPermissions(permissions, []string{
		"builtin:web_fetch", "builtin:image_generate", "builtin:save_memory", "builtin:python_execute",
	}, true)
	if !configured || len(ids) != 1 || ids[0] != "builtin:web_fetch" {
		t.Fatalf("restricted explicit tools = %#v configured=%v", ids, configured)
	}
}

func TestSelectedSkillPolicyKeepsUseSkillAvailableAndCarriesSkillCeiling(t *testing.T) {
	permissions := store.DefaultUserGroupPermissions()
	permissions.Skills = store.ResourceAccessPolicy{
		Mode: store.ResourceAccessSelected,
		IDs:  []string{"skill_allowed"},
	}

	policy := runToolAccessPolicy(permissions)
	if !policy.AllowSkills || policy.SkillMode != store.ResourceAccessSelected ||
		len(policy.SkillIDs) != 1 || policy.SkillIDs[0] != "skill_allowed" {
		t.Fatalf("selected skill policy was not preserved: %+v", policy)
	}
	if !toolPolicyAllowsID(permissions, "builtin:use_skill") {
		t.Fatal("selected skills incorrectly disabled the use_skill tool")
	}

	permissions.Skills.IDs = nil
	if runToolAccessPolicy(permissions).AllowSkills || toolPolicyAllowsID(permissions, "builtin:use_skill") {
		t.Fatal("empty selected skill policy left use_skill available")
	}
}

func TestMemoryMasterSwitchBlocksEveryUserMemoryRoute(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "memory-master-switch.db"))
	t.Cleanup(func() { _ = db.Close() })
	permissionsRaw, err := json.Marshal(store.DefaultUserGroupPermissions())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,features,permissions) VALUES('memory_group','Memory','[]',?)`, string(permissionsRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id) VALUES
		('memory_member','memory@example.test','Memory member','h','user','active','memory_group')`)
	if err := store.SetSetting(db, "memory_enabled", false); err != nil {
		t.Fatal(err)
	}

	user := &store.User{ID: "memory_member", Role: "user", Status: "active", GroupID: "memory_group"}
	called := 0
	handler := requireMemoryHandler(func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called++
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/me/memories"},
		{http.MethodPost, "/api/me/memories"},
		{http.MethodPatch, "/api/me/memories/memory_1"},
		{http.MethodDelete, "/api/me/memories/memory_1"},
	} {
		recorder := httptest.NewRecorder()
		handler(Deps{DB: db}, recorder, userGroupPermissionRequest(test.method, test.path, user, nil, `{}`))
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), errMemoryGroupPermission.Error()) {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if called != 0 {
		t.Fatalf("memory handler called %d times while the master switch was off", called)
	}

	if err := store.SetSetting(db, "memory_enabled", true); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler(Deps{DB: db}, recorder, userGroupPermissionRequest(http.MethodGet, "/api/me/memories", user, nil, ""))
	if recorder.Code != http.StatusOK || called != 1 {
		t.Fatalf("enabled memory route status=%d body=%s called=%d", recorder.Code, recorder.Body.String(), called)
	}
}

func TestDrawingPermissionHidesExistingImageGallery(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "drawing-gallery-permission.db"))
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	permissions := store.DefaultUserGroupPermissions()
	permissions.AllowDrawing = false
	permissionsRaw, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO user_groups(id,name,features,permissions) VALUES('drawing_group','Drawing','[]',?)`, string(permissionsRaw))
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,group_id) VALUES
		('drawing_member','drawing@example.test','Drawing member','h','user','active','drawing_group')`)
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "drawing_conversation", UserID: "drawing_member", Title: "Existing drawing",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.CreateMessage(ctx, db, store.Message{
		ID: "drawing_message", ConversationID: conversation.ID, Role: "assistant",
		AuthorID: "drawing_member", Blocks: json.RawMessage(`[]`), Status: "complete",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.CreateArtifact(ctx, db, store.Artifact{
		ID: "drawing_artifact", MessageID: message.ID, Filename: "existing.png",
		StoragePath: "/tmp/existing.png", MimeType: "image/png", SizeBytes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	user := &store.User{ID: "drawing_member", Role: "user", Status: "active", GroupID: "drawing_group"}
	request := func() *http.Request {
		return userGroupPermissionRequest(http.MethodGet, "/api/me/images", user, nil, "")
	}
	restricted := httptest.NewRecorder()
	listMyImages(Deps{DB: db}, restricted, request())
	if restricted.Code != http.StatusOK || strings.Contains(restricted.Body.String(), artifact.ID) || strings.TrimSpace(restricted.Body.String()) != "[]" {
		t.Fatalf("restricted gallery status=%d body=%s", restricted.Code, restricted.Body.String())
	}

	permissions.AllowDrawing = true
	permissionsRaw, err = json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='drawing_group'`, string(permissionsRaw))
	allowed := httptest.NewRecorder()
	listMyImages(Deps{DB: db}, allowed, request())
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), artifact.ID) {
		t.Fatalf("allowed gallery status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}
