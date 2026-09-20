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

func TestAdminDecisionPoliciesAndModels(t *testing.T) {
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db := openMigrated(t, filepath.Join(t.TempDir(), "decisions.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_key,enabled) VALUES ('ts','TypeSafe','typesafe','key',1),('nokey','No key','typesafe','',1),('chat','Chat','openai','key',1),('off','Disabled','typesafe','key',0)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES ('jev','ts','decision','jev-1.13.0','Jev',1),('disabled','ts','decision','jev-disabled','Disabled',0),('nokey','nokey','decision','jev','No key',1),('wrong','chat','decision','jev','Wrong channel',1),('off','off','decision','jev','Disabled channel',1)`)
	patch := func(key, id string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{key: id})
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		adminSettingsSet(Deps{DB: db}, rec, req)
		return rec
	}
	for _, key := range []string{"file_route_model_id", "tool_route_model_id", "memory_dedup_model_id", "memory_adjudicate_model_id", "moderation_model_id"} {
		if rec := patch(key, " jev "); rec.Code != 200 {
			t.Fatalf("%s: %d %s", key, rec.Code, rec.Body)
		}
		for _, id := range []string{"disabled", "nokey", "wrong", "off", "missing"} {
			if rec := patch(key, id); rec.Code != 409 {
				t.Fatalf("%s/%s: %d %s", key, id, rec.Code, rec.Body)
			}
			raw, err := store.GetSetting(db, key)
			if err != nil || string(raw) != `"jev"` {
				t.Fatalf("invalid patch changed setting: %s %v", raw, err)
			}
		}
		if rec := patch(key, ""); rec.Code != 200 {
			t.Fatalf("clear %s: %d", key, rec.Code)
		}
	}
	for _, key := range []string{"default_model_id", "task_model_id", "title_model_id", "verify_model_id", "fallback_model_id"} {
		if rec := patch(key, "jev"); rec.Code != 409 {
			t.Fatalf("decision allowed for %s: %d", key, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/models", strings.NewReader(`{"channel_id":"ts","kind":"chat","request_id":"jev-latest","label":"Jev latest","research_enabled":true,"stream":true,"vision":true,"price_output":10}`))
	rec := httptest.NewRecorder()
	createModelAdmin(Deps{DB: db}, rec, req)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var model store.Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Kind != "decision" || model.Stream || model.Vision || model.ResearchEnabled || model.PriceOutput != 0 || model.ToolMode != "none" {
		t.Fatalf("invalid decision capabilities: %+v", model)
	}
	chatModels, err := store.ListModels(context.Background(), db, "chat", true)
	if err != nil || len(chatModels) != 0 {
		t.Fatalf("decision leaked into chat catalog: %+v %v", chatModels, err)
	}
}

func TestAdminDecisionModelDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("invalid discovery request")
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-1.13.0","description":"Pinned release"},{"name":"jev-latest","description":"Latest"}]}`))
	}))
	defer srv.Close()
	discovery, err := discoverChannelModels(context.Background(), &store.Channel{Type: "typesafe", BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(discovery)
	if !strings.Contains(string(raw), `"kind":"decision"`) || !strings.Contains(string(raw), `jev-1.13.0`) {
		t.Fatalf("discovery=%s", raw)
	}
}

func TestAdminDecisionBatchImportAndUpdate(t *testing.T) {
	fx := newChannelModelImportFixture(t)
	fx.mux.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) { updateModelAdmin(Deps{DB: fx.db}, w, r) })
	channel, err := store.CreateChannel(t.Context(), fx.db, "TypeSafe", "typesafe", "", "", "key")
	if err != nil {
		t.Fatal(err)
	}
	rec := fx.request(t, http.MethodPost, "/api/admin/channels/"+channel.ID+"/models/batch", `{"models":[{"request_id":"jev-1.13.0","label":"Jev","kind":"chat"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	models, err := store.ListModels(t.Context(), fx.db, "decision", false)
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%v err=%v", models, err)
	}
	m := models[0]
	if m.PriceInput != 0.042 || m.ResearchEnabled || m.Stream || m.Vision {
		t.Fatalf("bad defaults: %+v", m)
	}
	rec = fx.request(t, http.MethodPatch, "/api/admin/models/"+m.ID, `{"research_enabled":true,"stream":true,"vision":true,"price_output":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	updated, err := store.GetModel(t.Context(), fx.db, m.ID)
	if err != nil || updated.ResearchEnabled || updated.Stream || updated.Vision || updated.PriceOutput != 0 {
		t.Fatalf("bad update: %+v %v", updated, err)
	}
}
