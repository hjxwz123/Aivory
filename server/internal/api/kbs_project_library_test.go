package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
	var listed []store.KnowledgeBase
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode KB list: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "standalone-kb" {
		t.Fatalf("listed KBs=%#v, want only standalone-kb", listed)
	}

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
