package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
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

func seedPersonalKnowledgeBaseShares(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db, `INSERT INTO knowledge_base_shares(kb_id,user_id,role) VALUES
		('personal-kb','member','read'),
		('personal-kb','outsider','write')`)
	exec(t, db, `UPDATE documents SET uploaded_by_user_id='creator' WHERE id='personal-document'`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id) VALUES
		('writer-document','personal-kb','writer.txt','text/plain',1,'ready','','outsider'),
		('owner-failed-document','personal-kb','owner-failed.txt','text/plain',1,'failed','','creator'),
		('writer-failed-document','personal-kb','writer-failed.txt','text/plain',1,'failed','','outsider')`)
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
		{name: "workspace ordinary member with default permissions", documentID: "workspace-document", kbID: "workspace-kb", actor: "member", wantAllowed: true},
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

func TestPersonalKnowledgeBaseCollaborationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		actor       string
		documentID  string
		wantAllowed bool
	}{
		{name: "owner deletes any file", actor: "creator", documentID: "writer-document", wantAllowed: true},
		{name: "writer deletes own file", actor: "outsider", documentID: "writer-document", wantAllowed: true},
		{name: "writer cannot delete owner file", actor: "outsider", documentID: "personal-document"},
		{name: "reader cannot delete file", actor: "member", documentID: "personal-document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			seedPersonalKnowledgeBaseShares(t, db)
			err := DeleteDocumentForUser(context.Background(), db, tc.documentID, "kb", "personal-kb", tc.actor)
			if tc.wantAllowed && err != nil {
				t.Fatalf("DeleteDocumentForUser error=%v, want success", err)
			}
			if !tc.wantAllowed && !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteDocumentForUser error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "documents", tc.documentID, !tc.wantAllowed)
		})
	}

	for _, tc := range []struct {
		name        string
		actor       string
		documentID  string
		wantAllowed bool
	}{
		{name: "owner retries any file", actor: "creator", documentID: "writer-failed-document", wantAllowed: true},
		{name: "writer retries own file", actor: "outsider", documentID: "writer-failed-document", wantAllowed: true},
		{name: "writer cannot retry owner file", actor: "outsider", documentID: "owner-failed-document"},
		{name: "reader cannot retry file", actor: "member", documentID: "owner-failed-document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			seedPersonalKnowledgeBaseShares(t, db)
			err := RetryKBDocumentForUser(context.Background(), db, tc.documentID, "personal-kb", tc.actor)
			if tc.wantAllowed && err != nil {
				t.Fatalf("RetryKBDocumentForUser error=%v, want success", err)
			}
			if !tc.wantAllowed && !errors.Is(err, ErrNotFound) {
				t.Fatalf("RetryKBDocumentForUser error=%v, want ErrNotFound", err)
			}
			var status string
			if err := db.QueryRow(`SELECT status FROM documents WHERE id=?`, tc.documentID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			wantStatus := "failed"
			if tc.wantAllowed {
				wantStatus = "pending"
			}
			if status != wantStatus {
				t.Fatalf("status=%q, want %q", status, wantStatus)
			}
		})
	}

	for _, tc := range []struct {
		name         string
		actor        string
		documentID   string
		wantAllowed  bool
		wantFilename string
	}{
		{name: "owner renames any file", actor: "creator", documentID: "writer-document", wantAllowed: true, wantFilename: "owner-renamed.txt"},
		{name: "writer renames own file", actor: "outsider", documentID: "writer-document", wantAllowed: true, wantFilename: "writer-renamed.txt"},
		{name: "writer cannot rename owner file", actor: "outsider", documentID: "personal-document", wantFilename: "personal.txt"},
		{name: "reader cannot rename file", actor: "member", documentID: "personal-document", wantFilename: "personal.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			seedPersonalKnowledgeBaseShares(t, db)
			requested := tc.wantFilename
			if !tc.wantAllowed {
				requested = "forged-name.txt"
			}
			err := RenameDocumentForUser(context.Background(), db, tc.documentID, "kb", "personal-kb", tc.actor, requested)
			if tc.wantAllowed && err != nil {
				t.Fatalf("RenameDocumentForUser error=%v, want success", err)
			}
			if !tc.wantAllowed && !errors.Is(err, ErrNotFound) {
				t.Fatalf("RenameDocumentForUser error=%v, want ErrNotFound", err)
			}
			var filename string
			if err := db.QueryRow(`SELECT filename FROM documents WHERE id=?`, tc.documentID).Scan(&filename); err != nil {
				t.Fatal(err)
			}
			if filename != tc.wantFilename {
				t.Fatalf("filename=%q, want %q", filename, tc.wantFilename)
			}
		})
	}
}

func TestPersonalKnowledgeBaseShareRolesControlReadAndUpload(t *testing.T) {
	db := openKBPermissionTestDB(t)
	seedPersonalKnowledgeBaseShares(t, db)
	ctx := context.Background()

	readerKB, err := GetKB(ctx, db, "personal-kb", "member")
	if err != nil || readerKB.AccessRole != "read" || readerKB.CanUpload || readerKB.CanDelete || readerKB.CanShare {
		t.Fatalf("reader access=%+v err=%v", readerKB, err)
	}
	writerKB, err := GetKB(ctx, db, "personal-kb", "outsider")
	if err != nil || writerKB.AccessRole != "write" || !writerKB.CanUpload || writerKB.CanDelete || writerKB.CanShare {
		t.Fatalf("writer access=%+v err=%v", writerKB, err)
	}
	if _, err := CreateDocumentForUser(ctx, db, Document{KBID: "personal-kb", Filename: "reader-upload.txt", MimeType: "text/plain"}, "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reader upload error=%v, want ErrNotFound", err)
	}
	created, err := CreateDocumentForUser(ctx, db, Document{KBID: "personal-kb", Filename: "writer-upload.txt", MimeType: "text/plain"}, "outsider")
	if err != nil {
		t.Fatalf("writer upload: %v", err)
	}
	if created.UploadedByUserID != "outsider" {
		t.Fatalf("writer upload attribution=%q", created.UploadedByUserID)
	}
	if !created.CanDelete {
		t.Fatalf("writer upload can_delete=%v, want true for own newly uploaded file", created.CanDelete)
	}
	if err := DeleteKB(ctx, db, "personal-kb", "outsider"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("writer deleted shared knowledge base: %v", err)
	}
}

func TestPersonalKnowledgeBaseWriteRevocationBlocksEveryMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		changeRole func(*testing.T, *sql.DB)
	}{
		{
			name: "share revoked",
			changeRole: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if err := DeleteKnowledgeBaseShare(context.Background(), db, "personal-kb", "creator", "outsider"); err != nil {
					t.Fatalf("revoke write share: %v", err)
				}
			},
		},
		{
			name: "write share downgraded to read",
			changeRole: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := UpsertKnowledgeBaseShare(context.Background(), db, "personal-kb", "creator", "outsider@example.test", "read"); err != nil {
					t.Fatalf("downgrade write share: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openKBPermissionTestDB(t)
			seedPersonalKnowledgeBaseShares(t, db)
			exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id,is_public)
				VALUES('writer-conversation','outsider','Writer chat','',0)`)
			exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,uploaded_by_user_id)
				VALUES('writer-conversation-document','writer-conversation','writer-chat.txt','text/plain',1,'ready','','outsider')`)

			tc.changeRole(t, db)
			ctx := context.Background()

			if _, err := CreateDocumentForUser(ctx, db, Document{
				ID: "upload-after-role-change", KBID: "personal-kb",
				Filename: "blocked-upload.txt", MimeType: "text/plain",
			}, "outsider"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("upload after role change error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "documents", "upload-after-role-change", false)

			if err := RenameDocumentForUser(ctx, db, "writer-document", "kb", "personal-kb", "outsider", "forged-name.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rename after role change error=%v, want ErrNotFound", err)
			}
			var filename string
			if err := db.QueryRow(`SELECT filename FROM documents WHERE id='writer-document'`).Scan(&filename); err != nil {
				t.Fatal(err)
			}
			if filename != "writer.txt" {
				t.Fatalf("filename=%q, want writer.txt", filename)
			}

			if err := RetryKBDocumentForUser(ctx, db, "writer-failed-document", "personal-kb", "outsider"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("retry after role change error=%v, want ErrNotFound", err)
			}
			var status string
			if err := db.QueryRow(`SELECT status FROM documents WHERE id='writer-failed-document'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "failed" {
				t.Fatalf("status=%q, want failed", status)
			}

			if err := DeleteDocumentForUser(ctx, db, "writer-document", "kb", "personal-kb", "outsider"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("delete after role change error=%v, want ErrNotFound", err)
			}
			assertStoreRowPresence(t, db, "documents", "writer-document", true)

			if err := PromoteDocumentToKB(ctx, db, "writer-conversation-document", "personal-kb", "outsider"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("promote after role change error=%v, want ErrNotFound", err)
			}
			var kbID, conversationID sql.NullString
			if err := db.QueryRow(`SELECT kb_id,conversation_id FROM documents WHERE id='writer-conversation-document'`).Scan(&kbID, &conversationID); err != nil {
				t.Fatal(err)
			}
			if kbID.Valid || !conversationID.Valid || conversationID.String != "writer-conversation" {
				t.Fatalf("promoted document parent kb=%v conversation=%v, want original conversation", kbID, conversationID)
			}
		})
	}
}

