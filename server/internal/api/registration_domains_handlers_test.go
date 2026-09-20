package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminDomainMemberSearchAddAndRemoveRoutes(t *testing.T) {
	d := newAuthSecurityDeps(t, "domain-member-admin.db")
	ctx := t.Context()
	admin, err := store.CreateUserWithRole(ctx, d.DB, "admin@outside.example", "Platform Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := store.CreateWorkspace(ctx, d.DB, admin.ID, "Company")
	if err != nil {
		t.Fatal(err)
	}
	rule := store.RegistrationDomain{
		Domain: "company.example", Domains: []string{"company.example", "affiliate.example"},
		WorkspaceID: workspace.ID, LockPersonal: true,
	}
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, true); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.CreateUser(ctx, d.DB, "person@unrelated.example", "Search Target", "hash")
	if err != nil {
		t.Fatal(err)
	}
	token := issueBoundTestAccessToken(t, d.DB, d.Auth, admin)
	router := NewRouter(d)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	search := request(http.MethodGet, "/api/admin/domains/company.example/candidates?q=search%20target", "")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), candidate.ID) {
		t.Fatalf("candidate search status=%d body=%s", search.Code, search.Body.String())
	}
	add := request(http.MethodPost, "/api/admin/domains/company.example/users", `{"user_ids":["`+candidate.ID+`"]}`)
	if add.Code != http.StatusOK || !strings.Contains(add.Body.String(), `"added":1`) {
		t.Fatalf("member add status=%d body=%s", add.Code, add.Body.String())
	}
	remove := request(http.MethodDelete, "/api/admin/domains/company.example/users/"+candidate.ID, "")
	if remove.Code != http.StatusOK || !strings.Contains(remove.Body.String(), `"workspace_membership_removed":true`) {
		t.Fatalf("member remove status=%d body=%s", remove.Code, remove.Body.String())
	}
	if access, err := store.GetDomainAccess(ctx, d.DB, candidate.ID); err != nil || access != nil {
		t.Fatalf("removed member access=%+v err=%v", access, err)
	}
	if again := request(http.MethodDelete, "/api/admin/domains/company.example/users/"+candidate.ID, ""); again.Code != http.StatusNotFound {
		t.Fatalf("second removal status=%d body=%s", again.Code, again.Body.String())
	}
}

func TestDomainSubscriptionPolicyAndCurrentPrivateGroup(t *testing.T) {
	d := newAuthSecurityDeps(t, "domain-subscriptions.db")
	ctx := t.Context()
	admin, err := store.CreateUserWithRole(ctx, d.DB, "owner@outside.example", "Owner", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := store.CreateWorkspace(ctx, d.DB, admin.ID, "Company")
	if err != nil {
		t.Fatal(err)
	}
	rule := store.RegistrationDomain{Domain: "company.example", WorkspaceID: workspace.ID}
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, d.DB, "user@company.example", "Member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE user_groups SET is_public=0 WHERE id=?`, user.GroupID); err != nil {
		t.Fatal(err)
	}
	token := issueBoundTestAccessToken(t, d.DB, d.Auth, user)
	request := func(method, path string, h handler) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		requireAuth(d, h).ServeHTTP(rec, req)
		return rec
	}
	probe := func(_ Deps, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	checkoutPaths := []string{"/api/payments/checkout", "/api/payments/orders/existing/resume"}
	for _, path := range checkoutPaths {
		if rec := request(http.MethodPost, path, probe); rec.Code != http.StatusNoContent {
			t.Fatalf("default policy %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rule.SubscriptionPurchaseDisabled = true
	rule.Enabled = false // Stopping enrollment must not release existing members.
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	rules, err := store.ListRegistrationDomains(ctx, d.DB)
	if err != nil || len(rules) != 1 || !rules[0].SubscriptionPurchaseDisabled {
		t.Fatalf("saved policy=%+v err=%v", rules, err)
	}
	for _, path := range checkoutPaths {
		if rec := request(http.MethodPost, path, probe); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "subscription_purchase_disabled") {
			t.Fatalf("restricted %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := request(http.MethodGet, "/api/me", meHandler); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscription_purchase_disabled":true`) {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/api/user-groups", listUserGroupsPublic); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), user.GroupID) {
		t.Fatalf("current private group: %d %s", rec.Code, rec.Body.String())
	}
	public := httptest.NewRecorder()
	listUserGroupsPublic(d, public, httptest.NewRequest(http.MethodGet, "/api/public/user-groups", nil))
	if strings.Contains(public.Body.String(), user.GroupID) {
		t.Fatalf("private group exposed publicly: %s", public.Body.String())
	}
	if rec := request(http.MethodGet, "/api/payments/orders/existing", probe); rec.Code != http.StatusNoContent {
		t.Fatalf("order reads: %d %s", rec.Code, rec.Body.String())
	}
	rule.SubscriptionPurchaseDisabled = false
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodPost, checkoutPaths[0], probe); rec.Code != http.StatusNoContent {
		t.Fatalf("live re-enable: %d %s", rec.Code, rec.Body.String())
	}
	rule.SubscriptionPurchaseDisabled = true
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemoveDomainUser(ctx, d.DB, rule.Domain, user.ID); err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodGet, "/api/me", meHandler); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscription_purchase_disabled":false`) {
		t.Fatalf("unbound profile: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, checkoutPaths[0], probe); rec.Code != http.StatusNoContent {
		t.Fatalf("unbound purchase: %d %s", rec.Code, rec.Body.String())
	}
}
