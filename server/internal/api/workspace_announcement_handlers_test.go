package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceAnnouncementHTTPAuthorizationAndReadScope(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	payload := map[string]any{
		"enabled": true, "title": "Team update", "body": "<strong>Hello</strong>",
		"remember_dismiss": true, "require_read": false,
		"bar_enabled": true, "bar_html": "Short update",
	}
	denied := httptest.NewRecorder()
	updateWorkspaceAnnouncementHandler(deps, denied, workspacePermissionRequest(t, http.MethodPatch,
		"/api/workspaces/x/announcement", member, map[string]string{"id": workspaceID}, payload))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("member update status=%d body=%s, want 403", denied.Code, denied.Body.String())
	}

	saved := httptest.NewRecorder()
	updateWorkspaceAnnouncementHandler(deps, saved, workspacePermissionRequest(t, http.MethodPatch,
		"/api/workspaces/x/announcement", owner, map[string]string{"id": workspaceID}, payload))
	if saved.Code != http.StatusOK {
		t.Fatalf("owner update status=%d body=%s", saved.Code, saved.Body.String())
	}
	var response announcement
	if err := json.Unmarshal(saved.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Enabled || response.Title != "Team update" || response.BarHTML != "Short update" || response.UpdatedAt == 0 {
		t.Fatalf("saved announcement=%+v", response)
	}

	read := httptest.NewRecorder()
	workspaceAnnouncementHandler(deps, read, workspacePermissionRequest(t, http.MethodGet,
		"/api/workspaces/x/announcement", member, map[string]string{"id": workspaceID}, nil))
	if read.Code != http.StatusOK {
		t.Fatalf("member read status=%d body=%s", read.Code, read.Body.String())
	}
	var loaded announcement
	if err := json.Unmarshal(read.Body.Bytes(), &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Title != response.Title || loaded.UpdatedAt != response.UpdatedAt {
		t.Fatalf("loaded announcement=%+v, saved=%+v", loaded, response)
	}
}
