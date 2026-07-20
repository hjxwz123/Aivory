package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

type imageCapabilityFixture struct {
	deps      Deps
	user      *store.User
	conv      *store.Conversation
	uploadDir string
}

func seedImageCapabilityFixture(t *testing.T) imageCapabilityFixture {
	t.Helper()
	db := openMigrated(t, filepath.Join(t.TempDir(), "image-capability.db"))
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','vision@example.com','h','admin')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type,enabled) VALUES('ch1','Provider','openai',1)`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,enabled,vision,fast) VALUES
		('m_plain','ch1','chat','plain','Plain',1,0,0),
		('m_vision','ch1','chat','vision','Vision',1,1,0),
		('m_image','ch1','image','image','Image',1,0,0),
		('m_fast','ch1','chat','fast-hidden','Hidden Fast',1,1,1),
		('m_disabled','ch1','chat','disabled','Disabled',0,1,0)`)
	if err := store.SetSetting(db, "default_model_id", "m_image"); err != nil {
		t.Fatalf("set default model: %v", err)
	}
	conv, err := store.CreateConversation(context.Background(), db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Vision gate", ModelID: "m_plain",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	uploadDir := filepath.Join(t.TempDir(), "uploads")
	return imageCapabilityFixture{
		deps: Deps{
			DB: db, Cache: cache.NewMemory(),
			Config: config.Config{UploadDir: uploadDir, MaxUploadBytes: 10 << 20},
		},
		user:      &store.User{ID: "u1", Role: "admin", Status: "active"},
		conv:      conv,
		uploadDir: uploadDir,
	}
}

func TestResolveEffectiveConversationModelPrecedenceAndImageCapability(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	db := fx.deps.DB
	ctx := context.Background()

	model, err := resolveEffectiveConversationModel(ctx, db, fx.conv, "m_vision", false)
	if err != nil || model.ID != "m_vision" {
		t.Fatalf("request model = %v, err=%v; want m_vision", model, err)
	}
	if !modelSupportsImageInput(model) {
		t.Fatal("vision chat model must accept images")
	}

	model, err = resolveEffectiveConversationModel(ctx, db, fx.conv, "", false)
	if err != nil || model.ID != "m_plain" {
		t.Fatalf("conversation model = %v, err=%v; want m_plain", model, err)
	}
	if modelSupportsImageInput(model) {
		t.Fatal("non-vision chat model must reject images")
	}

	withoutModel := *fx.conv
	withoutModel.ModelID = ""
	model, err = resolveEffectiveConversationModel(ctx, db, &withoutModel, "", false)
	if err != nil || model.ID != "m_image" {
		t.Fatalf("default model = %v, err=%v; want m_image", model, err)
	}
	if !modelSupportsImageInput(model) {
		t.Fatal("image model must accept reference images even when vision=false")
	}

	model, err = resolveEffectiveConversationModel(ctx, db, fx.conv, "m_plain", true)
	if err != nil || model.ID != "m_fast" {
		t.Fatalf("fast model = %v, err=%v; want hidden m_fast", model, err)
	}

	if _, err := resolveEffectiveConversationModel(ctx, db, fx.conv, "m_disabled", false); !errors.Is(err, errSelectedModelInvalid) {
		t.Fatalf("disabled model error = %v, want selected-model error", err)
	}
}

func TestEnsureImageAttachmentsSupportedUsesNormalizedServerMetadata(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	db := fx.deps.DB
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	writeFile(t, imagePath, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind)
		VALUES('f_image','u1','c1','photo.png','image/png',10,?,'image')`, imagePath)

	// The browser claims text/plain + other. Normalization must replace every
	// client-controlled field before the capability check sees the attachment.
	forged := []llm.Attachment{{ID: "f_image", Filename: "notes.txt", MimeType: "text/plain", Kind: "other"}}
	normalized, err := normalizeConversationAttachments(context.Background(), db, "c1", "u1", forged)
	if err != nil {
		t.Fatalf("normalize forged image: %v", err)
	}
	if len(normalized) != 1 || normalized[0].Kind != "image" || normalized[0].MimeType != "image/png" || normalized[0].Filename != "photo.png" || normalized[0].URL != "/api/files/f_image" {
		t.Fatalf("normalized attachment = %+v", normalized)
	}
	err = ensureImageAttachmentsSupported(context.Background(), db, fx.conv, "m_plain", false, normalized)
	if !errors.Is(err, errImageInputUnsupported) {
		t.Fatalf("forged kind error = %v, want image unsupported", err)
	}
	if err := ensureImageAttachmentsSupported(context.Background(), db, fx.conv, "m_vision", false, normalized); err != nil {
		t.Fatalf("vision model rejected server image: %v", err)
	}
	if err := ensureImageAttachmentsSupported(context.Background(), db, fx.conv, "m_image", false, normalized); err != nil {
		t.Fatalf("image model rejected reference image: %v", err)
	}
	if err := ensureImageAttachmentsSupported(context.Background(), db, fx.conv, "m_plain", true, normalized); err != nil {
		t.Fatalf("hidden fast vision model rejected image: %v", err)
	}
}

