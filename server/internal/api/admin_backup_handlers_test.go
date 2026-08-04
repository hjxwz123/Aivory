package api

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	"time"

	"aivory/server/internal/config"
	"aivory/server/internal/oauth"
	paymentcore "aivory/server/internal/payment"
	"aivory/server/internal/store"
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
	mustExec(t, srcDB, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled,sort_order) VALUES('ch_legacy','Legacy','openai','responses','https://api.example','sk-test',1,1)`)
	mustExec(t, srcDB, `INSERT INTO models(id,channel_id,kind,request_id,label,official_tools,enabled,sort_order) VALUES('model_legacy','ch_legacy','chat','gpt-test','Legacy tools','["web_search","code_interpreter"]',1,1)`)
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
	mustExec(t, tgtDB, `INSERT INTO users(id,email,password_hash,name,role,status,password_set) VALUES('old','x@y.z','old-hash','Old','user','active',1)`)
	mustExec(t, tgtDB, `INSERT INTO users(id,email,password_hash,name,role,status,password_set) VALUES('adm','admin@example.test','admin-hash','Admin','admin','active',1)`)
	mustExec(t, tgtDB, `INSERT INTO refresh_tokens(jti,user_id,expires_at) VALUES('adm-refresh','adm',9999999999)`)

	tgtDeps := Deps{
		DB:     tgtDB,
		Config: config.Config{UploadDir: tgtUploads, ArtifactDir: tgtArtifacts},
		Logger: log.New(io.Discard, "", 0),
	}

	// Build the multipart import request.
	body, contentType := multipartArchive(t, archive)
	irec := httptest.NewRecorder()
	ireq := httptest.NewRequest("POST", "/api/admin/backup/import", body)
	ireq = ireq.WithContext(context.WithValue(ireq.Context(), userCtxKey{}, &store.User{ID: "adm", Role: "admin", Status: "active"}))
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
	if !res.OK || res.Tables["users"] != 2 || res.Tables["messages"] != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if res.FilesRestored != 2 {
		t.Fatalf("files_restored = %d, want 2", res.FilesRestored)
	}

	// The wiped junk is gone; the imported user is present.
	var userCount int
	mustQuery(t, tgtDB, "SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 2 {
		t.Fatalf("target users = %d, want 2 (junk should be wiped, importing admin retained)", userCount)
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
	assertCanonicalOfficialTools(t, tgtDB, "model_legacy", "web_search", "code_interpreter")
}

func TestBackupRestoreBackfillsLegacyMessageFeedbackOnlyWhenTableMissing(t *testing.T) {
	baseRows := func(legacyRating string) map[string][]map[string]any {
		return map[string][]map[string]any{
			"users": {
				{"id": "u_owner", "email": "owner@example.test", "password_hash": "h", "name": "Owner", "role": "user", "group_id": "ug_free"},
				{"id": "u_eval", "email": "evaluator@example.test", "password_hash": "h", "name": "Evaluator", "role": "user", "group_id": "ug_free"},
			},
			"user_groups": {
				{"id": "ug_free", "name": "Free", "is_default": 1},
			},
			"conversations": {
				{"id": "conv_legacy", "user_id": "u_owner", "title": "Legacy feedback"},
			},
			"messages": {
				{"id": "question_legacy", "conversation_id": "conv_legacy", "role": "user", "blocks": `[{"kind":"text","text":"Question"}]`, "created_at": 100},
				{"id": "answer_legacy", "conversation_id": "conv_legacy", "parent_id": "question_legacy", "role": "assistant", "model_id": "model_snapshot", "model_label": "Snapshot model", "blocks": `[{"kind":"text","text":"Answer"}]`, "feedback": legacyRating, "created_at": 101},
			},
		}
	}
	depsFor := func(t *testing.T) Deps {
		t.Helper()
		root := t.TempDir()
		db := openMigrated(t, filepath.Join(root, "restore.db"))
		t.Cleanup(func() { _ = db.Close() })
		return Deps{
			DB: db,
			Config: config.Config{
				UploadDir:   filepath.Join(root, "uploads"),
				ArtifactDir: filepath.Join(root, "artifacts"),
			},
			Logger: log.New(io.Discard, "", 0),
		}
	}

	t.Run("legacy archive without normalized table", func(t *testing.T) {
		d := depsFor(t)
		zr, man := backupRowsArchiveForTest(t, baseRows("dislike"))
		counts, err := restoreDatabase(t.Context(), d, zr, man, "")
		if err != nil {
			t.Fatalf("restore legacy backup: %v", err)
		}
		if counts["message_feedback"] != 1 {
			t.Fatalf("backfilled feedback count = %d, want 1", counts["message_feedback"])
		}
		feedback, err := store.GetMessageFeedbackForUser(t.Context(), d.DB, "answer_legacy", "u_owner")
		if err != nil {
			t.Fatalf("load backfilled owner feedback: %v", err)
		}
		if feedback.ID != "mfb_legacy_answer_legacy" || feedback.Rating != "dislike" || feedback.UserID != "u_owner" || len(feedback.Reasons) != 0 {
			t.Fatalf("backfilled feedback = %+v", feedback)
		}
		var marker string
		if err := d.DB.QueryRowContext(t.Context(), `SELECT value FROM settings WHERE key='message_feedback_backfill_v1'`).Scan(&marker); err != nil || marker != "1" {
			t.Fatalf("feedback backfill marker = %q, err=%v", marker, err)
		}
		if inserted, err := store.BackfillLegacyMessageFeedback(t.Context(), d.DB); err != nil || inserted != 0 {
			t.Fatalf("idempotent backfill inserted=%d err=%v, want 0", inserted, err)
		}
	})

	t.Run("current archive preserves per-user rows", func(t *testing.T) {
		d := depsFor(t)
		rows := baseRows("like")
		rows["settings"] = []map[string]any{{"key": "message_feedback_backfill_v1", "value": "1"}}
		rows["message_feedback"] = []map[string]any{{
			"id": "mfb_current", "message_id": "answer_legacy", "conversation_id": "conv_legacy", "user_id": "u_eval",
			"model_id": "model_snapshot", "rating": "dislike", "reasons": `["incorrect_fact"]`, "comment": "Wrong answer",
			"created_at": 200, "updated_at": 201,
		}}
		zr, man := backupRowsArchiveForTest(t, rows)
		counts, err := restoreDatabase(t.Context(), d, zr, man, "")
		if err != nil {
			t.Fatalf("restore current backup: %v", err)
		}
		if counts["message_feedback"] != 1 {
			t.Fatalf("restored feedback count = %d, want 1", counts["message_feedback"])
		}
		feedback, err := store.GetMessageFeedbackForUser(t.Context(), d.DB, "answer_legacy", "u_eval")
		if err != nil {
			t.Fatalf("load restored evaluator feedback: %v", err)
		}
		if feedback.ID != "mfb_current" || feedback.Rating != "dislike" || len(feedback.Reasons) != 1 || feedback.Reasons[0] != "incorrect_fact" || feedback.Comment != "Wrong answer" {
			t.Fatalf("restored evaluator feedback = %+v", feedback)
		}
		var total, ownerRows int
		if err := d.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_feedback`).Scan(&total); err != nil {
			t.Fatalf("count restored feedback: %v", err)
		}
		if err := d.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_feedback WHERE user_id='u_owner'`).Scan(&ownerRows); err != nil {
			t.Fatalf("count synthesized owner feedback: %v", err)
		}
		var legacyMirror string
		if err := d.DB.QueryRowContext(t.Context(), `SELECT feedback FROM messages WHERE id='answer_legacy'`).Scan(&legacyMirror); err != nil {
			t.Fatalf("load legacy mirror: %v", err)
		}
		if total != 1 || ownerRows != 0 || legacyMirror != "like" {
			t.Fatalf("current restore total=%d owner_rows=%d legacy_mirror=%q", total, ownerRows, legacyMirror)
		}
	})
}

func TestBackupRestoreUsageStatsCompatibility(t *testing.T) {
	depsFor := func(t *testing.T) Deps {
		t.Helper()
		root := t.TempDir()
		db := openMigrated(t, filepath.Join(root, "restore.db"))
		t.Cleanup(func() { _ = db.Close() })
		return Deps{
			DB: db,
			Config: config.Config{
				UploadDir:   filepath.Join(root, "uploads"),
				ArtifactDir: filepath.Join(root, "artifacts"),
			},
			Logger: log.New(io.Discard, "", 0),
		}
	}
	userRows := []map[string]any{{
		"id": "u_stats", "email": "stats@example.test", "password_hash": "h", "role": "user",
	}}

	t.Run("legacy archive backfills successful logs only", func(t *testing.T) {
		d := depsFor(t)
		rows := map[string][]map[string]any{
			"users": userRows,
			"usage_logs": {
				{
					"id": 4101, "user_id": "u_stats", "conversation_id": "conv_archived", "message_id": "msg_archived",
					"model_id": "model_archived", "purpose": "chat", "input_tokens": 11, "output_tokens": 7,
					"cache_read_tokens": 5, "cache_write_tokens": 3, "images_count": 2, "cost": 1.25,
					"currency": "CNY", "credits": 0.75, "workspace_id": "ws_archived", "channel_id": "ch_archived",
					"fallback": 1, "status": "ok", "ttft_fallback_model": "model_fallback", "created_at": 1710000001,
				},
				{
					"id": 4102, "user_id": "u_stats", "model_id": "model_error", "purpose": "error-call",
					"input_tokens": 99, "status": "error", "error": "upstream failed", "created_at": 1710000002,
				},
			},
		}
		zr, man := backupRowsArchiveForTest(t, rows)
		counts, err := restoreDatabase(t.Context(), d, zr, man, "")
		if err != nil {
			t.Fatalf("restore legacy usage archive: %v", err)
		}
		if counts["usage_logs"] != 2 || counts["usage_stats"] != 1 {
			t.Fatalf("restored counts = logs:%d stats:%d, want 2/1", counts["usage_logs"], counts["usage_stats"])
		}

		var (
			sourceLogID                                              int64
			userID, conversationID, messageID, modelID, purpose      string
			currency, workspaceID, channelID, ttftFallbackModel      string
			inputTokens, outputTokens, cacheRead, cacheWrite, images int64
			cost, credits                                            float64
			fallback, createdAt                                      int64
		)
		err = d.DB.QueryRowContext(t.Context(), `SELECT source_log_id, user_id, conversation_id, message_id,
			model_id, purpose, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			images_count, cost, currency, credits, workspace_id, channel_id, fallback,
			ttft_fallback_model, created_at FROM usage_stats`).Scan(
			&sourceLogID, &userID, &conversationID, &messageID, &modelID, &purpose,
			&inputTokens, &outputTokens, &cacheRead, &cacheWrite, &images, &cost, &currency,
			&credits, &workspaceID, &channelID, &fallback, &ttftFallbackModel, &createdAt,
		)
		if err != nil {
			t.Fatalf("load backfilled usage stats: %v", err)
		}
		if sourceLogID != 4101 || userID != "u_stats" || conversationID != "conv_archived" || messageID != "msg_archived" ||
			modelID != "model_archived" || purpose != "chat" || inputTokens != 11 || outputTokens != 7 ||
			cacheRead != 5 || cacheWrite != 3 || images != 2 || cost != 1.25 || currency != "CNY" || credits != 0.75 ||
			workspaceID != "ws_archived" || channelID != "ch_archived" || fallback != 1 ||
			ttftFallbackModel != "model_fallback" || createdAt != 1710000001 {
			t.Fatalf("backfilled usage stats mismatch: source=%d user=%q conversation=%q message=%q model=%q purpose=%q tokens=%d/%d cache=%d/%d images=%d cost=%v currency=%q credits=%v workspace=%q channel=%q fallback=%d ttft=%q created=%d",
				sourceLogID, userID, conversationID, messageID, modelID, purpose, inputTokens, outputTokens,
				cacheRead, cacheWrite, images, cost, currency, credits, workspaceID, channelID, fallback, ttftFallbackModel, createdAt)
		}
		var errorStats int
		if err := d.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM usage_stats WHERE source_log_id=4102 OR purpose='error-call'`).Scan(&errorStats); err != nil {
			t.Fatalf("count error usage stats: %v", err)
		}
		if errorStats != 0 {
			t.Fatalf("error log produced %d usage stats rows, want 0", errorStats)
		}

		mustExec(t, d.DB, `INSERT INTO usage_logs(id,user_id,model_id,purpose,input_tokens,output_tokens,status,created_at)
			VALUES(4103,'u_stats','model_after_restore','post-restore',13,17,'ok',1710000003)`)
		var mirroredModel string
		if err := d.DB.QueryRowContext(t.Context(), `SELECT model_id FROM usage_stats WHERE source_log_id=4103`).Scan(&mirroredModel); err != nil {
			t.Fatalf("load post-restore mirrored stats: %v", err)
		}
		if mirroredModel != "model_after_restore" {
			t.Fatalf("post-restore mirrored model = %q, want model_after_restore", mirroredModel)
		}
	})

	t.Run("current archive keeps explicit empty stats authoritative", func(t *testing.T) {
		d := depsFor(t)
		rows := map[string][]map[string]any{
			"users": userRows,
			"usage_logs": {
				{
					"id": 5101, "user_id": "u_stats", "model_id": "model_log_only", "purpose": "archived-log",
					"input_tokens": 23, "output_tokens": 29, "status": "ok", "created_at": 1720000001,
				},
			},
			"usage_stats": {},
		}
		zr, man := backupRowsArchiveForTest(t, rows)
		counts, err := restoreDatabase(t.Context(), d, zr, man, "")
		if err != nil {
			t.Fatalf("restore current usage archive: %v", err)
		}
		if counts["usage_logs"] != 1 {
			t.Fatalf("restored usage logs = %d, want 1", counts["usage_logs"])
		}
		if got, present := counts["usage_stats"]; !present || got != 0 {
			t.Fatalf("restored explicit usage stats count = %d (present=%v), want 0/present", got, present)
		}
		var statsCount int
		if err := d.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM usage_stats`).Scan(&statsCount); err != nil {
			t.Fatalf("count current archive usage stats: %v", err)
		}
		if statsCount != 0 {
			t.Fatalf("current archive synthesized %d usage stats rows, want 0", statsCount)
		}

		mustExec(t, d.DB, `INSERT INTO usage_logs(id,user_id,model_id,purpose,input_tokens,output_tokens,status,created_at)
			VALUES(5102,'u_stats','model_after_empty_restore','post-empty-restore',31,37,'ok',1720000002)`)
		var total, archivedRows, newRows int
		if err := d.DB.QueryRowContext(t.Context(), `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN source_log_id=5101 THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN source_log_id=5102 THEN 1 ELSE 0 END),0)
			FROM usage_stats`).Scan(&total, &archivedRows, &newRows); err != nil {
			t.Fatalf("load stats after current restore insert: %v", err)
		}
		if total != 1 || archivedRows != 0 || newRows != 1 {
			t.Fatalf("stats after current restore insert = total:%d archived:%d new:%d, want 1/0/1", total, archivedRows, newRows)
		}
	})
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

func TestArchiveImportsRejectManifestVersionsOutsideSupportedRange(t *testing.T) {
	tests := []struct {
		name       string
		current    int
		archive    func(*testing.T, int) []byte
		invoke     func(Deps, http.ResponseWriter, *http.Request)
		requestURL string
	}{
		{
			name:    "config archive",
			current: configArchiveVersion,
			archive: func(t *testing.T, version int) []byte {
				return manifestOnlyArchiveForTest(t, configManifest{
					Format: "aivory-config", Version: version, MergeMode: "upsert",
				})
			},
			invoke:     importConfigAdmin,
			requestURL: "/api/admin/config/import",
		},
		{
			name:    "full backup",
			current: store.BackupVersion,
			archive: func(t *testing.T, version int) []byte {
				return manifestOnlyArchiveForTest(t, backupManifest{
					Format: "aivory-backup", Version: version, Dialect: "sqlite",
				})
			},
			invoke:     importBackupAdmin,
			requestURL: "/api/admin/backup/import",
		},
	}

	for _, tc := range tests {
		for _, version := range []int{-1, 0, tc.current + 1} {
			t.Run(fmt.Sprintf("%s/version_%d", tc.name, version), func(t *testing.T) {
				body, contentType := multipartArchive(t, tc.archive(t, version))
				req := httptest.NewRequest(http.MethodPost, tc.requestURL, body)
				req.Header.Set("content-type", contentType)
				rec := httptest.NewRecorder()
				tc.invoke(Deps{Config: config.Config{}}, rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("import status = %d, want 400; body=%s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "supported versions: v1 through") {
					t.Fatalf("import error does not report supported range: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestFullBackupImportAcceptsVersion1Archive(t *testing.T) {
	d := newBackupAdminFixture(t, false)
	archive := manifestOnlyArchiveForTest(t, backupManifest{
		Format: "aivory-backup", Version: 1, Dialect: "sqlite", Counts: map[string]int64{},
	})
	body, contentType := multipartArchive(t, archive)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/backup/import", body)
	req.Header.Set("content-type", contentType)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{
		ID: "adm", Role: "admin", Status: "active",
	}))
	rec := httptest.NewRecorder()
	importBackupAdmin(d, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("version-1 full backup import status = %d, body=%s", rec.Code, rec.Body.String())
	}
	assertOneRestoredAdmin(t, d.DB, "admin@example.test", "adm", "current-admin-hash")
}

func TestBackupExportImportRoundTripsQdrant(t *testing.T) {
	qdrant := newFakeQdrant(t)
	qdrant.setPoints("aivory_c2", []qdrantDumpPoint{{
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
	if man.Version != 2 {
		t.Fatalf("backup manifest version=%d, want breaking namespace format v2", man.Version)
	}
	if !man.IncludesQdrant || man.QdrantPoints != 1 {
		t.Fatalf("manifest qdrant = includes:%v points:%d, want true/1", man.IncludesQdrant, man.QdrantPoints)
	}
	if findZipFile(zr, qdrantZipManifest) == nil || findZipFile(zr, "qdrant/collections/aivory_c2.jsonl") == nil {
		t.Fatalf("archive missing qdrant entries")
	}

	qdrant.clear()
	tgtRoot := t.TempDir()
	tgtDB := openMigrated(t, filepath.Join(tgtRoot, "tgt.db"))
	defer tgtDB.Close()
	mustExec(t, tgtDB, `INSERT INTO users(id,email,password_hash,name,role,status,password_set) VALUES('adm','admin@example.test','admin-hash','Admin','admin','active',1)`)
	tgtDeps := Deps{
		DB:     tgtDB,
		Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir(), QdrantURL: qdrant.url},
		Logger: log.New(io.Discard, "", 0),
	}
	body, contentType := multipartArchive(t, archive)
	irec := httptest.NewRecorder()
	ireq := httptest.NewRequest("POST", "/api/admin/backup/import", body)
	ireq.Header.Set("content-type", contentType)
	ireq = ireq.WithContext(context.WithValue(ireq.Context(), userCtxKey{}, &store.User{ID: "adm", Role: "admin", Status: "active"}))
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
	got := qdrant.pointsFor("aivory_c2")
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
	mustExec(t, srcDB, `INSERT INTO user_groups(id,name,description,features,monthly_price_amount_minor,yearly_price_amount_minor,is_default,sort_order,is_purchasable) VALUES('ug_paid','Paid','P','["fast"]',1299,12999,1,1,0)`)
	mustExec(t, srcDB, `INSERT INTO credit_packages(id,name,description,credits,price_amount_minor,enabled,sort_order) VALUES('cp_cfg','Credits','P',10000,899,1,1)`)
	mustExec(t, srcDB, `INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled,sort_order) VALUES('ch_cfg','Main','openai','chat','https://api.example','sk-live',1,1)`)
	mustExec(t, srcDB, `INSERT INTO skills(id,name,description,instructions,assets,enabled,sort_order) VALUES('sk_cfg','Skill','desc','do it',?,1,1)`, string(assetJSON))
	mustExec(t, srcDB, `INSERT INTO oauth_providers(id,kind,name,client_id,client_secret,enabled,sort_order) VALUES('oa_cfg','github','GitHub','cid','osecret',1,1)`)
	mustExec(t, srcDB, `INSERT INTO model_tags(id,name,sort_order) VALUES('tag_cfg','Fast',1)`)
	mustExec(t, srcDB, `INSERT INTO image_styles(id,name,hidden_prompt,enabled,sort_order) VALUES('sty_cfg','Poster','hidden',1,1)`)
	mustExec(t, srcDB, `INSERT INTO models(id,channel_id,kind,request_id,label,icon,param_controls,extra_params,official_tools,tags,enabled,sort_order) VALUES('m_cfg','ch_cfg','chat','gpt-x','Configured','/api/icons/abcdef123456.png','[{"name":"temperature"}]','{"temperature":0.25}','["web_search","code_interpreter"]','["tag_cfg"]',1,1)`)
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
	configZip, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	configMan, err := readConfigManifest(configZip)
	if err != nil {
		t.Fatal(err)
	}
	if configMan.Version != 2 {
		t.Fatalf("config manifest version=%d, want breaking OAuth format v2", configMan.Version)
	}

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
	ireq = authorizeConfigImportRequestForTest(t, tgtDB, ireq)
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
	if !res.OK || res.Tables["channels"] != 1 || res.Tables["models"] != 1 || res.Tables["settings"] != 2 || res.Tables["credit_packages"] != 1 {
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
	var label, params, extraParams string
	mustQuery(t, tgtDB, `SELECT label, param_controls, extra_params FROM models WHERE id='m_cfg'`).Scan(&label, &params, &extraParams)
	if label != "Configured" || !strings.Contains(params, "temperature") || !strings.Contains(extraParams, "temperature") {
		t.Fatalf("model not imported: label=%q params=%q extra_params=%q", label, params, extraParams)
	}
	assertCanonicalOfficialTools(t, tgtDB, "m_cfg", "web_search", "code_interpreter")
	if b, err := os.ReadFile(filepath.Join(tgtUploads, "icons", "abcdef123456.png")); err != nil || string(b) != "icon-bytes" {
		t.Fatalf("icon not restored: %v bytes=%q", err, string(b))
	}
	var quota float64
	mustQuery(t, tgtDB, `SELECT limit_value FROM model_group_quotas WHERE model_id='m_cfg' AND group_id='ug_paid'`).Scan(&quota)
	if quota != 20 {
		t.Fatalf("quota = %v, want 20", quota)
	}
	var monthlyPrice, yearlyPrice int64
	var isPurchasable int
	mustQuery(t, tgtDB, `SELECT monthly_price_amount_minor, yearly_price_amount_minor, is_purchasable FROM user_groups WHERE id='ug_paid'`).Scan(&monthlyPrice, &yearlyPrice, &isPurchasable)
	if monthlyPrice != 1299 || yearlyPrice != 12999 || isPurchasable != 0 {
		t.Fatalf("user-group monthly/yearly prices and purchase availability = %d/%d/%d, want 1299/12999/0", monthlyPrice, yearlyPrice, isPurchasable)
	}
	var packageCredits float64
	var packagePrice int64
	mustQuery(t, tgtDB, `SELECT credits, price_amount_minor FROM credit_packages WHERE id='cp_cfg'`).Scan(&packageCredits, &packagePrice)
	if packageCredits != 10000 || packagePrice != 899 {
		t.Fatalf("credit package = %v/%d, want 10000/899", packageCredits, packagePrice)
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

func TestConfigOAuthProviderImportRotatesNamespaceWithoutReusingLegacyIdentity(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-oauth-namespace.db"))
	defer db.Close()
	user, err := store.CreateUser(t.Context(), db, "owner@example.test", "Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	oldProvider := store.OAuthProvider{
		ID: "oa_config_trust", Kind: "oidc", Name: "Config OIDC", ClientID: "client-id",
		ClientSecret: "client-secret", IssuerURL: "https://issuer-a.example.test",
		JWKSURL: "https://issuer-a.example.test/keys", AuthURL: "https://issuer-a.example.test/authorize",
		TokenURL: "https://issuer-a.example.test/token", Enabled: true,
	}
	if _, err := store.CreateOAuthProvider(t.Context(), db, oldProvider); err != nil {
		t.Fatal(err)
	}
	const rawSubject = "same-raw-subject"
	if err := store.BindOAuthIdentity(t.Context(), db, oldProvider.ID, rawSubject, user.ID, user.Email); err != nil {
		t.Fatal(err)
	}
	oldNamespace := oauth.Resolve(toOAuthConfig(&oldProvider)).SubjectNamespace()

	archiveRow := map[string]any{
		"id": oldProvider.ID, "issuer_url": "https://issuer-b.example.test",
		"jwks_url":          "https://issuer-b.example.test/keys",
		"subject_namespace": "oauth:v1:attacker-controlled:",
	}
	importRow := func() {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(archiveRow); err != nil {
			t.Fatal(err)
		}
		normalized, err := normalizeConfigOAuthProviderRows(t.Context(), tx, &encoded)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := store.UpsertTable(t.Context(), tx, "oauth_providers", normalized); err != nil || n != 1 {
			t.Fatalf("upsert normalized OAuth provider count=%d err=%v", n, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	importRow()
	// Re-importing the same archive must be idempotent and must not prefix the
	// already-migrated old identity a second time.
	importRow()

	stored, err := store.GetOAuthProvider(t.Context(), db, oldProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	newNamespace := oauth.Resolve(toOAuthConfig(stored)).SubjectNamespace()
	if stored.SubjectNamespace != newNamespace || newNamespace == oldNamespace ||
		stored.SubjectNamespace == "oauth:v1:attacker-controlled:" {
		t.Fatalf("imported provider old=%q new=%q stored=%q", oldNamespace, newNamespace, stored.SubjectNamespace)
	}
	if owner, err := store.FindOAuthIdentityUser(t.Context(), db, oldProvider.ID, oldNamespace+rawSubject); err != nil || owner != user.ID {
		t.Fatalf("old trust identity owner=%q err=%v", owner, err)
	}
	if owner, err := store.FindOAuthIdentityUser(t.Context(), db, oldProvider.ID, newNamespace+rawSubject); owner != "" || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("new trust inherited old identity owner=%q err=%v", owner, err)
	}
	if owner, err := store.FindOAuthIdentityUser(t.Context(), db, oldProvider.ID, oldNamespace+oldNamespace+rawSubject); owner != "" || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("config re-import double-prefixed identity owner=%q err=%v", owner, err)
	}
}

func TestConfigOAuthProviderImportUpgradesVersion1UserInfoKind(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-oauth-v1.db"))
	defer db.Close()
	target := store.OAuthProvider{
		ID: "oa_legacy_config", Kind: "oidc", Name: "Target Strict OIDC", ClientID: "target-client",
		ClientSecret: "target-secret", IssuerURL: "https://target.example.test",
		JWKSURL: "https://target.example.test/keys", AuthURL: "https://target.example.test/authorize",
		TokenURL: "https://target.example.test/token", Enabled: true,
	}
	target.SubjectNamespace = oauth.Resolve(toOAuthConfig(&target)).SubjectNamespace()
	if _, err := store.CreateOAuthProvider(t.Context(), db, target); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(map[string]any{
		"id": "oa_legacy_config", "kind": "oidc", "name": "Legacy UserInfo",
		"client_id": "client-id", "client_secret": "legacy-secret",
		"auth_url":  "https://legacy.example.test/authorize",
		"token_url": "https://legacy.example.test/token", "userinfo_url": "https://legacy.example.test/me",
		"enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeConfigOAuthProviderRows(t.Context(), tx, &encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertTable(t.Context(), tx, "oauth_providers", normalized); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetOAuthProvider(t.Context(), db, "oa_legacy_config")
	if err != nil {
		t.Fatal(err)
	}
	wantNamespace := oauth.Resolve(toOAuthConfig(stored)).SubjectNamespace()
	if stored.Kind != "oauth2" || stored.SubjectNamespace != wantNamespace || stored.SubjectNamespace == "" ||
		stored.Scopes != "openid email profile" {
		t.Fatalf("v1 UserInfo provider kind=%q marker=%q scopes=%q want oauth2/%q/default scopes",
			stored.Kind, stored.SubjectNamespace, stored.Scopes, wantNamespace)
	}
}

func TestConfigImportAcceptsVersion1Archive(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-v1-compatible.db"))
	defer db.Close()
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}
	archive := paymentConfigArchiveVersionForTest(t, 1, map[string][]map[string]any{
		"settings": {{"key": "site_title", "value": `"Imported from v1"`}},
	})
	rec := importPaymentConfigArchiveForTest(t, d, archive)
	if rec.Code != http.StatusOK {
		t.Fatalf("version-1 config import status=%d body=%s", rec.Code, rec.Body.String())
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='site_title'`).Scan(&value); err != nil || value != `"Imported from v1"` {
		t.Fatalf("version-1 setting value=%q err=%v", value, err)
	}
}

