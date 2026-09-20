package api

import (
	"aivory/server/internal/store"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceProfileHTTPAndMemberDirectoryPrivacy(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	payload := map[string]any{"icon_url": "https://example.com/logo.png", "description": "A private team"}
	denied := httptest.NewRecorder()
	updateWorkspaceProfileHandler(deps, denied, workspacePermissionRequest(t, http.MethodPatch, "/api/workspaces/x/profile", member, map[string]string{"id": workspaceID}, payload))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("member update=%d %s", denied.Code, denied.Body.String())
	}
	saved := httptest.NewRecorder()
	updateWorkspaceProfileHandler(deps, saved, workspacePermissionRequest(t, http.MethodPatch, "/api/workspaces/x/profile", owner, map[string]string{"id": workspaceID}, payload))
	var workspace store.Workspace
	if err := json.Unmarshal(saved.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	if saved.Code != http.StatusOK || workspace.Description != "A private team" || workspace.IconURL != payload["icon_url"] {
		t.Fatalf("save=%d %s", saved.Code, saved.Body.String())
	}
	hidden := httptest.NewRecorder()
	workspaceMembersHandler(deps, hidden, workspacePermissionRequest(t, http.MethodGet, "/api/workspaces/x/members", member, map[string]string{"id": workspaceID}, nil))
	if hidden.Code != http.StatusNotFound {
		t.Fatalf("member directory exposed=%d %s", hidden.Code, hidden.Body.String())
	}
	directory := httptest.NewRecorder()
	workspaceMembersHandler(deps, directory, workspacePermissionRequest(t, http.MethodGet, "/api/workspaces/x/members", owner, map[string]string{"id": workspaceID}, nil))
	if directory.Code != http.StatusOK {
		t.Fatalf("owner directory=%d %s", directory.Code, directory.Body.String())
	}
}

func TestWorkspaceIconUploadRequiresAdminAndValidatesSVG(t *testing.T) {
	owner, member, deps, workspaceID, _ := openWorkspacePermissionHTTPTest(t)
	deps.Config.UploadDir = t.TempDir()
	for _, tc := range []struct {
		name   string
		user   *store.User
		svg    string
		status int
	}{
		{"member", member, `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24"/></svg>`, http.StatusForbidden},
		{"admin", owner, `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24"/></svg>`, http.StatusOK},
		{"unsafe", owner, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "logo.svg")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = part.Write([]byte(tc.svg)); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			original := workspacePermissionRequest(t, http.MethodPost, "/api/workspaces/x/icon", tc.user, map[string]string{"id": workspaceID}, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/workspaces/x/icon", &body).WithContext(original.Context())
			req.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			uploadWorkspaceIconHandler(deps, response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status == http.StatusOK && !strings.Contains(response.Body.String(), "/api/icons/") {
				t.Fatal(response.Body.String())
			}
		})
	}
}
