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

func TestReplyAnimationSettings(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		want   string
		status int
	}{
		{"plane", `"paper-plane"`, "paper-plane", http.StatusOK},
		{"origami", `"origami"`, "origami", http.StatusOK},
		{"ink", `"ink"`, "ink", http.StatusOK},
		{"sprout", `"sprout"`, "sprout", http.StatusOK},
		{"orbit", `"orbit"`, "orbit", http.StatusOK},
		{"prism", `"prism"`, "prism", http.StatusOK},
		{"unknown", `"spinner"`, "origami", http.StatusBadRequest},
		{"empty", `""`, "origami", http.StatusBadRequest},
		{"null", `null`, "origami", http.StatusBadRequest},
		{"number", `1`, "origami", http.StatusBadRequest},
		{"bool", `true`, "origami", http.StatusBadRequest},
		{"object", `{}`, "origami", http.StatusBadRequest},
		{"array", `[]`, "origami", http.StatusBadRequest},
		{"omitted", ``, "origami", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "reply-animation.db"))
			defer db.Close()
			if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role,settings) VALUES('animation-user','animation@example.com','h','user','{"reply_animation":"origami","font_family":"serif","persona_nickname":"original"}')`); err != nil {
				t.Fatal(err)
			}
			user, err := store.FindUserByID(context.Background(), db, "animation-user")
			if err != nil {
				t.Fatal(err)
			}
			body := `{"persona_nickname":"updated","unknown_setting":true}`
			if tc.value != "" {
				body = `{"reply_animation":` + tc.value + `,"persona_nickname":"updated","unknown_setting":true}`
			}
			request := httptest.NewRequest(http.MethodPatch, "/api/me/settings", strings.NewReader(body))
			request = request.WithContext(context.WithValue(request.Context(), userCtxKey{}, user))
			response := httptest.NewRecorder()
			updateMeSettingsHandler(Deps{DB: db}, response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			stored, err := store.FindUserByID(context.Background(), db, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			settings := map[string]any{}
			if err := json.Unmarshal(stored.Settings, &settings); err != nil {
				t.Fatal(err)
			}
			if settings["reply_animation"] != tc.want || settings["font_family"] != "serif" {
				t.Fatalf("unexpected persisted settings: %#v", settings)
			}
			if _, exists := settings["unknown_setting"]; exists {
				t.Fatal("unknown setting was persisted")
			}
			if tc.status == http.StatusBadRequest && settings["persona_nickname"] != "original" {
				t.Fatal("invalid request partially changed settings")
			}
			if tc.status == http.StatusOK {
				read := httptest.NewRequest(http.MethodGet, "/api/me/settings", nil)
				read = read.WithContext(context.WithValue(read.Context(), userCtxKey{}, stored))
				result := httptest.NewRecorder()
				meSettingsHandler(Deps{}, result, read)
				returned := map[string]any{}
				if err := json.Unmarshal(result.Body.Bytes(), &returned); err != nil {
					t.Fatal(err)
				}
				if returned["reply_animation"] != tc.want {
					t.Fatalf("read-back animation = %v, want %v", returned["reply_animation"], tc.want)
				}
			}
		})
	}
}
