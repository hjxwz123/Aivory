package api

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/store"
	toolpkg "aivory/server/internal/tools"
)

func TestListBuiltinToolsAdminUsesLiveSortedRegistry(t *testing.T) {
	registry := toolpkg.NewRegistry(nil, nil, config.Config{}, log.New(io.Discard, "", 0))
	rec := httptest.NewRecorder()
	listBuiltinToolsAdmin(Deps{Tools: registry}, rec, httptest.NewRequest(http.MethodGet, "/api/admin/tools/builtins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var items []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("live registry returned no built-in tools")
	}
	names := make([]string, len(items))
	for i, item := range items {
		if item.Name == "" || item.Description == "" {
			t.Fatalf("incomplete item: %+v", item)
		}
		names[i] = item.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("tools are not sorted: %v", names)
	}

	rec = httptest.NewRecorder()
	listBuiltinToolsAdmin(Deps{}, rec, httptest.NewRequest(http.MethodGet, "/api/admin/tools/builtins", nil))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("nil registry response = %s", rec.Body.String())
	}
}

func TestBuiltinToolsAdminRouteRequiresAuthentication(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "builtin-tools-route.db"))
	defer db.Close()
	rec := httptest.NewRecorder()
	NewRouter(Deps{DB: db}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/tools/builtins", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminModelBuiltinToolsOmittedEmptyAndNullSemantics(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-builtin-tools.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Main','openai','chat','https://api.example','sk',1)`)
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) { listModelsAdmin(d, w, r) })
	mx.handle(http.MethodPost, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) { createModelAdmin(d, w, r) })
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) { updateModelAdmin(d, w, r) })

	post := func(body string) (*httptest.ResponseRecorder, store.Model) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/models", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		mx.ServeHTTP(rec, req)
		var model store.Model
		_ = json.Unmarshal(rec.Body.Bytes(), &model)
		return rec, model
	}

	rec, defaultModel := post(`{"channel_id":"ch1","request_id":"default","label":"Default"}`)
	if rec.Code != http.StatusCreated || string(defaultModel.BuiltinTools) != "null" {
		t.Fatalf("omitted create status=%d builtin_tools=%s body=%s", rec.Code, defaultModel.BuiltinTools, rec.Body.String())
	}
	rec, nullModel := post(`{"channel_id":"ch1","request_id":"null","label":"Null","builtin_tools":null}`)
	if rec.Code != http.StatusCreated || string(nullModel.BuiltinTools) != "null" {
		t.Fatalf("null create status=%d builtin_tools=%s body=%s", rec.Code, nullModel.BuiltinTools, rec.Body.String())
	}
	rec, noneModel := post(`{"channel_id":"ch1","request_id":"none","label":"None","builtin_tools":[]}`)
	if rec.Code != http.StatusCreated || string(noneModel.BuiltinTools) != "[]" {
		t.Fatalf("empty create status=%d builtin_tools=%s body=%s", rec.Code, noneModel.BuiltinTools, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/models", nil))
	var listed []store.Model
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil {
		t.Fatalf("admin list status=%d body=%s", rec.Code, rec.Body.String())
	}
	listedPolicies := map[string]string{}
	for _, model := range listed {
		listedPolicies[model.ID] = string(model.BuiltinTools)
	}
	if listedPolicies[defaultModel.ID] != "null" || listedPolicies[noneModel.ID] != "[]" {
		t.Fatalf("admin list lost nullable policy: %+v", listedPolicies)
	}

	patch := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/"+noneModel.ID, strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		mx.ServeHTTP(recorder, req)
		return recorder
	}
	if rec = patch(`{"label":"Still none"}`); rec.Code != http.StatusOK {
		t.Fatalf("omitted patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := store.GetModel(t.Context(), db, noneModel.ID)
	if err != nil || string(stored.BuiltinTools) != "[]" {
		t.Fatalf("omitted patch changed policy to %s, err=%v", stored.BuiltinTools, err)
	}
	if rec = patch(`{"builtin_tools":null}`); rec.Code != http.StatusOK {
		t.Fatalf("null reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err = store.GetModel(t.Context(), db, noneModel.ID)
	if err != nil || stored.BuiltinTools != nil {
		t.Fatalf("null patch did not reset default-all: %s, err=%v", stored.BuiltinTools, err)
	}
	if rec = patch(`{"builtin_tools":{}}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "builtin_tools") {
		t.Fatalf("invalid patch status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPublicModelsExposeResolvedBuiltinToolCapabilities(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "public-model-builtin-tools.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Main','openai','chat','https://api.example','sk',1)`)
	registry := toolpkg.NewRegistry(db, nil, config.Config{}, log.New(io.Discard, "", 0))
	d := Deps{DB: db, Tools: registry}

	create := func(requestID, toolMode string, builtinTools json.RawMessage) *store.Model {
		t.Helper()
		model, err := store.CreateModel(t.Context(), db, store.Model{
			ChannelID:   "ch1",
			Kind:        "chat",
			RequestID:   requestID,
			Label:       requestID,
			Enabled:     true,
			ToolMode:    toolMode,
			BuiltinTools: builtinTools,
		})
		if err != nil {
			t.Fatalf("create %s: %v", requestID, err)
		}
		return model
	}
	defaultModel := create("default-all", "native", nil)
	customModel := create("custom", "native", json.RawMessage(`["web_search","python_execute","removed_tool"]`))
	noneModel := create("none", "native", json.RawMessage(`[]`))
	modeNoneModel := create("mode-none", "none", nil)
	if err := store.SetSetting(db, "disabled_tools", []string{"python_execute"}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	listModelsHandler(d, rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Models []struct {
			ID           string   `json:"id"`
			BuiltinTools []string `json:"builtin_tools"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := make(map[string][]string, len(payload.Models))
	for _, model := range payload.Models {
		got[model.ID] = model.BuiltinTools
	}

	expectedDefault := []string{}
	for _, definition := range registry.List("") {
		if definition.Name != "python_execute" {
			expectedDefault = append(expectedDefault, definition.Name)
		}
	}
	if strings.Join(got[defaultModel.ID], ",") != strings.Join(expectedDefault, ",") {
		t.Fatalf("default-all capabilities=%v want=%v", got[defaultModel.ID], expectedDefault)
	}
	if strings.Join(got[customModel.ID], ",") != "web_search" {
		t.Fatalf("custom capabilities=%v want=[web_search]", got[customModel.ID])
	}
	for _, id := range []string{noneModel.ID, modeNoneModel.ID} {
		if tools, exists := got[id]; !exists || tools == nil || len(tools) != 0 {
			t.Fatalf("deny-all capabilities for %s=%v exists=%v, want non-nil []", id, tools, exists)
		}
	}
}

func TestModelArchiveNormalizationPreservesBuiltinToolPolicy(t *testing.T) {
	input := strings.NewReader(`{"id":"default","builtin_tools":null}
{"id":"none","builtin_tools":"[]"}
{"id":"custom","builtin_tools":"[\" web_search \",\"web_search\"]"}
{"id":"legacy"}
`)
	reader, err := normalizeModelOfficialToolsArchiveRows(input)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(reader)
	var rows []map[string]json.RawMessage
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || string(rows[0]["builtin_tools"]) != "null" {
		t.Fatalf("default policy changed during archive normalization: %+v", rows)
	}
	if value, present, isNull, err := backupNullableStringField(rows[1], "builtin_tools"); err != nil || !present || isNull || value != "[]" {
		t.Fatalf("deny-all archive value=%q present=%v null=%v err=%v", value, present, isNull, err)
	}
	if value, present, isNull, err := backupNullableStringField(rows[2], "builtin_tools"); err != nil || !present || isNull || value != `["web_search"]` {
		t.Fatalf("custom archive value=%q present=%v null=%v err=%v", value, present, isNull, err)
	}
	if _, exists := rows[3]["builtin_tools"]; exists {
		t.Fatalf("older archive row acquired an explicit policy: %+v", rows[3])
	}

	if _, err := normalizeModelOfficialToolsArchiveRows(strings.NewReader("{\"builtin_tools\":\"{}\"}\n")); err == nil {
		t.Fatal("invalid builtin_tools archive value was accepted")
	}
}
