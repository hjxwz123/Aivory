package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminModelMCPDefaultsOmittedEmptyNullAndInvalidSemantics(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-mcp-defaults.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Main','openai','chat','https://api.example','sk',1)`)
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) { listModelsAdmin(d, w, r) })
	mx.handle(http.MethodPost, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) { createModelAdmin(d, w, r) })
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) { updateModelAdmin(d, w, r) })

	post := func(body string) (*httptest.ResponseRecorder, store.Model) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/admin/models", strings.NewReader(body))
		request.Header.Set("content-type", "application/json")
		mx.ServeHTTP(recorder, request)
		var model store.Model
		_ = json.Unmarshal(recorder.Body.Bytes(), &model)
		return recorder, model
	}

	recorder, defaultModel := post(`{"channel_id":"ch1","request_id":"default","label":"Default"}`)
	if recorder.Code != http.StatusCreated || string(defaultModel.MCPServerIDs) != "null" {
		t.Fatalf("omitted create status=%d mcp_server_ids=%s body=%s", recorder.Code, defaultModel.MCPServerIDs, recorder.Body.String())
	}
	recorder, configuredModel := post(`{"channel_id":"ch1","request_id":"configured","label":"Configured","mcp_server_ids":[" rail ","offline","rail"]}`)
	if recorder.Code != http.StatusCreated || string(configuredModel.MCPServerIDs) != `["rail","offline"]` {
		t.Fatalf("configured create status=%d mcp_server_ids=%s body=%s", recorder.Code, configuredModel.MCPServerIDs, recorder.Body.String())
	}
	recorder, noneModel := post(`{"channel_id":"ch1","request_id":"none","label":"None","mcp_server_ids":[]}`)
	if recorder.Code != http.StatusCreated || string(noneModel.MCPServerIDs) != "[]" {
		t.Fatalf("empty create status=%d mcp_server_ids=%s body=%s", recorder.Code, noneModel.MCPServerIDs, recorder.Body.String())
	}
	if recorder, _ = post(`{"channel_id":"ch1","request_id":"invalid","label":"Invalid","mcp_server_ids":{}}`); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "mcp_server_ids") {
		t.Fatalf("invalid create status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	patch := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+noneModel.ID, strings.NewReader(body))
		request.Header.Set("content-type", "application/json")
		mx.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder = patch(`{"label":"Still none"}`); recorder.Code != http.StatusOK {
		t.Fatalf("omitted patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored, err := store.GetModel(t.Context(), db, noneModel.ID)
	if err != nil || string(stored.MCPServerIDs) != "[]" {
		t.Fatalf("omitted patch changed policy to %s, err=%v", stored.MCPServerIDs, err)
	}
	if recorder = patch(`{"mcp_server_ids":null}`); recorder.Code != http.StatusOK {
		t.Fatalf("null default-off reset status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored, err = store.GetModel(t.Context(), db, noneModel.ID)
	if err != nil || stored.MCPServerIDs != nil {
		t.Fatalf("null patch did not reset default-off: %s, err=%v", stored.MCPServerIDs, err)
	}
	if recorder = patch(`{"mcp_server_ids":[""]}`); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "mcp_server_ids") {
		t.Fatalf("invalid patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	mx.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/models", nil))
	var listed []store.Model
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &listed) != nil {
		t.Fatalf("admin list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	listedPolicies := map[string]string{}
	for _, model := range listed {
		listedPolicies[model.ID] = string(model.MCPServerIDs)
	}
	if listedPolicies[defaultModel.ID] != "null" || listedPolicies[configuredModel.ID] != `["rail","offline"]` || listedPolicies[noneModel.ID] != "null" {
		t.Fatalf("admin list lost MCP defaults: %+v", listedPolicies)
	}
}
