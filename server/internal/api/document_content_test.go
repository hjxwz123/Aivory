package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestDocumentContentEnforcesKnowledgeBaseAccess(t *testing.T) {
	root := t.TempDir()
	db := openMigrated(t, filepath.Join(root, "document-content.db"))
	defer db.Close()

	for _, userID := range []string{"owner", "member", "outsider"} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?, 'h','user')`, userID, userID+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb1','ch1','embedding','emb','Embedding',3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('kb1','owner','KB','emb1',3,'ws1')`)

	path := filepath.Join(root, "source.txt")
	writeFile(t, path, []byte("authorized knowledge"))
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('doc1','kb1','source.txt','text/plain',20,'ready',?)`, path)

	deps := Deps{DB: db, Config: config.Config{UploadDir: root}}
	request := func(userID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/documents/doc1/content", nil)
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "doc1"}))
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"}))
		rec := httptest.NewRecorder()
		documentContentHandler(deps, rec, req)
		return rec
	}

	for _, userID := range []string{"owner", "member"} {
		rec := request(userID)
		if rec.Code != http.StatusOK || rec.Body.String() != "authorized knowledge" {
			t.Fatalf("%s content status=%d body=%q, want authorized bytes", userID, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s X-Content-Type-Options=%q, want nosniff", userID, got)
		}
	}

	if rec := request("outsider"); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider content status=%d body=%q, want 404", rec.Code, rec.Body.String())
	}

	mustExec(t, db, `DELETE FROM workspace_members WHERE workspace_id='ws1' AND user_id='member'`)
	if rec := request("member"); rec.Code != http.StatusNotFound {
		t.Fatalf("revoked member content status=%d body=%q, want 404", rec.Code, rec.Body.String())
	}
}
