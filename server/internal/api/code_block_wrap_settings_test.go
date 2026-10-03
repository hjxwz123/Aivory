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

func TestCodeBlockWrapSettings(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		wrap   bool
	}{
		{"enable", `{"code_block_wrap":true,"persona_nickname":"updated","unknown_setting":true}`, http.StatusOK, true},
		{"disable", `{"code_block_wrap":false}`, http.StatusOK, false},
		{"omitted", `{"persona_nickname":"updated"}`, http.StatusOK, true},
		{"string", `{"code_block_wrap":"true","persona_nickname":"updated"}`, http.StatusBadRequest, true},
		{"number", `{"code_block_wrap":1,"persona_nickname":"updated"}`, http.StatusBadRequest, true},
		{"null", `{"code_block_wrap":null,"persona_nickname":"updated"}`, http.StatusBadRequest, true},
		{"object", `{"code_block_wrap":{},"persona_nickname":"updated"}`, http.StatusBadRequest, true},
		{"array", `{"code_block_wrap":[],"persona_nickname":"updated"}`, http.StatusBadRequest, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := openMigrated(t, filepath.Join(t.TempDir(), "code-wrap.db"))
			defer database.Close()
			if _, err := database.Exec(`INSERT INTO users(id,email,password_hash,role,settings) VALUES('wrap-user','wrap@example.com','h','user','{"code_block_wrap":true,"persona_nickname":"original","font_family":"serif"}')`); err != nil {
				t.Fatal(err)
			}
			user, err := store.FindUserByID(context.Background(), database, "wrap-user")
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPatch, "/api/me/settings", strings.NewReader(testCase.body))
			request = request.WithContext(context.WithValue(request.Context(), userCtxKey{}, user))
			response := httptest.NewRecorder()
			updateMeSettingsHandler(Deps{DB: database}, response, request)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.status, response.Body.String())
			}
			stored, err := store.FindUserByID(context.Background(), database, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			settings := map[string]any{}
			if err := json.Unmarshal(stored.Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if settings["code_block_wrap"] != testCase.wrap || settings["font_family"] != "serif" {
				t.Fatalf("unexpected persisted settings: %#v", settings)
			}
			if _, exists := settings["unknown_setting"]; exists {
				t.Fatal("unknown setting was persisted")
			}
			if testCase.status == http.StatusBadRequest && settings["persona_nickname"] != "original" {
				t.Fatal("invalid request partially updated settings")
			}
			if testCase.status == http.StatusOK {
				readRequest := httptest.NewRequest(http.MethodGet, "/api/me/settings", nil)
				readRequest = readRequest.WithContext(context.WithValue(readRequest.Context(), userCtxKey{}, stored))
				readResponse := httptest.NewRecorder()
				meSettingsHandler(Deps{}, readResponse, readRequest)
				returned := map[string]any{}
				if err := json.Unmarshal(readResponse.Body.Bytes(), &returned); err != nil {
					t.Fatal(err)
				}
				if returned["code_block_wrap"] != testCase.wrap {
					t.Fatalf("read-back wrap = %v, want %v", returned["code_block_wrap"], testCase.wrap)
				}
			}
		})
	}
}
