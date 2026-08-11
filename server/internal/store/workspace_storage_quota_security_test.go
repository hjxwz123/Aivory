package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkspaceStorageUsageFollowsCanonicalBillingPrincipal(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-storage-accounting.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, id := range []string{"owner", "member"} {
		exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES(?,?, 'h','user','active')`, id, id+"@example.test")
	}
	workspace, err := CreateWorkspace(ctx, db, "owner", "Storage")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	if _, err := CreateConversation(ctx, db, Conversation{
		ID: "workspace-conversation", UserID: "member", WorkspaceID: workspace.ID, IsPublic: true, Title: "Shared",
	}); err != nil {
		t.Fatalf("create workspace conversation: %v", err)
	}
	if _, err := CreateConversation(ctx, db, Conversation{
		ID: "personal-conversation", UserID: "member", Title: "Personal",
	}); err != nil {
		t.Fatalf("create personal conversation: %v", err)
	}

	// Workspace committed bytes move to the canonical owner, regardless of the
	// uploader. Workspace drafts remain on the uploader until message commit.
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at) VALUES
		('workspace-committed','member','workspace-conversation','committed.txt','text/plain',20,'/quota/workspace-committed','text',0,1),
		('workspace-image','member','workspace-conversation','image.png','image/png',999,'/quota/workspace-image','image',0,2),
		('workspace-draft','member','workspace-conversation','draft.txt','text/plain',10,'/quota/workspace-draft','text',1,3),
		('standalone','member',NULL,'standalone.txt','text/plain',30,'/quota/standalone','text',0,4),
		('personal-file','member','personal-conversation','personal.txt','text/plain',40,'/quota/personal-file','text',0,5)`)
	// The first row is a files twin and must not double-count. The second is an
	// independent workspace document billed to owner; the third stays personal.
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,created_at) VALUES
		('workspace-twin','workspace-conversation','committed.txt','text/plain',20,'ready','/quota/workspace-committed',1),
		('workspace-document','workspace-conversation','workspace-doc.txt','text/plain',50,'ready','/quota/workspace-document',2),
		('personal-document','personal-conversation','personal-doc.txt','text/plain',60,'ready','/quota/personal-document',3)`)

	if used, err := UserStorageUsage(ctx, db, "owner"); err != nil || used != 70 {
		t.Fatalf("owner usage=%d err=%v, want 70 committed workspace bytes", used, err)
	}
	if used, err := UserStorageUsage(ctx, db, "member"); err != nil || used != 140 {
		t.Fatalf("member usage=%d err=%v, want 140 draft/personal bytes", used, err)
	}
	ownerRows, err := ListAdminFiles(ctx, db, AdminFileFilter{
		BillingUserID: "owner", AccessUserID: "owner",
	}, 20, 0)
	if err != nil {
		t.Fatalf("owner billing inventory: %v", err)
	}
	if len(ownerRows) != 3 { // committed file, shared image, independent document
		t.Fatalf("owner billing inventory=%#v, want 3 shared committed rows", ownerRows)
	}
	for _, row := range ownerRows {
		if row.BillingUserID != "owner" {
			t.Fatalf("row %s billing user=%q, want owner", row.ID, row.BillingUserID)
		}
	}

	// Remove the unsent draft before revocation. Retained committed bytes must
	// remain charged to owner after the uploader is no longer a member.
	exec(t, db, `DELETE FROM files WHERE id='workspace-draft'`)
	if err := RemoveWorkspaceMember(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if used, err := UserStorageUsage(ctx, db, "owner"); err != nil || used != 70 {
		t.Fatalf("owner usage after kick=%d err=%v, want retained 70", used, err)
	}
	if used, err := UserStorageUsage(ctx, db, "member"); err != nil || used != 130 {
		t.Fatalf("member usage after kick=%d err=%v, want personal 130", used, err)
	}
}

func TestConcurrentWorkspaceDraftCommitCannotExceedOwnerStorageQuota(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-storage-commit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,created_at,updated_at) VALUES('storage-capped','Storage capped',1,1,1)`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('owner','owner@example.test','h','user','active','storage-capped')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member','member@example.test','h','user','active')`)
	workspace, err := CreateWorkspace(ctx, db, "owner", "Storage")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	if _, err := CreateConversation(ctx, db, Conversation{
		ID: "quota-conversation", UserID: "member", WorkspaceID: workspace.ID, IsPublic: true, Title: "Shared",
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	const kib = int64(1024)
	exec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at)
		VALUES('owner-existing','owner','existing.bin','application/octet-stream',?,'/quota/owner-existing','other',0,1)`, 700*kib)
	for _, id := range []string{"draft-a", "draft-b"} {
		if _, err := CreateFile(ctx, db, File{
			ID: id, UserID: "member", ConversationID: "quota-conversation",
			Filename: id + ".bin", MimeType: "application/octet-stream",
			SizeBytes: 200 * kib, StoragePath: "/quota/" + id, Kind: "other", Draft: true,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, fileID := range []string{"draft-a", "draft-b"} {
		wg.Add(1)
		go func(fileID string) {
			defer wg.Done()
			<-start
			attachments, _ := json.Marshal([]map[string]string{{"id": fileID}})
			_, err := CreateMessageForUser(ctx, db, Message{
				ID: "quota-message-" + fileID, ConversationID: "quota-conversation",
				Role: "user", AuthorID: "member", Blocks: json.RawMessage(`[]`), Attachments: attachments,
			}, "member")
			errs <- err
		}(fileID)
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded, quotaRejected := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrStorageQuotaExceeded):
			quotaRejected++
		default:
			t.Fatalf("unexpected concurrent commit error: %v", err)
		}
	}
	if succeeded != 1 || quotaRejected != 1 {
		t.Fatalf("concurrent commits succeeded=%d quota_rejected=%d, want 1/1", succeeded, quotaRejected)
	}
	if used, err := UserStorageUsage(ctx, db, "owner"); err != nil || used != 900*kib {
		t.Fatalf("owner usage=%d err=%v, want %d", used, err, 900*kib)
	}
	var committed, messages int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE id IN ('draft-a','draft-b') AND draft=0`).Scan(&committed); err != nil {
		t.Fatalf("count committed drafts: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id LIKE 'quota-message-%'`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if committed != 1 || messages != 1 {
		t.Fatalf("committed drafts=%d messages=%d, want 1/1", committed, messages)
	}
}

