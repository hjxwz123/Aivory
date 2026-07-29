package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestAdminModelTagsReorderPersistsAndRenamePreservesOrder(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-model-tags.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO model_tags(id, name, sort_order) VALUES
		('tag_alpha', 'Alpha', 0),
		('tag_beta', 'Beta', 1),
		('tag_gamma', 'Gamma', 2)`)

	d := Deps{DB: db}
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/model-tags", wrap(d, listModelTagsAdmin))
	// The concrete route must precede /:id or the mux treats "reorder" as an id.
	mx.handle(http.MethodPatch, "/api/admin/model-tags/reorder", wrap(d, reorderModelTagsAdmin))
	mx.handle(http.MethodPatch, "/api/admin/model-tags/:id", wrap(d, updateModelTagAdmin))

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}

	reorder := request(http.MethodPatch, "/api/admin/model-tags/reorder",
		`{"ids":["tag_gamma","tag_alpha","tag_beta"]}`)
	if reorder.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, want %d; body=%s", reorder.Code, http.StatusOK, reorder.Body.String())
	}
	assertModelTagOrder(t, db, []string{"tag_gamma", "tag_alpha", "tag_beta"})

	rename := request(http.MethodPatch, "/api/admin/model-tags/tag_alpha", `{"name":"Alpha renamed"}`)
	if rename.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want %d; body=%s", rename.Code, http.StatusOK, rename.Body.String())
	}
	var renamed store.ModelTag
	if err := json.Unmarshal(rename.Body.Bytes(), &renamed); err != nil {
		t.Fatalf("decode renamed tag: %v; body=%s", err, rename.Body.String())
	}
	if renamed.Name != "Alpha renamed" || renamed.SortOrder != 1 {
		t.Fatalf("renamed tag = %+v, want name Alpha renamed at sort order 1", renamed)
	}
	assertModelTagOrder(t, db, []string{"tag_gamma", "tag_alpha", "tag_beta"})
}

func assertModelTagOrder(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	tags, err := store.ListModelTags(t.Context(), db)
	if err != nil {
		t.Fatalf("list model tags: %v", err)
	}
	if len(tags) != len(want) {
		t.Fatalf("tag count = %d, want %d; tags=%+v", len(tags), len(want), tags)
	}
	for i, tag := range tags {
		if tag.ID != want[i] || tag.SortOrder != i {
			t.Fatalf("tag %d = %+v, want id %q at sort order %d", i, tag, want[i], i)
		}
	}
}
