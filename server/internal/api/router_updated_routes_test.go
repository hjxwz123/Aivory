package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdatedRoutesAreRegisteredInFullRouter(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "updated-routes.db"))
	defer db.Close()
	router := NewRouter(Deps{DB: db})
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/admin/audit-logs?until=2026-10-07T01%3A53%3A50.334Z"},
		{"GET", "/api/admin/audit-logs/export"},
		{"DELETE", "/api/admin/audit-logs?until=2026-10-07T01%3A53%3A50.334Z"},
		{"DELETE", "/api/admin/audit-logs/route-probe"},
		{"GET", "/api/admin/channels/health"},
		{"GET", "/api/admin/channels/capabilities?request_id=route-probe"},
		{"GET", "/api/admin/channels/route-probe/models"},
		{"PUT", "/api/admin/channels/route-probe/models"},
		{"GET", "/api/admin/channels/route-probe/health"},
		{"POST", "/api/admin/channels/route-probe/recover"},
		{"GET", "/api/admin/models/route-probe/channels"},
		{"PUT", "/api/admin/models/route-probe/channels"},
		{"POST", "/api/admin/models/route-probe/channels/route-probe/recover?role=regular"},
		{"GET", "/api/conversation-shares"},
		{"DELETE", "/api/conversation-shares/route-probe"},
		{"GET", "/api/html-previews"},
		{"DELETE", "/api/html-previews/route-probe"},
		{"POST", "/api/conversations/route-probe/reorder"},
		{"GET", "/api/admin/overview?days=30"},
		{"GET", "/api/admin/redeem-codes?q=route-probe"},
		{"GET", "/api/admin/users/route-probe/conversations"},
		{"GET", "/api/admin/users/route-probe/memories"},
		{"GET", "/api/admin/users/route-probe/login-history"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("route did not reach authentication: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFullRouterAuditDeletionWithMillisecondCutoff(t *testing.T) {
	d, adminToken, _ := newAuditEvidenceFixture(t)
	router := NewRouter(d)
	cutoff := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	mustExec(t, d.DB, `INSERT INTO admin_audit_logs(id,actor_user_id,event_type,action,occurred_at_ms,created_at)
		VALUES('cutoff-delete','audit-admin','models','admin.models.update',?,?),
		('cutoff-keep','audit-admin','models','admin.models.update',?,?)`,
		cutoff.UnixMilli()-1, cutoff.Unix(), cutoff.UnixMilli()+1, cutoff.Unix())
	path := "/api/admin/audit-logs?type=models&until=" + cutoff.Format(time.RFC3339Nano)
	request := func(method string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cutoff-delete") || strings.Contains(rec.Body.String(), "cutoff-keep") {
		t.Fatalf("filtered listing failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := request("DELETE")
	var response struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Deleted != 1 {
		t.Fatalf("filtered deletion failed: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	var remaining int
	if err := d.DB.QueryRow(`SELECT count(*) FROM admin_audit_logs WHERE id='cutoff-keep'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("cutoff deleted a newer event: remaining=%d err=%v", remaining, err)
	}
	req := httptest.NewRequest("DELETE", "/api/admin/audit-logs/cutoff-keep", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("single deletion failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
