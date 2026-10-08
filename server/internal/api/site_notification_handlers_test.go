package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestSiteNotificationRoutesLifecycleAndUserIsolation(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	router := NewRouter(d)
	request := func(method, path, token, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/notifications"}, {"GET", "/api/notifications/test"}, {"POST", "/api/notifications/test/read"},
		{"GET", "/api/admin/notifications"}, {"GET", "/api/admin/notifications/test"},
		{"POST", "/api/admin/notifications"}, {"PUT", "/api/admin/notifications/test"}, {"DELETE", "/api/admin/notifications/test"},
	} {
		request(route.method, route.path, "", "", 401)
		if strings.HasPrefix(route.path, "/api/admin/") {
			request(route.method, route.path, userToken, "{}", 403)
		}
	}
	decode := func(w *httptest.ResponseRecorder) store.SiteNotification {
		t.Helper()
		var n store.SiteNotification
		if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	list := func(token string) store.SiteNotificationPage {
		t.Helper()
		var page store.SiteNotificationPage
		w := request("GET", "/api/notifications", token, "", 200)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("user state must not be cached")
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	create := request("POST", "/api/admin/notifications", adminToken, `{"title":"A notice","body":"<p>Body is private to detail</p>","enabled":true}`, 201)
	n := decode(create)
	if n.ID == "" || n.Version == "" {
		t.Fatal("missing identity/version")
	}
	path := "/api/admin/notifications/" + n.ID
	var audits int
	if err := d.DB.QueryRow(`SELECT count(*) FROM admin_audit_logs WHERE target_id=? AND target_type='notification' AND action='admin.content.create'`, n.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("create audit missing: %d %v", audits, err)
	}
	page := list(userToken)
	if page.Total != 1 || len(page.Notifications) != 1 || !page.Notifications[0].Unread || !page.Notifications[0].ShouldPopup || page.Notifications[0].Body != "" {
		t.Fatalf("summary/state: %+v", page)
	}
	request("POST", "/api/notifications/"+n.ID+"/read", userToken, `{"version":"`+n.Version+`"}`, 200)
	page = list(userToken)
	if page.Notifications[0].Unread || !page.Notifications[0].ShouldPopup {
		t.Fatal("ordinary reading must not suppress future visits")
	}
	request("POST", "/api/notifications/"+n.ID+"/read", userToken, `{"version":"`+n.Version+`","dismiss":true,"read":false}`, 200)
	if list(userToken).Notifications[0].ShouldPopup {
		t.Fatal("dismissed version still pops up")
	}
	if !list(adminToken).Notifications[0].Unread || !list(adminToken).Notifications[0].ShouldPopup {
		t.Fatal("state leaked to another user")
	}
	edited := decode(request("PUT", path, adminToken, `{"title":"Edited notice","body":"New body","enabled":true}`, 200))
	if edited.Version == n.Version {
		t.Fatal("same-second edits must create a new version")
	}
	request("POST", "/api/notifications/"+n.ID+"/read", userToken, `{"version":"`+n.Version+`","dismiss":true}`, 409)
	page = list(userToken)
	if !page.Notifications[0].Unread || !page.Notifications[0].ShouldPopup {
		t.Fatal("new version was suppressed by stale read")
	}
	request("POST", "/api/notifications/"+n.ID+"/read", userToken, `{"version":"`+edited.Version+`","dismiss":true,"read":false}`, 200)
	page = list(userToken)
	if !page.Notifications[0].Unread || page.Notifications[0].ShouldPopup {
		t.Fatal("suppression must not mark unviewed content as read")
	}
	request("PUT", path, adminToken, `{"title":"Draft","body":"Hidden","enabled":false}`, 200)
	if list(userToken).Total != 0 {
		t.Fatal("draft leaked")
	}
	request("GET", "/api/notifications/"+n.ID, userToken, "", 404)
	request("GET", path, adminToken, "", 200)
	request("DELETE", path, adminToken, "", 200)
	var states int
	if err := d.DB.QueryRow(`SELECT count(*) FROM site_notification_states WHERE notification_id=?`, n.ID).Scan(&states); err != nil || states != 0 {
		t.Fatalf("orphan states: %d %v", states, err)
	}
	request("GET", path, adminToken, "", 404)
	request("DELETE", path, adminToken, "", 404)
	for _, body := range []string{`{}`, `{"title":" ","body":"text"}`, `{"title":"Title","body":" "}`, `{"title":"` + strings.Repeat("a", 121) + `","body":"text"}`} {
		request("POST", "/api/admin/notifications", adminToken, body, 400)
	}
	var views int
	if err := d.DB.QueryRow(`SELECT count(*) FROM admin_audit_logs WHERE method='GET' AND target_type='notification'`).Scan(&views); err != nil || views != 0 {
		t.Fatalf("view actions were audited: %d %v", views, err)
	}
}

func TestPopupMessageCustomButtonValidationAndLegacySettings(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"body":"Legacy","remember_dismiss":true,"updated_at":42}`,
		`{"button_text":" Learn more ","button_url":" https://example.test/promo "}`,
		`{"button_url":"/subscription?campaign=popup"}`, `{"button_url":"http://localhost:5173/chat"}`,
	} {
		if _, err := normalizePopupMessage(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid config: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"button_url":"javascript:alert(1)"}`, `{"button_url":"//example.test"}`, `{"button_url":"file:///tmp/test"}`, `{"button_url":"https://user:secret@example.test"}`, `{"button_url":"/\\example.test"}`, `{"button_text":"` + strings.Repeat("a", 81) + `"}`} {
		if _, err := normalizePopupMessage(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
	d, token, _ := newAuditEvidenceFixture(t)
	router := NewRouter(d)
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	r := httptest.NewRequest("PATCH", "/api/admin/settings", strings.NewReader(`{"announcement":{"enabled":true,"body":"Promotion","button_text":"Get started","button_url":"https://example.test/start","bar_enabled":true,"bar_html":"Independent bar"}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("settings save: %d %s", w.Code, w.Body.String())
	}
	a := readAnnouncement(t, d)
	if a.ButtonText != "Get started" || a.ButtonURL != "https://example.test/start" || !a.BarEnabled || a.BarHTML != "Independent bar" {
		t.Fatalf("CTA/bar lost: %+v", a)
	}
}
