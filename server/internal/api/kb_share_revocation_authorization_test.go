package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestUnauthorizedKnowledgeBaseShareDeleteHasNoRevocationSideEffects(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "kb-share-revoke-authorization.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('share-owner','share-owner@example.test','Owner','h','admin','active'),
		('share-member','share-member@example.test','Member','h','admin','active'),
		('share-attacker','share-attacker@example.test','Attacker','h','admin','active')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('share-embedding-channel','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES
		('share-embedding','share-embedding-channel','embedding','embed','Embedding',1,3)`)
	kb, err := store.CreateKB(context.Background(), db, store.KnowledgeBase{
		ID: "share-revocation-kb", UserID: "share-owner", Name: "Private library",
		EmbeddingModelID: "share-embedding", EmbeddingDim: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertKnowledgeBaseShare(
		context.Background(), db, kb.ID, "share-owner", "share-member@example.test", "read",
	); err != nil {
		t.Fatal(err)
	}
	attacker, err := store.FindUserByID(context.Background(), db, "share-attacker")
	if err != nil {
		t.Fatal(err)
	}
	memoryCache := cache.NewMemory()
	d := Deps{DB: db, Cache: memoryCache}
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/kbs/:id/shares/:uid", wrap(d, deleteKBShareHandler))

	req := httptest.NewRequest(http.MethodDelete, "/api/kbs/"+kb.ID+"/shares/share-member", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, attacker))
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := currentKnowledgeBaseUserAccessEpoch(d, kb.ID, "share-member"); got != "0" {
		t.Fatalf("unauthorized request advanced access epoch to %q", got)
	}
	if _, revoked := memoryCache.Get(knowledgeBaseUserGenerationRevocationKey(kb.ID, "share-member", "0")); revoked {
		t.Fatal("unauthorized request installed a generation tombstone")
	}
	var shares int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_base_shares WHERE kb_id=? AND user_id='share-member'`, kb.ID).Scan(&shares); err != nil {
		t.Fatal(err)
	}
	if shares != 1 {
		t.Fatalf("unauthorized request changed share count to %d", shares)
	}
}