func TestConfigImportRejectsLegacyJSON(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-json.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('search_api_key','"current-secret"')`)
	d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

	legacy := []byte(`{"format":"aivory-config","settings":{"search_api_key":"legacy-secret"}}`)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "config.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(legacy); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/config/import", &body)
	req.Header.Set("content-type", mw.FormDataContentType())
	importConfigAdmin(d, rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("legacy JSON import status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "expected a ZIP file") {
		t.Fatalf("legacy JSON import error = %s", rec.Body.String())
	}
	var got string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='search_api_key'`).Scan(&got)
	if got != `"current-secret"` {
		t.Fatalf("legacy JSON import changed settings: %q", got)
	}
}

func TestConfigImportRechecksAdminAfterSlowUpload(t *testing.T) {
	tests := []struct {
		name   string
		revoke string
	}{
		{name: "demoted", revoke: `UPDATE users SET role='user' WHERE id=?`},
		{name: "banned", revoke: `UPDATE users SET status='banned' WHERE id=?`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "config-import-recheck.db"))
			defer db.Close()
			mustExec(t, db, `INSERT INTO settings(key,value) VALUES('search_api_key','"original-secret"')`)
			uploadDir := t.TempDir()
			d := Deps{DB: db, Config: config.Config{UploadDir: uploadDir, ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}

			archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
				"settings": {{"key": "search_api_key", "value": `"changed-secret"`, "updated_at": 2}},
			}, configArchiveEntryForTest{
				name: configZipSkillAssets + "revoked-admin.txt",
				data: []byte("must not be restored"),
			})
			multipartBody, contentType := multipartArchive(t, archive)
			started := make(chan struct{})
			release := make(chan struct{})
			slowBody := &gatedConfigUploadBody{
				reader:  bytes.NewReader(append([]byte(nil), multipartBody.Bytes()...)),
				started: started,
				release: release,
			}
			req := httptest.NewRequest(http.MethodPost, "/api/admin/config/import", slowBody)
			req.Header.Set("content-type", contentType)
			req = authorizeConfigImportRequestForTest(t, db, req)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				importConfigAdmin(d, rec, req)
			}()

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("config import did not begin reading the slow upload")
			}
			mustExec(t, db, tc.revoke, configImportAdminForTestID)
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("config import did not finish after releasing the upload")
			}

			if rec.Code != http.StatusForbidden {
				t.Fatalf("config import after admin was %s status=%d body=%s, want 403", tc.name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), errConfigImportAdminUnauthorized.Error()) {
				t.Fatalf("config import after admin was %s error=%s", tc.name, rec.Body.String())
			}
			var got string
			mustQuery(t, db, `SELECT value FROM settings WHERE key='search_api_key'`).Scan(&got)
			if got != `"original-secret"` {
				t.Fatalf("config import after admin was %s changed secret setting to %q", tc.name, got)
			}
			if _, err := os.Stat(filepath.Join(uploadDir, skillAssetsSubdir, "revoked-admin.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("config import after admin was %s restored an asset, stat error=%v", tc.name, err)
			}
		})
	}
}

