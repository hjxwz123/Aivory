package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestKnowledgeBaseShareCandidatesHTTPRequiresExactEmail(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-share-candidates.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('candidate-owner','owner@example.test','Owner','h','admin','active'),
		('candidate-match','match@example.test','Matching Person','h','user','active'),
		('candidate-other','other@example.test','Other Person','h','user','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('candidate-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('candidate-embedding','candidate-channel','embedding','embed','Embedding',1,3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim)
		VALUES('candidate-kb','candidate-owner','Private library','candidate-embedding',3)`)

	owner, err := store.FindUserByID(context.Background(), db, "candidate-owner")
	if err != nil {
		t.Fatal(err)
	}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/kbs/:id/share-candidates", wrap(Deps{DB: db}, listKBShareCandidatesHandler))
	mx.handle(http.MethodPut, "/api/kbs/:id/shares", wrap(Deps{DB: db}, upsertKBShareHandler))

	request := func(t *testing.T, search *string) []store.KnowledgeBaseShare {
		t.Helper()
		path := "/api/kbs/candidate-kb/share-candidates"
		if search != nil {
			path += "?search=" + url.QueryEscape(*search)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, owner))
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var rows []store.KnowledgeBaseShare
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		return rows
	}

	partial := "match"
	if rows := request(t, nil); len(rows) != 0 {
		t.Fatalf("missing search returned users=%+v, want none", rows)
	}
	if rows := request(t, &partial); len(rows) != 0 {
		t.Fatalf("partial search returned users=%+v, want none", rows)
	}
	exact := "  MATCH@EXAMPLE.TEST  "
	rows := request(t, &exact)
	if len(rows) != 1 || rows[0].UserID != "candidate-match" {
		t.Fatalf("exact email returned users=%+v, want candidate-match", rows)
	}

	putShare := func(t *testing.T, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/kbs/candidate-kb/shares", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, owner))
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	status, body := putShare(t, `{"user_id":"candidate-match","role":"read"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("user-id-only share status=%d body=%s, want 400", status, body)
	}
	var shareCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_base_shares WHERE kb_id='candidate-kb'`).Scan(&shareCount); err != nil {
		t.Fatal(err)
	}
	if shareCount != 0 {
		t.Fatalf("user-id-only request created %d shares, want none", shareCount)
	}

	status, body = putShare(t, `{"email":"match@example","role":"read"}`)
	if status != http.StatusNotFound {
		t.Fatalf("partial-email share status=%d body=%s, want 404", status, body)
	}

	status, body = putShare(t, `{"email":"  MATCH@EXAMPLE.TEST  ","role":"read"}`)
	if status != http.StatusOK {
		t.Fatalf("exact-email share status=%d body=%s, want 200", status, body)
	}
	var share store.KnowledgeBaseShare
	if err := json.Unmarshal(body, &share); err != nil {
		t.Fatalf("decode exact-email share %q: %v", body, err)
	}
	if share.UserID != "candidate-match" || share.Email != "match@example.test" || share.Role != "read" {
		t.Fatalf("exact-email share=%+v, want candidate-match read", share)
	}
}
