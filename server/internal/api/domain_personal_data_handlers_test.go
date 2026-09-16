package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestLockedDomainPersonalDataExportAndMigrationRoutes(t *testing.T) {
	d := newAuthSecurityDeps(t, "domain-personal-data.db")
	ctx := t.Context()
	owner, err := store.CreateUserWithRole(ctx, d.DB, "owner@outside.example", "Owner", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := store.CreateWorkspace(ctx, d.DB, owner.ID, "Company")
	if err != nil {
		t.Fatal(err)
	}
	rule := store.RegistrationDomain{Domain: "company.example", WorkspaceID: workspace.ID, Enabled: true}
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, true); err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, d.DB, "history@company.example", "History", "hash")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateConversation(ctx, d.DB, store.Conversation{ID: "personal-history", UserID: user.ID, Title: "History"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMessage(ctx, d.DB, store.Message{
		ID: "personal-history-message", ConversationID: conversation.ID, Role: "user",
		Blocks: json.RawMessage(`[{"kind":"text","text":"preserve this"}]`),
	}); err != nil {
		t.Fatal(err)
	}
	inline, err := store.CreateConversation(ctx, d.DB, store.Conversation{
		ID: "personal-history-inline", UserID: user.ID, Title: "Inline history", InlineSourceConv: conversation.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMessage(ctx, d.DB, store.Message{
		ID: "personal-history-inline-message", ConversationID: inline.ID, Role: "user",
		Blocks: json.RawMessage(`[{"kind":"text","text":"preserve inline"}]`),
	}); err != nil {
		t.Fatal(err)
	}
	rule.LockPersonal = true
	if err := store.SaveRegistrationDomain(ctx, d.DB, rule, false); err != nil {
		t.Fatal(err)
	}
	token := issueBoundTestAccessToken(t, d.DB, d.Auth, user)
	router := NewRouter(d)
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	if recorder := request(http.MethodGet, "/api/conversations"); recorder.Code != http.StatusForbidden {
		t.Fatalf("normal personal route status=%d body=%s, want locked", recorder.Code, recorder.Body.String())
	}
	statusRecorder := request(http.MethodGet, "/api/me/domain-data")
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"needs_action":true`) {
		t.Fatalf("domain-data status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
	listRecorder := request(http.MethodGet, "/api/me/domain-data/conversations")
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), conversation.ID) || !strings.Contains(listRecorder.Body.String(), inline.ID) {
		t.Fatalf("domain-data conversations=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	messagesRecorder := request(http.MethodGet, "/api/me/domain-data/conversations/"+conversation.ID+"/messages")
	if messagesRecorder.Code != http.StatusOK || !strings.Contains(messagesRecorder.Body.String(), "preserve this") {
		t.Fatalf("domain-data messages=%d body=%s", messagesRecorder.Code, messagesRecorder.Body.String())
	}
	inlineMessagesRecorder := request(http.MethodGet, "/api/me/domain-data/conversations/"+inline.ID+"/messages")
	if inlineMessagesRecorder.Code != http.StatusOK || !strings.Contains(inlineMessagesRecorder.Body.String(), "preserve inline") {
		t.Fatalf("domain-data inline messages=%d body=%s", inlineMessagesRecorder.Code, inlineMessagesRecorder.Body.String())
	}

	if _, err := d.DB.Exec(`UPDATE workspace_members SET can_private_conversations=0 WHERE workspace_id=? AND user_id=?`, workspace.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if recorder := request(http.MethodPost, "/api/me/domain-data/migrate"); recorder.Code != http.StatusForbidden {
		t.Fatalf("migration without private permission=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := d.DB.Exec(`UPDATE workspace_members SET can_private_conversations=1 WHERE workspace_id=? AND user_id=?`, workspace.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if recorder := request(http.MethodPost, "/api/me/domain-data/migrate"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"migrated_conversations":1`) {
		t.Fatalf("authorized migration=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := request(http.MethodGet, "/api/me/domain-data/conversations"); recorder.Code != http.StatusNotFound {
		t.Fatalf("export remained actionable after migration=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