func TestConfigImportHoldsAdminLockThroughAssetRestore(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "config-import-assets-lock.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status,password_set) VALUES(?, 'asset-admin@example.test', 'hash', 'Asset Admin', 'admin', 'active', 1)`, configImportAdminForTestID)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status,password_set) VALUES('asset-admin-backup', 'asset-admin-backup@example.test', 'hash', 'Backup Admin', 'admin', 'active', 1)`)
	mustExec(t, db, `INSERT INTO settings(key,value) VALUES('search_api_key','"original-secret"')`)
	uploadDir := t.TempDir()
	d := Deps{DB: db, Config: config.Config{UploadDir: uploadDir, ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}
	archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
		"settings": {{"key": "search_api_key", "value": `"changed-secret"`, "updated_at": 2}},
	}, configArchiveEntryForTest{
		name: configZipSkillAssets + "locked-admin.txt",
		data: []byte("restored before revocation returns"),
	})
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	man, err := readConfigManifest(zr)
	if err != nil {
		t.Fatal(err)
	}

	externalStarted := make(chan struct{})
	releaseExternal := make(chan struct{})
	externalFinished := make(chan struct{})
	importDone := make(chan error, 1)
	go func() {
		_, mergeErr := mergeConfigArchive(t.Context(), d, zr, man, configImportAdminForTestID, func() error {
			close(externalStarted)
			<-releaseExternal
			if restored := restoreConfigAssetsFromZip(d, zr); restored != 1 {
				return fmt.Errorf("restored assets=%d, want 1", restored)
			}
			close(externalFinished)
			return nil
		})
		importDone <- mergeErr
	}()

	select {
	case <-externalStarted:
	case <-time.After(5 * time.Second):
		close(releaseExternal)
		t.Fatal("config import did not reach the asset restore stage")
	}
	revokeDone := make(chan error, 1)
	go func() {
		revokeDone <- store.SetUserRole(t.Context(), db, configImportAdminForTestID, "user")
	}()
	select {
	case err := <-revokeDone:
		close(releaseExternal)
		t.Fatalf("admin demotion returned before asset restore finished: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseExternal)
	if err := <-importDone; err != nil {
		t.Fatalf("config import: %v", err)
	}
	if err := <-revokeDone; err != nil {
		t.Fatalf("demote admin: %v", err)
	}
	select {
	case <-externalFinished:
	default:
		t.Fatal("admin demotion returned before asset restoration completed")
	}
	assetPath := filepath.Join(uploadDir, skillAssetsSubdir, "locked-admin.txt")
	if content, err := os.ReadFile(assetPath); err != nil || string(content) != "restored before revocation returns" {
		t.Fatalf("restored asset after demotion: content=%q err=%v", string(content), err)
	}
	var role string
	if err := db.QueryRow(`SELECT role FROM users WHERE id=?`, configImportAdminForTestID).Scan(&role); err != nil || role != "user" {
		t.Fatalf("importing admin role=%q err=%v, want user", role, err)
	}
}

func TestFullBackupRestoreHoldsAdminLockThroughFileRestore(t *testing.T) {
	d := newBackupAdminFixture(t, false)
	zr, man := backupRowsArchiveForTest(t, map[string][]map[string]any{
		"users": {{"id": "restored-user", "email": "restored@example.test", "password_hash": "hash", "role": "user", "status": "active", "password_set": 1}},
	}, configArchiveEntryForTest{
		name: backupZipUploads + "locked-backup.txt",
		data: []byte("restored before ban returns"),
	})

	externalStarted := make(chan struct{})
	releaseExternal := make(chan struct{})
	externalFinished := make(chan struct{})
	restoreDone := make(chan error, 1)
	go func() {
		_, restoreErr := restoreDatabase(t.Context(), d, zr, man, "adm", func() error {
			close(externalStarted)
			<-releaseExternal
			if restored := restoreFilesFromZip(d, zr); restored != 1 {
				return fmt.Errorf("restored files=%d, want 1", restored)
			}
			close(externalFinished)
			return nil
		})
		restoreDone <- restoreErr
	}()

	select {
	case <-externalStarted:
	case <-time.After(5 * time.Second):
		close(releaseExternal)
		t.Fatal("full restore did not reach the file restore stage")
	}
	banDone := make(chan error, 1)
	go func() {
		_, banErr := d.DB.ExecContext(t.Context(), `UPDATE users SET status='banned', token_ver=token_ver+1 WHERE id='adm'`)
		banDone <- banErr
	}()
	select {
	case err := <-banDone:
		close(releaseExternal)
		t.Fatalf("admin ban returned before file restore finished: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseExternal)
	if err := <-restoreDone; err != nil {
		t.Fatalf("full restore: %v", err)
	}
	if err := <-banDone; err != nil {
		t.Fatalf("ban admin: %v", err)
	}
	select {
	case <-externalFinished:
	default:
		t.Fatal("admin ban returned before file restoration completed")
	}
	path := filepath.Join(d.Config.UploadDir, "locked-backup.txt")
	if content, err := os.ReadFile(path); err != nil || string(content) != "restored before ban returns" {
		t.Fatalf("restored backup file after ban: content=%q err=%v", string(content), err)
	}
	var status string
	if err := d.DB.QueryRow(`SELECT status FROM users WHERE id='adm'`).Scan(&status); err != nil || status != "banned" {
		t.Fatalf("restored admin status=%q err=%v, want banned", status, err)
	}
}

func TestConfigImportProtectsIncompletePaymentOrdersAndRollsBack(t *testing.T) {
	for _, status := range []string{store.PaymentOrderPending, store.PaymentOrderProcessing} {
		t.Run(status, func(t *testing.T) {
			fx := newPaymentAdminFixture(t)
			if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
				t.Fatalf("set settlement currency: %v", err)
			}
			channel := createPaymentChannelForAdminTest(t, fx, "Protected import Stripe", paymentcore.ProviderStripe,
				paymentcore.StripeConfig{SecretKey: "sk_test_config_import_original", WebhookSecret: "whsec_config_import_original"}, 0)
			method := createPaymentMethodForAdminTest(t, fx, "Protected import card", "credit-card", channel.ID,
				paymentcore.StripeMethodConfig{}, 0)
			mustExec(t, fx.db, `INSERT INTO payment_orders(
				id, user_email, provider, environment, channel_id, channel_name,
				method_id, method_name, method_type, product_type, product_id,
				product_name, amount_minor, currency, status
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				"po_config_import_"+status, "config-import@example.test", channel.Provider, channel.Environment,
				channel.ID, channel.Name, method.ID, method.Name, channel.Provider,
				store.PaymentProductCreditPackage, "cp_config_import", "Config import package", 1299, "USD", status,
			)
			before, err := store.GetPaymentChannel(context.Background(), fx.db, channel.ID)
			if err != nil {
				t.Fatalf("get payment channel before import: %v", err)
			}

			archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
				"settings": {{"key": "settlement_currency", "value": `"EUR"`, "updated_at": 2}},
				"payment_channels": {{
					"id": channel.ID, "name": "Changed import Stripe", "provider": paymentcore.ProviderStripe,
					"environment": store.PaymentEnvironmentTest,
					"config": paymentConfigArchiveJSONText(t, paymentcore.StripeConfig{
						SecretKey: "sk_test_config_import_changed", WebhookSecret: "whsec_config_import_changed",
					}),
					"enabled": 1, "sort_order": 0,
				}},
			})
			rec := importPaymentConfigArchiveForTest(t, fx.d, archive)
			if rec.Code != http.StatusConflict {
				t.Fatalf("config import with %s order status = %d, want 409; body=%s", status, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), store.ErrPaymentChannelHasPending.Error()) {
				t.Fatalf("config import conflict is not recognizable: %s", rec.Body.String())
			}

			after, err := store.GetPaymentChannel(context.Background(), fx.db, channel.ID)
			if err != nil {
				t.Fatalf("get payment channel after import: %v", err)
			}
			if after.Name != before.Name || string(after.Config) != string(before.Config) {
				t.Fatalf("rejected import changed payment channel: before=%+v after=%+v", before, after)
			}
			assertConfigImportSettlementCurrency(t, fx.db, `"USD"`)
		})
	}
}

func TestConfigImportRejectsProviderChangeWithBoundMethods(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
		t.Fatalf("set settlement currency: %v", err)
	}
	channel := createPaymentChannelForAdminTest(t, fx, "Bound import Stripe", paymentcore.ProviderStripe,
		paymentcore.StripeConfig{SecretKey: "sk_test_bound_import", WebhookSecret: "whsec_bound_import"}, 0)
	createPaymentMethodForAdminTest(t, fx, "Bound import card", "credit-card", channel.ID,
		paymentcore.StripeMethodConfig{}, 0)

	archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
		"settings": {{"key": "settlement_currency", "value": `"EUR"`, "updated_at": 2}},
		"payment_channels": {{
			"id": channel.ID, "name": channel.Name, "provider": paymentcore.ProviderEPay,
			"environment": store.PaymentEnvironmentTest,
			"config": paymentConfigArchiveJSONText(t, paymentcore.EPayConfig{
				GatewayURL: "https://epay.example.test", MerchantID: "bound-import",
				MerchantKey: "bound-import-secret", Currency: "USD",
				ConversionRate: "1.1", ConversionRateBaseCurrency: "EUR",
			}),
			"enabled": 1, "sort_order": 0,
		}},
	})
	rec := importPaymentConfigArchiveForTest(t, fx.d, archive)
	if rec.Code != http.StatusConflict {
		t.Fatalf("provider-changing config import status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), store.ErrPaymentChannelHasMethods.Error()) {
		t.Fatalf("bound-method conflict is not recognizable: %s", rec.Body.String())
	}
	stored, err := store.GetPaymentChannel(context.Background(), fx.db, channel.ID)
	if err != nil {
		t.Fatalf("get payment channel after rejected import: %v", err)
	}
	if stored.Provider != paymentcore.ProviderStripe {
		t.Fatalf("rejected import changed provider to %q", stored.Provider)
	}
	assertConfigImportSettlementCurrency(t, fx.db, `"USD"`)
}

func TestConfigImportRejectsInvalidPaymentRowsAndRollsBack(t *testing.T) {
	t.Run("channel", func(t *testing.T) {
		fx := newPaymentAdminFixture(t)
		if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
			t.Fatalf("set settlement currency: %v", err)
		}
		archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
			"settings": {{"key": "settlement_currency", "value": `"EUR"`, "updated_at": 2}},
			"payment_channels": {{
				"id": "paych_invalid_archive", "name": "Invalid provider", "provider": "unsupported",
				"environment": store.PaymentEnvironmentLive, "config": `{}`, "enabled": 1, "sort_order": 0,
			}},
		})
		rec := importPaymentConfigArchiveForTest(t, fx.d, archive)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid channel import status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), errInvalidPaymentConfigArchive.Error()) {
			t.Fatalf("invalid channel error is not recognizable: %s", rec.Body.String())
		}
		var count int
		mustQuery(t, fx.db, `SELECT COUNT(*) FROM payment_channels WHERE id='paych_invalid_archive'`).Scan(&count)
		if count != 0 {
			t.Fatalf("invalid payment channel was imported")
		}
		assertConfigImportSettlementCurrency(t, fx.db, `"USD"`)
	})

	t.Run("method", func(t *testing.T) {
		fx := newPaymentAdminFixture(t)
		if err := store.SetSetting(fx.db, "settlement_currency", "USD"); err != nil {
			t.Fatalf("set settlement currency: %v", err)
		}
		channel := createPaymentChannelForAdminTest(t, fx, "Invalid method EPay", paymentcore.ProviderEPay,
			paymentcore.EPayConfig{
				GatewayURL: "https://epay.example.test", MerchantID: "invalid-method",
				MerchantKey: "invalid-method-secret", Currency: "USD",
			}, 0)
		archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
			"settings": {{"key": "settlement_currency", "value": `"EUR"`, "updated_at": 2}},
			"payment_methods": {{
				"id": "paym_invalid_archive", "channel_id": channel.ID, "name": "Invalid route",
				"type": paymentcore.ProviderStripe, "icon": "scan-line", "config": `{"type":"not valid"}`,
				"enabled": 1, "sort_order": 0,
			}},
		})
		rec := importPaymentConfigArchiveForTest(t, fx.d, archive)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid method import status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), errInvalidPaymentConfigArchive.Error()) {
			t.Fatalf("invalid method error is not recognizable: %s", rec.Body.String())
		}
		var count int
		mustQuery(t, fx.db, `SELECT COUNT(*) FROM payment_methods WHERE id='paym_invalid_archive'`).Scan(&count)
		if count != 0 {
			t.Fatalf("invalid payment method was imported")
		}
		assertConfigImportSettlementCurrency(t, fx.db, `"USD"`)
	})
}

func TestConfigImportNormalizesPaymentChannelsAndMethods(t *testing.T) {
	fx := newPaymentAdminFixture(t)
	archive := paymentConfigArchiveForTest(t, map[string][]map[string]any{
		"payment_channels": {{
			"id": "paych_normalized_archive", "name": "  Imported EPay  ", "provider": " EPAY ",
			"environment": " LIVE ",
			"config": paymentConfigArchiveJSONText(t, paymentcore.EPayConfig{
				GatewayURL: "  https://epay.example.test/base  ", MerchantID: " imported-merchant ",
				MerchantKey: " imported-secret ", Currency: " usd ",
			}),
			"enabled": true, "sort_order": 7,
		}},
		"payment_methods": {{
			"id": "paym_normalized_archive", "channel_id": " paych_normalized_archive ",
			"name": "  WeChat Pay  ", "type": paymentcore.ProviderStripe, "icon": "  scan-line  ",
			"config":  paymentConfigArchiveJSONText(t, paymentcore.EPayMethodConfig{Type: " wxpay "}),
			"enabled": true, "sort_order": 3,
		}},
	})
	rec := importPaymentConfigArchiveForTest(t, fx.d, archive)
	if rec.Code != http.StatusOK {
		t.Fatalf("normalized payment config import status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	channel, err := store.GetPaymentChannel(context.Background(), fx.db, "paych_normalized_archive")
	if err != nil {
		t.Fatalf("get imported payment channel: %v", err)
	}
	if channel.Name != "Imported EPay" || channel.Provider != paymentcore.ProviderEPay ||
		channel.Environment != store.PaymentEnvironmentLive || !channel.Enabled || channel.SortOrder != 7 {
		t.Fatalf("imported payment channel was not normalized: %+v", channel)
	}
	var channelConfig paymentcore.EPayConfig
	if err := json.Unmarshal(channel.Config, &channelConfig); err != nil {
		t.Fatalf("decode imported EPay config: %v", err)
	}
	if channelConfig.GatewayURL != "https://epay.example.test/base" ||
		channelConfig.MerchantID != "imported-merchant" || channelConfig.MerchantKey != "imported-secret" ||
		channelConfig.Currency != "USD" {
		t.Fatalf("imported EPay config was not normalized: %+v", channelConfig)
	}

	method, err := store.GetPaymentMethod(context.Background(), fx.db, "paym_normalized_archive")
	if err != nil {
		t.Fatalf("get imported payment method: %v", err)
	}
	if method.ChannelID != channel.ID || method.Name != "WeChat Pay" || method.Icon != "scan-line" ||
		method.Type != paymentcore.ProviderEPay || !method.Enabled || method.SortOrder != 3 {
		t.Fatalf("imported payment method was not normalized: %+v", method)
	}
	var methodConfig paymentcore.EPayMethodConfig
	if err := json.Unmarshal(method.ProviderMethodConfig, &methodConfig); err != nil || methodConfig.Type != "wxpay" {
		t.Fatalf("imported EPay method config = %+v, err=%v", methodConfig, err)
	}
}

func TestConfigImportRejectsInvalidBillingRowsAndRollsBack(t *testing.T) {
	tests := []struct {
		name string
		rows map[string][]map[string]any
	}{
		{
			name: "negative credits per usd",
			rows: map[string][]map[string]any{
				"settings": {
					{"key": "quota_exceeded_message", "value": `"changed"`, "updated_at": 2},
					{"key": "credits_per_usd", "value": `-1`, "updated_at": 2},
				},
			},
		},
		{
			name: "negative group allowance",
			rows: map[string][]map[string]any{
				"settings": {{"key": "quota_exceeded_message", "value": `"changed"`, "updated_at": 2}},
				"user_groups": {{
					"id": "ug_bad", "name": "Bad", "credit_allowance": -1, "credit_period_seconds": 3600,
				}},
			},
		},
		{
			name: "zero group period with allowance",
			rows: map[string][]map[string]any{
				"settings": {{"key": "quota_exceeded_message", "value": `"changed"`, "updated_at": 2}},
				"user_groups": {{
					"id": "ug_bad", "name": "Bad", "credit_allowance": 10, "credit_period_seconds": 0,
				}},
			},
		},
		{
			name: "non usd model",
			rows: map[string][]map[string]any{
				"settings": {{"key": "quota_exceeded_message", "value": `"changed"`, "updated_at": 2}},
				"models":   {{"id": "m_bad", "currency": "EUR", "price_input": 1}},
			},
		},
		{
			name: "negative model quota",
			rows: map[string][]map[string]any{
				"settings": {{"key": "quota_exceeded_message", "value": `"changed"`, "updated_at": 2}},
				"model_group_quotas": {{
					"model_id": "m_bad", "group_id": "ug_bad", "period_seconds": 3600,
					"limit_type": "cost", "limit_value": -1,
				}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrated(t, filepath.Join(t.TempDir(), "invalid-billing-config.db"))
			defer db.Close()
			if err := store.SetSetting(db, "quota_exceeded_message", "original"); err != nil {
				t.Fatal(err)
			}
			d := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}, Logger: log.New(io.Discard, "", 0)}
			rec := importPaymentConfigArchiveForTest(t, d, paymentConfigArchiveForTest(t, tc.rows))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid billing import status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), errInvalidBillingConfigArchive.Error()) {
				t.Fatalf("invalid billing error is not recognizable: %s", rec.Body.String())
			}
			var got string
			mustQuery(t, db, `SELECT value FROM settings WHERE key='quota_exceeded_message'`).Scan(&got)
			if got != `"original"` {
				t.Fatalf("rejected config import did not roll back prior rows: %q", got)
			}
		})
	}
}

func TestNormalizeLegacyUserGroupPriceArchiveRows(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"id":"ug_monthly","monthly_price_amount_minor":1499,"yearly_price_amount_minor":14999,"price_amount_minor":999}`,
		`{"id":"ug_single","price_amount_minor":1299,"price_usd":99}`,
		`{"id":"ug_usd","price_usd":9.99,"price_cny":69}`,
		`{"id":"ug_cny","price_usd":0,"price_cny":68.5}`,
	}, "\n"))

	normalized, err := normalizeLegacyUserGroupPriceArchiveRows(input)
	if err != nil {
		t.Fatalf("normalize legacy user-group prices: %v", err)
	}

	dec := json.NewDecoder(normalized)
	want := []struct {
		id      string
		monthly int64
		yearly  int64
	}{
		{id: "ug_monthly", monthly: 1499, yearly: 14999},
		{id: "ug_single", monthly: 1299},
		{id: "ug_usd", monthly: 999},
		{id: "ug_cny", monthly: 6850},
	}
	for _, tc := range want {
		var row struct {
			ID                      string `json:"id"`
			MonthlyPriceAmountMinor int64  `json:"monthly_price_amount_minor"`
			YearlyPriceAmountMinor  int64  `json:"yearly_price_amount_minor"`
		}
		if err := dec.Decode(&row); err != nil {
			t.Fatalf("decode normalized %s row: %v", tc.id, err)
		}
		if row.ID != tc.id || row.MonthlyPriceAmountMinor != tc.monthly || row.YearlyPriceAmountMinor != tc.yearly {
			t.Fatalf("normalized row = %+v, want id=%q monthly/yearly=%d/%d", row, tc.id, tc.monthly, tc.yearly)
		}
	}
}

func TestNormalizeConversationArchiveRemovesRetiredRAGMode(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"id":"legacy","rag_mode":"tool"}`,
		`{"id":"inject","rag_mode":"inject"}`,
		`{"id":"unknown","rag_mode":"future-mode"}`,
	}, "\n"))
	normalized, err := normalizeConversationRAGModeArchiveRows(input)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(normalized)
	for _, want := range []struct{ id, mode string }{
		{id: "legacy", mode: "auto"},
		{id: "inject", mode: "inject"},
		{id: "unknown", mode: "auto"},
	} {
		var row struct {
			ID      string `json:"id"`
			RAGMode string `json:"rag_mode"`
		}
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		if row.ID != want.id || row.RAGMode != want.mode {
			t.Fatalf("normalized conversation=%+v want=%+v", row, want)
		}
	}
}

func TestNormalizeSettingsArchiveRemovesRetiredBuiltinTool(t *testing.T) {
	input := strings.NewReader(`{"key":"disabled_tools","value":"[\"search_knowledge_base\",\"python_execute\"]","updated_at":0}`)
	normalized, err := normalizeSettingsArchiveRows(input)
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(normalized).Decode(&row); err != nil {
		t.Fatal(err)
	}
	if row.Key != "disabled_tools" || row.Value != `["python_execute"]` {
		t.Fatalf("normalized settings row=%+v", row)
	}
}

func TestRestoreLegacyPricingSettingsPersistsCurrencyAndCreatesPackage(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "legacy-pricing-restore.db"))
	defer db.Close()

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, err := zw.Create("db/settings.jsonl")
	if err != nil {
		t.Fatalf("create legacy settings entry: %v", err)
	}
	for _, row := range []string{
		`{"key":"permanent_credit_purchase_credits","value":"10000","updated_at":0}`,
		`{"key":"permanent_credit_purchase_price_amount_minor","value":"899","updated_at":0}`,
	} {
		if _, err := io.WriteString(w, row+"\n"); err != nil {
			t.Fatalf("write legacy settings row: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close legacy archive: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("open legacy archive: %v", err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	deps := Deps{DB: db, Config: config.Config{UploadDir: t.TempDir(), ArtifactDir: t.TempDir()}}
	if _, err := restoreInto(context.Background(), tx, zr, backupManifest{}, deps, nil); err != nil {
		_ = tx.Rollback()
		t.Fatalf("restore legacy pricing settings: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit restore: %v", err)
	}

	var currency string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='settlement_currency'`).Scan(&currency)
	if currency != `"USD"` {
		t.Fatalf("settlement currency setting = %s, want %q", currency, `"USD"`)
	}
	var credits float64
	var price int64
	mustQuery(t, db, `SELECT credits, price_amount_minor FROM credit_packages WHERE id='cp_legacy_default'`).Scan(&credits, &price)
	if credits != 10000 || price != 899 {
		t.Fatalf("legacy credit package = %v/%d, want 10000/899", credits, price)
	}
	var retired int
	mustQuery(t, db, `SELECT COUNT(*) FROM settings WHERE key IN ('permanent_credit_purchase_credits','permanent_credit_purchase_price_amount_minor','group_buy_url','credit_buy_url','credit_packages_from_legacy_settings_v1')`).Scan(&retired)
	if retired != 0 {
		t.Fatalf("legacy pricing settings remaining after restore = %d", retired)
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
	if err := json.NewEncoder(mw).Encode(configManifest{Format: "aivory-config", Version: configArchiveVersion, Tables: []string{"settings"}, MergeMode: "upsert"}); err != nil {
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
	req = authorizeConfigImportRequestForTest(t, db, req)
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

func assertCanonicalOfficialTools(t *testing.T, db *sql.DB, modelID string, wantNames ...string) {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(context.Background(), `SELECT official_tools FROM models WHERE id=?`, modelID).Scan(&raw); err != nil {
		t.Fatalf("query %s official_tools: %v", modelID, err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("decode persisted %s official_tools %q: %v", modelID, raw, err)
	}
	for i, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '{' {
			t.Fatalf("persisted %s official_tools item %d is not canonical: %s", modelID, i, raw)
		}
	}
	definitions, err := store.ParseOfficialTools(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse persisted %s official_tools: %v", modelID, err)
	}
	if len(definitions) != len(wantNames) {
		t.Fatalf("persisted %s official_tools count = %d, want %d: %s", modelID, len(definitions), len(wantNames), raw)
	}
	for i, want := range wantNames {
		if definitions[i].Name != want {
			t.Fatalf("persisted %s official_tools[%d].name = %q, want %q", modelID, i, definitions[i].Name, want)
		}
	}
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

func paymentConfigArchiveJSONText(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payment config archive JSON: %v", err)
	}
	return string(encoded)
}

type configArchiveEntryForTest struct {
	name string
	data []byte
}

func paymentConfigArchiveForTest(t *testing.T, rowsByTable map[string][]map[string]any, extraEntries ...configArchiveEntryForTest) []byte {
	return paymentConfigArchiveVersionForTest(t, configArchiveVersion, rowsByTable, extraEntries...)
}

func paymentConfigArchiveVersionForTest(t *testing.T, version int, rowsByTable map[string][]map[string]any, extraEntries ...configArchiveEntryForTest) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	tables := make([]string, 0, len(rowsByTable))
	counts := make(map[string]int64, len(rowsByTable))
	for _, table := range store.ConfigTableOrder() {
		rows, ok := rowsByTable[table]
		if !ok {
			continue
		}
		tables = append(tables, table)
		counts[table] = int64(len(rows))
		entry, err := zw.Create("db/" + table + ".jsonl")
		if err != nil {
			t.Fatalf("create config archive %s entry: %v", table, err)
		}
		enc := json.NewEncoder(entry)
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				t.Fatalf("encode config archive %s row: %v", table, err)
			}
		}
	}
	for _, extra := range extraEntries {
		entry, err := zw.Create(extra.name)
		if err != nil {
			t.Fatalf("create config archive extra entry %s: %v", extra.name, err)
		}
		if _, err := entry.Write(extra.data); err != nil {
			t.Fatalf("write config archive extra entry %s: %v", extra.name, err)
		}
	}
	manifestEntry, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatalf("create config archive manifest: %v", err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(configManifest{
		Format: "aivory-config", Version: version, Tables: tables, Counts: counts,
		MergeMode: "upsert", SecretsIncluded: true, IncludesAssets: len(extraEntries) != 0,
	}); err != nil {
		t.Fatalf("encode config archive manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close config archive: %v", err)
	}
	return append([]byte(nil), archive.Bytes()...)
}

func importPaymentConfigArchiveForTest(t *testing.T, d Deps, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	if d.Logger == nil {
		d.Logger = log.New(io.Discard, "", 0)
	}
	body, contentType := multipartArchive(t, archive)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/config/import", body)
	req.Header.Set("content-type", contentType)
	req = authorizeConfigImportRequestForTest(t, d.DB, req)
	rec := httptest.NewRecorder()
	importConfigAdmin(d, rec, req)
	return rec
}

const configImportAdminForTestID = "config-import-admin"

func authorizeConfigImportRequestForTest(t *testing.T, db *sql.DB, req *http.Request) *http.Request {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO users(id,email,password_hash,name,role,status,password_set)
		VALUES(?, 'config-import-admin@example.test', 'hash', 'Config Import Admin', 'admin', 'active', 1)
		ON CONFLICT(id) DO NOTHING`, configImportAdminForTestID)
	ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{
		ID: configImportAdminForTestID, Role: "admin", Status: "active",
	})
	return req.WithContext(ctx)
}

type gatedConfigUploadBody struct {
	reader  io.Reader
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *gatedConfigUploadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(p)
}

func (b *gatedConfigUploadBody) Close() error { return nil }

func assertConfigImportSettlementCurrency(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var got string
	mustQuery(t, db, `SELECT value FROM settings WHERE key='settlement_currency'`).Scan(&got)
	if got != want {
		t.Fatalf("settlement currency after config import = %q, want %q", got, want)
	}
}

func backupRowsArchiveForTest(t *testing.T, rowsByTable map[string][]map[string]any, extraEntries ...configArchiveEntryForTest) (*zip.Reader, backupManifest) {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	man := backupManifest{
		Format:  "aivory-backup",
		Version: store.BackupVersion,
		Dialect: "sqlite",
		Counts:  make(map[string]int64, len(rowsByTable)),
	}
	for _, table := range store.BackupTableOrder() {
		rows, present := rowsByTable[table]
		if !present {
			continue
		}
		man.Tables = append(man.Tables, table)
		man.Counts[table] = int64(len(rows))
		entry, err := zw.Create("db/" + table + ".jsonl")
		if err != nil {
			t.Fatalf("create backup archive %s entry: %v", table, err)
		}
		enc := json.NewEncoder(entry)
		for _, row := range rows {
			if err := enc.Encode(row); err != nil {
				t.Fatalf("encode backup archive %s row: %v", table, err)
			}
		}
	}
	for _, extra := range extraEntries {
		entry, err := zw.Create(extra.name)
		if err != nil {
			t.Fatalf("create backup archive extra entry %s: %v", extra.name, err)
		}
		if _, err := entry.Write(extra.data); err != nil {
			t.Fatalf("write backup archive extra entry %s: %v", extra.name, err)
		}
		if strings.HasPrefix(extra.name, backupZipUploads) || strings.HasPrefix(extra.name, backupZipArtifacts) {
			man.IncludesFiles = true
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close backup rows archive: %v", err)
	}
	data := append([]byte(nil), archive.Bytes()...)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open backup rows archive: %v", err)
	}
	return zr, man
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

func manifestOnlyArchiveForTest(t *testing.T, manifest any) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if err := json.NewEncoder(entry).Encode(manifest); err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close manifest archive: %v", err)
	}
	return append([]byte(nil), archive.Bytes()...)
}

// The product was renamed Aurelia -> Auven -> Aivory; archives from those
// builds must keep importing (§ backup compatibility).
func TestAcceptedArchiveFormatLegacyAliases(t *testing.T) {
	for _, tc := range []struct {
		got, want string
		ok        bool
	}{
		{"aivory-backup", "aivory-backup", true},
		{"aurelia-backup", "aivory-backup", true},
		{"auven-backup", "aivory-backup", true},
		{"aurelia-config", "aivory-config", true},
		{"auven-config", "aivory-config", true},
		{"aurelia-config", "aivory-backup", false},
		{"other-backup", "aivory-backup", false},
		{"", "aivory-backup", false},
	} {
		if got := acceptedArchiveFormat(tc.got, tc.want); got != tc.ok {
			t.Errorf("acceptedArchiveFormat(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.ok)
		}
	}
}

func TestFullBackupRestoreReconcilesVerifiedAdminInsideTransaction(t *testing.T) {
	t.Run("zero admins and banned matching account", func(t *testing.T) {
		d := newBackupAdminFixture(t, true)
		rows := map[string][]map[string]any{
			"users": {
				{"id": "imported", "email": "admin@example.test", "password_hash": "attacker-hash", "role": "user", "status": "banned", "password_set": 1},
				{"id": "extra", "email": "extra@example.test", "password_hash": "h", "role": "admin", "status": "active", "password_set": 1},
			},
		}
		zr, man := backupRowsArchiveForTest(t, rows)
		if _, err := restoreDatabase(t.Context(), d, zr, man, "adm"); err != nil {
			t.Fatalf("restore: %v", err)
		}
		assertOneRestoredAdmin(t, d.DB, "admin@example.test", "imported", "current-admin-hash")
		var extraRole string
		if err := d.DB.QueryRow(`SELECT role FROM users WHERE id='extra'`).Scan(&extraRole); err != nil || extraRole != "user" {
			t.Fatalf("extra imported admin role=%q err=%v, want user", extraRole, err)
		}
	})

	t.Run("id collision allocates a new administrator id", func(t *testing.T) {
		d := newBackupAdminFixture(t, false)
		rows := map[string][]map[string]any{
			"users": {{"id": "adm", "email": "different@example.test", "password_hash": "h", "role": "user", "status": "active", "password_set": 1}},
		}
		zr, man := backupRowsArchiveForTest(t, rows)
		if _, err := restoreDatabase(t.Context(), d, zr, man, "adm"); err != nil {
			t.Fatalf("restore: %v", err)
		}
		assertOneRestoredAdmin(t, d.DB, "admin@example.test", "", "current-admin-hash")
		var role string
		if err := d.DB.QueryRow(`SELECT role FROM users WHERE id='adm'`).Scan(&role); err != nil || role != "user" {
			t.Fatalf("id-conflicting imported row role=%q err=%v, want user", role, err)
		}
	})

	t.Run("reconciliation failure rolls back the wipe", func(t *testing.T) {
		d := newBackupAdminFixture(t, false)
		mustExec(t, d.DB, `INSERT INTO settings(key,value) VALUES('restore-sentinel','keep')`)
		rows := map[string][]map[string]any{
			"users": {
				{"id": "u1", "email": "ADMIN@example.test", "password_hash": "h", "role": "user", "status": "active", "password_set": 1},
				{"id": "u2", "email": "admin@example.test", "password_hash": "h", "role": "user", "status": "active", "password_set": 1},
			},
		}
		zr, man := backupRowsArchiveForTest(t, rows)
		if _, err := restoreDatabase(t.Context(), d, zr, man, "adm"); !errors.Is(err, errBackupImportAdminUnauthorized) {
			t.Fatalf("restore error=%v, want administrator reconciliation error", err)
		}
		var marker, email, hash string
		if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key='restore-sentinel'`).Scan(&marker); err != nil || marker != "keep" {
			t.Fatalf("sentinel after failed restore=%q err=%v", marker, err)
		}
		if err := d.DB.QueryRow(`SELECT email,password_hash FROM users WHERE id='adm'`).Scan(&email, &hash); err != nil || email != "admin@example.test" || hash != "current-admin-hash" {
			t.Fatalf("verified admin was not preserved after rollback: email=%q hash=%q err=%v", email, hash, err)
		}
	})
}