func TestNormalizeConversationAttachmentsRejectsSpoofingAndCrossConversationIDs(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	db := fx.deps.DB
	ctx := context.Background()
	if _, err := store.CreateConversation(ctx, db, store.Conversation{ID: "c2", UserID: "u1", Title: "Other"}); err != nil {
		t.Fatalf("create other conversation: %v", err)
	}

	pdfPath := filepath.Join(t.TempDir(), "report.pdf")
	writeFile(t, pdfPath, []byte("%PDF-1.7\nnot an image"))
	bigPath := filepath.Join(t.TempDir(), "large.bin")
	writeFile(t, bigPath, []byte("arbitrary non-image payload"))
	if err := os.Truncate(bigPath, 64<<20); err != nil {
		t.Fatalf("expand large file: %v", err)
	}
	legacyImagePath := filepath.Join(t.TempDir(), "legacy.bin")
	writeFile(t, legacyImagePath, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))

	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES
		('f_pdf','u1','c1','report.pdf','application/pdf',22,?,'pdf'),
		('f_fake_image','u1','c1','renamed.png','image/png',22,?,'image'),
		('f_big','u1','c1','large.bin','application/octet-stream',67108864,?,'text'),
		('f_legacy_bytes','u1','c1','legacy.bin','application/octet-stream',40,?,'text'),
		('f_legacy_mime','u1','c1','legacy-missing.bin','image/png',40,'/missing/legacy.png','text'),
		('f_other_conv','u1','c2','other.png','image/png',40,?,'image')`,
		pdfPath, pdfPath, bigPath, legacyImagePath, legacyImagePath)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('d_pdf','c1','report.pdf','application/pdf',22,'ready',?)`, pdfPath)

	forged := []llm.Attachment{
		{ID: "f_pdf", DocumentID: "forged-document", Filename: "fake.png", MimeType: "image/png", Kind: "image", URL: "https://attacker.invalid/pdf"},
		{ID: "f_fake_image", Filename: "fake.png", MimeType: "image/png", Kind: "image"},
		{ID: "f_big", Filename: "fake.png", MimeType: "image/png", Kind: "image"},
		{ID: "f_legacy_bytes", Filename: "notes.txt", MimeType: "text/plain", Kind: "text"},
		{ID: "f_legacy_mime", Filename: "notes.txt", MimeType: "text/plain", Kind: "text"},
	}
	normalized, err := normalizeConversationAttachments(ctx, db, "c1", "u1", forged)
	if err != nil {
		t.Fatalf("normalize attachments: %v", err)
	}
	if normalized[0].Kind != "pdf" || normalized[0].MimeType != "application/pdf" || normalized[0].Filename != "report.pdf" || normalized[0].DocumentID != "d_pdf" || normalized[0].URL != "/api/files/f_pdf" {
		t.Fatalf("PDF spoof was not canonicalized: %+v", normalized[0])
	}
	// Even a legacy DB row incorrectly marked image is demoted when its bounded
	// file prefix is definitely PDF.
	if normalized[1].Kind != "pdf" || normalized[1].MimeType != "application/pdf" {
		t.Fatalf("image-labelled PDF remained image: %+v", normalized[1])
	}
	if normalized[2].Kind == "image" || strings.HasPrefix(normalized[2].MimeType, "image/") {
		t.Fatalf("large arbitrary file remained provider image: %+v", normalized[2])
	}
	if normalized[3].Kind != "image" || normalized[3].MimeType != "image/png" {
		t.Fatalf("legacy image bytes were not promoted: %+v", normalized[3])
	}
	if normalized[4].Kind != "image" || normalized[4].MimeType != "image/png" {
		t.Fatalf("legacy image MIME fallback was not promoted: %+v", normalized[4])
	}

	for _, tc := range []struct {
		name string
		atts []llm.Attachment
		err  error
	}{
		{name: "missing id", atts: []llm.Attachment{{Filename: "x"}}, err: errAttachmentIDRequired},
		{name: "unknown id", atts: []llm.Attachment{{ID: "missing"}}, err: errAttachmentUnavailable},
		{name: "cross conversation", atts: []llm.Attachment{{ID: "f_other_conv"}}, err: errAttachmentUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeConversationAttachments(ctx, db, "c1", "u1", tc.atts)
			if !errors.Is(err, tc.err) {
				t.Fatalf("error=%v, want %v", err, tc.err)
			}
		})
	}
}

