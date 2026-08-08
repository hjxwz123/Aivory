package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

const testFeedbackPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func feedbackRequestUser(req *http.Request, id string, role string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: id, Role: role, Status: "active"}))
}

func multipartFeedbackRequest(t *testing.T, messageID, description string, screenshot []byte, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message_id", messageID); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("description", description); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("page_path", "/chat/test?from=feedback"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("viewport_width", "1280"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("viewport_height", "720"); err != nil {
		t.Fatal(err)
	}
	if screenshot != nil {
		part, err := writer.CreateFormFile("screenshot", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(screenshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/user-feedback", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestUserFeedbackSubmissionValidationAndAdminScreenshot(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-feedback.db"))
	defer db.Close()
	for _, user := range []struct {
		id, email, role string
	}{{"admin", "admin@example.test", "admin"}, {"u1", "one@example.test", "user"}, {"u2", "two@example.test", "user"}} {
		mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES(?,?,?,'h',?,'active')`, user.id, user.email, user.id, user.role)
	}
	conv, err := store.CreateConversation(t.Context(), db, store.Conversation{ID: "feedback_conv", UserID: "u1", Title: "A reportable thread"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	message, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "feedback_message", ConversationID: conv.ID, Role: "assistant",
		Blocks: json.RawMessage(`[{"kind":"text","text":"A response"}]`),
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	d := Deps{DB: db, Cache: cache.NewMemory()}
	call := func(req *http.Request, handler handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(d, rec, req)
		return rec
	}

	missingDescription := feedbackRequestUser(multipartFeedbackRequest(t, message.ID, "", nil, ""), "u1", "user")
	if rec := call(missingDescription, createUserFeedbackHandler); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing description status=%d body=%s", rec.Code, rec.Body.String())
	}

	foreign := feedbackRequestUser(multipartFeedbackRequest(t, message.ID, "not my thread", nil, ""), "u2", "user")
	if rec := call(foreign, createUserFeedbackHandler); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign message status=%d body=%s", rec.Code, rec.Body.String())
	}

	noScreenshot := feedbackRequestUser(multipartFeedbackRequest(t, message.ID, "The layout shifted after sending.", nil, ""), "u1", "user")
	createdRec := call(noScreenshot, createUserFeedbackHandler)
	if createdRec.Code != http.StatusCreated {
		t.Fatalf("no screenshot status=%d body=%s", createdRec.Code, createdRec.Body.String())
	}

	png, err := base64.StdEncoding.DecodeString(testFeedbackPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	withScreenshot := feedbackRequestUser(multipartFeedbackRequest(t, message.ID, "The answer is clipped on mobile.", png, "capture.jpg"), "u1", "user")
	withScreenshot.Header.Set("User-Agent", "feedback-test")
	withScreenshotRec := call(withScreenshot, createUserFeedbackHandler)
	if withScreenshotRec.Code != http.StatusCreated {
		t.Fatalf("screenshot status=%d body=%s", withScreenshotRec.Code, withScreenshotRec.Body.String())
	}

	badImage := feedbackRequestUser(multipartFeedbackRequest(t, message.ID, "This is not an image.", []byte("plain text"), "capture.png"), "u1", "user")
	if rec := call(badImage, createUserFeedbackHandler); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("bad image status=%d body=%s", rec.Code, rec.Body.String())
	}

	mux := newMux()
	mux.handle(http.MethodGet, "/api/admin/user-feedback", func(w http.ResponseWriter, r *http.Request) {
		listUserFeedbackAdmin(d, w, r)
	})
	mux.handle(http.MethodGet, "/api/admin/user-feedback/:id/screenshot", func(w http.ResponseWriter, r *http.Request) {
		userFeedbackScreenshotAdmin(d, w, r)
	})
	adminReq := feedbackRequestUser(httptest.NewRequest(http.MethodGet, "/api/admin/user-feedback?q=mobile", nil), "admin", "admin")
	adminRec := httptest.NewRecorder()
	mux.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", adminRec.Code, adminRec.Body.String())
	}
	var page store.AdminUserFeedbackPage
	if err := json.Unmarshal(adminRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode admin list: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || !page.Items[0].HasScreenshot || page.Items[0].UserEmail != "one@example.test" {
		t.Fatalf("admin page=%+v", page)
	}

	screenshotReq := feedbackRequestUser(httptest.NewRequest(http.MethodGet, "/api/admin/user-feedback/"+page.Items[0].ID+"/screenshot", nil), "admin", "admin")
	screenshotRec := httptest.NewRecorder()
	mux.ServeHTTP(screenshotRec, screenshotReq)
	if screenshotRec.Code != http.StatusOK || screenshotRec.Header().Get("Content-Type") != "image/png" || screenshotRec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("screenshot response status=%d headers=%v", screenshotRec.Code, screenshotRec.Header())
	}
	if !bytes.Equal(screenshotRec.Body.Bytes(), png) {
		t.Fatalf("screenshot bytes changed: got %d want %d", screenshotRec.Body.Len(), len(png))
	}

	if got, _, err := store.GetUserFeedbackScreenshot(t.Context(), db, page.Items[0].ID); err != nil || len(got) != len(png) {
		t.Fatalf("stored screenshot err=%v bytes=%d", err, len(got))
	}
	if strings.TrimSpace(page.Items[0].Description) == "" {
		t.Fatal("admin description is empty")
	}
}
