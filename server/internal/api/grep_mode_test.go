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

func TestGrepModeAllowsKBAndProjectCreationWithoutEmbeddingModel(t *testing.T) {
	ctx := context.Background()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)

	db, err := store.Open(filepath.Join(t.TempDir(), "api-grep.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES('admin1','admin@example.test','h','admin')`); err != nil {
		t.Fatal(err)
	}
	user := &store.User{ID: "admin1", Email: "admin@example.test", Role: "admin", Status: "active"}
	withAdminUser := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userCtxKey{}, user))
	}
	d := Deps{DB: db}

	// Switch global document retrieval mode to grep via admin handler.
	patchBody := `{"document_retrieval_mode":"grep","grep_full_text_threshold":500,"grep_code_full_text_max_lines":200,"grep_max_matches":25,"grep_context_lines":2,"grep_read_max_lines":150,"grep_read_max_chars":12000}`
	req := withAdminUser(httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(patchBody)))
	rec := httptest.NewRecorder()
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("adminSettingsSet status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mode := store.DocumentRetrievalMode(db); mode != store.DocumentRetrievalModeGrep {
		t.Fatalf("DocumentRetrievalMode=%q, want grep", mode)
	}

	// Reject invalid mode.
	badReq := withAdminUser(httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"document_retrieval_mode":"invalid"}`)))
	badRec := httptest.NewRecorder()
	adminSettingsSet(d, badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status=%d, want 400", badRec.Code)
	}

	// Create two KBs without any embedding model configured.
	for _, name := range []string{"Grep KB One", "Grep KB Two"} {
		body, _ := json.Marshal(map[string]any{"name": name})
		kbReq := withAdminUser(httptest.NewRequest(http.MethodPost, "/api/kbs", strings.NewReader(string(body))))
		kbRec := httptest.NewRecorder()
		createKBHandler(d, kbRec, kbReq)
		if kbRec.Code != http.StatusCreated {
			t.Fatalf("createKB(%s) status=%d body=%s", name, kbRec.Code, kbRec.Body.String())
		}
	}

	kbs, err := store.ListKBs(ctx, db, user.ID)
	if err != nil || len(kbs) != 2 {
		t.Fatalf("ListKBs=%d err=%v", len(kbs), err)
	}
	if err := store.ValidateKBEmbeddingCompatibility(ctx, db, []string{kbs[0].ID, kbs[1].ID}); err != nil {
		t.Fatalf("ValidateKBEmbeddingCompatibility in grep mode failed: %v", err)
	}

	// Create a project without embedding model; in grep mode it still creates its library KB.
	projReq := withAdminUser(httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"Grep Project"}`)))
	projRec := httptest.NewRecorder()
	createProjectHandler(d, projRec, projReq)
	if projRec.Code != http.StatusCreated {
		t.Fatalf("createProject status=%d body=%s", projRec.Code, projRec.Body.String())
	}
	var projResp struct {
		ID   string `json:"id"`
		KBID string `json:"kb_id"`
	}
	if err := json.Unmarshal(projRec.Body.Bytes(), &projResp); err != nil || projResp.KBID == "" {
		t.Fatalf("expected project library kb_id in grep mode, got %s err=%v", projRec.Body.String(), err)
	}
}
