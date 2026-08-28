package api

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/config"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

// embeddingGuardDeps mirrors the admin_models_test.go Deps fixture shape.
func embeddingGuardDeps(t *testing.T, db *sql.DB) Deps {
	t.Helper()
	return Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
}

func TestChannelDeleteRefusedWhileEmbeddingModelIsGlobalLock(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "ch-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"emb1"')`)
	d := embeddingGuardDeps(t, db)
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/channels/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteChannelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/channels/ch1", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("channel delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "embedding_model_locked") {
		t.Fatalf("expected embedding_model_locked code, body=%s", rec.Body.String())
	}
	var count int
	mustQuery(t, db, `SELECT COUNT(*) FROM channels WHERE id='ch1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("referenced channel was deleted")
	}
}

func TestChannelDeleteRefusedWhileEmbeddingModelIsKBLocked(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "ch-kblock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Handbook','emb1',1536)`)
	d := embeddingGuardDeps(t, db)
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/channels/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteChannelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/channels/ch1", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("channel delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "embedding_model_in_use") {
		t.Fatalf("expected embedding_model_in_use code, body=%s", rec.Body.String())
	}
	var count int
	mustQuery(t, db, `SELECT COUNT(*) FROM models WHERE id='emb1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("KB-locked embedding model was destroyed")
	}
}

func TestChannelDeleteAllowedWhenEmbeddingModelUnreferenced(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "ch-free.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	d := embeddingGuardDeps(t, db)
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/channels/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteChannelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/channels/ch1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unreferenced channel delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var count int
	mustQuery(t, db, `SELECT COUNT(*) FROM models WHERE id='emb1'`).Scan(&count)
	if count != 0 {
		t.Fatalf("cascade delete did not remove the unreferenced model")
	}
}

func TestModelDeleteRefusedWhileKBLocked(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-kblock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Handbook','emb1',1536)`)
	d := embeddingGuardDeps(t, db)
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteModelAdmin(d, w, r)
	})

	rec := httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/models/emb1", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("model delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "embedding_model_in_use") {
		t.Fatalf("expected embedding_model_in_use code, body=%s", rec.Body.String())
	}
	var count int
	mustQuery(t, db, `SELECT COUNT(*) FROM models WHERE id='emb1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("KB-locked embedding model was deleted")
	}
}

func TestKBLockedEmbeddingModelCannotBeDisabledOrRepointed(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "model-kbupdate.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Handbook','emb1',1536)`)
	// Deliberately NO settings.embedding_model_id row: the KB lock alone must
	// protect vector identity (historically only the global lock did).
	d := embeddingGuardDeps(t, db)
	mx := newMux()
	mx.handle(http.MethodPatch, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		updateModelAdmin(d, w, r)
	})

	patch := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/models/emb1", strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		mx.ServeHTTP(rec, req)
		return rec
	}
	if rec := patch(`{"enabled":false}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "embedding_model_locked") {
		t.Fatalf("KB-locked model disable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := patch(`{"kind":"chat"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "embedding_model_locked") {
		t.Fatalf("KB-locked model kind change status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := patch(`{"label":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("display-field change status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestEmbeddingSettingLockRepairableWhenModelMissing(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "lock-repair.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb2','ch1','embedding','text-embedding-3-small','Emb2',1,1536)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb-off','ch1','embedding','text-embedding-off','Off',0,1536)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES('chat1','ch1','chat','gpt-x','Chat',1)`)
	// The historical damage: the lock points at a model that no longer exists.
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"ghost"')`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}

	setEmbedding := func(t *testing.T, value string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"embedding_model_id": value})
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(string(body)))
		req.Header.Set("content-type", "application/json")
		adminSettingsSet(d, rec, req)
		return rec
	}
	readSetting := func() string {
		var raw string
		mustQuery(t, db, `SELECT value FROM settings WHERE key='embedding_model_id'`).Scan(&raw)
		var v string
		_ = json.Unmarshal([]byte(raw), &v)
		return v
	}

	// A dangling lock refuses an invalid replacement (chat model)…
	if rec := setEmbedding(t, "chat1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("repair to chat model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// …a nonexistent id… (pin both untested repair branches: while the lock is
	// still dangling, NOT afterwards — post-repair requests short-circuit to
	// the 409 live-lock path and never reach the replacement validation)
	if rec := setEmbedding(t, "ghost2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("repair to missing model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// …and a disabled embedding model.
	if rec := setEmbedding(t, "emb-off"); rec.Code != http.StatusBadRequest {
		t.Fatalf("repair to disabled embedding status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// …accepts a real embedding model…
	if rec := setEmbedding(t, "emb2"); rec.Code != http.StatusOK {
		t.Fatalf("repair status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := readSetting(); got != "emb2" {
		t.Fatalf("repaired lock = %q, want emb2", got)
	}
	// …and once repaired the lock is live again: further changes refuse.
	if rec := setEmbedding(t, "emb-other"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "embedding_model_locked") {
		t.Fatalf("post-repair change status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestEmbeddingSettingLockClearableWhenModelMissing(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "lock-clear.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"ghost"')`)
	d := Deps{
		DB:     db,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()},
		Logger: log.New(io.Discard, "", 0),
	}
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"embedding_model_id": ""})
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear dangling lock status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var raw string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='embedding_model_id'`).Scan(&raw)
	if raw != `""` {
		t.Fatalf("lock not cleared: %q", raw)
	}
}

func TestEmbeddingSettingFirstSetRequiresValidEmbeddingModel(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "lock-firstset.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb1',1,1536)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES('chat1','ch1','chat','gpt-x','Chat',1)`)
	d := embeddingGuardDeps(t, db)

	setEmbedding := func(t *testing.T, value string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"embedding_model_id": value})
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(string(body)))
		req.Header.Set("content-type", "application/json")
		adminSettingsSet(d, rec, req)
		return rec
	}

	// The write-once lock must never be ESTABLISHED onto a row that can't serve
	// embeddings — that used to be a permanent dead end (only healable via raw
	// SQL) before the repair valve widened to kind-invalid locks.
	if rec := setEmbedding(t, "chat1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("first set to chat model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := setEmbedding(t, "ghost"); rec.Code != http.StatusBadRequest {
		t.Fatalf("first set to missing model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := setEmbedding(t, "emb1"); rec.Code != http.StatusOK {
		t.Fatalf("first set to valid embedding status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestEmbeddingSettingChatKindLockIsRepairable(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "lock-chatkind.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled) VALUES('chat1','ch1','chat','gpt-x','Chat',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb2','ch1','embedding','text-embedding-3-small','Emb2',1,1536)`)
	// Live-but-invalid lock (legacy bad first-set or archive drift): the row
	// exists so the old row-absence valve stayed shut — forever.
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"chat1"')`)
	d := embeddingGuardDeps(t, db)

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"embedding_model_id": "emb2"})
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("repair chat-kind lock status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='embedding_model_id'`).Scan(&got)
	if got != `"emb2"` {
		t.Fatalf("lock not repaired: %q", got)
	}
	// A chat-kind lock is unusable, but still delete-guarded on id equality —
	// deleting chat1 while locked would just dangle again; the admin clears or
	// repairs the setting first.
	mx := newMux()
	mx.handle(http.MethodDelete, "/api/admin/models/:id", func(w http.ResponseWriter, r *http.Request) {
		deleteModelAdmin(d, w, r)
	})
	// (Post-repair the global lock points at emb2; chat1 is now free to delete.)
	rec = httptest.NewRecorder()
	mx.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/models/chat1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unlocked chat model delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfigImportCannotOverwriteKBLockedEmbeddingModelRow(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-kblock.db"))
	defer db.Close()
	// KB-locked embedding model with NO global settings lock — the state
	// TestKBLockedEmbeddingModelCannotBeDisabledOrRepointed proves the HTTP
	// PATCH guard protects. The config archive must honor the same invariant.
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,1536)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Handbook','emb1',1536)`)
	d := embeddingGuardDeps(t, db)

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(mw).Encode(configManifest{Format: "aivory-config", Version: configArchiveVersion, Tables: []string{"models"}, MergeMode: "upsert"}); err != nil {
		t.Fatal(err)
	}
	models, err := zw.Create("db/models.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := models.Write([]byte(`{"id":"emb1","channel_id":"ch1","kind":"chat","request_id":"text-embedding-3-large","label":"Emb","enabled":1,"dim":3072}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	body, contentType := multipartArchive(t, archive.Bytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/config/import", body)
	req.Header.Set("content-type", contentType)
	req = authorizeConfigImportRequestForTest(t, db, req)
	importConfigAdmin(d, rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "embedding_model_locked") {
		t.Fatalf("config import over KB-locked model status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var kind string
	var dim int
	mustQuery(t, db, `SELECT kind, dim FROM models WHERE id='emb1'`).Scan(&kind, &dim)
	if kind != "embedding" || dim != 1536 {
		t.Fatalf("config import rewrote KB-locked embedding identity: kind=%q dim=%d", kind, dim)
	}
}

func TestUserDocumentsDeriveParserNotConfiguredCode(t *testing.T) {
	rows := []store.Document{
		{
			ID:     "d1",
			Status: "failed",
			Error: "scan.pdf — could not extract text (2 MB). " + rag.ParserNotConfiguredCode +
				": this document looks scanned/image-only, which needs MinerU OCR — but MinerU isn't fully configured. Missing: mineru_api_url is empty.",
		},
		{
			ID:     "d2",
			Status: "failed",
			Error:  "scan2.pdf — could not extract text (2 MB). MinerU OCR was attempted but failed: status 500: boom",
		},
		{
			ID:     "d3",
			Status: "ready",
		},
	}
	out := userDocuments(rows)
	if out[0].Error != "" {
		t.Fatalf("user responses must never carry raw ingest diagnostics, got %q", out[0].Error)
	}
	if out[0].ErrorCode != rag.ParserNotConfiguredCode {
		t.Fatalf("parser-not-configured document lost its machine code: %+v", out[0])
	}
	if out[1].ErrorCode != "" {
		t.Fatalf("a genuine MinerU attempt failure must not be labeled as unconfigured: %+v", out[1])
	}
	if out[2].ErrorCode != "" {
		t.Fatalf("ready document must not carry a code: %+v", out[2])
	}
	single := userDocument(&rows[0])
	if single.Error != "" || single.ErrorCode != rag.ParserNotConfiguredCode {
		t.Fatalf("userDocument sanitize mismatch: %+v", single)
	}
}
