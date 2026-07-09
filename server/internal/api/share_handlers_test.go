package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aurelia/server/internal/store"
)

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
