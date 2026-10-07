package api

import (
	"context"
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

func newAuditEvidenceFixture(t *testing.T) (Deps, string, string) {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "audit-evidence.db"))
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES('audit-admin','audit-admin@example.test','Audit Administrator','h','admin','active'),('audit-member','audit-member@example.test','Member','h','user','active')`)
	c := cache.NewMemory()
	d := Deps{DB: db, Cache: c, Auth: authsvc.New("audit-evidence-test-secret-32-bytes", time.Hour, 24*time.Hour, c)}
	issue := func(id string) string {
		user, err := store.FindUserByID(t.Context(), db, id)
		if err != nil {
			t.Fatal(err)
		}
		c.Set("seen:"+id, "1", time.Minute)
		return issueBoundTestAccessToken(t, db, d.Auth, user)
	}
	return d, issue("audit-admin"), issue("audit-member")
}

func auditEvidenceRequest(mx *mux, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Audit test client")
	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, req)
	return rec
}

func TestAuditEvidenceCapturesChangesAndExportsWithoutViews(t *testing.T) {
	d, token, _ := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.auditDeps = &d
	mx.handle("PATCH", "/api/admin/users/:id/role", requireAdmin(d, setUserRoleAdmin))
	mx.handle("POST", "/api/admin/users/:id/password", requireAdmin(d, setUserPasswordAdmin))
	mx.handle("POST", "/api/admin/channels", requireAdmin(d, createChannelAdmin))
	mx.handle("PATCH", "/api/admin/settings", requireAdmin(d, adminSettingsSet))
	mx.handle("GET", "/api/admin/users/:id/conversations", requireAdmin(d, listUserConversationsAdmin))
	mx.handle("GET", "/api/admin/audit-logs/export", requireAdmin(d, exportAdminAuditLogsHandler))
	for _, request := range []struct{ method, path, body string }{
		{"PATCH", "/api/admin/users/audit-member/role", `{"role":"admin"}`},
		{"POST", "/api/admin/users/audit-member/password", `{"new_password":"SecureAuditPassword42!"}`},
		{"POST", "/api/admin/channels", `{"name":"Audit upstream","type":"openai","base_url":"https://api.example.test/v1","api_key":"audit-secret-key"}`},
		{"PATCH", "/api/admin/settings", `{"login_captcha_required":true}`},
		{"GET", "/api/admin/users/audit-member/conversations", ""},
		{"GET", "/api/admin/audit-logs/export?type=users", ""},
	} {
		rec := auditEvidenceRequest(mx, request.method, request.path, token, request.body)
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", request.method, request.path, rec.Code, rec.Body.String())
		}
		if request.method != "GET" || strings.Contains(request.path, "/export") {
			if rec.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing correlation ID")
			}
		}
	}
	logs, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "", 50, 0)
	if err != nil || total != 5 {
		t.Fatalf("total=%d err=%v logs=%+v", total, err, logs)
	}
	seen := map[string]store.AdminAuditLog{}
	for _, event := range logs {
		seen[event.Action] = event
		if event.ActorName != "Audit Administrator" || event.ActorRole != "admin" || event.RequestID == "" || event.UserAgent != "Audit test client" {
			t.Fatalf("missing context: %+v", event)
		}
	}
	if change := seen["admin.users.role"].Changes["role"]; change.Before != "user" || change.After != "admin" {
		t.Fatalf("role evidence: %+v", change)
	}
	if !seen["admin.users.password"].Changes["password_hash"].Redacted {
		t.Fatal("password change not redacted")
	}
	created := seen["admin.channels.create"]
	if created.TargetID == "" || created.TargetName != "Audit upstream" || !created.Changes["api_key"].Redacted {
		t.Fatalf("creation evidence missing: %+v", created)
	}
	if seen["admin.settings.update"].Changes["login_captcha_required"].After != true {
		t.Fatalf("settings evidence missing: %+v", seen["admin.settings.update"])
	}
	if _, recorded := seen["admin.users.conversations"]; recorded {
		t.Fatal("viewing conversations created an audit event")
	}
	if seen["admin.logs.export"].HTTPStatus != 200 {
		t.Fatal("export evidence missing")
	}
	raw, _ := json.Marshal(logs)
	for _, secret := range []string{"SecureAuditPassword42!", "audit-secret-key", "$2a$", "access_token", "refresh_token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q reached audit", secret)
		}
	}
}

func TestAdminAuditViewsBypassCollectorEvenWhenDenied(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.auditDeps = &d
	for _, path := range []string{
		"/api/admin/users/:id", "/api/admin/users/:id/conversations", "/api/admin/users/:id/memories",
		"/api/admin/users/:id/login-history", "/api/admin/users/:id/library", "/api/admin/settings",
		"/api/admin/channels", "/api/admin/config", "/api/admin/usage", "/api/admin/usage/:id",
		"/api/admin/files/:id", "/api/admin/files/content", "/api/admin/audit-logs", "/api/admin/html-previews",
	} {
		mx.handle("GET", path, requireAdmin(d, func(_ Deps, w http.ResponseWriter, r *http.Request) {
			if auditState(r) != nil || store.AuditContextFrom(r.Context()) != nil {
				t.Fatal("view request entered the audit collector")
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}))
		for _, token := range []string{adminToken, userToken, ""} {
			requestPath := strings.ReplaceAll(path, ":id", "audit-member")
			rec := auditEvidenceRequest(mx, "GET", requestPath, token, "")
			if token == adminToken && rec.Code != 200 {
				t.Fatalf("view %s failed: %d", path, rec.Code)
			}
		}
	}
	if _, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "", 10, 0); err != nil || total != 0 {
		t.Fatalf("view requests created %d audit events: %v", total, err)
	}
}

func TestAuditEvidenceCapturesDenialsAndSurvivesCancellation(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.handle("PATCH", "/api/admin/users/:id/role", requireAdmin(d, setUserRoleAdmin))
	for _, token := range []string{"", userToken} {
		rec := auditEvidenceRequest(mx, "PATCH", "/api/admin/users/audit-member/role", token, `{"role":"admin"}`)
		if rec.Code != 401 && rec.Code != 403 {
			t.Fatalf("expected denial: %d", rec.Code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	mx.handle("PATCH", "/api/admin/users/:id/email", requireAdmin(d, func(d Deps, w http.ResponseWriter, r *http.Request) {
		mustExec(t, d.DB, `UPDATE users SET email='updated@example.test' WHERE id='audit-member'`)
		cancel()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	req := httptest.NewRequest("PATCH", "/api/admin/users/audit-member/email", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	mx.ServeHTTP(httptest.NewRecorder(), req)
	logs, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "users", 50, 0)
	if err != nil || total != 3 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	denied, success := 0, 0
	for _, event := range logs {
		if event.Result == "denied" {
			denied++
			if event.HTTPStatus == 403 && event.ActorUserID != "audit-member" {
				t.Fatalf("missing authenticated actor: %+v", event)
			}
		}
		if event.Result == "success" {
			success++
			if event.Changes["email"].After != "updated@example.test" {
				t.Fatalf("cancelled request lost changes: %+v", event)
			}
		}
	}
	if denied != 2 || success != 1 {
		t.Fatalf("denied=%d success=%d", denied, success)
	}
}

func TestAuditEvidenceCapturesFailedLoginAndRateLimitWithoutBodies(t *testing.T) {
	d, _, _ := newAuditEvidenceFixture(t)
	if err := store.SetSetting(d.DB, "login_captcha_required", false); err != nil {
		t.Fatal(err)
	}
	mx := newMux()
	mx.auditDeps = &d
	mx.handle("POST", "/api/auth/login", rateLimitedIP(d, "audit-login", 1, time.Minute, wrap(d, loginHandler)))
	first := auditEvidenceRequest(mx, "POST", "/api/auth/login", "", `{"email":"unknown@example.test","password":"must-never-be-stored","captcha_token":"private-captcha"}`)
	second := auditEvidenceRequest(mx, "POST", "/api/auth/login", "", `{"email":"unknown@example.test","password":"must-never-be-stored"}`)
	if first.Code != 401 || second.Code != 429 {
		t.Fatalf("login=%d %s rate=%d", first.Code, first.Body.String(), second.Code)
	}
	logs, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "authentication", 10, 0)
	if err != nil || total != 2 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	raw, _ := json.Marshal(logs)
	for _, secret := range []string{"must-never-be-stored", "private-captcha", "unknown@example.test"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("login credentials leaked")
		}
	}
	if logs[0].ActorUserID != "" || logs[1].ActorUserID != "" {
		t.Fatal("failed login incorrectly assigned authenticated actor")
	}
}

func TestAuditCollectorBypassesChatStreaming(t *testing.T) {
	d, _, _ := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.auditDeps = &d
	rec := httptest.NewRecorder()
	mx.handle("POST", "/api/conversations/:id/messages", func(w http.ResponseWriter, r *http.Request) {
		if w != rec || store.AuditContextFrom(r.Context()) != nil {
			t.Fatal("chat response wrapped by audit collector")
		}
		w.(http.Flusher).Flush()
	})
	mx.ServeHTTP(rec, httptest.NewRequest("POST", "/api/conversations/chat/messages", nil))
	if !rec.Flushed {
		t.Fatal("stream not flushed")
	}
	if _, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "", "", 10, 0); err != nil || total != 0 {
		t.Fatalf("chat audited: %d %v", total, err)
	}
}

func TestAuditJobsRetainOriginAndSafeFailureReason(t *testing.T) {
	d, _, _ := newAuditEvidenceFixture(t)
	origin := &store.AuditContext{ActorID: "audit-admin", ActorName: "Snapshot admin", ActorRole: "admin", RequestID: "job-request"}
	recordAuditJobResult(d, origin, "admin.system.vector_check_completed", "job-42", "failure", "job_failed", time.Now(), map[string]any{"failed": 2})
	logs, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "job-request", "system", 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if logs[0].ActorName != "Snapshot admin" || logs[0].Source != "background_job" || logs[0].Result != "failure" || logs[0].TargetID != "job-42" {
		t.Fatalf("job result: %+v", logs[0])
	}
}

func TestAuditBackupJobRecordsFinalFailure(t *testing.T) {
	d, _, _ := newAuditEvidenceFixture(t)
	previous := adminBackupExports
	adminBackupExports = &backupExportManager{jobs: map[string]*backupExportJob{}}
	t.Cleanup(func() { adminBackupExports = previous })
	d.Config.BackupDir = ""
	job, ok := adminBackupExports.start(false, false)
	if !ok {
		t.Fatal("job not started")
	}
	runBackupExportJob(d, job, &store.AuditContext{ActorID: "audit-admin", ActorName: "Initiator", ActorRole: "admin", RequestID: "failed-backup-request"})
	logs, total, err := store.ListAdminAuditLogs(t.Context(), d.DB, "failed-backup-request", "system", 10, 0)
	if err != nil || total != 1 || len(logs) != 1 {
		t.Fatalf("job logs=%+v total=%d err=%v", logs, total, err)
	}
	if logs[0].Result != "failure" || logs[0].Reason != "job_failed" || logs[0].TargetID != job.ID {
		t.Fatalf("wrong final result: %+v", logs[0])
	}
}

func TestAuditFilterValidationAndExportAccess(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.handle("GET", "/api/admin/audit-logs", requireAdmin(d, adminAuditLogsHandler))
	mx.handle("GET", "/api/admin/audit-logs/export", requireAdmin(d, exportAdminAuditLogsHandler))
	for _, query := range []string{"type=unrecognized", "result=unrecognized", "from=invalid", "from=2026-10-06T00:00:00Z&until=2026-10-05T00:00:00Z"} {
		rec := auditEvidenceRequest(mx, "GET", "/api/admin/audit-logs?"+query, adminToken, "")
		if rec.Code != 400 {
			t.Fatalf("invalid filter accepted: %s %d", query, rec.Code)
		}
	}
	if rec := auditEvidenceRequest(mx, "GET", "/api/admin/audit-logs/export", userToken, ""); rec.Code != 403 {
		t.Fatalf("user export allowed: %d", rec.Code)
	}
	if rec := auditEvidenceRequest(mx, "GET", "/api/admin/audit-logs/export", adminToken, ""); rec.Code != 200 || rec.Header().Get("Content-Disposition") == "" {
		t.Fatalf("admin export failed: %d", rec.Code)
	}
	filter, err := parseAuditFilter(httptest.NewRequest("GET", "/api/admin/audit-logs?from=2026-10-06T08:00:00.123%2B08:00&until=2026-10-06T00:00:00.123Z", nil))
	if err != nil || filter.From != filter.Until || filter.From%1000 != 123 {
		t.Fatalf("timestamp precision/timezone lost: %+v %v", filter, err)
	}
}
