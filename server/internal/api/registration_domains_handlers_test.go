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