func imageUploadRequest(t *testing.T, target string, user *store.User) *http.Request {
	return uploadRequestWithFile(t, target, user, "photo.png", []byte("\x89PNG\r\n\x1a\n"))
}

func uploadRequestWithFile(t *testing.T, target string, user *store.User, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, user))
}

func TestUploadDetectsImageBytesBehindTextFilenameBeforeDisk(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

	rejected := httptest.NewRecorder()
	uploadFileHandler(
		fx.deps,
		rejected,
		uploadRequestWithFile(t, "/api/files?conversation_id=c1&model_id=m_plain&fast=0", fx.user, "notes.txt", png),
	)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disguised image status=%d body=%s, want 422", rejected.Code, rejected.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fx.uploadDir, fx.user.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected disguised image created upload directory: %v", err)
	}

	accepted := httptest.NewRecorder()
	uploadFileHandler(
		fx.deps,
		accepted,
		uploadRequestWithFile(t, "/api/files?conversation_id=c1&model_id=m_vision&fast=0", fx.user, "notes.txt", png),
	)
	if accepted.Code != http.StatusCreated {
		t.Fatalf("vision disguised image status=%d body=%s, want 201", accepted.Code, accepted.Body.String())
	}
	var stored store.File
	if err := json.Unmarshal(accepted.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode stored file: %v", err)
	}
	if stored.Kind != "image" || stored.MimeType != "image/png" {
		t.Fatalf("stored classification=%q/%q, want image/image/png", stored.Kind, stored.MimeType)
	}
}

func TestUploadImageRequiresConversationAndCapableEffectiveModelBeforeDisk(t *testing.T) {
	fx := seedImageCapabilityFixture(t)

	for _, tc := range []struct {
		name       string
		target     string
		wantStatus int
		wantError  string
	}{
		{name: "no conversation scope", target: "/api/files", wantStatus: http.StatusBadRequest, wantError: "conversation_id is required"},
		{name: "conversation non-vision model", target: "/api/files?conversation_id=c1", wantStatus: http.StatusUnprocessableEntity, wantError: "does not support image input"},
		{name: "explicit vision model", target: "/api/files?conversation_id=c1&model_id=m_vision&fast=false", wantStatus: http.StatusCreated},
		{name: "explicit image model", target: "/api/files?conversation_id=c1&model_id=m_image&fast=0", wantStatus: http.StatusCreated},
		{name: "hidden fast vision model", target: "/api/files?conversation_id=c1&model_id=m_plain&fast=true", wantStatus: http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			uploadFileHandler(fx.deps, rec, imageUploadRequest(t, tc.target, fx.user))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantError != "" && !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("body=%s, want error containing %q", rec.Body.String(), tc.wantError)
			}
		})
	}

	entries, err := os.ReadDir(filepath.Join(fx.uploadDir, fx.user.ID))
	if err != nil {
		t.Fatalf("read upload dir: %v", err)
	}
	// Only the three accepted requests may have created files. The two rejected
	// requests ran before uploadDestPath/os.Create.
	if len(entries) != 3 {
		t.Fatalf("upload dir contains %d files, want 3 accepted uploads only", len(entries))
	}
}

func TestPostMessageRejectsServerImageForNonVisionBeforeSSE(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	db := fx.deps.DB
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind)
		VALUES('f_image','u1','c1','photo.png','image/png',10,'/tmp/photo.png','image')`)
	body := `{"text":"inspect this","model_id":"m_plain","attachments":[{"id":"f_image","filename":"notes.txt","mime_type":"text/plain","kind":"other"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/c1/messages", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), userCtxKey{}, fx.user)
	ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": "c1"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	postMessageHandler(Deps{DB: db}, rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content type=%q, SSE opened before rejection", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), errImageInputUnsupported.Error()) {
		t.Fatalf("body=%s, want clear image capability error", rec.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id='c1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected turn persisted %d messages, err=%v", count, err)
	}
}

func TestListModelsExposesFastVisionWithoutHiddenIdentity(t *testing.T) {
	fx := seedImageCapabilityFixture(t)
	db := fx.deps.DB

	request := func() map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, fx.user))
		listModelsHandler(Deps{DB: db}, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "m_fast") || strings.Contains(rec.Body.String(), "Hidden Fast") {
			t.Fatalf("response leaked hidden fast model identity: %s", rec.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return payload
	}

	payload := request()
	if payload["fast_available"] != true || payload["fast_vision"] != true {
		t.Fatalf("fast flags=%v/%v, want true/true", payload["fast_available"], payload["fast_vision"])
	}
	mustExec(t, db, `UPDATE models SET vision=0 WHERE id='m_fast'`)
	payload = request()
	if payload["fast_available"] != true || payload["fast_vision"] != false {
		t.Fatalf("fast flags=%v/%v, want true/false", payload["fast_available"], payload["fast_vision"])
	}
}
