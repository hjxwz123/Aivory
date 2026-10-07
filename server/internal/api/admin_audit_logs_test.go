package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestAdminAuditLogsRouteRequiresAdminAndReturnsSearchPage(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-audit-logs.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES
		('admin','admin@example.test','Audit Admin','h','admin','active'),
		('user','user@example.test','Regular User','h','user','active')`)
	workspace, err := store.CreateWorkspace(t.Context(), db, "user", "Audit workspace")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustExec(t, db, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,metadata,created_at)
		VALUES('aud-test-1',?,'user','member.role_updated','member','member-123','{"role":"admin"}',1700000000)`, workspace.ID)

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("admin-audit-logs-test-secret-32-bytes", time.Hour, 24*time.Hour, c),
	}
	issue := func(userID string) string {
		t.Helper()
		user, err := store.FindUserByID(t.Context(), db, userID)
		if err != nil {
			t.Fatalf("find %s: %v", userID, err)
		}
		token := issueBoundTestAccessToken(t, db, d.Auth, user)
		c.Set("seen:"+user.ID, "1", time.Minute)
		return token
	}
	adminToken, userToken := issue("admin"), issue("user")
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/audit-logs", requireAdmin(d, adminAuditLogsHandler))
	mx.handle(http.MethodPatch, "/api/admin/users/:id/role", requireAdmin(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mx.handle(http.MethodPatch, "/api/admin/users/:id/email", requireAdmin(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid email"})
	}))
	get := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?q=role_updated&page=1&page_size=10", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get(userToken); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := get(adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Logs     []store.WorkspaceAuditLog `json:"logs"`
		Total    int                       `json:"total"`
		Page     int                       `json:"page"`
		PageSize int                       `json:"page_size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Total != 1 || body.Page != 1 || body.PageSize != 10 || len(body.Logs) != 1 {
		t.Fatalf("response pagination = %+v", body)
	}
	if body.Logs[0].ID != "aud-test-1" || body.Logs[0].WorkspaceName != "Audit workspace" || string(body.Logs[0].Metadata) != `{"role":"admin"}` {
		t.Fatalf("response log = %+v", body.Logs[0])
	}

	mutation := httptest.NewRequest(http.MethodPatch, "/api/admin/users/user-123/role", strings.NewReader(`{"role":"admin","password":"must-not-be-recorded"}`))
	mutation.Header.Set("Authorization", "Bearer "+adminToken)
	mutation.Header.Set("Content-Type", "application/json")
	mutationRecorder := httptest.NewRecorder()
	mx.ServeHTTP(mutationRecorder, mutation)
	if mutationRecorder.Code != http.StatusOK {
		t.Fatalf("admin mutation status=%d body=%s", mutationRecorder.Code, mutationRecorder.Body.String())
	}
	failedMutation := httptest.NewRequest(http.MethodPatch, "/api/admin/users/user-123/email", strings.NewReader(`{"email":"invalid"}`))
	failedMutation.Header.Set("Authorization", "Bearer "+adminToken)
	failedMutationRecorder := httptest.NewRecorder()
	mx.ServeHTTP(failedMutationRecorder, failedMutation)
	if failedMutationRecorder.Code != http.StatusBadRequest {
		t.Fatalf("failed admin mutation status=%d body=%s", failedMutationRecorder.Code, failedMutationRecorder.Body.String())
	}

	filtered := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?type=users&page=1&page_size=10", nil)
	filtered.Header.Set("Authorization", "Bearer "+adminToken)
	filteredRecorder := httptest.NewRecorder()
	mx.ServeHTTP(filteredRecorder, filtered)
	var filteredBody struct {
		Logs  []store.AdminAuditLog `json:"logs"`
		Total int                   `json:"total"`
	}
	if err := json.Unmarshal(filteredRecorder.Body.Bytes(), &filteredBody); err != nil {
		t.Fatalf("decode filtered response: %v", err)
	}
	if filteredRecorder.Code != http.StatusOK || filteredBody.Total != 2 || len(filteredBody.Logs) != 2 {
		t.Fatalf("filtered admin operation response = %d %+v body=%s", filteredRecorder.Code, filteredBody, filteredRecorder.Body.String())
	}
	var success, failure *store.AdminAuditLog
	for i := range filteredBody.Logs {
		if filteredBody.Logs[i].Result == "success" {
			success = &filteredBody.Logs[i]
		} else {
			failure = &filteredBody.Logs[i]
		}
	}
	if success == nil || failure == nil || failure.Result != "failure" || failure.HTTPStatus != 400 {
		t.Fatalf("missing success/failure evidence: %+v", filteredBody.Logs)
	}
	if success.Action != "admin.users.role" || string(success.Metadata) != `{"method":"PATCH","route":"/api/admin/users/:id/role","status":200}` {
		t.Fatalf("recorded admin operation contains unexpected fields: %+v metadata=%s", filteredBody.Logs[0], filteredBody.Logs[0].Metadata)
	}
	if strings.Contains(string(filteredBody.Logs[0].Metadata), "must-not-be-recorded") || strings.Contains(filteredBody.Logs[0].TargetID, "password") {
		t.Fatalf("sensitive request body reached audit record: %+v", filteredBody.Logs[0])
	}
}

func TestAdminAuditDeletionRequiresAdminAndRecordsDeletion(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	workspace, err := store.CreateWorkspace(t.Context(), d.DB, "audit-member", "Delete audit test")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d.DB, `DELETE FROM workspace_audit_logs`)
	mustExec(t, d.DB, `INSERT INTO workspace_audit_logs(id,workspace_id,actor_user_id,action,target_type,target_id,created_at) VALUES('workspace-delete',?,'audit-admin','policy.updated','workspace',?,1)`, workspace.ID, workspace.ID)
	mustExec(t, d.DB, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,created_at) VALUES('admin-delete','audit-admin','models','admin.models.update',1),('admin-keep','audit-admin','channels','admin.channels.update',1)`)
	mx := newMux()
	mx.auditDeps = &d
	mx.handle("DELETE", "/api/admin/audit-logs", requireAdmin(d, deleteFilteredAdminAuditLogsHandler))
	mx.handle("DELETE", "/api/admin/audit-logs/:id", requireAdmin(d, deleteAdminAuditLogHandler))
	for _, path := range []string{"/api/admin/audit-logs/workspace-delete", "/api/admin/audit-logs?type=models"} {
		for _, token := range []string{"", userToken} {
			rec := auditEvidenceRequest(mx, "DELETE", path, token, "")
			want := http.StatusUnauthorized
			if token != "" {
				want = http.StatusForbidden
			}
			if rec.Code != want {
				t.Fatalf("delete %s returned %d; wanted %d", path, rec.Code, want)
			}
		}
	}
	if _, count, err := store.ListAdminAuditLogs(t.Context(), d.DB, "workspace-delete", "workspace", 10, 0); err != nil || count != 1 {
		t.Fatalf("unauthorized delete removed workspace evidence: count=%d err=%v", count, err)
	}
	if _, count, err := store.ListAdminAuditLogs(t.Context(), d.DB, "admin-delete", "models", 10, 0); err != nil || count != 1 {
		t.Fatalf("unauthorized delete removed admin evidence: count=%d err=%v", count, err)
	}
	for _, query := range []string{"type=unknown", "result=unknown", "from=invalid", "from=2026-10-07T00:00:00Z&until=2026-10-06T00:00:00Z"} {
		if rec := auditEvidenceRequest(mx, "DELETE", "/api/admin/audit-logs?"+query, adminToken, ""); rec.Code != 400 {
			t.Fatalf("invalid filter %q accepted: %d", query, rec.Code)
		}
	}
	if rec := auditEvidenceRequest(mx, "DELETE", "/api/admin/audit-logs/workspace-delete", adminToken, ""); rec.Code != 200 {
		t.Fatalf("single delete failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := auditEvidenceRequest(mx, "DELETE", "/api/admin/audit-logs/workspace-delete", adminToken, ""); rec.Code != 404 {
		t.Fatalf("missing log delete returned %d", rec.Code)
	}
	rec := auditEvidenceRequest(mx, "DELETE", "/api/admin/audit-logs?type=models", adminToken, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":1`) {
		t.Fatalf("filtered delete failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, count, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "models", 10, 0); err != nil || count != 0 {
		t.Fatalf("model logs not deleted: count=%d err=%v", count, err)
	}
	if _, count, err := store.ListAdminAuditLogs(t.Context(), d.DB, "admin-keep", "channels", 10, 0); err != nil || count != 1 {
		t.Fatalf("unmatched log deleted: count=%d err=%v", count, err)
	}
	logs, _, err := store.ListFilteredAdminAuditLogs(t.Context(), d.DB, store.AdminAuditFilter{Type: "logs", Result: "success"}, 50, 0)
	if err != nil || len(logs) != 2 {
		t.Fatalf("successful deletion evidence=%+v err=%v", logs, err)
	}
	for _, log := range logs {
		var metadata struct {
			Deleted int `json:"deleted_count"`
		}
		if err := json.Unmarshal(log.Metadata, &metadata); err != nil || metadata.Deleted != 1 || log.ActorUserID != "audit-admin" || log.Action != "admin.logs.delete" {
			t.Fatalf("deletion missing evidence: %+v metadata=%s", log, log.Metadata)
		}
	}
}
