package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminDomainWithoutWorkspaceAndPersonalAccess(t *testing.T) {
	d, adminToken, userToken := newAuditEvidenceFixture(t)
	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/domains", requireAdmin(d, adminSaveDomainHandler))
	mx.handle(http.MethodGet, "/api/admin/domains", requireAdmin(d, adminListDomainsHandler))
	mx.handle(http.MethodPatch, "/api/admin/domains/:domain", requireAdmin(d, adminSaveDomainHandler))
	mx.handle(http.MethodPost, "/api/admin/domains/:domain/users", requireAdmin(d, adminEnrollDomainUsersHandler))
	mx.handle(http.MethodPatch, "/api/admin/domains/:domain/users/:uid", requireAdmin(d, adminDomainUserAccessHandler))
	mx.handle(http.MethodGet, "/api/conversations", requireAuth(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		return auditEvidenceRequest(mx, method, path, adminToken, body)
	}
	if response := request(http.MethodPost, "/api/admin/domains", `{"domain":"unassigned.example","email_verification_required":true}`); response.Code != http.StatusOK {
		t.Fatalf("create unassigned rule: %d %s", response.Code, response.Body.String())
	}
	response := request(http.MethodGet, "/api/admin/domains", "")
	var listed struct {
		Domains []store.RegistrationDomain `json:"domains"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || response.Code != http.StatusOK || len(listed.Domains) != 1 || listed.Domains[0].WorkspaceID != "" || listed.Domains[0].LockPersonal {
		t.Fatalf("list unassigned rule: %+v, %v; response=%s", listed, err, response.Body.String())
	}
	if response := request(http.MethodPatch, "/api/admin/domains/unassigned.example", `{"lock_personal":true}`); response.Code != http.StatusBadRequest {
		t.Fatalf("lock unassigned rule: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/admin/domains/unassigned.example/users", `{"user_ids":["audit-member"]}`); response.Code != http.StatusOK {
		t.Fatalf("enroll unassigned member: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPatch, "/api/admin/domains/unassigned.example/users/audit-member", `{"lock_override":true}`); response.Code != http.StatusBadRequest {
		t.Fatalf("lock unassigned member: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPatch, "/api/admin/domains/unassigned.example/users/audit-member", `{"lock_override":false}`); response.Code != http.StatusOK {
		t.Fatalf("unlock unassigned member: %d %s", response.Code, response.Body.String())
	}
	personal := auditEvidenceRequest(mx, http.MethodGet, "/api/conversations", userToken, "")
	if personal.Code != http.StatusNoContent {
		t.Fatalf("unassigned rule blocked personal space: %d %s", personal.Code, personal.Body.String())
	}
}

func TestAdminDomainWorkspaceChangeRequiresNoMembers(t *testing.T) {
	d, adminToken, _ := newAuditEvidenceFixture(t)
	workspace, err := store.CreateWorkspace(t.Context(), d.DB, "audit-admin", "Target")
	if err != nil {
		t.Fatal(err)
	}
	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/domains", requireAdmin(d, adminSaveDomainHandler))
	mx.handle(http.MethodPatch, "/api/admin/domains/:domain", requireAdmin(d, adminSaveDomainHandler))
	mx.handle(http.MethodPost, "/api/admin/domains/:domain/users", requireAdmin(d, adminEnrollDomainUsersHandler))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return auditEvidenceRequest(mx, method, path, adminToken, body)
	}
	if response := request(http.MethodPost, "/api/admin/domains", `{"domain":"company.example"}`); response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	for _, workspaceID := range []string{workspace.ID, "", workspace.ID} {
		body, err := json.Marshal(map[string]any{"workspace_id": workspaceID, "enabled": true})
		if err != nil {
			t.Fatal(err)
		}
		if response := request(http.MethodPatch, "/api/admin/domains/company.example", string(body)); response.Code != http.StatusOK {
			t.Fatalf("change empty rule to %q: %d %s", workspaceID, response.Code, response.Body.String())
		}
	}
	if response := request(http.MethodPost, "/api/admin/domains/company.example/users", `{"user_ids":["audit-member"]}`); response.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", response.Code, response.Body.String())
	}
	// The server must check actual membership, even if the editor loaded before
	// enrollment or a caller supplies an inaccurate member_count.
	response := request(http.MethodPatch, "/api/admin/domains/company.example", `{"workspace_id":"","member_count":0,"enabled":false}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "domain_workspace_has_members") {
		t.Fatalf("change populated rule: %d %s", response.Code, response.Body.String())
	}
	rules, err := store.ListRegistrationDomains(t.Context(), d.DB)
	if err != nil || len(rules) != 1 || rules[0].WorkspaceID != workspace.ID || !rules[0].Enabled || rules[0].MemberCount != 1 {
		t.Fatalf("rejected update changed rule: %+v, %v", rules, err)
	}
	if response := request(http.MethodPatch, "/api/admin/domains/company.example", `{"workspace_id":"`+workspace.ID+`","enabled":true,"lock_personal":true}`); response.Code != http.StatusOK {
		t.Fatalf("edit other policies: %d %s", response.Code, response.Body.String())
	}
}

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
