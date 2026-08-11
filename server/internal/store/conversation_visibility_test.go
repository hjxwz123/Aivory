package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorkspaceConversationVisibilityIsPrivateByDefaultAndCreatorControlled(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "conversation-visibility.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, userID := range []string{"workspace-owner", "creator", "member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, userID, userID+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "workspace-owner", "Visibility")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, userID := range []string{"creator", "member"} {
		if err := JoinWorkspace(ctx, db, workspace.ID, userID); err != nil {
			t.Fatalf("join %s: %v", userID, err)
		}
	}

	conversation, err := CreateConversation(ctx, db, Conversation{
		ID: "private-by-default", UserID: "creator", WorkspaceID: workspace.ID, Title: "Visibility secret",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if conversation.IsPublic {
		t.Fatal("new workspace conversation is public; want creator-private default")
	}
	if _, err := GetConversation(ctx, db, conversation.ID, "creator"); err != nil {
		t.Fatalf("creator cannot read private conversation: %v", err)
	}
	for _, userID := range []string{"workspace-owner", "member"} {
		if _, err := GetConversation(ctx, db, conversation.ID, userID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s private read error=%v, want ErrNotFound", userID, err)
		}
		rows, listErr := ListWorkspaceConversationsForUser(ctx, db, workspace.ID, "", "active", userID, 20, 0)
		if listErr != nil || len(rows) != 0 {
			t.Fatalf("%s private list=%v err=%v, want empty", userID, rows, listErr)
		}
	}

	makePublic := true
	conversation, err = UpdateConversation(ctx, db, conversation.ID, "creator", ConversationPatch{IsPublic: &makePublic})
	if err != nil || !conversation.IsPublic {
		t.Fatalf("creator publish conversation=%+v err=%v", conversation, err)
	}
	if _, err := GetConversation(ctx, db, conversation.ID, "member"); err != nil {
		t.Fatalf("member cannot read public conversation: %v", err)
	}

	makePrivate := false
	if _, err := UpdateConversation(ctx, db, conversation.ID, "member", ConversationPatch{IsPublic: &makePrivate}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member visibility update error=%v, want ErrNotFound", err)
	}
	if current, err := GetConversation(ctx, db, conversation.ID, "member"); err != nil || !current.IsPublic {
		t.Fatalf("unauthorized visibility update changed row=%+v err=%v", current, err)
	}

	memberMessage, err := CreateMessageForUser(ctx, db, Message{
		ID: "member-history", ConversationID: conversation.ID, Role: "user", AuthorID: "member",
		Blocks: json.RawMessage(`[{"kind":"text","text":"member history remains"}]`),
	}, "member")
	if err != nil {
		t.Fatalf("member write while public: %v", err)
	}
	if _, err := UpdateConversation(ctx, db, conversation.ID, "creator", ConversationPatch{IsPublic: &makePrivate}); err != nil {
		t.Fatalf("creator make private: %v", err)
	}
	if _, err := GetConversation(ctx, db, conversation.ID, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member read after private error=%v, want ErrNotFound", err)
	}
	if message, err := GetMessage(ctx, db, memberMessage.ID); err != nil || message.AuthorID != "member" {
		t.Fatalf("historical member attribution=%+v err=%v", message, err)
	}
	messages, err := ListAllMessages(ctx, db, conversation.ID)
	if err != nil || len(messages) != 1 || messages[0].AuthorID != "member" {
		t.Fatalf("creator history=%+v err=%v; member turn must be retained", messages, err)
	}
	titles, hits, err := SearchConversations(ctx, db, "member", workspace.ID, "member history", 10, 10)
	if err != nil || len(titles) != 0 || len(hits) != 0 {
		t.Fatalf("member search leaked private conversation titles=%v hits=%v err=%v", titles, hits, err)
	}

	if _, err := CreateFile(ctx, db, File{
		ID: "private-file", UserID: "creator", ConversationID: conversation.ID,
		Filename: "private.txt", MimeType: "text/plain", Kind: "text", StoragePath: "/tmp/private-file", SizeBytes: 7,
	}); err != nil {
		t.Fatalf("create private conversation file: %v", err)
	}
	if files, err := ListFilesByConversation(ctx, db, conversation.ID, "member"); err != nil || len(files) != 0 {
		t.Fatalf("member private files=%+v err=%v, want empty", files, err)
	}

	personal, err := CreateConversation(ctx, db, Conversation{ID: "personal", UserID: "creator", Title: "Personal"})
	if err != nil {
		t.Fatalf("create personal: %v", err)
	}
	if _, err := UpdateConversation(ctx, db, personal.ID, "creator", ConversationPatch{IsPublic: &makePublic}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal visibility update error=%v, want ErrNotFound", err)
	}
}

func TestConversationVisibilityMigrationKeepsExistingWorkspaceRowsPublic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "legacy-conversation-visibility.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE conversations ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("add legacy workspace column: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE conversations DROP COLUMN is_public`); err != nil {
		t.Fatalf("remove new visibility column: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('owner','owner@example.test','h','user','active')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member','member@example.test','h','user','active')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('legacy-ws','Legacy','owner','legacy-token')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('legacy-ws','owner','owner')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('legacy-ws','member','member')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('legacy-conv','owner','Previously shared','legacy-ws')`)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	conversation, err := GetConversation(ctx, db, "legacy-conv", "member")
	if err != nil || !conversation.IsPublic {
		t.Fatalf("legacy conversation=%+v err=%v, want public and member-visible", conversation, err)
	}
}
