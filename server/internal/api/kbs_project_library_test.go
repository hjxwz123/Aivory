package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestStandaloneKBAPIHidesAndProtectsProjectLibrary(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "project-library-api.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb1','ch1','embedding','emb','Embedding',3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('standalone-kb','u1','Standalone','emb1',3),
		('project-kb','u1','Project library','emb1',3)`)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name,kb_id) VALUES('project-1','u1','Project','project-kb')`)

	user := &store.User{ID: "u1", Role: "user", Status: "active"}
	listReq := httptest.NewRequest(http.MethodGet, "/api/kbs", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), userCtxKey{}, user))
	listRec := httptest.NewRecorder()
	listKBsHandler(Deps{DB: db}, listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list KBs status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode KB list: %v", err)
	}
	if len(listed) != 1 || listed[0]["id"] != "standalone-kb" {
		t.Fatalf("listed KBs=%#v, want only standalone-kb", listed)
	}
	assertNoRetrievalImplementationFields(t, listed[0], "embedding_model_id", "embedding_dim")

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/kbs/project-kb", nil)
	deleteCtx := context.WithValue(deleteReq.Context(), pathCtxKey{}, map[string]string{"id": "project-kb"})
	deleteCtx = context.WithValue(deleteCtx, userCtxKey{}, user)
	deleteReq = deleteReq.WithContext(deleteCtx)
	deleteRec := httptest.NewRecorder()
	deleteKBHandler(Deps{DB: db}, deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete project KB status=%d body=%s, want 404", deleteRec.Code, deleteRec.Body.String())
	}

	var projectKBID string
	if err := db.QueryRow(`SELECT COALESCE(kb_id,'') FROM projects WHERE id='project-1'`).Scan(&projectKBID); err != nil {
		t.Fatalf("read project after rejected delete: %v", err)
	}
	if projectKBID != "project-kb" {
		t.Fatalf("project kb_id=%q, want project-kb retained", projectKBID)
	}
	var libraryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_bases WHERE id='project-kb'`).Scan(&libraryCount); err != nil {
		t.Fatalf("count project library: %v", err)
	}
	if libraryCount != 1 {
		t.Fatalf("project library count=%d, want 1", libraryCount)
	}
}

func TestCreateKBUsesAdministratorConfigurationAndHidesImplementation(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-user-response.db"))
	defer db.Close()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u@example.test','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('admin-index','ch1','embedding','admin-index','Administrator index',1,1536),
		('forged-index','ch1','embedding','forged-index','Forged index',1,3072)`)
	if err := store.SetSetting(db, "embedding_model_id", "admin-index"); err != nil {
		t.Fatalf("configure administrator index: %v", err)
	}

	user := &store.User{ID: "u1", Role: "user", Status: "active"}
	req := httptest.NewRequest(http.MethodPost, "/api/kbs", strings.NewReader(
		`{"name":"Private library","description":"Documents","embedding_model_id":"forged-index","embedding_dim":99}`,
	))
	req.Header.Set("content-type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
	rec := httptest.NewRecorder()
	createKBHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create KB status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	assertNoRetrievalImplementationFields(t, body, "embedding_model_id", "embedding_dim")

	var modelID string
	var dim int
	if err := db.QueryRow(`SELECT embedding_model_id, embedding_dim FROM knowledge_bases WHERE id=?`, body["id"]).Scan(&modelID, &dim); err != nil {
		t.Fatalf("read created knowledge base: %v", err)
	}
	if modelID != "admin-index" || dim != 1536 {
		t.Fatalf("created knowledge base uses model=%q dim=%d, want administrator configuration", modelID, dim)
	}
}

func TestKnowledgeBaseCreationFailureHidesImplementation(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-unavailable-response.db"))
	defer db.Close()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u@example.test','h','user','active')`)

	req := httptest.NewRequest(http.MethodPost, "/api/kbs", strings.NewReader(`{"name":"Private library"}`))
	req.Header.Set("content-type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	createKBHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create KB status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, errKnowledgeBaseUnavailable.Error()) || strings.Contains(body, "embedding") {
		t.Fatalf("create failure exposes retrieval implementation: %s", body)
	}
}

