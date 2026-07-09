package api

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aurelia/server/internal/config"
	"aurelia/server/internal/store"
)

// TestBackupExportImportEndToEnd drives the real export + import handlers across
// two deployments with DIFFERENT upload/artifact dirs, verifying the zip round
// trip, file bundling, and storage_path rewrite.
func TestBackupExportImportEndToEnd(t *testing.T) {
	// --- Source deployment ---------------------------------------------------
	srcRoot := t.TempDir()
	srcUploads := filepath.Join(srcRoot, "uploads")
	srcArtifacts := filepath.Join(srcRoot, "artifacts")
	srcDB := openMigrated(t, filepath.Join(srcRoot, "src.db"))
	defer srcDB.Close()

	// A user, a conversation, a message, an uploaded file + a generated artifact.
	mustExec(t, srcDB, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, srcDB, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, srcDB, `INSERT INTO messages(id,conversation_id,role) VALUES('m1','c1','assistant')`)
	upPath := filepath.Join(srcUploads, "u1", "f_test.txt")
	artPath := filepath.Join(srcArtifacts, "m1_img.png")
	writeFile(t, upPath, []byte("hello-upload"))
	writeFile(t, artPath, []byte("PNGDATA"))
	mustExec(t, srcDB, `INSERT INTO files(id,user_id,filename,storage_path) VALUES('file1','u1','f_test.txt',?)`, upPath)
	mustExec(t, srcDB, `INSERT INTO artifacts(id,message_id,filename,storage_path) VALUES('art1','m1','img.png',?)`, artPath)

	srcDeps := Deps{
		DB:     srcDB,
		Config: config.Config{UploadDir: srcUploads, ArtifactDir: srcArtifacts},
		Logger: log.New(io.Discard, "", 0),
	}

	// Export with files=1.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/backup/export?files=1", nil)
	exportBackupAdmin(srcDeps, rec, req)
	if rec.Code != 200 {
		t.Fatalf("export status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); ct != "application/zip" {
		t.Fatalf("export content-type = %q", ct)
	}
	archive := rec.Body.Bytes()
	if len(archive) < 200 {
		t.Fatalf("archive suspiciously small: %d bytes", len(archive))
	}

	// --- Target deployment (different dirs) ----------------------------------
	tgtRoot := t.TempDir()
	tgtUploads := filepath.Join(tgtRoot, "up2")
	tgtArtifacts := filepath.Join(tgtRoot, "art2")
	_ = os.MkdirAll(tgtUploads, 0o755)
	_ = os.MkdirAll(tgtArtifacts, 0o755)
	tgtDB := openMigrated(t, filepath.Join(tgtRoot, "tgt.db"))
	defer tgtDB.Close()
	// Pre-existing junk that import must wipe.
	mustExec(t, tgtDB, `INSERT INTO users(id,email,password_hash,role) VALUES('old','x@y.z','h','user')`)

	tgtDeps := Deps{
		DB:     tgtDB,
		Config: config.Config{UploadDir: tgtUploads, ArtifactDir: tgtArtifacts},
		Logger: log.New(io.Discard, "", 0),
	}

	// Build the multipart import request.
	body, contentType := multipartArchive(t, archive)
	irec := httptest.NewRecorder()
	ireq := httptest.NewRequest("POST", "/api/admin/backup/import", body)
	ireq.Header.Set("content-type", contentType)
	importBackupAdmin(tgtDeps, irec, ireq)
	if irec.Code != 200 {
		t.Fatalf("import status = %d, body=%s", irec.Code, irec.Body.String())
	}
	var res struct {
		OK            bool             `json:"ok"`
		Tables        map[string]int64 `json:"tables"`
		FilesRestored int              `json:"files_restored"`
	}
	if err := json.Unmarshal(irec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode import response: %v (%s)", err, irec.Body.String())
	}
	if !res.OK || res.Tables["users"] != 1 || res.Tables["messages"] != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if res.FilesRestored != 2 {
		t.Fatalf("files_restored = %d, want 2", res.FilesRestored)
	}

	// The wiped junk is gone; the imported user is present.
	var userCount int
	mustQuery(t, tgtDB, "SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 1 {
		t.Fatalf("target users = %d, want 1 (junk should be wiped)", userCount)
	}
	var email string
	mustQuery(t, tgtDB, "SELECT email FROM users WHERE id='u1'").Scan(&email)
	if email != "a@b.c" {
		t.Fatalf("imported user email = %q", email)
	}

	// storage_path rewritten to the TARGET dirs, and the bytes exist there.
	var fPath, aPath string
	mustQuery(t, tgtDB, "SELECT storage_path FROM files WHERE id='file1'").Scan(&fPath)
	mustQuery(t, tgtDB, "SELECT storage_path FROM artifacts WHERE id='art1'").Scan(&aPath)
	if !strings.HasPrefix(filepath.Clean(fPath), filepath.Clean(tgtUploads)) {
		t.Fatalf("file storage_path not rewritten to target: %q", fPath)
	}
	if !strings.HasPrefix(filepath.Clean(aPath), filepath.Clean(tgtArtifacts)) {
		t.Fatalf("artifact storage_path not rewritten to target: %q", aPath)
	}
	if b, err := os.ReadFile(fPath); err != nil || string(b) != "hello-upload" {
		t.Fatalf("uploaded file not restored at %q: %v", fPath, err)
	}
	if b, err := os.ReadFile(aPath); err != nil || string(b) != "PNGDATA" {
		t.Fatalf("artifact not restored at %q: %v", aPath, err)
	}
}

// TestBackupImportRequiresConfirm rejects an import without the confirm token.
func TestBackupImportRequiresConfirm(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "x.db"))
	defer db.Close()
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "b.zip")
	_, _ = fw.Write([]byte("not-a-zip"))
	_ = mw.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/backup/import", &body)
	req.Header.Set("content-type", mw.FormDataContentType())
	importBackupAdmin(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing-confirm import status = %d, want 400", rec.Code)
	}
}