func TestFullBackupRestorePreservesOAuthOnlyAdministrator(t *testing.T) {
	d := newBackupAdminFixture(t, false)
	mustExec(t, d.DB, `UPDATE users SET password_set=0, password_hash='throwaway' WHERE id='adm'`)
	provider := store.OAuthProvider{ID: "oa_admin", Kind: "google", Name: "Admin Google", ClientID: "cid", ClientSecret: "secret", Enabled: true}
	provider.SubjectNamespace = oauth.Resolve(toOAuthConfig(&provider)).SubjectNamespace()
	mustExec(t, d.DB, `INSERT INTO oauth_providers(id,kind,name,client_id,client_secret,subject_namespace,enabled) VALUES(?,?,?,?,?,?,1)`,
		provider.ID, provider.Kind, provider.Name, provider.ClientID, provider.ClientSecret, provider.SubjectNamespace)
	mustExec(t, d.DB, `INSERT INTO oauth_identities(provider_id,subject,user_id,email) VALUES(?,?,?,?)`,
		provider.ID, provider.SubjectNamespace+"subject-1", "adm", "admin@example.test")
	rows := map[string][]map[string]any{"users": {{"id": "u1", "email": "user@example.test", "password_hash": "h", "role": "user", "status": "active", "password_set": 1}}}
	zr, man := backupRowsArchiveForTest(t, rows)
	if _, err := restoreDatabase(t.Context(), d, zr, man, "adm"); err != nil {
		t.Fatalf("restore OAuth-only admin: %v", err)
	}
	assertOneRestoredAdmin(t, d.DB, "admin@example.test", "adm", "throwaway")
	var subject, secret, marker string
	if err := d.DB.QueryRow(`SELECT i.subject,p.client_secret,p.subject_namespace FROM oauth_identities i JOIN oauth_providers p ON p.id=i.provider_id WHERE i.user_id='adm'`).Scan(&subject, &secret, &marker); err != nil ||
		subject != provider.SubjectNamespace+"subject-1" || secret != "secret" || marker != provider.SubjectNamespace {
		t.Fatalf("OAuth identity/provider not preserved: subject=%q secret=%q marker=%q err=%v", subject, secret, marker, err)
	}
}

