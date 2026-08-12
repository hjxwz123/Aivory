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

func TestUpdateConversationRejectsIncompatibleKnowledgeBases(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "conversation-kb-compatibility.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('u1','u1@example.test','h','user'),
		('u2','u2@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES
		('emb-a','ch1','embedding','emb-a','Embedding A',3),
		('emb-b','ch1','embedding','emb-b','Embedding B',3)`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Conversation')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('kb-a','u1','A','emb-a',3),
		('kb-same','u1','Same signature','emb-a',3),
		('kb-model','u1','Different model','emb-b',3),
		('kb-dim','u1','Different dimension','emb-a',4),
		('kb-project','u1','Project index','emb-b',3),
		('kb-unauthorized','u2','Other user','emb-b',99)`)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name,kb_id) VALUES('project-b','u1','Project B','kb-project')`)

	patchKBs := func(ids ...string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"kb_ids": ids})
		if err != nil {
			t.Fatalf("marshal patch: %v", err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/conversations/c1", strings.NewReader(string(body)))
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		updateConversationHandler(Deps{DB: db}, rec, req)
		return rec
	}
	storedKBIDs := func() []string {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT kb_ids FROM conversations WHERE id='c1'`).Scan(&raw); err != nil {
			t.Fatalf("read stored kb_ids: %v", err)
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			t.Fatalf("decode stored kb_ids %q: %v", raw, err)
		}
		return ids
	}
	patchProject := func(projectID string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"project_id": projectID})
		if err != nil {
			t.Fatalf("marshal project patch: %v", err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/conversations/c1", strings.NewReader(string(body)))
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
		ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		updateConversationHandler(Deps{DB: db}, rec, req)
		return rec
	}
	storedProjectID := func() string {
		t.Helper()
		var projectID string
		if err := db.QueryRow(`SELECT COALESCE(project_id,'') FROM conversations WHERE id='c1'`).Scan(&projectID); err != nil {
			t.Fatalf("read stored project_id: %v", err)
		}
		return projectID
	}

	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{name: "different model ids", ids: []string{"kb-a", "kb-model"}},
		{name: "same model but different indexed dimensions", ids: []string{"kb-a", "kb-dim"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := patchKBs(tc.ids...)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), errKnowledgeBaseSelectionIncompatible.Error()) {
				t.Fatalf("body=%s, want generic compatibility error", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "embedding") {
				t.Fatalf("body=%s exposes retrieval implementation details", rec.Body.String())
			}
			if got := storedKBIDs(); len(got) != 0 {
				t.Fatalf("rejected patch changed kb_ids to %v", got)
			}
		})
	}

	t.Run("project knowledge base participates in both update directions", func(t *testing.T) {
		if rec := patchProject("project-b"); rec.Code != http.StatusOK {
			t.Fatalf("attach project status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if rec := patchKBs("kb-a"); rec.Code != http.StatusConflict {
			t.Fatalf("KB patch against project status=%d body=%s, want 409", rec.Code, rec.Body.String())
		}
		if got := storedKBIDs(); len(got) != 0 {
			t.Fatalf("rejected KB patch changed kb_ids to %v", got)
		}
		if rec := patchProject(""); rec.Code != http.StatusOK {
			t.Fatalf("detach project status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if rec := patchKBs("kb-a"); rec.Code != http.StatusOK {
			t.Fatalf("attach baseline KB status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if rec := patchProject("project-b"); rec.Code != http.StatusConflict {
			t.Fatalf("project patch against KB status=%d body=%s, want 409", rec.Code, rec.Body.String())
		}
		if got := storedProjectID(); got != "" {
			t.Fatalf("rejected project patch changed project_id to %q", got)
		}
	})

	t.Run("same model and dimension is allowed", func(t *testing.T) {
		rec := patchKBs("kb-a", "kb-same")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if got := stringSet(storedKBIDs()); !got["kb-a"] || !got["kb-same"] || len(got) != 2 {
			t.Fatalf("stored kb_ids=%v, want both compatible KBs", got)
		}
	})

	t.Run("unauthorized ids are filtered before compatibility validation", func(t *testing.T) {
		rec := patchKBs("kb-a", "kb-unauthorized")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
		}
		got := storedKBIDs()
		if len(got) != 1 || got[0] != "kb-a" {
			t.Fatalf("stored kb_ids=%v, want only authorized kb-a", got)
		}
	})
}

func TestMessageEndpointsHideKnowledgeBaseCompatibilityDetails(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-kb-compatibility-response.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('u1','u1@example.test','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES
		('index-a','ch1','embedding','index-a','Index A',3),
		('index-b','ch1','embedding','index-b','Index B',3)`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Conversation')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('kb-a','u1','A','index-a',3),
		('kb-b','u1','B','index-b',3)`)

	user := &store.User{ID: "u1", Role: "user", Status: "active"}
	request := func(target, body string, endpoint handler) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"})
		ctx = context.WithValue(ctx, userCtxKey{}, user)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		endpoint(Deps{DB: db}, rec, req)
		return rec
	}
	assertGenericConflict := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, errKnowledgeBaseSelectionIncompatible.Error()) || strings.Contains(body, "embedding") {
			t.Fatalf("response exposes retrieval implementation: %s", body)
		}
	}

	assertGenericConflict(request(
		"/api/conversations/c1/messages",
		`{"text":"Question","kb_ids":["kb-a","kb-b"]}`,
		postMessageHandler,
	))
	assertGenericConflict(request(
		"/api/conversations/c1/regenerate",
		`{"assistant_id":"assistant-1","kb_ids":["kb-a","kb-b"]}`,
		regenerateHandler,
	))

	var messageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&messageCount); err != nil || messageCount != 0 {
		t.Fatalf("rejected requests persisted %d messages, err=%v", messageCount, err)
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
