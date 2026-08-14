package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestWorkspaceDraftHTTPBoundaries(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-draft-http.db"))
	defer db.Close()
	uploadDir := t.TempDir()
	for _, user := range []string{"owner", "member", "outsider"} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?, 'h','user')`, user, user+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id,is_public) VALUES('c1','owner','Shared chat','ws1',1)`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb1','ch1','embedding','emb','Embedding',3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES
		('kb1','owner','Project KB','emb1',3,'ws1'),
		('kb2','owner','Workspace KB','emb1',3,'ws1')`)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,auto_add_uploads,workspace_id) VALUES('p1','owner','Shared project','kb1',1,'ws1')`)
	mustExec(t, db, `UPDATE knowledge_bases SET project_id='p1' WHERE id='kb1'`)
	mustExec(t, db, `UPDATE conversations SET project_id='p1' WHERE id='c1'`)
	paths := map[string]string{}
	insertFile := func(id, user string, draft int, body string) {
		t.Helper()
		path := filepath.Join(uploadDir, id+".txt")
		writeFile(t, path, []byte(body))
		paths[id] = path
		mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, id, user, "c1", id+".txt", "text/plain", len(body), path, "other", draft, 1)
	}
	insertFile("owner-draft", "owner", 1, "owner draft")
	insertFile("owner-committed", "owner", 0, "owner committed")
	insertFile("member-draft", "member", 1, "member draft")
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,error,storage_path)
		VALUES('doc-owner-draft','c1','owner-draft.txt','text/plain',11,'failed','parse failed',?)`, paths["owner-draft"])
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,error,storage_path) VALUES
		('doc-owner-project-draft','kb1','owner-draft.txt','text/plain',11,'failed','parse failed',?),
		('doc-owner-kb-draft','kb2','owner-draft.txt','text/plain',11,'failed','parse failed',?)`, paths["owner-draft"], paths["owner-draft"])

	deps := Deps{DB: db, Config: config.Config{UploadDir: uploadDir}}

	// The shared files drawer exposes committed collaboration rows and the
	// caller's own draft, but never another member's draft.
	listReq := httptest.NewRequest(http.MethodGet, "/api/conversations/c1/files", nil)
	listCtx := context.WithValue(listReq.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
	listCtx = context.WithValue(listCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	listReq = listReq.WithContext(listCtx)
	listRec := httptest.NewRecorder()
	listConversationFilesHandler(deps, listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("member file list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listed []convFile
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode member file list: %v", err)
	}
	seen := map[string]bool{}
	for _, row := range listed {
		seen[row.ID] = true
	}
	if seen["owner-draft"] || !seen["owner-committed"] || !seen["member-draft"] {
		t.Fatalf("member file list = %#v; want committed owner file plus own draft", seen)
	}

	listDocs := func(userID string) (*httptest.ResponseRecorder, []store.Document) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/conversations/c1/documents", nil)
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		listConversationDocsHandler(deps, rec, req)
		var docs []store.Document
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
				t.Fatalf("decode %s conversation docs: %v", userID, err)
			}
		}
		return rec, docs
	}
	if rec, docs := listDocs("member"); rec.Code != http.StatusOK || len(docs) != 0 {
		t.Fatalf("member conversation docs status=%d docs=%#v; owner draft must be hidden", rec.Code, docs)
	}
	if rec, docs := listDocs("owner"); rec.Code != http.StatusOK || len(docs) != 1 || docs[0].ID != "doc-owner-draft" {
		t.Fatalf("owner conversation docs status=%d docs=%#v; want own draft", rec.Code, docs)
	}

	listKBReq := httptest.NewRequest(http.MethodGet, "/api/kbs/kb2/documents", nil)
	listKBCtx := context.WithValue(listKBReq.Context(), pathCtxKey{}, map[string]string{"id": "kb2"})
	listKBCtx = context.WithValue(listKBCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	listKBReq = listKBReq.WithContext(listKBCtx)
	listKBRec := httptest.NewRecorder()
	listKBDocsHandler(deps, listKBRec, listKBReq)
	var kbDocs []store.Document
	if err := json.Unmarshal(listKBRec.Body.Bytes(), &kbDocs); err != nil || listKBRec.Code != http.StatusOK || len(kbDocs) != 0 {
		t.Fatalf("member KB draft list status=%d docs=%#v decode=%v", listKBRec.Code, kbDocs, err)
	}
	listProjectReq := httptest.NewRequest(http.MethodGet, "/api/projects/p1/documents", nil)
	listProjectCtx := context.WithValue(listProjectReq.Context(), pathCtxKey{}, map[string]string{"id": "p1"})
	listProjectCtx = context.WithValue(listProjectCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	listProjectReq = listProjectReq.WithContext(listProjectCtx)
	listProjectRec := httptest.NewRecorder()
	listProjectDocsHandler(deps, listProjectRec, listProjectReq)
	var projectDocs []store.Document
	if err := json.Unmarshal(listProjectRec.Body.Bytes(), &projectDocs); err != nil || listProjectRec.Code != http.StatusOK || len(projectDocs) != 0 {
		t.Fatalf("member project auto-add draft list status=%d docs=%#v decode=%v", listProjectRec.Code, projectDocs, err)
	}

	// Guessed document IDs cannot be retried, promoted, or deleted through the
	// shared conversation/project APIs while their file is another user's draft.
	retryReq := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/documents/doc-owner-draft/retry", nil)
	retryCtx := context.WithValue(retryReq.Context(), pathCtxKey{}, map[string]string{"id": "c1", "docId": "doc-owner-draft"})
	retryCtx = context.WithValue(retryCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	retryReq = retryReq.WithContext(retryCtx)
	retryRec := httptest.NewRecorder()
	retryConversationDocumentHandler(deps, retryRec, retryReq)
	if retryRec.Code != http.StatusNotFound {
		t.Fatalf("member retry owner draft doc status=%d body=%s; want 404", retryRec.Code, retryRec.Body.String())
	}
	promoteReq := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/documents/doc-owner-draft/promote", nil)
	promoteCtx := context.WithValue(promoteReq.Context(), pathCtxKey{}, map[string]string{"id": "c1", "docId": "doc-owner-draft"})
	promoteCtx = context.WithValue(promoteCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	promoteReq = promoteReq.WithContext(promoteCtx)
	promoteRec := httptest.NewRecorder()
	promoteDocumentHandler(deps, promoteRec, promoteReq)
	if promoteRec.Code != http.StatusNotFound {
		t.Fatalf("member promote owner draft doc status=%d body=%s; want 404", promoteRec.Code, promoteRec.Body.String())
	}
	deleteKBReq := httptest.NewRequest(http.MethodDelete, "/api/kbs/kb2/documents/doc-owner-kb-draft", nil)
	deleteKBCtx := context.WithValue(deleteKBReq.Context(), pathCtxKey{}, map[string]string{"id": "kb2", "docId": "doc-owner-kb-draft"})
	deleteKBCtx = context.WithValue(deleteKBCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	deleteKBReq = deleteKBReq.WithContext(deleteKBCtx)
	deleteKBRec := httptest.NewRecorder()
	deleteKBDocHandler(deps, deleteKBRec, deleteKBReq)
	if deleteKBRec.Code != http.StatusNotFound {
		t.Fatalf("member delete owner KB draft doc status=%d body=%s; want 404", deleteKBRec.Code, deleteKBRec.Body.String())
	}

	// Private download and delete routes both use the same scoped GetFile gate.
	draftRec := httptest.NewRecorder()
	draftReqReq := httptest.NewRequest(http.MethodGet, "/api/files/owner-draft", nil)
	draftCtx := context.WithValue(draftReqReq.Context(), pathCtxKey{}, map[string]string{"id": "owner-draft"})
	draftCtx = context.WithValue(draftCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	draftReqReq = draftReqReq.WithContext(draftCtx)
	downloadFileHandler(deps, draftRec, draftReqReq)
	if draftRec.Code != http.StatusNotFound {
		t.Fatalf("member download owner draft status=%d body=%s; want 404", draftRec.Code, draftRec.Body.String())
	}
	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/conversations/c1/files/owner-draft", nil)
	deleteCtx := context.WithValue(deleteReq.Context(), pathCtxKey{}, map[string]string{"id": "c1", "fileId": "owner-draft"})
	deleteCtx = context.WithValue(deleteCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	deleteReq = deleteReq.WithContext(deleteCtx)
	deleteConversationFileHandler(deps, deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("member delete owner draft status=%d body=%s; want 404", deleteRec.Code, deleteRec.Body.String())
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE id='owner-draft'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("owner draft after member delete: count=%d err=%v", count, err)
	}

	if _, err := normalizeConversationAttachments(ctx, db, "c1", "member", []llm.Attachment{{ID: "owner-draft"}}); !errors.Is(err, errAttachmentUnavailable) {
		t.Fatalf("member normalize owner draft = %v; want attachment unavailable", err)
	}
	normalized, err := normalizeConversationAttachments(ctx, db, "c1", "member", []llm.Attachment{{ID: "owner-committed", Filename: "forged.txt", Kind: "image"}})
	if err != nil || len(normalized) != 1 || normalized[0].Filename != "owner-committed.txt" {
		t.Fatalf("member normalize committed = %#v, %v; want authoritative committed metadata", normalized, err)
	}

	// Preflight must require only this member's unsent draft. The owner's draft
	// is intentionally omitted from the request and must not cause a cross-user
	// denial of service.
	if err := ensureAttachedDocumentsReadyForUser(ctx, db, "c1", "member", []llm.Attachment{{ID: "member-draft"}}); err != nil {
		t.Fatalf("member preflight with own draft: %v", err)
	}
	if err := ensureAttachedDocumentsReadyForUser(ctx, db, "c1", "member", nil); err == nil || !strings.Contains(err.Error(), "unsent attachments") {
		t.Fatalf("member preflight without own draft = %v; want unsent-attachment error", err)
	}
	if err := ensureAttachedDocumentsReadyForUser(ctx, db, "c1", "owner", []llm.Attachment{{ID: "owner-draft"}}); err != nil {
		t.Fatalf("owner preflight with own draft: %v", err)
	}

	// Once the uploader persists the user message, the same row becomes an
	// ordinary collaborative attachment and the member may download it.
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ID:             "owner-submit",
		ConversationID: "c1",
		Role:           "user",
		AuthorID:       "owner",
		Attachments:    json.RawMessage(`[{"id":"owner-draft"}]`),
	}); err != nil {
		t.Fatalf("owner submit draft: %v", err)
	}
	committedReq := httptest.NewRequest(http.MethodGet, "/api/files/owner-draft", nil)
	committedCtx := context.WithValue(committedReq.Context(), pathCtxKey{}, map[string]string{"id": "owner-draft"})
	committedCtx = context.WithValue(committedCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	committedReq = committedReq.WithContext(committedCtx)
	committedRec := httptest.NewRecorder()
	downloadFileHandler(deps, committedRec, committedReq)
	if committedRec.Code != http.StatusOK || committedRec.Body.String() != "owner draft" {
		t.Fatalf("member download committed owner file status=%d body=%q", committedRec.Code, committedRec.Body.String())
	}
	if rec, docs := listDocs("member"); rec.Code != http.StatusOK || len(docs) != 1 || docs[0].ID != "doc-owner-draft" {
		t.Fatalf("member conversation docs after commit status=%d docs=%#v; want shared", rec.Code, docs)
	}
	listKBReq = httptest.NewRequest(http.MethodGet, "/api/kbs/kb2/documents", nil)
	listKBCtx = context.WithValue(listKBReq.Context(), pathCtxKey{}, map[string]string{"id": "kb2"})
	listKBCtx = context.WithValue(listKBCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	listKBReq = listKBReq.WithContext(listKBCtx)
	listKBRec = httptest.NewRecorder()
	listKBDocsHandler(deps, listKBRec, listKBReq)
	kbDocs = nil
	if err := json.Unmarshal(listKBRec.Body.Bytes(), &kbDocs); err != nil || listKBRec.Code != http.StatusOK || len(kbDocs) != 1 || kbDocs[0].ID != "doc-owner-kb-draft" {
		t.Fatalf("member KB docs after commit status=%d docs=%#v decode=%v", listKBRec.Code, kbDocs, err)
	}
	listProjectReq = httptest.NewRequest(http.MethodGet, "/api/projects/p1/documents", nil)
	listProjectCtx = context.WithValue(listProjectReq.Context(), pathCtxKey{}, map[string]string{"id": "p1"})
	listProjectCtx = context.WithValue(listProjectCtx, userCtxKey{}, &store.User{ID: "member", Role: "user", Status: "active"})
	listProjectReq = listProjectReq.WithContext(listProjectCtx)
	listProjectRec = httptest.NewRecorder()
	listProjectDocsHandler(deps, listProjectRec, listProjectReq)
	projectDocs = nil
	if err := json.Unmarshal(listProjectRec.Body.Bytes(), &projectDocs); err != nil || listProjectRec.Code != http.StatusOK || len(projectDocs) != 1 || projectDocs[0].ID != "doc-owner-project-draft" {
		t.Fatalf("member project docs after commit status=%d docs=%#v decode=%v", listProjectRec.Code, projectDocs, err)
	}
}
