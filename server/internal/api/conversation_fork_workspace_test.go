package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestForkWorkspaceConversationStaysInWorkspaceAndDefaultsPrivate(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "workspace-fork.db"))
	defer db.Close()
	for _, userID := range []string{"workspace-owner", "source-creator", "forker"} {
		mustExec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, userID, userID+"@example.test")
	}
	workspace, err := store.CreateWorkspace(t.Context(), db, "workspace-owner", "Forks")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, userID := range []string{"source-creator", "forker"} {
		if err := store.JoinWorkspace(t.Context(), db, workspace.ID, userID); err != nil {
			t.Fatalf("join %s: %v", userID, err)
		}
	}
	project, err := store.CreateProject(t.Context(), db, store.Project{
		ID: "workspace-project", UserID: "source-creator", WorkspaceID: workspace.ID, Name: "Shared project",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	source, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "workspace-source", UserID: "source-creator", WorkspaceID: workspace.ID,
		ProjectID: project.ID, Title: "Workspace source", ModelID: "model-1", IsPublic: true,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	legacyCreatorQuestion, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "legacy-source-question", ConversationID: source.ID, Role: "user",
		Blocks: json.RawMessage(`[{"kind":"text","text":"source creator question"}]`),
		// Empty author_id is a legacy source-creator turn.
	})
	if err != nil {
		t.Fatalf("create legacy creator question: %v", err)
	}
	answer, err := store.CreateMessage(t.Context(), db, store.Message{
		ID: "source-answer", ConversationID: source.ID, ParentID: legacyCreatorQuestion.ID, Role: "assistant",
		Blocks: json.RawMessage(`[{"kind":"text","text":"answer"}]`),
	})
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}
	forkerQuestion, err := store.CreateMessageForUser(t.Context(), db, store.Message{
		ID: "forker-question", ConversationID: source.ID, ParentID: answer.ID, Role: "user", AuthorID: "forker",
		Blocks: json.RawMessage(`[{"kind":"text","text":"forker follow-up"}]`),
	}, "forker")
	if err != nil {
		t.Fatalf("create forker question: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+source.ID+"/fork", strings.NewReader(`{"leaf_id":"`+forkerQuestion.ID+`"}`))
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": source.ID})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "forker", Role: "user", Status: "active"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	forkConversationHandler(Deps{DB: db}, rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", rec.Code, rec.Body.String())
	}
	var forked store.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &forked); err != nil {
		t.Fatalf("decode fork: %v", err)
	}
	if forked.UserID != "forker" || forked.WorkspaceID != workspace.ID || forked.ProjectID != project.ID || forked.IsPublic {
		t.Fatalf("fork metadata=%+v, want forker-owned private row in source workspace/project", forked)
	}
	if _, err := store.GetConversation(t.Context(), db, forked.ID, "forker"); err != nil {
		t.Fatalf("fork creator cannot read fork: %v", err)
	}
	for _, userID := range []string{"source-creator", "workspace-owner"} {
		if _, err := store.GetConversation(t.Context(), db, forked.ID, userID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s private fork read error=%v, want ErrNotFound", userID, err)
		}
	}

	messages, err := store.ListAllMessages(t.Context(), db, forked.ID)
	if err != nil || len(messages) != 3 {
		t.Fatalf("fork messages=%+v err=%v", messages, err)
	}
	if messages[0].AuthorID != "source-creator" || messages[2].AuthorID != "forker" {
		t.Fatalf("fork author attribution=%+v", messages)
	}
}
