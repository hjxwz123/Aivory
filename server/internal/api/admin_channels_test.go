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
	return channelAdminFixture{db: db, mux: mx}
}

func (fx channelAdminFixture) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateOpenAIChannelRequiresVersionedBaseURL(t *testing.T) {
	fx := newChannelAdminFixture(t)
	for _, baseURL := range []string{
		"https://api.openai.com",
		"api.openai.com/v1",
		"https://api.openai.com/v10",
		"https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1?tenant=one",
	} {
		t.Run(baseURL, func(t *testing.T) {
			body := `{"name":"Invalid ` + baseURL + `","type":"openai","api_format":"chat","base_url":"` + baseURL + `"}`
			rec := fx.request(t, http.MethodPost, "/api/admin/channels", body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ending in /v1") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreateOpenAIChannelNormalizesVersionedBaseURL(t *testing.T) {
	fx := newChannelAdminFixture(t)
	for i, baseURL := range []string{"", "https://api.openai.com/v1", "https://proxy.example.com/openai/v1/"} {
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

	// Unrelated edits remain possible for legacy rows created before this rule.
	if rec := fx.request(t, http.MethodPatch, "/api/admin/channels/legacy", `{"name":"Legacy renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("legacy rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/api/admin/channels/legacy", body: `{"base_url":"https://proxy.example"}`},
		{path: "/api/admin/channels/claude", body: `{"type":"openai","api_format":"chat"}`},
	} {
		if rec := fx.request(t, http.MethodPatch, tc.path, tc.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	rec := fx.request(t, http.MethodPatch, "/api/admin/channels/legacy", `{"base_url":"https://proxy.example/openai/v1/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var channel store.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if channel.BaseURL != "https://proxy.example/openai/v1" {
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