func TestVersion1FullBackupRestoreUpgradesLegacyUserInfoProvider(t *testing.T) {
	d := newBackupAdminFixture(t, false)
	rows := map[string][]map[string]any{
		"users": {{
			"id": "u1", "email": "user@example.test", "password_hash": "h",
			"role": "user", "status": "active", "password_set": 1,
		}},
		"oauth_providers": {{
			"id": "oa_legacy_v1", "kind": "oidc", "name": "Legacy UserInfo",
			"client_id": "cid", "auth_url": "https://legacy.example.test/authorize",
			"token_url": "https://legacy.example.test/token", "userinfo_url": "https://legacy.example.test/me",
			"enabled": 1,
		}},
		"oauth_identities": {{
			"provider_id": "oa_legacy_v1", "subject": "legacy-raw", "user_id": "u1",
			"email": "user@example.test",
		}},
	}
	zr, man := backupRowsArchiveForTest(t, rows)
	man.Version = 1
	if _, err := restoreDatabase(t.Context(), d, zr, man, ""); err != nil {
		t.Fatalf("restore version-1 OAuth provider: %v", err)
	}
	var kind, scopes, marker, subject string
	if err := d.DB.QueryRow(`SELECT kind,scopes,subject_namespace FROM oauth_providers WHERE id='oa_legacy_v1'`).Scan(&kind, &scopes, &marker); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT subject FROM oauth_identities WHERE provider_id='oa_legacy_v1'`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if kind != "oauth2" || scopes != "openid email profile" || marker != "" || subject != "legacy-raw" {
		t.Fatalf("restored v1 provider kind=%q scopes=%q marker=%q subject=%q", kind, scopes, marker, subject)
	}
}

func TestFullBackupRestoreRejectsOutOfTreeStoragePathsAndRollsBack(t *testing.T) {
	cases := []struct {
		name  string
		table string
		rows  map[string][]map[string]any
	}{
		{"files", "files", map[string][]map[string]any{
			"users": {{"id": "u1", "email": "u1@example.test", "password_hash": "h"}},
			"files": {{"id": "f1", "user_id": "u1", "filename": "x.txt", "storage_path": "/proc/self/environ"}},
		}},
		{"documents", "documents", map[string][]map[string]any{
			"users":         {{"id": "u1", "email": "u1@example.test", "password_hash": "h"}},
			"conversations": {{"id": "c1", "user_id": "u1", "title": "c"}},
			"documents":     {{"id": "d1", "conversation_id": "c1", "filename": "x.txt", "mime_type": "text/plain", "size_bytes": 1, "status": "ready", "storage_path": "/proc/self/environ"}},
		}},
		{"artifacts", "artifacts", map[string][]map[string]any{
			"users":         {{"id": "u1", "email": "u1@example.test", "password_hash": "h"}},
			"conversations": {{"id": "c1", "user_id": "u1", "title": "c"}},
			"messages":      {{"id": "m1", "conversation_id": "c1", "role": "assistant"}},
			"artifacts":     {{"id": "a1", "message_id": "m1", "filename": "x.bin", "storage_path": "/proc/self/environ"}},
		}},
		{"skill-assets", "skills", map[string][]map[string]any{
			"skills": {{"id": "sk1", "name": "Skill", "description": "test", "instructions": "test", "assets": `[{"filename":"x.py","storage_path":"/proc/self/environ"}]`}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db := openMigrated(t, filepath.Join(root, "restore.db"))
			defer db.Close()
			mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('old','old@example.test','old-hash','user')`)
			d := Deps{DB: db, Config: config.Config{UploadDir: filepath.Join(root, "uploads"), ArtifactDir: filepath.Join(root, "artifacts")}}
			zr, man := backupRowsArchiveForTest(t, tc.rows)
			man.SourceUploadDir = filepath.Join(root, "backup", "uploads")
			man.SourceArtifactDir = filepath.Join(root, "backup", "artifacts")
			if _, err := restoreDatabase(t.Context(), d, zr, man, ""); !errors.Is(err, errInvalidBackupStoragePath) {
				t.Fatalf("restore error=%v, want invalid backup storage path", err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id='old'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("old database was wiped after rejected %s path (n=%d err=%v)", tc.table, n, err)
			}
		})
	}
}

