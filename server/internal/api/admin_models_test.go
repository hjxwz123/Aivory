package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aurelia/server/internal/config"
)

func TestCreateModelReqTracksExplicitResearchEnabled(t *testing.T) {
	var disabled createModelReq
	if err := json.Unmarshal([]byte(`{"channel_id":"ch1","request_id":"m1","label":"M1","research_enabled":false}`), &disabled); err != nil {
		t.Fatalf("unmarshal disabled: %v", err)
	}
	if disabled.ResearchEnabled == nil {
		t.Fatal("expected explicit research_enabled=false to be tracked")
	}
	if *disabled.ResearchEnabled {
		t.Fatal("expected explicit research_enabled=false to decode as false")
	}

	var omitted createModelReq
	if err := json.Unmarshal([]byte(`{"channel_id":"ch1","request_id":"m2","label":"M2"}`), &omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if omitted.ResearchEnabled != nil {
		t.Fatal("expected omitted research_enabled to stay nil")
	}
}

func TestLockedEmbeddingModelCannotChangeVectorIdentityOrBeDeleted(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"emb1"')`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		updateModelAdmin(d, w, r)
	})
	mx.handle(http.MethodDelete, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteModelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/emb1", strings.NewReader(`{"request_id":"text-embedding-3-large"}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("locked model update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var reqID string
	mustQuery(t, db, `SELECT request_id FROM models WHERE id='emb1'`).Scan(&reqID)
	if reqID != "text-embedding-3-small" {
		t.Fatalf("locked embedding model request_id changed: %q", reqID)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/models/emb1", nil)
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("locked model delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var count int
	mustQuery(t, db, `SELECT COUNT(*) FROM models WHERE id='emb1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("locked embedding model was deleted")
	}
}

func TestConfigImportCannotOverwriteLockedEmbeddingModelRow(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-model-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"emb1"')`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(mw).Encode(configManifest{Format: "aurelia-config", Version: configArchiveVersion, Tables: []string{"models"}, MergeMode: "upsert"}); err != nil {
		t.Fatal(err)
	}
	models, err := zw.Create("db/models.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := models.Write([]byte(`{"id":"emb1","channel_id":"ch1","kind":"embedding","request_id":"text-embedding-3-large","label":"Emb","enabled":1,"dim":3072}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	body, contentType := multipartArchive(t, archive.Bytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/config/import", body)
	req.Header.Set("content-type", contentType)
	importConfigAdmin(d, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("config import status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var reqID string
	mustQuery(t, db, `SELECT request_id FROM models WHERE id='emb1'`).Scan(&reqID)
	if reqID != "text-embedding-3-small" {
		t.Fatalf("config import changed locked embedding model row: %q", reqID)
	}
}
