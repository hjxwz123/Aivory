package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestReplaceAssistantReplyTextPreservesNonAnswerBlocks(t *testing.T) {
	raw := json.RawMessage(`[
		{"kind":"thinking","text":"private reasoning"},
		{"kind":"text","text":"I will check."},
		{"kind":"tool_call","tool_name":"aivory_web_search","tool_id":"t1","summary":"result"},
		{"kind":"artifact","file_ref":"a1","title":"report.pdf"},
		{"kind":"text","text":"old "},
		{"kind":"text","text":"answer"}
	]`)

	got, err := replaceAssistantReplyText(raw, "  # Revised\n\nExact Markdown  \n")
	if err != nil {
		t.Fatalf("replace assistant reply: %v", err)
	}
	var blocks []llm.UnifiedBlock
	if err := json.Unmarshal(got, &blocks); err != nil {
		t.Fatalf("decode replaced blocks: %v", err)
	}
	if len(blocks) != 5 {
		t.Fatalf("blocks = %+v, want preserved reasoning/narration/tool/artifact plus one answer", blocks)
	}
	if blocks[0].Kind != "thinking" || blocks[1].Text != "I will check." || blocks[2].Kind != "tool_call" || blocks[3].Kind != "artifact" {
		t.Fatalf("non-answer blocks changed: %+v", blocks)
	}
	if gotText := blocks[4].Text; gotText != "  # Revised\n\nExact Markdown  \n" {
		t.Fatalf("replacement text = %q", gotText)
	}
}

func TestEditMessageHandlerAllowsWorkspaceAssistantButProtectsUserAuthorship(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "edit-assistant.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner-edit@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('member','member-edit@example.test','h','user')`)

	workspace, err := store.CreateWorkspace(t.Context(), db, "owner", "Editing")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?, 'member', 'member')`, workspace.ID)
	conversation, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "edit-conversation", UserID: "owner", Title: "Edit", WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	question, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "edit-question", ConversationID: conversation.ID, Role: "user", AuthorID: "owner",
		Blocks: json.RawMessage(`[{"kind":"text","text":"owner question"}]`),
	})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	answer, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "edit-answer", ConversationID: conversation.ID, ParentID: question.ID, Role: "assistant",
		Blocks: json.RawMessage(`[{"kind":"tool_call","tool_name":"aivory_web_search","tool_id":"t1","summary":"kept"},{"kind":"text","text":"old answer"}]`),
		Raw:    json.RawMessage(`[{"role":"assistant","content":"old answer"}]`),
	})
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}

	request := func(userID, messageID, text string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		req := httptest.NewRequest(http.MethodPatch, "/api/conversations/"+conversation.ID+"/messages/"+messageID, bytes.NewReader(body))
		ctx := context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: userID, Role: "user", Status: "active"})
		ctx = context.WithValue(ctx, pathCtxKey{}, map[string]string{"id": conversation.ID, "msgId": messageID})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		editMessageHandler(Deps{DB: db}, rec, req)
		return rec
	}

	revised := "# Revised\n\n- raw Markdown"
	if rec := request("member", answer.ID, revised); rec.Code != http.StatusOK {
		t.Fatalf("member edit assistant status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetMessage(t.Context(), db, answer.ID)
	if err != nil {
		t.Fatalf("get edited answer: %v", err)
	}
	if len(updated.Raw) != 0 {
		t.Fatalf("edited answer retained stale native raw: %s", updated.Raw)
	}
	var blocks []llm.UnifiedBlock
	if err := json.Unmarshal(updated.Blocks, &blocks); err != nil {
		t.Fatalf("decode edited answer: %v", err)
	}
	if len(blocks) != 2 || blocks[0].Kind != "tool_call" || blocks[0].Summary != "kept" || blocks[1].Text != revised {
		t.Fatalf("edited assistant blocks = %+v", blocks)
	}

	if rec := request("member", question.ID, "stolen edit"); rec.Code != http.StatusNotFound {
		t.Fatalf("member editing owner's question status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request("owner", question.ID, "owner edit"); rec.Code != http.StatusOK {
		t.Fatalf("owner editing own question status=%d body=%s", rec.Code, rec.Body.String())
	}
}
