package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestHTMLPreviewShareReturnsOnlySandboxedHTML(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "html-preview.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('preview-admin','preview@example.test','h','admin','active')`)

	html := `<!doctype html><title>Shared</title><script>document.body.dataset.ready='yes'</script>`
	body, _ := json.Marshal(map[string]string{"html": html})
	req := httptest.NewRequest(http.MethodPost, "/api/html-previews", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "preview-admin", Role: "admin", Status: "active"}))
	rec := httptest.NewRecorder()
	createHTMLPreviewShareHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || !strings.HasPrefix(created.ID, "hp_") || created.URL == "" {
		t.Fatalf("invalid create response: body=%s err=%v", rec.Body.String(), err)
	}

	publicReq := httptest.NewRequest(http.MethodGet, created.URL, nil)
	publicReq = publicReq.WithContext(context.WithValue(publicReq.Context(), pathCtxKey{}, map[string]string{"token": created.ID}))
	publicRec := httptest.NewRecorder()
	publicHTMLPreviewShareHandler(Deps{DB: db}, publicRec, publicReq)
	if publicRec.Code != http.StatusOK || publicRec.Body.String() != html {
		t.Fatalf("public status=%d body=%q", publicRec.Code, publicRec.Body.String())
	}
	if got := publicRec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := publicRec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox allow-scripts") || strings.Contains(got, "allow-same-origin") {
		t.Fatalf("unsafe Content-Security-Policy=%q", got)
	}
	if got := publicRec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy=%q", got)
	}
}

func TestHTMLPreviewShareRejectsOversizedHTML(t *testing.T) {
	reqBody, _ := json.Marshal(map[string]string{"html": strings.Repeat("x", maxHTMLPreviewShareBytes+1)})
	req := httptest.NewRequest(http.MethodPost, "/api/html-previews", bytes.NewReader(reqBody))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "unused"}))
	rec := httptest.NewRecorder()
	createHTMLPreviewShareHandler(Deps{}, rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHTMLPreviewShareFollowsCurrentSharingPermission(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "html-preview-permission.db"))
	defer db.Close()
	permissions := store.DefaultUserGroupPermissions()
	allowedRaw, _ := json.Marshal(permissions)
	mustExec(t, db, `INSERT INTO user_groups(id,name,permissions) VALUES('preview-group','Preview group',?)`, string(allowedRaw))
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('preview-user','preview-user@example.test','h','user','active','preview-group')`)

	share, err := store.CreateHTMLPreviewShare(t.Context(), db, "preview-user", "<h1>Preview</h1>")
	if err != nil {
		t.Fatalf("create preview share: %v", err)
	}
	permissions.AllowSharing = false
	revokedRaw, _ := json.Marshal(permissions)
	mustExec(t, db, `UPDATE user_groups SET permissions=? WHERE id='preview-group'`, string(revokedRaw))

	req := httptest.NewRequest(http.MethodGet, "/api/public/html-previews/"+share.ID, nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"token": share.ID}))
	rec := httptest.NewRecorder()
	publicHTMLPreviewShareHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want not found after permission revocation", rec.Code, rec.Body.String())
	}
}
