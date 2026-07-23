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
	"strconv"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
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

func TestAdminModelOfficialToolsDefaultsValidationAndPublicMasking(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-official-tools.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES
		('ch_responses','OpenAI Responses','openai','responses','https://api.example','sk',1),
		('ch_vendor','Vendor','claude','','https://vendor.example','sk',1)`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) {
		createModelAdmin(d, w, r)
	})
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		updateModelAdmin(d, w, r)
	})
	mx.handle(http.MethodGet, "/api/models", func(w http.ResponseWriter, r *http.Request) {
		listModelsHandler(d, w, r)
	})

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/models", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		mx.ServeHTTP(rec, req)
		return rec
	}
	decodeModel := func(rec *httptest.ResponseRecorder) store.Model {
		t.Helper()
		var model store.Model
		if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
			t.Fatalf("decode model: %v, body=%s", err, rec.Body.String())
		}
		return model
	}

	// Omission on a newly-created OpenAI Responses model installs the three
	// historical hosted-tool definitions, including their full request objects.
	rec := post(`{"channel_id":"ch_responses","request_id":"gpt-default-tools","label":"Defaults"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("default create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	defaultModel := decodeModel(rec)
	defaultTools, err := store.ParseOfficialTools(defaultModel.OfficialTools)
	if err != nil || len(defaultTools) != 3 {
		t.Fatalf("default official tools = %+v, err=%v", defaultTools, err)
	}
	if defaultTools[0].Name != "web_search" || !strings.Contains(string(defaultTools[0].Request), `"search_context_size":"medium"`) {
		t.Fatalf("default web search definition = %+v", defaultTools[0])
	}

	// The Responses defaults are chat capabilities. Image and embedding rows may
	// share the same channel, but must not acquire hidden, unusable chat tools.
	for _, kind := range []string{"image", "embedding"} {
		rec = post(`{"channel_id":"ch_responses","kind":"` + kind + `","request_id":"gpt-` + kind + `","label":"` + kind + `"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s create status = %d, body=%s", kind, rec.Code, rec.Body.String())
		}
		nonChatModel := decodeModel(rec)
		if string(nonChatModel.OfficialTools) != "[]" {
			t.Fatalf("%s model received chat official tools: %s", kind, nonChatModel.OfficialTools)
		}
	}

	// Explicit [] is distinct from omission and keeps hosted tools disabled.
	rec = post(`{"channel_id":"ch_responses","request_id":"gpt-no-tools","label":"No tools","official_tools":[]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("explicit empty create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	emptyModel := decodeModel(rec)
	if string(emptyModel.OfficialTools) != "[]" {
		t.Fatalf("explicit empty tools = %s", emptyModel.OfficialTools)
	}

	// Hosted definitions are provider-agnostic, and legacy string arrays are
	// accepted at the API boundary then persisted in canonical object form.
	rec = post(`{"channel_id":"ch_vendor","request_id":"vendor-tools","label":"Vendor tools","official_tools":["vendor_lookup",{"name":"maps","icon":"map","request":{"kind":"maps","options":{"region":"cn"}}}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("custom create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	customModel := decodeModel(rec)
	customTools, err := store.ParseOfficialTools(customModel.OfficialTools)
	if err != nil || len(customTools) != 2 || customTools[0].Name != "vendor_lookup" || customTools[1].Name != "maps" {
		t.Fatalf("custom official tools = %+v, err=%v", customTools, err)
	}
	var persisted string
	if err := db.QueryRow(`SELECT official_tools FROM models WHERE id=?`, customModel.ID).Scan(&persisted); err != nil {
		t.Fatalf("read persisted custom tools: %v", err)
	}
	if !strings.Contains(persisted, `"name":"vendor_lookup"`) || strings.HasPrefix(persisted, `["`) {
		t.Fatalf("legacy tools were not canonicalized: %s", persisted)
	}

	for index, officialTools := range []string{
		`null`,
		`{}`,
		`[{"name":"missing-request","icon":"wrench"}]`,
		`[{"name":"bad-request","icon":"wrench","request":[]}]`,
		`[{"name":"same","icon":"wrench","request":{}},{"name":"same","icon":"wrench","request":{}}]`,
	} {
		body := `{"channel_id":"ch_vendor","request_id":"invalid-` + strconv.Itoa(index) + `","label":"Invalid","official_tools":` + officialTools + `}`
		rec = post(body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "official_tools must be a JSON array of tool definitions") {
			t.Fatalf("invalid official_tools %s: status=%d body=%s", officialTools, rec.Code, rec.Body.String())
		}
	}

	// PATCH omission preserves the configured list; an explicit replacement is
	// validated through the same normalizer.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+customModel.ID, strings.NewReader(`{"label":"Vendor tools renamed"}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("omitted patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var afterOmitted string
	if err := db.QueryRow(`SELECT official_tools FROM models WHERE id=?`, customModel.ID).Scan(&afterOmitted); err != nil {
		t.Fatalf("read after omitted patch: %v", err)
	}
	if afterOmitted != persisted {
		t.Fatalf("omitted PATCH changed official tools:\nbefore=%s\nafter=%s", persisted, afterOmitted)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+customModel.ID, strings.NewReader(`{"official_tools":[{"name":"replacement","icon":"sparkles","request":{"type":"replacement"}}]}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replacement patch status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// Public model metadata contains only name/icon. The provider request template
	// remains available to admins but never crosses this response boundary.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/models", nil)
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public models status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var public struct {
		Models []struct {
			ID            string                       `json:"id"`
			Enabled       bool                         `json:"enabled"`
			OfficialTools []map[string]json.RawMessage `json:"official_tools"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode public models: %v", err)
	}
	found := false
	for _, model := range public.Models {
		if model.ID != customModel.ID {
			continue
		}
		found = true
		if !model.Enabled {
			t.Fatal("public model omitted its enabled state, so the client cannot honor the configured default")
		}
		if len(model.OfficialTools) != 1 {
			t.Fatalf("public official tools = %+v", model.OfficialTools)
		}
		if _, leaked := model.OfficialTools[0]["request"]; leaked {
			t.Fatalf("public model leaked request JSON: %+v", model.OfficialTools[0])
		}
		if _, ok := model.OfficialTools[0]["name"]; !ok {
			t.Fatalf("public model omitted tool name: %+v", model.OfficialTools[0])
		}
		if _, ok := model.OfficialTools[0]["icon"]; !ok {
			t.Fatalf("public model omitted tool icon: %+v", model.OfficialTools[0])
		}
	}
	if !found {
		t.Fatalf("public response omitted model %q", customModel.ID)
	}
}

func TestAdminModelExtraParamsValidationPersistenceAndPublicMasking(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-extra-params.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Main','openai','chat','https://api.example','sk',1)`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) {
		createModelAdmin(d, w, r)
	})
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		updateModelAdmin(d, w, r)
	})
	mx.handle(http.MethodGet, "/api/models", func(w http.ResponseWriter, r *http.Request) {
		listModelsHandler(d, w, r)
	})

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/models", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		mx.ServeHTTP(rec, req)
		return rec
	}
	for _, body := range []string{
		`{"channel_id":"ch1","request_id":"bad-array","label":"Bad","extra_params":[]}`,
		`{"channel_id":"ch1","request_id":"bad-scalar","label":"Bad","extra_params":true}`,
		`{"channel_id":"ch1","request_id":"bad-null","label":"Bad","extra_params":null}`,
	} {
		rec := post(body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid extra_params status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "extra_params must be a JSON object") {
			t.Fatalf("invalid extra_params error = %s", rec.Body.String())
		}
	}
	rec := post(`{"channel_id":"ch1","kind":"image","request_id":"image-extra","label":"Image","extra_params":{"quality":"high"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "only supported for chat models") {
		t.Fatalf("non-chat extra_params status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = post(`{"channel_id":"ch1","kind":"image","request_id":"image-default","label":"Image default"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("non-chat default extra_params status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = post(`{"channel_id":"ch1","request_id":"gpt-extra","label":"Extra","extra_params":{"temperature":0.25,"vendor":{"mode":"strict"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID          string         `json:"id"`
		ExtraParams map[string]any `json:"extra_params"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.ExtraParams["temperature"] != 0.25 {
		t.Fatalf("created model did not preserve extra_params: %+v", created)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+created.ID, strings.NewReader(`{"extra_params":{"top_p":0.8}}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var persisted string
	if err := db.QueryRow(`SELECT extra_params FROM models WHERE id=?`, created.ID).Scan(&persisted); err != nil {
		t.Fatalf("read persisted extra_params: %v", err)
	}
	if persisted != `{"top_p":0.8}` {
		t.Fatalf("persisted extra_params = %s", persisted)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/models", nil)
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public models status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "extra_params") {
		t.Fatalf("public model response leaked admin-only extra_params: %s", rec.Body.String())
	}
}

func TestPatchModelKindClearsInheritedExtraParamsWhenOmitted(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-extra-params-kind-patch.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Main','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,extra_params) VALUES('m1','ch1','chat','gpt-extra','Extra',1,'{"temperature":0.25}')`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		updateModelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/m1", strings.NewReader(`{"kind":"image"}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat -> image patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var kind, extraParams string
	if err := db.QueryRow(`SELECT kind, extra_params FROM models WHERE id='m1'`).Scan(&kind, &extraParams); err != nil {
		t.Fatalf("read patched model: %v", err)
	}
	if kind != "image" || extraParams != "{}" {
		t.Fatalf("chat -> image should clear inherited extras: kind=%q extra_params=%q", kind, extraParams)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/admin/models/m1", strings.NewReader(`{"extra_params":{"quality":"high"}}`))
	req.Header.Set("content-type", "application/json")
	mx.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "only supported for chat models") {
		t.Fatalf("explicit non-chat extras status = %d, body=%s", rec.Code, rec.Body.String())
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
	if err := json.NewEncoder(mw).Encode(configManifest{Format: "aivory-config", Version: configArchiveVersion, Tables: []string{"models"}, MergeMode: "upsert"}); err != nil {
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
