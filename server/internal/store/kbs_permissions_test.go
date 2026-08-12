package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openKBPermissionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "kb-permissions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('owner','owner@example.test','h','user'),
		('creator','creator@example.test','h','user'),
		('member','member@example.test','h','user'),
		('outsider','outsider@example.test','h','user')`)
	exec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	exec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES
		('emb-a','ch1','embedding','emb-a','Embedding A',3),
		('emb-b','ch1','embedding','emb-b','Embedding B',3)`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite-token')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES
		('ws1','owner','owner'),
		('ws1','creator','member'),
		('ws1','member','member')`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES
		('personal-kb','creator','Personal KB','emb-a',3,''),
		('workspace-kb','creator','Workspace KB','emb-a',3,'ws1'),
		('compatible-kb','creator','Compatible KB','emb-a',3,''),
		('different-model-kb','creator','Different Model KB','emb-b',3,''),
		('different-dim-kb','creator','Different Dimension KB','emb-a',4,'')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id,is_public) VALUES
		('personal-conversation','creator','Personal','',0),
		('workspace-conversation','creator','Workspace','ws1',1)`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES
		('personal-document','personal-kb','personal.txt','text/plain',1,'ready',''),
		('workspace-document','workspace-kb','workspace.txt','text/plain',1,'ready','')`)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES
		('personal-conversation-document','personal-conversation','personal-chat.txt','text/plain',1,'ready',''),
		('workspace-conversation-document','workspace-conversation','workspace-chat.txt','text/plain',1,'ready','')`)

	return db
}

func TestValidateKBEmbeddingCompatibility(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		ids     []string
		wantErr bool
	}{
		{name: "same signature", ids: []string{"personal-kb", "compatible-kb"}},
		{name: "different model", ids: []string{"personal-kb", "different-model-kb"}, wantErr: true},
		{name: "different indexed dimension", ids: []string{"personal-kb", "different-dim-kb"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKBEmbeddingCompatibility(ctx, db, tc.ids)
			if tc.wantErr && !errors.Is(err, ErrMixedKBEmbeddingModels) {
				t.Fatalf("error=%v, want ErrMixedKBEmbeddingModels", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("compatible selection rejected: %v", err)
			}
		})
	}
}

func TestDeleteKBRequiresResourceManager(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kbID          string
		actor         string
		revokeCreator bool
		wantAllowed   bool
	}{
		{name: "personal creator", kbID: "personal-kb", actor: "creator", wantAllowed: true},
		{name: "personal non-creator", kbID: "personal-kb", actor: "member"},
		{name: "workspace owner", kbID: "workspace-kb", actor: "owner", wantAllowed: true},
		{name: "workspace current creator", kbID: "workspace-kb", actor: "creator", wantAllowed: true},
		{name: "workspace ordinary member", kbID: "workspace-kb", actor: "member"},
		{name: "workspace former creator", kbID: "workspace-kb", actor: "creator", revokeCreator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			if tc.revokeCreator {
				exec(t, db, `DELETE FROM workspace_members WHERE workspace_id='ws1' AND user_id='creator'`)
			}

			err := DeleteKB(context.Background(), db, tc.kbID, tc.actor)
			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("DeleteKB error=%v, want success", err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteKB error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "knowledge_bases", tc.kbID, !tc.wantAllowed)
		})
	}
}

func TestDeleteKBDocumentRequiresResourceManager(t *testing.T) {
	for _, tc := range []struct {
		name          string
		documentID    string
		kbID          string
		actor         string
		revokeCreator bool
		wantAllowed   bool
	}{
		{name: "personal creator", documentID: "personal-document", kbID: "personal-kb", actor: "creator", wantAllowed: true},
		{name: "personal non-creator", documentID: "personal-document", kbID: "personal-kb", actor: "member"},
		{name: "workspace owner", documentID: "workspace-document", kbID: "workspace-kb", actor: "owner", wantAllowed: true},
		{name: "workspace current creator", documentID: "workspace-document", kbID: "workspace-kb", actor: "creator", wantAllowed: true},
		{name: "workspace ordinary member", documentID: "workspace-document", kbID: "workspace-kb", actor: "member"},
		{name: "workspace former creator", documentID: "workspace-document", kbID: "workspace-kb", actor: "creator", revokeCreator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			if tc.revokeCreator {
				exec(t, db, `DELETE FROM workspace_members WHERE workspace_id='ws1' AND user_id='creator'`)
			}

			err := DeleteDocumentForUser(context.Background(), db, tc.documentID, "kb", tc.kbID, tc.actor)
			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("DeleteDocumentForUser error=%v, want success", err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteDocumentForUser error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "documents", tc.documentID, !tc.wantAllowed)
		})
	}
}

func TestDeleteConversationDocumentKeepsConversationAccessRules(t *testing.T) {
	for _, tc := range []struct {
		name           string
		documentID     string
		conversationID string
		actor          string
		wantAllowed    bool
	}{
		{name: "personal creator", documentID: "personal-conversation-document", conversationID: "personal-conversation", actor: "creator", wantAllowed: true},
		{name: "personal non-creator", documentID: "personal-conversation-document", conversationID: "personal-conversation", actor: "member"},
		{name: "workspace ordinary member", documentID: "workspace-conversation-document", conversationID: "workspace-conversation", actor: "member", wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)

			err := DeleteDocumentForUser(context.Background(), db, tc.documentID, "conversation", tc.conversationID, tc.actor)
			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("DeleteDocumentForUser error=%v, want success", err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteDocumentForUser error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "documents", tc.documentID, !tc.wantAllowed)
		})
	}
}

func assertStoreRowPresence(t *testing.T, db *sql.DB, table, id string, wantPresent bool) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatalf("count %s %s: %v", table, id, err)
	}
	wantCount := 0
	if wantPresent {
		wantCount = 1
	}
	if count != wantCount {
		t.Fatalf("%s %s count=%d, want %d", table, id, count, wantCount)
	}
}