func TestBackupExportImportRoundTripsQdrant(t *testing.T) {
	qdrant := newFakeQdrant(t)
	qdrant.setPoints("aurelia_c2", []qdrantDumpPoint{{
		ID:      json.RawMessage(`"point-1"`),
		Vector:  json.RawMessage(`[0.25,0.75]`),
		Payload: json.RawMessage(`{"chunk_id":"ch1","document_id":"d1","content":"hello vector"}`),
	}})

	srcRoot := t.TempDir()
	srcDB := openMigrated(t, filepath.Join(srcRoot, "src.db"))
	defer srcDB.Close()
	mustExec(t, srcDB, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, srcDB, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Chat')`)
	mustExec(t, srcDB, `INSERT INTO documents(id,conversation_id,filename,mime_type,status,size_bytes) VALUES('d1','c1','doc.txt','text/plain','ready',12)`)
	mustExec(t, srcDB, `INSERT INTO chunks(id,document_id,conversation_id,seq,content,embedding_model) VALUES('ch1','d1','c1',0,'hello vector','emb')`)
	srcDeps := Deps{
		DB:     srcDB,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir(), QdrantURL: qdrant.url},
		Logger: log.New(io.Discard, "", 0),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/backup/export?files=1", nil)
	exportBackupAdmin(srcDeps, rec, req)
	if rec.Code != 200 {
		t.Fatalf("export status = %d, body=%s", rec.Code, rec.Body.String())
	}
	archive := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	man, err := readBackupManifest(zr)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !man.IncludesQdrant || man.QdrantPoints != 1 {
		t.Fatalf("manifest qdrant = includes:%v points:%d, want true/1", man.IncludesQdrant, man.QdrantPoints)
	}
	if findZipFile(zr, qdrantZipManifest) == nil || findZipFile(zr, "qdrant/collections/aurelia_c2.jsonl") == nil {
		t.Fatalf("archive missing qdrant entries")
	}

	qdrant.clear()
	tgtRoot := t.TempDir()
	tgtDB := openMigrated(t, filepath.Join(tgtRoot, "tgt.db"))
	defer tgtDB.Close()
	tgtDeps := Deps{
		DB:     tgtDB,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir(), QdrantURL: qdrant.url},
		Logger: log.New(io.Discard, "", 0),
	}
	body, contentType := multipartArchive(t, archive)
	irec := httptest.NewRecorder()
	ireq := httptest.NewRequest("POST", "/api/admin/backup/import", body)
	ireq.Header.Set("content-type", contentType)
	importBackupAdmin(tgtDeps, irec, ireq)
	if irec.Code != 200 {
		t.Fatalf("import status = %d, body=%s", irec.Code, irec.Body.String())
	}
	var res struct {
		OK             bool   `json:"ok"`
		QdrantRestored int64  `json:"qdrant_restored"`
		QdrantError    string `json:"qdrant_error"`
	}
	if err := json.Unmarshal(irec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !res.OK || res.QdrantRestored != 1 || res.QdrantError != "" {
		t.Fatalf("unexpected qdrant import response: %+v", res)
	}
	got := qdrant.pointsFor("aurelia_c2")
	if len(got) != 1 {
		t.Fatalf("restored qdrant points = %d, want 1", len(got))
	}
	if string(got[0].Vector) != `[0.25,0.75]` || !strings.Contains(string(got[0].Payload), "hello vector") {
		t.Fatalf("restored point mismatch: %+v", got[0])
	}
}

func TestConfigExportImportMergesAdminConfigOnly(t *testing.T) {
	srcRoot := t.TempDir()
	srcUploads := filepath.Join(srcRoot, "uploads")
	srcDB := openMigrated(t, filepath.Join(t.TempDir(), "src-config.db"))
	defer srcDB.Close()
	iconPath := filepath.Join(srcUploads, "icons", "abcdef123456.png")
	assetPath := filepath.Join(srcUploads, skillAssetsSubdir, "asset1.txt")
	writeFile(t, iconPath, []byte("icon-bytes"))
	writeFile(t, assetPath, []byte("asset-bytes"))
	assetJSON, err := json.Marshal([]skillAssetRow{{
		Filename:    "asset1.txt",
		StoragePath: assetPath,
		MimeType:    "text/plain",
		SizeBytes:   11,
	}})
	if err != nil {
		t.Fatalf("marshal asset json: %v", err)
	}
	mustExec(t, srcDB, `INSERT INTO settings(key,value) VALUES('default_model_id','"m_cfg"')`)
	mustExec(t, srcDB, `INSERT INTO settings(key,value) VALUES('search_api_key','"search-secret"')`)
	mustExec(t, srcDB, `INSERT INTO settings(key,value) VALUES('fallback_model_id','null')`)
	mustExec(t, srcDB, `INSERT INTO user_groups(id,name,description,features,price_usd,price_cny,is_default,sort_order) VALUES('ug_paid','Paid','P','["fast"]',9,69,1,1)`)
	mustExec(t, srcDB, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled,sort_order) VALUES('ch_cfg','Main','openai','chat','https://api.example','sk-live',1,1)`)
	mustExec(t, srcDB, `INSERT INTO skills(id,name,description,instructions,assets,enabled,sort_order) VALUES('sk_cfg','Skill','desc','do it',?,1,1)`, string(assetJSON))
	mustExec(t, srcDB, `INSERT INTO oauth_providers(id,kind,name,client_id,client_secret,enabled,sort_order) VALUES('oa_cfg','github','GitHub','cid','osecret',1,1)`)
	mustExec(t, srcDB, `INSERT INTO model_tags(id,name,sort_order) VALUES('tag_cfg','Fast',1)`)
	mustExec(t, srcDB, `INSERT INTO image_styles(id,name,hidden_prompt,enabled,sort_order) VALUES('sty_cfg','Poster','hidden',1,1)`)
	mustExec(t, srcDB, `INSERT INTO models(id,channel_id,kind,request_id,label,icon,param_controls,tags,enabled,sort_order) VALUES('m_cfg','ch_cfg','chat','gpt-x','Configured','/api/icons/abcdef123456.png','[{"name":"temperature"}]','["tag_cfg"]',1,1)`)
	mustExec(t, srcDB, `INSERT INTO model_group_quotas(model_id,group_id,period_seconds,limit_type,limit_value) VALUES('m_cfg','ug_paid',3600,'count',20)`)
	mustExec(t, srcDB, `INSERT INTO model_skills(model_id,skill_id) VALUES('m_cfg','sk_cfg')`)
	mustExec(t, srcDB, `INSERT INTO redeem_codes(id,code,group_id,duration_days,max_uses,note) VALUES('rc_cfg','PROMO','ug_paid',30,10,'launch')`)
	srcDeps := Deps{DB: srcDB, Config: config.Config{UploadDir: srcUploads, ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/config/export", nil)
	exportConfigAdmin(srcDeps, rec, req)
	if rec.Code != 200 {
		t.Fatalf("config export status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); ct != "application/zip" {
		t.Fatalf("config export content-type = %q", ct)
	}
	archive := rec.Body.Bytes()

	tgtRoot := t.TempDir()
	tgtUploads := filepath.Join(tgtRoot, "uploads")
	tgtDB := openMigrated(t, filepath.Join(t.TempDir(), "tgt-config.db"))
	defer tgtDB.Close()
	mustExec(t, tgtDB, `INSERT INTO users(id,email,password_hash,role) VALUES('u_keep','keep@example.com','h','user')`)
	mustExec(t, tgtDB, `INSERT INTO conversations(id,user_id,title,model_id) VALUES('c_keep','u_keep','Keep','local_model')`)
	mustExec(t, tgtDB, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled,sort_order) VALUES('ch_cfg','Old','openai','chat','https://old','old-key',1,9)`)
	tgtDeps := Deps{DB: tgtDB, Config: config.Config{UploadDir: tgtUploads, ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	body, contentType := multipartArchive(t, archive)
	irec := httptest.NewRecorder()
	ireq := httptest.NewRequest("POST", "/api/admin/config/import", body)
	ireq.Header.Set("content-type", contentType)
	importConfigAdmin(tgtDeps, irec, ireq)
	if irec.Code != 200 {
		t.Fatalf("config import status = %d, body=%s", irec.Code, irec.Body.String())
	}
	var res struct {
		OK             bool             `json:"ok"`
		Tables         map[string]int64 `json:"tables"`
		AssetsRestored int              `json:"assets_restored"`
	}
	if err := json.Unmarshal(irec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode config import: %v (%s)", err, irec.Body.String())
	}
	if !res.OK || res.Tables["channels"] != 1 || res.Tables["models"] != 1 || res.Tables["settings"] != 2 {
		t.Fatalf("unexpected config import result: %+v", res)
	}
	if res.AssetsRestored != 2 {
		t.Fatalf("assets_restored = %d, want 2", res.AssetsRestored)
	}

	var users, convs int
	mustQuery(t, tgtDB, `SELECT COUNT(*) FROM users WHERE id='u_keep'`).Scan(&users)
	mustQuery(t, tgtDB, `SELECT COUNT(*) FROM conversations WHERE id='c_keep'`).Scan(&convs)
	if users != 1 || convs != 1 {
		t.Fatalf("user/conversation data was not preserved: users=%d convs=%d", users, convs)
	}
	var apiKey, chName string
	mustQuery(t, tgtDB, `SELECT api_key, name FROM channels WHERE id='ch_cfg'`).Scan(&apiKey, &chName)
	if apiKey != "sk-live" || chName != "Main" {
		t.Fatalf("channel not upserted: key=%q name=%q", apiKey, chName)
	}
	var label, params string
	mustQuery(t, tgtDB, `SELECT label, param_controls FROM models WHERE id='m_cfg'`).Scan(&label, &params)
	if label != "Configured" || !strings.Contains(params, "temperature") {
		t.Fatalf("model not imported: label=%q params=%q", label, params)
	}
	if b, err := os.ReadFile(filepath.Join(tgtUploads, "icons", "abcdef123456.png")); err != nil || string(b) != "icon-bytes" {
		t.Fatalf("icon not restored: %v bytes=%q", err, string(b))
	}
	var quota float64
	mustQuery(t, tgtDB, `SELECT limit_value FROM model_group_quotas WHERE model_id='m_cfg' AND group_id='ug_paid'`).Scan(&quota)
	if quota != 20 {
		t.Fatalf("quota = %v, want 20", quota)
	}
	var skillCount, redeemCount int
	mustQuery(t, tgtDB, `SELECT COUNT(*) FROM model_skills WHERE model_id='m_cfg' AND skill_id='sk_cfg'`).Scan(&skillCount)
	mustQuery(t, tgtDB, `SELECT COUNT(*) FROM redeem_codes WHERE code='PROMO'`).Scan(&redeemCount)
	if skillCount != 1 || redeemCount != 1 {
		t.Fatalf("joins/redeem not imported: skill=%d redeem=%d", skillCount, redeemCount)
	}
	var importedAssets string
	mustQuery(t, tgtDB, `SELECT assets FROM skills WHERE id='sk_cfg'`).Scan(&importedAssets)
	var rows []skillAssetRow
	if err := json.Unmarshal([]byte(importedAssets), &rows); err != nil {
		t.Fatalf("decode imported skill assets: %v", err)
	}
	if len(rows) != 1 || !strings.HasPrefix(filepath.Clean(rows[0].StoragePath), filepath.Clean(filepath.Join(tgtUploads, skillAssetsSubdir))) {
		t.Fatalf("skill asset path not rewritten to target: %+v", rows)
	}
	if b, err := os.ReadFile(rows[0].StoragePath); err != nil || string(b) != "asset-bytes" {
		t.Fatalf("skill asset not restored: %v bytes=%q", err, string(b))
	}
	var searchKey string
	mustQuery(t, tgtDB, `SELECT value FROM settings WHERE key='search_api_key'`).Scan(&searchKey)
	if searchKey != `"search-secret"` {
		t.Fatalf("secret setting not imported: %q", searchKey)
	}
	var nullRows int
	mustQuery(t, tgtDB, `SELECT COUNT(*) FROM settings WHERE key='fallback_model_id' AND value='null'`).Scan(&nullRows)
	if nullRows != 0 {
		t.Fatalf("null settings should not be exported/imported")
	}
}

func TestAdminSettingsPatchSkipsNullValues(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "settings-null.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('default_model_id','"m_old"')`)
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/admin/settings", strings.NewReader(`{"default_model_id":null,"search_api_key":"sk-new"}`))
	req.Header.Set("content-type", "application/json")
	adminSettingsSet(d, rec, req)
	if rec.Code != 200 {
		t.Fatalf("settings patch status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var defaultModel, searchKey string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='default_model_id'`).Scan(&defaultModel)
	mustQuery(t, db, `SELECT value FROM settings WHERE key='search_api_key'`).Scan(&searchKey)
	if defaultModel != `"m_old"` {
		t.Fatalf("null patch overwrote default_model_id: %q", defaultModel)
	}
	if searchKey != `"sk-new"` {
		t.Fatalf("non-null patch not written: %q", searchKey)
	}
}

func TestEmbeddingModelSettingIsLockedAfterConfigured(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "settings-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"emb1"')`)
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/admin/settings", strings.NewReader(`{"embedding_model_id":"emb2"}`))
	req.Header.Set("content-type", "application/json")
	adminSettingsSet(d, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("settings patch status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='embedding_model_id'`).Scan(&got)
	if got != `"emb1"` {
		t.Fatalf("locked embedding_model_id changed: %q", got)
	}
}

func TestConfigImportCannotChangeLockedEmbeddingModel(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('embedding_model_id','"emb1"')`)
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	mw, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(mw).Encode(configManifest{Format: "aurelia-config", Version: configArchiveVersion, Tables: []string{"settings"}, MergeMode: "upsert"}); err != nil {
		t.Fatal(err)
	}
	sw, err := zw.Create("db/settings.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write([]byte(`{"key":"embedding_model_id","value":"\"emb2\"","updated_at":1}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mwForm := multipart.NewWriter(&body)
	fw, err := mwForm.CreateFormFile("file", "config.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mwForm.Close(); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/config/import", &body)
	req.Header.Set("content-type", mwForm.FormDataContentType())
	importConfigAdmin(d, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("config import status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='embedding_model_id'`).Scan(&got)
	if got != `"emb1"` {
		t.Fatalf("config import changed locked embedding_model_id: %q", got)
	}
}

type fakeQdrant struct {
	url    string
	server *httptest.Server
	mu     sync.Mutex
	points map[string][]qdrantDumpPoint
}

func newFakeQdrant(t *testing.T) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{points: map[string][]qdrantDumpPoint{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	f.server = srv
	f.url = srv.URL
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeQdrant) setPoints(collection string, points []qdrantDumpPoint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]qdrantDumpPoint(nil), points...)
	f.points[collection] = cp
}

func (f *fakeQdrant) pointsFor(collection string) []qdrantDumpPoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]qdrantDumpPoint(nil), f.points[collection]...)
}

func (f *fakeQdrant) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.points = map[string][]qdrantDumpPoint{}
}

func (f *fakeQdrant) serveHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method == http.MethodGet && r.URL.Path == "/collections" {
		f.mu.Lock()
		collections := make([]map[string]string, 0, len(f.points))
		for name := range f.points {
			collections = append(collections, map[string]string{"name": name})
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"collections": collections}})
		return
	}
	if len(parts) < 2 || parts[0] != "collections" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	name := parts[1]
	switch {
	case r.Method == http.MethodPut && len(parts) == 2:
		f.mu.Lock()
		if _, ok := f.points[name]; !ok {
			f.points[name] = nil
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	case r.Method == http.MethodDelete && len(parts) == 2:
		f.mu.Lock()
		delete(f.points, name)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	case r.Method == http.MethodPut && len(parts) == 3 && parts[2] == "index":
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[2] == "points" && parts[3] == "scroll":
		f.mu.Lock()
		points := append([]qdrantDumpPoint(nil), f.points[name]...)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"result": map[string]any{
				"points":           points,
				"next_page_offset": nil,
			},
		})
	case r.Method == http.MethodPut && len(parts) == 3 && parts[2] == "points":
		var body struct {
			Points []qdrantDumpPoint `json:"points"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		f.mu.Lock()
		f.points[name] = append(f.points[name], body.Points...)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// --- helpers ---------------------------------------------------------------

func openMigrated(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustQuery(t *testing.T, db *sql.DB, q string) *sql.Row {
	t.Helper()
	return db.QueryRowContext(context.Background(), q)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func multipartArchive(t *testing.T, archive []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("confirm", "REPLACE")
	fw, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(archive); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close mw: %v", err)
	}
	return &body, mw.FormDataContentType()
}
