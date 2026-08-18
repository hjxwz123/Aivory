package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestEnterpriseAuthPolicyAllowsRequiredPasswordAndProtectsProvider(t *testing.T) {
	d := newAuthSecurityDeps(t, "enterprise-auth-policy.db")
	admin, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, store.OAuthProvider{
		ID: "oa_enterprise", Kind: "google", Name: "Company login", ClientID: "client-id", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindOAuthIdentity(t.Context(), d.DB, provider.ID, "admin-subject", admin.ID, admin.Email); err != nil {
		t.Fatal(err)
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{
		"password_login_enabled": false,
		"auth_entry_mode": "provider_picker",
		"oauth_initial_password_policy": "required"
	}`))
	patch.Header.Set("Content-Type", "application/json")
	patch = patch.WithContext(context.WithValue(patch.Context(), userCtxKey{}, admin))
	patchRec := httptest.NewRecorder()
	adminSettingsSet(d, patchRec, patch)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("save enterprise policy status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}

	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/oauth-providers/:id", wrap(d, deleteOAuthProviderAdmin))
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/admin/oauth-providers/"+provider.ID, nil)
	deleteReq = deleteReq.WithContext(context.WithValue(deleteReq.Context(), userCtxKey{}, admin))
	deleteRec := httptest.NewRecorder()
	mx.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict || !strings.Contains(deleteRec.Body.String(), errAuthPolicyProviderRequired.Error()) {
		t.Fatalf("delete required provider status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := store.GetOAuthProvider(t.Context(), d.DB, provider.ID); err != nil {
		t.Fatalf("required provider was deleted: %v", err)
	}
}
