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

func TestHomeSceneSettingsPreservePlainAccent(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "home-scene.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,settings) VALUES('scene','scene@example.test','h','{"accent_color":"rose"}')`)
	user, err := store.FindUserByID(t.Context(), db, "scene")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value, want string
		status      int
	}{
		{`"pottery"`, "pottery", http.StatusOK},
		{`"vinyl"`, "vinyl", http.StatusOK},
		{`"postroom"`, "postroom", http.StatusOK},
		{`"bathysphere"`, "bathysphere", http.StatusOK},
		{`"darkroom"`, "darkroom", http.StatusOK},
		{`"observatory"`, "observatory", http.StatusOK},
		{`"unknown"`, "observatory", http.StatusBadRequest},
		{`{}`, "observatory", http.StatusBadRequest},
		{`"plain"`, "plain", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/settings", strings.NewReader(`{"home_scene":`+tc.value+`}`))
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
		rec := httptest.NewRecorder()
		updateMeSettingsHandler(Deps{DB: db}, rec, req)
		if rec.Code != tc.status {
			t.Fatalf("scene %s: status %d, want %d", tc.value, rec.Code, tc.status)
		}
		user, err = store.FindUserByID(t.Context(), db, "scene")
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if err := json.Unmarshal(user.Settings, &saved); err != nil {
			t.Fatal(err)
		}
		if saved["home_scene"] != tc.want || saved["accent_color"] != "rose" {
			t.Fatalf("scene %s: saved settings %v", tc.value, saved)
		}
	}
}
