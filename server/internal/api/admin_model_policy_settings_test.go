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

func TestAdminModelPolicySettingsRequireAvailableChatModel(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-policy-settings.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES
		('enabled-channel','Enabled','openai','chat','https://enabled.example/v1','key',1),
		('disabled-channel','Disabled','openai','chat','https://disabled.example/v1','key',0)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES
		('chat-enabled','enabled-channel','chat','chat-enabled','Enabled chat',1),
		('chat-disabled','enabled-channel','chat','chat-disabled','Disabled chat',0),
		('chat-channel-disabled','disabled-channel','chat','chat-channel-disabled','Disabled channel chat',1),
		('image-enabled','enabled-channel','image','image-enabled','Image',1)`)
	d := Deps{DB: db}

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	for _, key := range []string{
		"default_model_id",
		"task_model_id",
		"tool_route_model_id",
		"verify_model_id",
		"fallback_model_id",
	} {
		t.Run(key+" accepts available chat model", func(t *testing.T) {
			rec := patch(`{"` + key + `":"  chat-enabled  "}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var raw string
			if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw != `"chat-enabled"` {
				t.Fatalf("stored %s=%q", key, raw)
			}
		})

		for _, modelID := range []string{"chat-disabled", "chat-channel-disabled", "image-enabled", "missing"} {
			t.Run(key+" rejects "+modelID, func(t *testing.T) {
				body, err := json.Marshal(map[string]string{key: modelID})
				if err != nil {
					t.Fatal(err)
				}
				rec := patch(string(body))
				if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errModelPolicyModelUnavailable.Error()) {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var raw string
				if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if raw != `"chat-enabled"` {
					t.Fatalf("rejected patch changed %s=%q", key, raw)
				}
			})
		}
	}

	if rec := patch(`{"task_model_id":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid type status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := patch(`{"task_model_id":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear task model status=%d body=%s", rec.Code, rec.Body.String())
	}
	if raw, err := store.GetSetting(db, "task_model_id"); err != nil || string(raw) != `""` {
		t.Fatalf("cleared task model=%s err=%v", raw, err)
	}
}

func TestAdminModelPolicySettingsValidateDefaultToolMode(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "default-tool-mode-settings.db"))
	defer db.Close()
	d := Deps{DB: db}

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		adminSettingsSet(d, rec, req)
		return rec
	}

	for _, mode := range []string{"auto", "enabled", "disabled"} {
		rec := patch(`{"tool_mode_default":"` + mode + `"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("mode %q status=%d body=%s", mode, rec.Code, rec.Body.String())
		}
		if raw, err := store.GetSetting(db, "tool_mode_default"); err != nil || string(raw) != `"`+mode+`"` {
			t.Fatalf("stored mode=%s err=%v, want %q", raw, err, mode)
		}
	}
	for _, body := range []string{
		`{"tool_mode_default":"sometimes"}`,
		`{"tool_mode_default":true}`,
	} {
		if rec := patch(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}