func TestConfigImportRejectsOutOfTreeSkillAsset(t *testing.T) {
	root := t.TempDir()
	db := openMigrated(t, filepath.Join(root, "config.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO skills(id,name,description,instructions,assets,enabled) VALUES('sk1','Existing','test','test','[]',1)`)
	source := filepath.Join(root, "source", "uploads")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("db/skills.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(entry).Encode(map[string]any{"id": "sk1", "name": "Imported", "description": "test", "instructions": "test", "assets": `[{"filename":"x.py","storage_path":"/proc/self/environ"}]`, "enabled": 1})
	manifest, err := zw.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(manifest).Encode(configManifest{Format: "aivory-config", Version: configArchiveVersion, SourceUploadDir: source, Tables: []string{"skills"}})
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	d := Deps{DB: db, Config: config.Config{UploadDir: filepath.Join(root, "target", "uploads"), ArtifactDir: filepath.Join(root, "artifacts")}, Logger: log.New(io.Discard, "", 0)}
	rec := importPaymentConfigArchiveForTest(t, d, archive.Bytes())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("config import status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	var name, assets string
	if err := db.QueryRow(`SELECT name,assets FROM skills WHERE id='sk1'`).Scan(&name, &assets); err != nil || name != "Existing" || assets != "[]" {
		t.Fatalf("config import changed skill after rejected path: name=%q assets=%q err=%v", name, assets, err)
	}
}

func TestBackupFilesystemCopyRejectsSymlinkEscapes(t *testing.T) {
	t.Run("export skips symlinked files", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "inside.txt"), []byte("inside"))
		outside := filepath.Join(t.TempDir(), "secret.txt")
		writeFile(t, outside, []byte("secret"))
		if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		var archive bytes.Buffer
		zw := zip.NewWriter(&archive)
		if err := addDirToZip(zw, root, backupZipUploads); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if findZipFile(zr, backupZipUploads+"inside.txt") == nil {
			t.Fatal("ordinary in-root file missing from export")
		}
		if findZipFile(zr, backupZipUploads+"leak.txt") != nil {
			t.Fatal("symlink target outside upload root was included in export")
		}
	})

	t.Run("restore refuses symlink destination", func(t *testing.T) {
		uploads := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.txt")
		writeFile(t, outside, []byte("unchanged"))
		if err := os.Symlink(outside, filepath.Join(uploads, "escape.txt")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		var archive bytes.Buffer
		zw := zip.NewWriter(&archive)
		entry, err := zw.Create(backupZipUploads + "escape.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte("overwritten"))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if restored := restoreFilesFromZip(Deps{Config: config.Config{UploadDir: uploads, ArtifactDir: t.TempDir()}}, zr); restored != 0 {
			t.Fatalf("restored=%d, want 0 for symlink destination", restored)
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "unchanged" {
			t.Fatalf("outside symlink target changed: bytes=%q err=%v", got, err)
		}
	})
}

func newBackupAdminFixture(t *testing.T, withRefresh bool) Deps {
	t.Helper()
	root := t.TempDir()
	db := openMigrated(t, filepath.Join(root, "restore.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,role,status,token_ver,password_set,totp_secret,totp_enabled) VALUES('adm','admin@example.test','current-admin-hash','Current Admin','admin','active',7,1,'TOTPSECRET',1)`)
	if withRefresh {
		mustExec(t, db, `INSERT INTO refresh_tokens(jti,user_id,expires_at) VALUES('admin-refresh','adm',9999999999)`)
	}
	return Deps{DB: db, Config: config.Config{UploadDir: filepath.Join(root, "uploads"), ArtifactDir: filepath.Join(root, "artifacts")}, Logger: log.New(io.Discard, "", 0)}
}

func assertOneRestoredAdmin(t *testing.T, db *sql.DB, email, id, hash string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("active admin count=%d err=%v, want 1", count, err)
	}
	var gotID, gotEmail, gotHash string
	if err := db.QueryRow(`SELECT id,email,password_hash FROM users WHERE role='admin' AND status='active'`).Scan(&gotID, &gotEmail, &gotHash); err != nil {
		t.Fatal(err)
	}
	if (id != "" && gotID != id) || gotEmail != email || gotHash != hash {
		t.Fatalf("restored admin=%s/%s/%s, want %s/%s/%s", gotID, gotEmail, gotHash, id, email, hash)
	}
}
