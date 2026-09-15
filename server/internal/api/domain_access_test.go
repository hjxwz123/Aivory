package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestDomainLockHTTPBoundaryAndLiveUnlock(t *testing.T) {
	d := newAuthSecurityDeps(t, "domain-access.db")
	ctx := t.Context()
	owner, err := store.CreateUserWithRole(ctx, d.DB, "owner@outside.example", "Owner", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.CreateWorkspace(ctx, d.DB, owner.ID, "Company")
	if err != nil {
		t.Fatal(err)
	}
	rule := store.RegistrationDomain{Domain: "company.example", WorkspaceID: ws.ID, Enabled: true, LockPersonal: false}
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, d.DB, "user@company.example", "User", "hash")
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreateConversation(ctx, d.DB, store.Conversation{UserID: user.ID, Title: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := store.CreateConversation(ctx, d.DB, store.Conversation{UserID: user.ID, WorkspaceID: ws.ID, Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	_, exp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRefreshToken(ctx, d.DB, jti, user.ID, exp, store.SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	token, _, err := d.Auth.IssueAccessForSession(user.ID, user.Role, user.TokenVer, jti)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, h handler) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Workspace-ID", ws.ID)
		rec := httptest.NewRecorder()
		requireAuth(d, h).ServeHTTP(rec, r)
		return rec
	}
	probe := func(_ Deps, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
	if rec := request("GET", "/api/conversations/"+personal.ID, "", probe); rec.Code != 204 {
		t.Fatalf("unlocked status=%d %s", rec.Code, rec.Body.String())
	}
	rule.LockPersonal = true
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/conversations/" + personal.ID, "", 403},
		{"GET", "/api/conversations/" + personal.ID + "?workspace_id=" + ws.ID, "", 403},
		{"GET", "/api/conversations/" + shared.ID, "", 204},
		{"GET", "/api/conversations/" + shared.ID + "?workspace_id=foreign", "", 403},
		{"GET", "/api/conversations", "", 403},
		{"GET", "/api/conversations?workspace_id=" + ws.ID, "", 204},
		{"POST", "/api/conversations", `{"title":"Personal"}`, 403},
		{"POST", "/api/conversations", fmt.Sprintf(`{"title":"Shared","workspace_id":%q}`, ws.ID), 204},
		{"POST", "/api/conversations/archive-all?workspace_id=" + ws.ID, "{}", 403},
		{"DELETE", "/api/conversations?workspace_id=" + ws.ID, "", 403},
		// The private handler now validates the assigned scope and policy itself.
		{"POST", "/api/private-chat?workspace_id=" + ws.ID, "{}", 204},
		{"POST", "/api/workspaces", "{}", 403},
		{"POST", "/api/workspaces/" + ws.ID + "/leave", "{}", 403},
		{"POST", "/api/workspaces/join/token", "{}", 403},
		{"GET", "/api/me/files", "", 403},
		{"GET", "/api/me/memories", "", 403},
		{"GET", "/api/me", "", 204},
		{"GET", "/api/events", "", 204},
		{"GET", "/api/workspaces", "", 204},
		{"PATCH", "/api/me/settings", "{}", 204},
		{"GET", "/api/models", "", 403},
		{"GET", "/api/models?workspace_id=" + ws.ID, "", 204},
		{"GET", "/api/future-personal-endpoint?workspace_id=" + ws.ID, "", 403},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			if rec := request(tc.method, tc.path, tc.body, probe); rec.Code != tc.want {
				t.Errorf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	body := fmt.Sprintf(`{"name":"New","workspace_id":%q}`, ws.ID)
	rec := request("POST", "/api/projects", body, func(_ Deps, w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if string(data) != body {
			t.Error("middleware consumed body")
		}
		w.WriteHeader(204)
	})
	if rec.Code != 204 {
		t.Fatalf("body-preserving request status=%d", rec.Code)
	}
	unlock := false
	if err := store.UpdateDomainUserAccess(ctx, d.DB, rule.Domain, user.ID, &unlock); err != nil {
		t.Fatal(err)
	}
	if rec := request("GET", "/api/conversations/"+personal.ID, "", probe); rec.Code != 204 {
		t.Fatalf("same-session unlock=%d", rec.Code)
	}
}

func TestDomainRegistrationVerificationPolicyAndAdminCreate(t *testing.T) {
	d := newAuthSecurityDeps(t, "domain-signup.db")
	ctx := t.Context()
	owner, err := store.CreateUserWithRole(ctx, d.DB, "admin@outside.example", "Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	assignee, err := store.CreateUser(ctx, d.DB, "manager@outside.example", "Manager", "hash")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/admin/workspaces", strings.NewReader(fmt.Sprintf(`{"name":"Company","owner_id":%q}`, assignee.ID)))
	r = r.WithContext(context.WithValue(ctx, userCtxKey{}, owner))
	rec := httptest.NewRecorder()
	adminCreateWorkspaceHandler(d, rec, r)
	if rec.Code != 201 {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	var ws store.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatal(err)
	}
	if ws.OwnerID != assignee.ID {
		t.Fatal("wrong assigned owner")
	}
	initialGroup, err := store.CreateUserGroup(ctx, d.DB, store.UserGroup{Name: "Company Members"})
	if err != nil {
		t.Fatal(err)
	}
	rule := store.RegistrationDomain{Domain: "company.example", WorkspaceID: ws.ID, Enabled: true, LockPersonal: true, EmailVerificationRequired: false, InitialGroupID: initialGroup.ID}
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(d.DB, "email_verification_required", false); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()

	register := func(email string) (*httptest.ResponseRecorder, *store.User) {
		t.Helper()
		result := runAuthJSONHandler(t, d, registerHandler, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":"password123","name":"New"}`, email))
		user, findErr := store.FindUserByEmail(ctx, d.DB, email)
		if findErr != nil {
			t.Fatalf("find %s: %v", email, findErr)
		}
		return result, user
	}

	// Both switches off: password registration is activated immediately while
	// domain enrollment still happens atomically.
	rec, user := register("direct@company.example")
	if rec.Code != 200 || user.Status != "active" || user.GroupID != initialGroup.ID || responseCookie(rec, "auth_token") == nil {
		t.Fatalf("direct signup response=%d body=%s user=%+v", rec.Code, rec.Body.String(), user)
	}
	if role, err := store.IsWorkspaceMember(ctx, d.DB, ws.ID, user.ID); err != nil || role != "member" {
		t.Fatalf("direct signup membership=%q err=%v", role, err)
	}

	// A domain rule may require verification independently of the global switch.
	rule.EmailVerificationRequired = true
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	rec, user = register("domain-verify@company.example")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"verification_required":true`) || user.Status != "pending" || user.GroupID != initialGroup.ID || responseCookie(rec, "auth_token") != nil {
		t.Fatalf("domain verification response=%d body=%s user=%+v", rec.Code, rec.Body.String(), user)
	}

	// The global requirement remains a ceiling even when this domain opts out.
	rule.EmailVerificationRequired = false
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(d.DB, "email_verification_required", true); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	rec, user = register("global-verify@company.example")
	if rec.Code != 200 || user.Status != "pending" || responseCookie(rec, "auth_token") != nil {
		t.Fatalf("global verification response=%d body=%s user=%+v", rec.Code, rec.Body.String(), user)
	}

	if err := store.SetSetting(d.DB, "email_verification_required", false); err != nil {
		t.Fatal(err)
	}
	store.InvalidateConfig()
	rec, user = register("ordinary@outside.example")
	if rec.Code != 200 || user.Status != "active" || user.GroupID != store.DefaultGroupID || responseCookie(rec, "auth_token") == nil {
		t.Fatalf("ordinary signup response=%d body=%s user=%+v", rec.Code, rec.Body.String(), user)
	}
}