func TestWorkspaceDocumentCreationUsesOwnerQuotaAndFileTwinDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-document-storage.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,created_at,updated_at) VALUES('document-capped','Document capped',1,1,1)`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status,group_id) VALUES('owner','owner@example.test','h','user','active','document-capped')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role,status) VALUES('member','member@example.test','h','user','active')`)
	workspace, err := CreateWorkspace(ctx, db, "owner", "Documents")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}
	if _, err := CreateConversation(ctx, db, Conversation{
		ID: "document-conversation", UserID: "member", WorkspaceID: workspace.ID, IsPublic: true, Title: "Shared",
	}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	const kib = int64(1024)
	exec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,draft,created_at)
		VALUES('owner-existing','owner','existing.bin','application/octet-stream',?,'/quota/document-existing','other',0,1)`, 900*kib)

	if _, err := CreateDocumentForUser(ctx, db, Document{
		ID: "too-large-document", ConversationID: "document-conversation",
		Filename: "large.txt", MimeType: "text/plain", SizeBytes: 200 * kib,
		StoragePath: "/quota/too-large-document", Status: "pending",
	}, "member"); !errors.Is(err, ErrStorageQuotaExceeded) {
		t.Fatalf("workspace document error=%v, want owner storage quota", err)
	}
	var rejectedRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents WHERE id='too-large-document'`).Scan(&rejectedRows); err != nil || rejectedRows != 0 {
		t.Fatalf("rejected document rows=%d err=%v, want 0", rejectedRows, err)
	}

	file, err := CreateFile(ctx, db, File{
		ID: "twin-file", UserID: "member", ConversationID: "document-conversation",
		Filename: "twin.txt", MimeType: "text/plain", SizeBytes: 100 * kib,
		StoragePath: "/quota/twin", Kind: "text",
	})
	if err != nil {
		t.Fatalf("create file at exact owner quota: %v", err)
	}
	if _, err := CreateDocumentForUser(ctx, db, Document{
		ID: "file-twin-document", ConversationID: "document-conversation",
		Filename: file.Filename, MimeType: file.MimeType, SizeBytes: file.SizeBytes,
		StoragePath: file.StoragePath, Status: "pending",
	}, "member"); err != nil {
		t.Fatalf("create file twin at full quota: %v", err)
	}
	if used, err := UserStorageUsage(ctx, db, "owner"); err != nil || used != 1000*kib {
		t.Fatalf("owner usage=%d err=%v, want %d without twin double-count", used, err, 1000*kib)
	}
}
