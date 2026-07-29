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

func TestCreateConversationDefaultsRespectUserModelPreference(t *testing.T) {
	tests := []struct {
		name          string
		settings      string
		body          string
		withFastModel bool
		wantModelID   string
		wantFast      bool
	}{
		{
			name: "no user default starts fast", settings: `{}`, body: `{}`,
			withFastModel: true, wantModelID: "m_global", wantFast: true,
		},
		{
			name: "user default starts advanced", settings: `{"default_model_id":"m_user"}`, body: `{}`,
			withFastModel: true, wantModelID: "m_user", wantFast: false,
		},
		{
			name: "explicit quick overrides user default", settings: `{"default_model_id":"m_user"}`, body: `{"fast":true}`,
			withFastModel: true, wantModelID: "m_user", wantFast: true,
		},
		{
			name: "explicit model starts advanced", settings: `{}`, body: `{"model_id":"m_user"}`,
			withFastModel: true, wantModelID: "m_user", wantFast: false,
		},
		{
			name: "explicit advanced overrides automatic quick", settings: `{}`, body: `{"fast":false}`,
			withFastModel: true, wantModelID: "m_global", wantFast: false,
		},
		{
			name: "missing fast model falls back to advanced", settings: `{}`, body: `{}`,
			withFastModel: false, wantModelID: "m_global", wantFast: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "conversation-defaults.db"))
			defer db.Close()
			mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,settings) VALUES('u1','user@example.test','h','user',?)`, tc.settings)
			mustExec(t, db, `INSERT INTO channels(id,name,type,enabled) VALUES('ch1','Provider','openai',1)`)
			mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,fast) VALUES
				('m_global','ch1','chat','global','Global',1,0),
				('m_user','ch1','chat','user','User default',1,0)`)
			if tc.withFastModel {
				mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,fast)
					VALUES('m_fast','ch1','chat','fast','Hidden fast',1,1)`)
			}
			if err := store.SetSetting(db, "default_model_id", "m_global"); err != nil {
				t.Fatalf("set global default model: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
				ID: "u1", Role: "user", Status: "active", Settings: json.RawMessage(tc.settings),
			}))
			rec := httptest.NewRecorder()
			createConversationHandler(Deps{DB: db}, rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create conversation status=%d body=%s", rec.Code, rec.Body.String())
			}
			var conversation store.Conversation
			if err := json.Unmarshal(rec.Body.Bytes(), &conversation); err != nil {
				t.Fatalf("decode conversation: %v", err)
			}
			if conversation.ModelID != tc.wantModelID || conversation.Fast != tc.wantFast {
				t.Fatalf("conversation model=%q fast=%v, want model=%q fast=%v", conversation.ModelID, conversation.Fast, tc.wantModelID, tc.wantFast)
			}
		})
	}
}
