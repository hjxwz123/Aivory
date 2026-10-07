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

func TestUserLinksOnlyExposeAndRevokeOwnedMetadata(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-links.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash) VALUES
		('owner','owner@example.test','h'),('other','other@example.test','h')`)
	mustExec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('left-workspace','Team','other','unused-invite')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES
		('own-chat','owner','Own',''),('own-chat-2','owner','Second',''),
		('other-chat','other','Other',''),('left-chat','owner','Left','left-workspace')`)
	mustExec(t, db, `INSERT INTO conversation_shares(id,conversation_id,user_id,title,snapshot,created_at) VALUES
		('sh_own','own-chat','owner','Own','["secret snapshot"]',100),
		('sh_second','own-chat-2','owner','Second','[]',300),
		('sh_other','other-chat','other','Other','[]',200),
		('sh_left','left-chat','owner','Left','[]',400)`)
	mustExec(t, db, `INSERT INTO html_preview_shares(id,user_id,html,created_at) VALUES
		('hp_own','owner','<h1>secret HTML</h1>',100),('hp_other','other','<h1>Other</h1>',200)`)
	// Cleanup remains available when the current group disables sharing.
	mustExec(t, db, `UPDATE user_groups SET permissions='{"allow_sharing":false}' WHERE id='ug_free'`)
	d := Deps{DB: db}
	request := func(method, target, id string, handler func(Deps, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"})
		ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": id})
		rec := httptest.NewRecorder()
		handler(d, rec, req.WithContext(ctx))
		return rec
	}
	var page struct {
		Items   []store.UserPublishedLink `json:"items"`
		HasMore bool                      `json:"has_more"`
	}
	first := request(http.MethodGet, "/api/conversation-shares?limit=1&user_id=other", "", listUserConversationShares)
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "sh_second" || !page.HasMore {
		t.Fatalf("unexpected first page: %d %s", first.Code, first.Body.String())
	}
	second := request(http.MethodGet, "/api/conversation-shares?limit=1&offset=1", "", listUserConversationShares)
	if json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "sh_own" || page.HasMore {
		t.Fatalf("unexpected second page: %s", second.Body.String())
	}
	if strings.Contains(first.Body.String()+second.Body.String(), "snapshot") || strings.Contains(first.Body.String()+second.Body.String(), "sh_left") {
		t.Fatal("list exposed content or a conversation after workspace access was lost")
	}
	html := request(http.MethodGet, "/api/html-previews", "", listUserHTMLPreviewShares)
	if html.Code != http.StatusOK || json.Unmarshal(html.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "hp_own" || strings.Contains(html.Body.String(), "secret HTML") {
		t.Fatalf("unexpected HTML metadata: %d %s", html.Code, html.Body.String())
	}
	for _, test := range []struct {
		id      string
		handler func(Deps, http.ResponseWriter, *http.Request)
	}{
		{"sh_other", deleteUserConversationShare}, {"sh_left", deleteUserConversationShare},
		{"hp_other", deleteUserHTMLPreviewShare},
	} {
		if rec := request(http.MethodDelete, "/api/links/"+test.id, test.id, test.handler); rec.Code != http.StatusNotFound {
			t.Fatalf("unauthorized revoke %s: %d %s", test.id, rec.Code, rec.Body.String())
		}
	}
	// A stale row must not revoke a subsequently regenerated token.
	mustExec(t, db, `UPDATE conversation_shares SET id='sh_replaced' WHERE id='sh_second'`)
	if rec := request(http.MethodDelete, "/api/conversation-shares/sh_second", "sh_second", deleteUserConversationShare); rec.Code != http.StatusNotFound {
		t.Fatalf("stale revoke status=%d", rec.Code)
	}
	for _, test := range []struct {
		id      string
		handler func(Deps, http.ResponseWriter, *http.Request)
	}{
		{"sh_own", deleteUserConversationShare}, {"hp_own", deleteUserHTMLPreviewShare},
	} {
		if rec := request(http.MethodDelete, "/api/links/"+test.id, test.id, test.handler); rec.Code != http.StatusOK {
			t.Fatalf("own revoke %s: %d %s", test.id, rec.Code, rec.Body.String())
		}
	}
	var conversations, remainingShares int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&conversations); err != nil || conversations != 4 {
		t.Fatalf("revocation deleted original conversations: %d %v", conversations, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_shares WHERE id='sh_replaced'`).Scan(&remainingShares); err != nil || remainingShares != 1 {
		t.Fatalf("stale revocation removed replacement: %d %v", remainingShares, err)
	}
	publicReq := httptest.NewRequest(http.MethodGet, "/api/public/html-previews/hp_own", nil)
	publicReq = publicReq.WithContext(context.WithValue(publicReq.Context(), pathCtxKey{}, map[string]string{"token": "hp_own"}))
	publicRec := httptest.NewRecorder()
	publicHTMLPreviewShareHandler(d, publicRec, publicReq)
	if publicRec.Code != http.StatusNotFound {
		t.Fatalf("revoked public preview status=%d", publicRec.Code)
	}
}

func TestUserLinkRoutesRequireAuthentication(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-links-auth.db"))
	defer db.Close()
	router := NewRouter(Deps{DB: db})
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/conversation-shares"}, {http.MethodDelete, "/api/conversation-shares/sh_test"},
		{http.MethodGet, "/api/html-previews"}, {http.MethodDelete, "/api/html-previews/hp_test"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(test.method, test.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d", test.method, test.path, rec.Code)
		}
	}
}
