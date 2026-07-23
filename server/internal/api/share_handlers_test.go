package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestCreateShareFreezesPublicDisplayIdentities(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "share-identities.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,settings) VALUES('owner','owner@example.test','h','Owner Name','{"avatar_url":"/api/icons/owner.png"}')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,name,settings) VALUES('contributor','contributor@example.test','h','Contributor Name','{"avatar_url":"https://cdn.example.test/contributor.png"}')`)

	channel, err := store.CreateChannel(t.Context(), db, "Share", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(t.Context(), db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "share-model",
		Label:     "Share Model",
		Icon:      "/api/icons/share-model.png",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conv, err := store.CreateConversation(t.Context(), db, store.Conversation{
		ID: "source-identities", UserID: "owner", Title: "Identity snapshot", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	ownerMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		Role:           "user",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"owner question"}]`),
		// Empty AuthorID exercises the legacy creator fallback.
	})
	if err != nil {
		t.Fatalf("create owner message: %v", err)
	}
	assistantMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       ownerMessage.ID,
		Role:           "assistant",
		ModelID:        model.ID,
		Blocks:         json.RawMessage(`[{"kind":"text","text":"model answer"}]`),
	})
	if err != nil {
		t.Fatalf("create assistant message: %v", err)
	}
	contributorMessage, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       assistantMessage.ID,
		Role:           "user",
		AuthorID:       "contributor",
		Blocks:         json.RawMessage(`[{"kind":"text","text":"contributor question"}]`),
	})
	if err != nil {
		t.Fatalf("create contributor message: %v", err)
	}
	if _, err := store.CreateMessage(t.Context(), db, store.Message{
		ConversationID: conv.ID,
		ParentID:       contributorMessage.ID,
		Role:           "assistant",
		ModelID:        model.ID,
		Fast:           true,
		Blocks:         json.RawMessage(`[{"kind":"text","text":"fast answer"}]`),
	}); err != nil {
		t.Fatalf("create fast assistant message: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/conversations/"+conv.ID+"/share", nil)
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": conv.ID})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "owner", Role: "user", Status: "active"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	createShareHandler(Deps{DB: db}, rec, req)
	if rec.Code != 201 {
		t.Fatalf("create share status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created shareInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode share info: %v", err)
	}

	// A public share is a frozen snapshot: later profile/model edits must not
	// silently rewrite the identity shown to people who already have the link.
	mustExec(t, db, `UPDATE users SET name='Renamed Owner', settings='{}' WHERE id='owner'`)
	mustExec(t, db, `UPDATE models SET label='Renamed Model', icon='' WHERE id=?`, model.ID)

	publicReq := httptest.NewRequest("GET", "/api/public/shared/"+created.ID, nil)
	publicReq = publicReq.WithContext(context.WithValue(publicReq.Context(), pathCtxKey{}, map[string]string{"token": created.ID}))
	publicRec := httptest.NewRecorder()
	publicSharedHandler(Deps{DB: db}, publicRec, publicReq)
	if publicRec.Code != 200 {
		t.Fatalf("public share status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}
	var payload struct {
		Title    string               `json:"title"`
		Messages []publicShareMessage `json:"messages"`
	}
	if err := json.Unmarshal(publicRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode public share: %v", err)
	}
	if payload.Title != "Identity snapshot" || len(payload.Messages) != 4 {
		t.Fatalf("public payload = %+v", payload)
	}
	if got := payload.Messages[0]; got.AuthorName != "Owner Name" || got.AuthorAvatar != "/api/icons/owner.png" {
		t.Fatalf("legacy owner identity = %+v", got)
	}
	if got := payload.Messages[1]; got.ModelLabel != "Share Model" || got.ModelIcon != "/api/icons/share-model.png" {
		t.Fatalf("assistant model identity = %+v", got)
	}
	if got := payload.Messages[2]; got.AuthorName != "Contributor Name" || got.AuthorAvatar != "https://cdn.example.test/contributor.png" {
		t.Fatalf("contributor identity = %+v", got)
	}
	if got := payload.Messages[3]; !got.Fast || got.ModelLabel != "" || got.ModelIcon != "" {
		t.Fatalf("fast identity was not masked: %+v", got)
	}
	body := publicRec.Body.String()
	for _, privateField := range []string{"owner@example.test", "contributor@example.test", `"author_id"`, `"model_id"`, `"provider"`, `"cost"`} {
		if strings.Contains(body, privateField) {
			t.Fatalf("public share leaked %q: %s", privateField, body)
		}
	}
}

func TestCloneSharedConversationCopiesSnapshotForCurrentUser(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "share-clone.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('viewer','viewer@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('source','owner','Source chat')`)
	snapshot := `[
		{
			"role":"user",
			"blocks":[{"kind":"text","text":"please inspect this image"}],
			"attachments":[{"id":"f_img","filename":"scan.png","kind":"image","url":"/api/files/f_img"}],
			"citations":[],
			"created_at":100
		},
		{
			"role":"assistant",
			"blocks":[
				{"kind":"text","text":"looks good"},
				{"kind":"artifact","title":"result.png","url":"/api/artifacts/art_img","summary":"image/png"}
			],
			"attachments":[],
			"citations":[],
			"created_at":101
		}
	]`
	mustExec(t, db, `INSERT INTO conversation_shares(id,conversation_id,user_id,title,snapshot) VALUES('sh_clone','source','owner','Shared title',?)`, snapshot)

	req := httptest.NewRequest("POST", "/api/shared/sh_clone/clone", nil)
	ctx := context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"token": "sh_clone"})
	ctx = context.WithValue(ctx, userCtxKey{}, &store.User{ID: "viewer", Role: "user", Status: "active"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	cloneSharedConversationHandler(Deps{DB: db}, rec, req)
	if rec.Code != 201 {
		t.Fatalf("clone status=%d body=%s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode cloned conversation: %v", err)
	}
	if conv.UserID != "viewer" || conv.Title != "Shared title" {
		t.Fatalf("cloned conversation = %+v, want viewer-owned Shared title", conv)
	}
	msgs, err := store.ListMessages(context.Background(), db, conv.ID, "")
	if err != nil {
		t.Fatalf("list cloned messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("cloned messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].AuthorID != "viewer" || msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("unexpected cloned message chain: %+v", msgs)
	}
	if got := string(msgs[0].Attachments); !strings.Contains(got, "/api/public/shared/sh_clone/files/f_img") {
		t.Fatalf("attachment URL was not share-scoped: %s", got)
	}
	if got := string(msgs[1].Blocks); !strings.Contains(got, "/api/public/shared/sh_clone/artifacts/art_img") {
		t.Fatalf("artifact URL was not share-scoped: %s", got)
	}
}
