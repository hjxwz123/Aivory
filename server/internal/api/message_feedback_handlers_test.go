package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestMessageFeedbackHTTPFlowAndPerUserHydration(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "feedback-http.db"))
	defer db.Close()
	for _, user := range []struct{ id, email, role string }{
		{"admin", "admin@example.test", "admin"},
		{"u1", "one@example.test", "user"},
		{"u2", "two@example.test", "user"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES(?,?,?,'h',?,'active')`, user.id, user.email, user.id, user.role)
	}
	workspace, err := store.CreateWorkspace(t.Context(), db, "u1", "Shared quality")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member')`, workspace.ID, "u2")
	channel, err := store.CreateChannel(t.Context(), db, "HTTP feedback channel", "openai", "chat", "https://example.test", "secret")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(t.Context(), db, store.Model{ChannelID: channel.ID, RequestID: "http-feedback-model", Label: "HTTP Feedback"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conv, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "feedback_http_conv", UserID: "u1", WorkspaceID: workspace.ID, Title: "Shared ratings", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	question, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "feedback_http_question", ConversationID: conv.ID, Role: "user", AuthorID: "u1",
		Blocks: json.RawMessage(`[{"kind":"text","text":"Give me a reliable answer."}]`),
	})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	answer, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "feedback_http_answer", ConversationID: conv.ID, ParentID: question.ID, Role: "assistant",
		Provider: "openai", ModelID: model.ID, ModelLabel: model.Label,
		Blocks: json.RawMessage(`[{"kind":"text","text":"This answer needs review."}]`),
	})
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}
	mustExec(t, db, `INSERT INTO usage_logs(user_id,conversation_id,message_id,model_id,purpose,channel_id,status)
		VALUES(?,?,?,?,? ,?,'ok')`, "u1", conv.ID, answer.ID, model.ID, "chat", channel.ID)

	feedbackCache := cache.NewMemory()
	d := Deps{DB: db, Cache: feedbackCache, Auth: authsvc.New("message-feedback-http-secret-32-bytes", time.Hour, 24*time.Hour, feedbackCache)}
	issue := func(userID string) string {
		t.Helper()
		user, err := store.FindUserByID(t.Context(), db, userID)
		if err != nil {
			t.Fatalf("find %s: %v", userID, err)
		}
		token, _, err := d.Auth.IssueAccess(user.ID, user.Role, user.TokenVer)
		if err != nil {
			t.Fatalf("issue %s token: %v", userID, err)
		}
		feedbackCache.Set("seen:"+user.ID, "1", time.Minute)
		return token
	}
	adminToken, user1Token, user2Token := issue("admin"), issue("u1"), issue("u2")

	mux := newMux()
	mux.handle(http.MethodPost, "/api/conversations/:id/messages/:msgId/feedback", requireAuth(d, feedbackMessageHandler))
	mux.handle(http.MethodGet, "/api/conversations/:id", requireAuth(d, getConversationHandler))
	mux.handle(http.MethodGet, "/api/admin/message-feedback", requireAdmin(d, listMessageFeedbackAdmin))
	feedbackPath := "/api/conversations/" + conv.ID + "/messages/" + answer.ID + "/feedback"
	request := func(method, path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := request(http.MethodPost, feedbackPath, "", map[string]any{"feedback": "dislike"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	invalidBodies := []map[string]any{
		{"feedback": "dislike", "reasons": []string{"unknown"}},
		{"feedback": "dislike", "reasons": []string{"incorrect_fact", "incorrect_fact"}},
		{"feedback": "dislike", "comment": strings.Repeat("错", 501)},
		{"feedback": "like", "reasons": []string{"unknown"}},
		{"feedback": "", "comment": strings.Repeat("错", 501)},
	}
	for _, body := range invalidBodies {
		rec := request(http.MethodPost, feedbackPath, user1Token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %#v status=%d body=%s", body, rec.Code, rec.Body.String())
		}
	}

	rec := request(http.MethodPost, feedbackPath, user1Token, map[string]any{
		"feedback": "dislike",
		"reasons":  []string{"incorrect_fact", "citation_issue"},
		"comment":  "The claim is unsupported.",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("dislike status=%d body=%s", rec.Code, rec.Body.String())
	}
	var postBody struct {
		OK              bool     `json:"ok"`
		Feedback        string   `json:"feedback"`
		FeedbackReasons []string `json:"feedback_reasons"`
		FeedbackComment string   `json:"feedback_comment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &postBody); err != nil {
		t.Fatalf("decode dislike response: %v", err)
	}
	if !postBody.OK || postBody.Feedback != "dislike" || len(postBody.FeedbackReasons) != 2 || postBody.FeedbackComment != "The claim is unsupported." {
		t.Fatalf("dislike response=%+v", postBody)
	}

	rec = request(http.MethodPost, feedbackPath, user2Token, map[string]any{
		"feedback": "like", "reasons": []string{}, "comment": "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("u2 like status=%d body=%s", rec.Code, rec.Body.String())
	}

	loadFeedback := func(token string) (string, []string, string) {
		t.Helper()
		rec := request(http.MethodGet, "/api/conversations/"+conv.ID, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("load conversation status=%d body=%s", rec.Code, rec.Body.String())
		}
		var response struct {
			Messages []struct {
				ID              string   `json:"id"`
				Feedback        string   `json:"feedback"`
				FeedbackReasons []string `json:"feedback_reasons"`
				FeedbackComment string   `json:"feedback_comment"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode conversation: %v", err)
		}
		for _, message := range response.Messages {
			if message.ID == answer.ID {
				return message.Feedback, message.FeedbackReasons, message.FeedbackComment
			}
		}
		t.Fatalf("assistant message missing from response")
		return "", nil, ""
	}
	if feedback, reasons, comment := loadFeedback(user1Token); feedback != "dislike" || len(reasons) != 2 || comment == "" {
		t.Fatalf("u1 hydrated feedback=%q reasons=%v comment=%q", feedback, reasons, comment)
	}
	if feedback, reasons, comment := loadFeedback(user2Token); feedback != "like" || len(reasons) != 0 || comment != "" {
		t.Fatalf("u2 hydrated feedback=%q reasons=%v comment=%q", feedback, reasons, comment)
	}

	if rec := request(http.MethodGet, "/api/admin/message-feedback?rating=dislike", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodGet, "/api/admin/message-feedback?rating=dislike", user1Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("user admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	adminRec := request(http.MethodGet, "/api/admin/message-feedback?rating=dislike&reason=incorrect_fact", adminToken, nil)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin report status=%d body=%s", adminRec.Code, adminRec.Body.String())
	}
	var report store.AdminMessageFeedbackReport
	if err := json.Unmarshal(adminRec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Summary.Total != 2 || report.Summary.Likes != 1 || report.Summary.Dislikes != 1 || report.Total != 1 || len(report.Items) != 1 {
		t.Fatalf("admin report=%+v", report)
	}
	if report.Items[0].ConversationOwnerID != "u1" || report.Items[0].UserID != "u1" {
		t.Fatalf("admin item owner/evaluator=%+v", report.Items[0])
	}
	likeAdminRec := request(http.MethodGet, "/api/admin/message-feedback?rating=like", adminToken, nil)
	if likeAdminRec.Code != http.StatusOK {
		t.Fatalf("admin like report status=%d body=%s", likeAdminRec.Code, likeAdminRec.Body.String())
	}
	if err := json.Unmarshal(likeAdminRec.Body.Bytes(), &report); err != nil || len(report.Items) != 1 || report.Items[0].ConversationOwnerID != "u1" || report.Items[0].UserID != "u2" {
		t.Fatalf("shared item owner/evaluator report=%+v err=%v", report, err)
	}

	clearRec := request(http.MethodPost, feedbackPath, user1Token, map[string]any{
		"feedback": "", "reasons": []string{}, "comment": "",
	})
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearRec.Code, clearRec.Body.String())
	}
	postBody = struct {
		OK              bool     `json:"ok"`
		Feedback        string   `json:"feedback"`
		FeedbackReasons []string `json:"feedback_reasons"`
		FeedbackComment string   `json:"feedback_comment"`
	}{}
	if err := json.Unmarshal(clearRec.Body.Bytes(), &postBody); err != nil || !postBody.OK || postBody.Feedback != "" || len(postBody.FeedbackReasons) != 0 || postBody.FeedbackComment != "" {
		t.Fatalf("clear response=%+v err=%v", postBody, err)
	}
	if _, err := store.GetMessageFeedbackForUser(t.Context(), db, answer.ID, "u1"); err != store.ErrNotFound {
		t.Fatalf("cleared feedback lookup err=%v, want ErrNotFound", err)
	}
	if feedback, _, _ := loadFeedback(user1Token); feedback != "" {
		t.Fatalf("u1 feedback after clear=%q", feedback)
	}
}
