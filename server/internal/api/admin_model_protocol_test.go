package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aivory/server/internal/store"
)

func TestModelProtocolAdminUsesGenericChannels(t *testing.T) {
	fx := newChannelAdminFixture(t)
	fx.mux.handle(http.MethodPost, "/api/admin/models", func(w http.ResponseWriter, r *http.Request) { createModelAdmin(Deps{DB: fx.db}, w, r) })
	fx.mux.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) { updateModelAdmin(Deps{DB: fx.db}, w, r) })
	rec := fx.request(t, http.MethodPost, "/api/admin/channels", `{"name":"Generic","base_url":"https://proxy.example/v1","api_key":"key"}`)
	if rec.Code != 201 {
		t.Fatalf("generic channel: %d %s", rec.Code, rec.Body)
	}
	var channel store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ kind, protocol string }{
		{"chat", "anthropic.messages"}, {"chat", "openai.responses"}, {"image", "gemini.generateContent"}, {"embedding", "openai.embeddings"}, {"decision", "typesafe.decisions"},
	} {
		body, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "kind": test.kind, "protocol": test.protocol, "request_id": test.protocol, "label": test.protocol, "stream": true, "vision": true, "research_enabled": true})
		rec = fx.request(t, http.MethodPost, "/api/admin/models", string(body))
		if rec.Code != 201 {
			t.Fatalf("%s: %d %s", test.protocol, rec.Code, rec.Body)
		}
		var model store.Model
		if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
			t.Fatal(err)
		}
		if model.Protocol != test.protocol || model.Kind != test.kind {
			t.Fatalf("incorrect model: %+v", model)
		}
		if (test.kind == "decision" || test.kind == "embedding") && (model.Stream || model.Vision || model.ResearchEnabled || model.ToolMode != "none") {
			t.Fatalf("special model exposes chat capabilities: %+v", model)
		}
		if test.kind == "decision" {
			raw, _ := json.Marshal(model.ID)
			if _, err := normalizeDecisionPolicySetting(t.Context(), Deps{DB: fx.db}, raw); err != nil {
				t.Fatal(err)
			}
		}
		rec = fx.request(t, http.MethodPatch, "/api/admin/models/"+model.ID, `{"enabled":false}`)
		if rec.Code != 200 {
			t.Fatalf("partial patch: %d %s", rec.Code, rec.Body)
		}
		stored, err := store.GetModel(t.Context(), fx.db, model.ID)
		if err != nil || stored.Protocol != test.protocol {
			t.Fatalf("partial patch changed protocol: %+v %v", stored, err)
		}
	}
	body, _ := json.Marshal(map[string]string{"channel_id": channel.ID, "kind": "embedding", "protocol": "openai.responses", "request_id": "bad", "label": "Bad"})
	rec = fx.request(t, http.MethodPost, "/api/admin/models", string(body))
	if rec.Code != 400 {
		t.Fatalf("incompatible protocol accepted: %d %s", rec.Code, rec.Body)
	}
	rec = fx.request(t, http.MethodPost, "/api/admin/models", `{"channel_id":"missing","kind":"chat","protocol":"openai.chat","request_id":"missing","label":"Missing"}`)
	if rec.Code != 400 {
		t.Fatalf("missing channel: %d %s", rec.Code, rec.Body)
	}
}

func TestModelProtocolDiscoveryUsesDraftFormatAndSavedSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "saved-key" || r.Header.Get("A") != "draft-header" {
			t.Errorf("incorrect discovery: %s %v", r.URL.Path, r.Header)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "claude-test", "display_name": "Claude"}}})
	}))
	defer srv.Close()
	fx := newChannelModelImportFixture(t)
	channel, err := store.CreateChannel(t.Context(), fx.db, "Generic discovery", "openai", "chat", "https://old.example/v1", "saved-key")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "protocol": "anthropic.messages", "base_url": srv.URL + "/v1", "headers": map[string]string{"A": "draft-header"}})
	rec := fx.request(t, http.MethodPost, "/api/admin/channels/models/discover", string(body))
	if rec.Code != 200 {
		t.Fatalf("discovery: %d %s", rec.Code, rec.Body)
	}
	var result channelModelDiscovery
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 1 || result.Models[0].Protocol != "anthropic.messages" {
		t.Fatalf("incorrect discovery protocol: %+v", result)
	}
	stored, err := store.GetChannel(t.Context(), fx.db, channel.ID)
	if err != nil || stored.Type != "openai" || stored.BaseURL != "https://old.example/v1" {
		t.Fatalf("discovery changed channel: %+v %v", stored, err)
	}
}