func TestCreatedWorkspaceKnowledgeBaseReturnsEffectiveCapabilities(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	created, err := CreateKB(ctx, db, KnowledgeBase{
		UserID: "creator", WorkspaceID: "ws1", Name: "Created workspace KB",
		EmbeddingModelID: "emb-a", EmbeddingDim: 3,
	})
	if err != nil {
		t.Fatalf("create workspace knowledge base: %v", err)
	}
	if created.AccessRole != "workspace" || !created.CanUpload || !created.CanDeleteContent || !created.CanManageMembers {
		t.Fatalf("create response capabilities=%+v, want effective creator permissions", created)
	}
}

func TestWorkspaceKnowledgeBaseCreatorKeepsManagementCapabilities(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()

	permissions := fullWorkspaceMemberPermissions()
	permissions.CanAddKBFiles = false
	permissions.CanDeleteKBContent = false
	if _, err := UpdateWorkspaceMemberPermissions(ctx, db, "ws1", "owner", "creator", permissions); err != nil {
		t.Fatalf("revoke creator member totals: %v", err)
	}

	kb, err := GetKB(ctx, db, "workspace-kb", "creator")
	if err != nil || !kb.CanUpload || !kb.CanDeleteContent || !kb.CanDelete || !kb.CanManageMembers {
		t.Fatalf("creator capabilities=%+v err=%v, want full library management", kb, err)
	}
	created, err := CreateDocumentForUser(ctx, db, Document{
		ID: "creator-upload-after-total-revoke", KBID: "workspace-kb",
		Filename: "creator-upload.txt", MimeType: "text/plain",
	}, "creator")
	if err != nil || !created.CanDelete {
		t.Fatalf("creator upload=%+v err=%v", created, err)
	}
	if err := DeleteDocumentForUser(ctx, db, created.ID, "kb", "workspace-kb", "creator"); err != nil {
		t.Fatalf("creator delete content after total revoke: %v", err)
	}
	if err := DeleteKB(ctx, db, "workspace-kb", "creator"); err != nil {
		t.Fatalf("creator delete library after total revoke: %v", err)
	}
}

