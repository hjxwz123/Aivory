package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/store"
)

type channelAdminFixture struct {
	db  *sql.DB
	mux *mux
}

func newChannelAdminFixture(t *testing.T) channelAdminFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-channels.db"))
	t.Cleanup(func() { _ = db.Close() })
	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/channels", func(w http.ResponseWriter, r *http.Request) {
		createChannelAdmin(d, w, r)
	})
	mx.handle(http.MethodPatch, "/api/admin/channels/:id", func(w http.ResponseWriter, r *http.Request) {
		updateChannelAdmin(d, w, r)
	})
	mx.handle(http.MethodGet, "/api/admin/channels/:id/health", func(w http.ResponseWriter, r *http.Request) {
		channelHealthAdmin(d, w, r)
	})
	mx.handle(http.MethodGet, "/api/admin/channels/health", func(w http.ResponseWriter, r *http.Request) {
		channelsHealthAdmin(d, w, r)
	})
	mx.handle(http.MethodPost, "/api/admin/channels/:id/recover", func(w http.ResponseWriter, r *http.Request) {
		recoverChannelAdmin(d, w, r)
	})
	mx.handle(http.MethodPost, "/api/admin/models/:id/channels/:channel_id/recover", func(w http.ResponseWriter, r *http.Request) {
		recoverModelChannelAdmin(d, w, r)
	})
	return channelAdminFixture{db: db, mux: mx}
}

