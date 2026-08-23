package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminWorkspaceDetailIncludesOwnerName(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-workspace-detail.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u_owner','owner@example.test','Ada Lovelace','h','user')`)
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws_owner','Research','u_owner','invite-token')`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws_owner','u_owner','admin')`)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/admin/workspaces/ws_owner", nil)
	request = request.WithContext(context.WithValue(request.Context(), pathCtxKey{}, map[string]string{"id": "ws_owner"}))
	adminWorkspaceDetailHandler(Deps{DB: db}, recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("GET workspace detail status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Workspace store.Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode workspace detail: %v", err)
	}
	if response.Workspace.OwnerName != "Ada Lovelace" {
		t.Fatalf("workspace owner_name=%q, want %q", response.Workspace.OwnerName, "Ada Lovelace")
	}
}