func TestUserDocumentResponsesHideIndexingDiagnostics(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "document-user-response.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u@example.test','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('index-model','ch1','embedding','index','Index',1,3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Library','index-model',3)`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Conversation')`)
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,error,storage_path) VALUES
		('kb-doc','kb1','kb.txt','text/plain',10,'failed','embedding failed for secret-index-model','/tmp/kb.txt')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,error,storage_path) VALUES
		('conv-doc','c1','conv.txt','text/plain',10,'failed','embedding failed for secret-index-model','/tmp/conv.txt')`)

	user := &store.User{ID: "u1", Role: "user", Status: "active"}
	request := func(target, parentID string, endpoint handler) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		ctx := context.WithValue(req.Context(), userCtxKey{}, user)
		ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": parentID})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		endpoint(Deps{DB: db}, rec, req)
		return rec
	}
	for _, rec := range []*httptest.ResponseRecorder{
		request("/api/kbs/kb1/documents", "kb1", listKBDocsHandler),
		request("/api/conversations/c1/documents", "c1", listConversationDocsHandler),
	} {
		if rec.Code != http.StatusOK {
			t.Fatalf("list documents status=%d body=%s", rec.Code, rec.Body.String())
		}
		var docs []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil || len(docs) != 1 {
			t.Fatalf("decode documents: items=%v err=%v", docs, err)
		}
		if got := docs[0]["error"]; got != "" {
			t.Fatalf("user document response exposes indexing diagnostic %q", got)
		}
		if strings.Contains(rec.Body.String(), "secret-index-model") || strings.Contains(rec.Body.String(), "embedding failed") {
			t.Fatalf("user document response exposes retrieval implementation: %s", rec.Body.String())
		}
	}

	adminRec := httptest.NewRecorder()
	adminReq := httptest.NewRequest(http.MethodGet, "/api/admin/kbs/kb1/documents", nil)
	adminReq = adminReq.WithContext(context.WithValue(adminReq.Context(), pathCtxKey{}, map[string]string{"id": "kb1"}))
	listKBDocumentsAdmin(Deps{DB: db}, adminRec, adminReq)
	if adminRec.Code != http.StatusOK || !strings.Contains(adminRec.Body.String(), "secret-index-model") {
		t.Fatalf("admin diagnostics were unexpectedly redacted: status=%d body=%s", adminRec.Code, adminRec.Body.String())
	}
}

func TestProjectUserResponsesHideLibraryImplementation(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "project-user-response.db"))
	defer db.Close()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u@example.test','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('admin-index','ch1','embedding','admin-index','Administrator index',1,1536)`)
	if err := store.SetSetting(db, "embedding_model_id", "admin-index"); err != nil {
		t.Fatalf("configure administrator index: %v", err)
	}

	user := &store.User{ID: "u1", Role: "user", Status: "active"}
	request := func(method, target, payload string, handler handler) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(payload))
		req.Header.Set("content-type", "application/json")
		ctx := context.WithValue(req.Context(), userCtxKey{}, user)
		if target != "/api/projects" {
			ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": "project-1"})
		}
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		handler(Deps{DB: db}, rec, req)
		return rec
	}

	created := request(http.MethodPost, "/api/projects", `{"name":"Project"}`, createProjectHandler)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", created.Code, created.Body.String())
	}
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create project response: %v", err)
	}
	assertNoRetrievalImplementationFields(t, createdBody, "kb_embedding_model_id", "kb_embedding_dim")
	projectID, _ := createdBody["id"].(string)
	if projectID == "" {
		t.Fatalf("create project response has no id: %s", created.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), userCtxKey{}, user))
	listRec := httptest.NewRecorder()
	listProjectsHandler(Deps{DB: db}, listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list projects status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listBody []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil || len(listBody) != 1 {
		t.Fatalf("decode project list: items=%v err=%v", listBody, err)
	}
	assertNoRetrievalImplementationFields(t, listBody[0], "kb_embedding_model_id", "kb_embedding_dim")

	detailReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID, nil)
	detailCtx := context.WithValue(detailReq.Context(), userCtxKey{}, user)
	detailCtx = context.WithValue(detailCtx, pathCtxKey{}, map[string]string{"id": projectID})
	detailReq = detailReq.WithContext(detailCtx)
	detailRec := httptest.NewRecorder()
	getProjectHandler(Deps{DB: db}, detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("get project status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detailBody struct {
		Project map[string]any `json:"project"`
	}
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detailBody); err != nil {
		t.Fatalf("decode project detail: %v", err)
	}
	assertNoRetrievalImplementationFields(t, detailBody.Project, "kb_embedding_model_id", "kb_embedding_dim")

	updateReq := httptest.NewRequest(http.MethodPatch, "/api/projects/"+projectID, strings.NewReader(`{"description":"Updated"}`))
	updateReq.Header.Set("content-type", "application/json")
	updateCtx := context.WithValue(updateReq.Context(), userCtxKey{}, user)
	updateCtx = context.WithValue(updateCtx, pathCtxKey{}, map[string]string{"id": projectID})
	updateReq = updateReq.WithContext(updateCtx)
	updateRec := httptest.NewRecorder()
	updateProjectHandler(Deps{DB: db}, updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update project status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updateBody); err != nil {
		t.Fatalf("decode update project response: %v", err)
	}
	assertNoRetrievalImplementationFields(t, updateBody, "kb_embedding_model_id", "kb_embedding_dim")
}

func TestUserEmbeddingModelsRouteDoesNotExist(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "embedding-models-route.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u@example.test','h','user','active')`)

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("embedding-models-route-test-secret", time.Hour, 24*time.Hour, c),
	}
	user, err := store.FindUserByID(t.Context(), db, "u1")
	if err != nil {
		t.Fatalf("find user: %v", err)
	}
	token := issueBoundTestAccessToken(t, db, d.Auth, user)

	req := httptest.NewRequest(http.MethodGet, "/api/embedding-models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("embedding-models route status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

func assertNoRetrievalImplementationFields(t *testing.T, body map[string]any, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if _, exists := body[field]; exists {
			t.Errorf("user response exposes %q: %#v", field, body[field])
		}
	}
}