func TestChannelAutoDisablePolicyAndRecoveryAPI(t *testing.T) {
	fx := newChannelAdminFixture(t)
	rec := fx.request(t, http.MethodPost, "/api/admin/channels", `{"name":"Health","type":"openai","api_format":"chat","auto_disable_errors":2,"auto_disable_timeouts":3,"auto_disable_minutes":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var channel store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if channel.AutoDisableErrors != 2 || channel.AutoDisableTimeouts != 3 || channel.AutoDisableMinutes != 5 {
		t.Fatalf("policy=%+v", channel)
	}
	if rec = fx.request(t, http.MethodPatch, "/api/admin/channels/"+channel.ID, `{"auto_disable_errors":4,"auto_disable_minutes":7}`); rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := fx.db.Exec(`UPDATE channels SET auto_disabled_until=?, consecutive_errors=2 WHERE id=?`, time.Now().Unix()+300, channel.ID); err != nil {
		t.Fatal(err)
	}
	if rec = fx.request(t, http.MethodGet, "/api/admin/channels/"+channel.ID+"/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = fx.request(t, http.MethodGet, "/api/admin/channels/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("channels health status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = fx.request(t, http.MethodPost, "/api/admin/channels/"+channel.ID+"/recover", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("recover status=%d body=%s", rec.Code, rec.Body.String())
	}
	saved, err := store.GetChannel(t.Context(), fx.db, channel.ID)
	if err != nil || saved.AutoDisabledUntil != 0 || saved.ConsecutiveErrors != 0 {
		t.Fatalf("recovered=%+v err=%v", saved, err)
	}
}

func TestModelChannelRecoveryLeavesGlobalChannelQuarantine(t *testing.T) {
	fx := newChannelAdminFixture(t)
	if _, err := fx.db.Exec(`INSERT INTO channels(id,name,type,api_key,enabled,auto_disabled_until) VALUES('recover-ch','Recover','openai','key',1,?)`, time.Now().Unix()+300); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.db.Exec(`INSERT INTO channel_models(id,channel_id,request_id,label,kind,enabled) VALUES('recover-cm','recover-ch','gpt-recover','Recover','chat',1)`); err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(t.Context(), fx.db, store.Model{ChannelID: "recover-ch", RequestID: "gpt-recover", Label: "Recover model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceModelChannelBindings(t.Context(), fx.db, model, []store.ModelChannelBinding{{ChannelID: "recover-ch", Priority: 1, Weight: 100}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordModelChannelResult(t.Context(), fx.db, model.ID, "recover-ch", "regular", "error", 1, 5); err != nil {
		t.Fatal(err)
	}
	rec := fx.request(t, http.MethodPost, "/api/admin/models/"+model.ID+"/channels/recover-ch/recover?role=regular", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("recover status=%d body=%s", rec.Code, rec.Body.String())
	}
	bindings, err := store.ListModelChannelBindings(t.Context(), fx.db, model.ID, "regular")
	if err != nil || len(bindings) != 1 || bindings[0].DisabledUntil != 0 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	channel, err := store.GetChannel(t.Context(), fx.db, "recover-ch")
	if err != nil || channel.AutoDisabledUntil <= time.Now().Unix() {
		t.Fatalf("channel=%+v err=%v", channel, err)
	}
}

func (fx channelAdminFixture) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateOpenAIChannelRejectsInvalidBaseURL(t *testing.T) {
	fx := newChannelAdminFixture(t)
	for _, baseURL := range []string{
		"api.openai.com/v1",
		"https://api.openai.com/v1?tenant=one",
		"https://user:secret@api.openai.com/v2",
		"ftp://api.openai.com/v3",
		"https://api.openai.com/open ai/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			body := `{"name":"Invalid ` + baseURL + `","type":"openai","api_format":"chat","base_url":"` + baseURL + `"}`
			rec := fx.request(t, http.MethodPost, "/api/admin/channels", body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "absolute HTTP(S) URL") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestChannelHeadersCreateUpdateAndClear(t *testing.T) {
	fx := newChannelAdminFixture(t)
	rec := fx.request(t, http.MethodPost, "/api/admin/channels", `{"name":"Headers","type":"openai","api_format":"chat","api_key":"secret","headers":{"A":"a","x-tenant":"team"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var channel store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if channel.Headers["A"] != "a" || channel.Headers["X-Tenant"] != "team" {
		t.Fatalf("headers=%v", channel.Headers)
	}
	path := "/api/admin/channels/" + channel.ID
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"headers":{"A":null}}`, http.StatusBadRequest},
		{`{"headers":{"A":1}}`, http.StatusBadRequest},
		{`{"headers":{"A":"a\r\nb"}}`, http.StatusBadRequest},
		{`{"headers":{"A":"a","a":"b"}}`, http.StatusBadRequest},
		{`{"name":"Headers renamed"}`, http.StatusOK},
	} {
		rec := fx.request(t, http.MethodPatch, path, tc.body)
		if rec.Code != tc.want {
			t.Fatalf("patch=%s status=%d body=%s", tc.body, rec.Code, rec.Body.String())
		}
	}
	rows, err := store.ListChannels(t.Context(), fx.db)
	if err != nil || len(rows) != 1 || rows[0].Headers["A"] != "a" {
		t.Fatalf("list=%v err=%v", rows, err)
	}
	rec = fx.request(t, http.MethodPatch, path, `{"headers":{"B":"b"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	saved, err := store.GetChannel(t.Context(), fx.db, channel.ID)
	if err != nil || len(saved.Headers) != 1 || saved.Headers["B"] != "b" || saved.APIKey != "secret" {
		t.Fatalf("saved=%v err=%v", saved, err)
	}
	rec = fx.request(t, http.MethodPatch, path, `{"headers":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	saved, err = store.GetChannel(t.Context(), fx.db, channel.ID)
	if err != nil || len(saved.Headers) != 0 {
		t.Fatalf("cleared=%v err=%v", saved, err)
	}
}

func TestCreateOpenAIChannelNormalizesVersionedBaseURL(t *testing.T) {
	fx := newChannelAdminFixture(t)
	for i, baseURL := range []string{
		"",
		"https://api.openai.com",
		"https://api.openai.com/v1",
		"https://proxy.example.com/openai/v2/",
		"https://proxy.example.com/openai/v3/",
		"https://proxy.example.com/openai/custom/",
	} {
		body, err := json.Marshal(map[string]any{
			"name": "OpenAI " + strconv.Itoa(i), "type": "openai", "api_format": "chat", "base_url": baseURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := fx.request(t, http.MethodPost, "/api/admin/channels", string(body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("baseURL=%q status=%d body=%s", baseURL, rec.Code, rec.Body.String())
		}
		var channel store.Channel
		if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
			t.Fatal(err)
		}
		want := strings.TrimRight(baseURL, "/")
		if channel.BaseURL != want {
			t.Fatalf("baseURL=%q stored=%q want=%q", baseURL, channel.BaseURL, want)
		}
	}
}

func TestChannelBaseURLValidationUsesEffectiveUpdateState(t *testing.T) {
	fx := newChannelAdminFixture(t)
	mustExec(t, fx.db, `INSERT INTO channels(id,name,type,api_format,base_url) VALUES
		('legacy','Legacy OpenAI','openai','chat','https://legacy.example'),
		('claude','Claude','claude','','https://claude.example')`)

	// Unrelated edits remain possible for legacy rows.
	if rec := fx.request(t, http.MethodPatch, "/api/admin/channels/legacy", `{"name":"Legacy renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("legacy rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := fx.request(t, http.MethodPatch, "/api/admin/channels/legacy", `{"base_url":"proxy.example/v2"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := fx.request(t, http.MethodPatch, "/api/admin/channels/claude", `{"type":"openai","api_format":"chat"}`); rec.Code != http.StatusOK {
		t.Fatalf("type update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := fx.request(t, http.MethodPatch, "/api/admin/channels/legacy", `{"base_url":"https://proxy.example/openai/v3/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var channel store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if channel.BaseURL != "https://proxy.example/openai/v3" {
		t.Fatalf("normalized base URL = %q", channel.BaseURL)
	}
}

func TestNonOpenAIChannelDoesNotRequireV1(t *testing.T) {
	fx := newChannelAdminFixture(t)
	rec := fx.request(t, http.MethodPost, "/api/admin/channels", `{"name":"Claude","type":"claude","base_url":"https://api.anthropic.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
