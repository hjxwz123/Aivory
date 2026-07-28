package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	appconfig "aivory/server/internal/config"
	"aivory/server/internal/store"
)

type oauthAdminFixture struct {
	db  *sql.DB
	mux *mux
}

func newOAuthAdminFixture(t *testing.T, callbackBase string) oauthAdminFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "oauth-admin.db"))
	t.Cleanup(func() { _ = db.Close() })
	d := Deps{DB: db, Config: appconfig.Config{OAuthCallbackBaseURL: callbackBase}}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/oauth-providers", wrap(d, listOAuthProvidersAdmin))
	mx.handle(http.MethodPost, "/api/admin/oauth-providers/prepare", wrap(d, prepareOAuthProviderAdmin))
	mx.handle(http.MethodPost, "/api/admin/oauth-providers", wrap(d, createOAuthProviderAdmin))
	return oauthAdminFixture{db: db, mux: mx}
}

func (fx oauthAdminFixture) request(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s request: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func decodeOAuthAdminResponse[T any](t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("response status = %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var result T
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return result
}

func TestOAuthProviderPreparedIDPersistsWithCanonicalCallback(t *testing.T) {
	fx := newOAuthAdminFixture(t, "https://login.example.test/")
	prepared := decodeOAuthAdminResponse[preparedOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers/prepare", nil), http.StatusOK)
	if !validPreparedRecordID(prepared.ID, "oa") {
		t.Fatalf("prepared oauth provider id = %q, want oa_<12 lowercase hex>", prepared.ID)
	}
	wantRedirect := "https://login.example.test/api/auth/oauth/" + prepared.ID + "/callback"
	if prepared.RedirectURI != wantRedirect {
		t.Fatalf("prepared redirect URI = %q, want %q", prepared.RedirectURI, wantRedirect)
	}

	created := decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"id": prepared.ID, "kind": "google", "name": "Prepared Google", "enabled": false,
		}), http.StatusCreated)
	if created.ID != prepared.ID {
		t.Fatalf("created id = %q, want prepared id %q", created.ID, prepared.ID)
	}
	if created.RedirectURI != prepared.RedirectURI {
		t.Fatalf("created redirect URI = %q, want prepared URI %q", created.RedirectURI, prepared.RedirectURI)
	}

	listed := decodeOAuthAdminResponse[[]adminOAuthProviderResponse](t,
		fx.request(t, http.MethodGet, "/api/admin/oauth-providers", nil), http.StatusOK)
	if len(listed) != 1 || listed[0].RedirectURI != prepared.RedirectURI {
		t.Fatalf("listed providers = %+v, want the canonical prepared URI", listed)
	}
}

func TestOAuthProviderCreateRejectsInvalidAndDuplicatePreparedIDs(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	invalid := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
		"id": "oa_NOT_HEX", "kind": "google", "name": "Invalid ID",
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid prepared id status = %d, want %d; body=%s", invalid.Code, http.StatusBadRequest, invalid.Body.String())
	}

	prepared := decodeOAuthAdminResponse[preparedOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers/prepare", nil), http.StatusOK)
	decodeOAuthAdminResponse[adminOAuthProviderResponse](t,
		fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
			"id": prepared.ID, "kind": "github", "name": "First provider",
		}), http.StatusCreated)
	duplicate := fx.request(t, http.MethodPost, "/api/admin/oauth-providers", map[string]any{
		"id": prepared.ID, "kind": "github", "name": "Different provider name",
	})
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate prepared id status = %d, want %d; body=%s", duplicate.Code, http.StatusConflict, duplicate.Body.String())
	}
	if !strings.Contains(duplicate.Body.String(), store.ErrOAuthProviderIDExists.Error()) {
		t.Fatalf("duplicate prepared id body = %s, want %q", duplicate.Body.String(), store.ErrOAuthProviderIDExists)
	}
}

func TestOAuthProviderPrepareRequiresAdmin(t *testing.T) {
	fx := newOAuthAdminFixture(t, "")
	router := NewRouter(Deps{DB: fx.db})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/oauth-providers/prepare", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("oauth prepare status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