func TestWorkspaceKnowledgeBaseCannotUsePersonalSharing(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	if _, err := UpsertKnowledgeBaseShare(ctx, db, "workspace-kb", "creator", "outsider@example.test", "read"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace share error=%v, want ErrNotFound", err)
	}
	if _, err := ListKnowledgeBaseShares(ctx, db, "workspace-kb", "creator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace share list error=%v, want ErrNotFound", err)
	}
	if _, err := SearchKnowledgeBaseShareCandidates(ctx, db, "workspace-kb", "creator", "", 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("workspace share candidates error=%v, want ErrNotFound", err)
	}
}

func TestListKnowledgeBaseAccessUserIDsDeduplicatesEveryAccessPath(t *testing.T) {
	db := openKBPermissionTestDB(t)
	seedPersonalKnowledgeBaseShares(t, db)
	ctx := context.Background()

	personal, err := ListKnowledgeBaseAccessUserIDs(ctx, db, "personal-kb")
	if err != nil {
		t.Fatal(err)
	}
	wantPersonal := []string{"creator", "member", "outsider"}
	if !slices.Equal(personal, wantPersonal) {
		t.Fatalf("personal access users=%v, want %v", personal, wantPersonal)
	}

	workspace, err := ListKnowledgeBaseAccessUserIDs(ctx, db, "workspace-kb")
	if err != nil {
		t.Fatal(err)
	}
	// The creator is both k.user_id and a workspace member; the canonical owner
	// is both the workspace owner and a member. Each principal appears once.
	wantWorkspace := []string{"creator", "member", "owner"}
	if !slices.Equal(workspace, wantWorkspace) {
		t.Fatalf("workspace access users=%v, want %v", workspace, wantWorkspace)
	}

	if _, err := ListKnowledgeBaseAccessUserIDs(ctx, db, "missing-kb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing knowledge base error=%v, want ErrNotFound", err)
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
