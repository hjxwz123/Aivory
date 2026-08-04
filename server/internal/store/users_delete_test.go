package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDeleteUserRemovesOwnedFilesDocumentsAndStorage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "users-delete.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	uploadPath := filepath.Join(root, "upload.txt")
	kbPath := filepath.Join(root, "kb.txt")
	if err := os.WriteFile(uploadPath, []byte("upload"), 0o600); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := os.WriteFile(kbPath, []byte("kb"), 0o600); err != nil {
		t.Fatalf("write kb: %v", err)
	}

	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('u2','u2@x.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('ch1','c','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('emb1','ch1','embedding','embed','Embed')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','own')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c2','u2','shared-owner')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','kb','emb1',3)`,
	} {
		exec(t, db, q)
	}
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f1','u1','c2','upload.txt',?)`, uploadPath)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_file','c2','upload.txt','text/plain',6,'ready',?)`, uploadPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,conversation_id,seq,content,embedding_model) VALUES('chunk_file','d_file','c2',0,'hello','emb:test')`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_kb','kb1','kb.txt','text/plain',2,'ready',?)`, kbPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,kb_id,seq,content,embedding_model) VALUES('chunk_kb','d_kb','kb1',0,'kb','emb:test')`)

	plan, err := BuildUserCleanupPlan(ctx, db, "u1")
	if err != nil {
		t.Fatalf("BuildUserCleanupPlan: %v", err)
	}
	if !has(plan.ConversationIDs, "c1") || !has(plan.KBIDs, "kb1") || !has(plan.DocumentIDs, "d_file") {
		t.Fatalf("cleanup plan missed side-state ids: %+v", plan)
	}
	if !has(plan.StoragePaths, uploadPath) || !has(plan.StoragePaths, kbPath) {
		t.Fatalf("cleanup plan missed storage paths: %+v", plan.StoragePaths)
	}

	if err := DeleteUser(ctx, db, "u1", root); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	assertMissing(t, db, `SELECT id FROM files WHERE id='f1'`)
	assertMissing(t, db, `SELECT id FROM documents WHERE id='d_file'`)
	assertMissing(t, db, `SELECT id FROM documents WHERE id='d_kb'`)
	assertMissing(t, db, `SELECT id FROM chunks WHERE id='chunk_file'`)
	if !convExists(t, db, "c2") {
		t.Fatalf("other user's conversation should survive")
	}
	if _, err := os.Stat(uploadPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upload storage should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(kbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kb storage should be removed, stat err=%v", err)
	}
}

func TestDeleteUserPreservesWorkspaceResourcesAndTransfersOwnership(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "workspace-member-delete.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	committedPath := filepath.Join(root, "shared-committed.txt")
	draftPath := filepath.Join(root, "shared-draft.txt")
	sharedArtifactPath := filepath.Join(root, "shared-artifact.txt")
	personalPath := filepath.Join(root, "personal.txt")
	personalKBPath := filepath.Join(root, "personal-kb.txt")
	personalArtifactPath := filepath.Join(root, "personal-artifact.txt")
	for path, body := range map[string]string{
		committedPath:        "shared",
		draftPath:            "draft",
		sharedArtifactPath:   "artifact",
		personalPath:         "personal",
		personalKBPath:       "personal kb",
		personalArtifactPath: "personal artifact",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('member','member@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('collab','collab@x.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('delete-ch','Delete','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('delete-emb','delete-ch','embedding','embed','Embed')`,
		`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-delete','Workspace','owner','invite-delete')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,joined_at) VALUES('ws-delete','owner','owner',1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,joined_at) VALUES('ws-delete','member','member',2)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,joined_at) VALUES('ws-delete','collab','member',3)`,
		`INSERT INTO projects(id,user_id,name,workspace_id) VALUES('shared-project','member','Shared Project','ws-delete')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('shared-kb','member','Shared KB','delete-emb',3,'ws-delete')`,
		`INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('shared-conv','member','Shared','ws-delete')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,author_id) VALUES('shared-user-message','shared-conv','user','[]','collab')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,author_id) VALUES('shared-assistant-message','shared-conv','assistant','[]','collab')`,
		`INSERT INTO conversation_shares(id,conversation_id,user_id,title,snapshot) VALUES('shared-link','shared-conv','member','Shared','[]')`,
		`INSERT INTO projects(id,user_id,name,workspace_id) VALUES('personal-project','member','Personal Project','')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('personal-kb','member','Personal KB','delete-emb',3,'')`,
		`INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('personal-conv','member','Personal','')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,author_id) VALUES('personal-message','personal-conv','user','[]','member')`,
	} {
		exec(t, db, query)
	}
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('shared-committed','member','shared-conv','shared.txt','text/plain',6,?,'text',0)`, committedPath)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('shared-committed-doc','shared-conv','shared.txt','text/plain',6,'ready',?)`, committedPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,conversation_id,seq,content,embedding_model)
		VALUES('shared-committed-chunk','shared-committed-doc','shared-conv',0,'shared','delete-emb')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('shared-draft','member','shared-conv','draft.txt','text/plain',5,?,'text',1)`, draftPath)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('shared-draft-doc','shared-conv','draft.txt','text/plain',5,'ready',?)`, draftPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,conversation_id,seq,content,embedding_model)
		VALUES('shared-draft-chunk','shared-draft-doc','shared-conv',0,'private draft','delete-emb')`)
	exec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('shared-artifact','shared-assistant-message','shared-artifact.txt',?,'text/plain',8)`, sharedArtifactPath)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('personal-file','member','personal-conv','personal.txt','text/plain',8,?,'text',0)`, personalPath)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('personal-doc','personal-conv','personal.txt','text/plain',8,'ready',?)`, personalPath)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('personal-kb-doc','personal-kb','personal-kb.txt','text/plain',11,'ready',?)`, personalKBPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,kb_id,seq,content,embedding_model)
		VALUES('personal-kb-chunk','personal-kb-doc','personal-kb',0,'personal kb','delete-emb')`)
	exec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('personal-artifact','personal-message','personal-artifact.txt',?,'text/plain',8)`, personalArtifactPath)

	plan, err := BuildUserCleanupPlan(ctx, db, "member")
	if err != nil {
		t.Fatalf("BuildUserCleanupPlan: %v", err)
	}
	if has(plan.ConversationIDs, "shared-conv") || has(plan.KBIDs, "shared-kb") || has(plan.DocumentIDs, "shared-committed-doc") {
		t.Fatalf("cleanup plan included committed workspace state: %+v", plan)
	}
	if !has(plan.ConversationIDs, "personal-conv") || !has(plan.KBIDs, "personal-kb") ||
		!has(plan.DocumentIDs, "personal-doc") || !has(plan.DocumentIDs, "personal-kb-doc") ||
		!has(plan.DocumentIDs, "shared-draft-doc") {
		t.Fatalf("cleanup plan missed personal/draft state: %+v", plan)
	}
	for _, preservedPath := range []string{committedPath, sharedArtifactPath} {
		if has(plan.StoragePaths, preservedPath) {
			t.Fatalf("cleanup plan included preserved path %q: %+v", preservedPath, plan.StoragePaths)
		}
	}
	for _, deletedPath := range []string{draftPath, personalPath, personalKBPath, personalArtifactPath} {
		if !has(plan.StoragePaths, deletedPath) {
			t.Fatalf("cleanup plan missed deletable path %q: %+v", deletedPath, plan.StoragePaths)
		}
	}
	batch, err := ConversationIDsByUser(ctx, db, "member", 100)
	if err != nil {
		t.Fatalf("ConversationIDsByUser: %v", err)
	}
	if !has(batch, "personal-conv") || has(batch, "shared-conv") {
		t.Fatalf("deletion conversation batch = %v", batch)
	}
	if err := DeleteConversationRows(ctx, db, "member", []string{"shared-conv"}); err != nil {
		t.Fatalf("DeleteConversationRows shared id: %v", err)
	}
	assertRowPresent(t, db, `SELECT id FROM conversations WHERE id='shared-conv'`)
	if changed, err := MarkUserDeleting(ctx, db, "member"); err != nil || !changed {
		t.Fatalf("MarkUserDeleting changed=%v err=%v", changed, err)
	}
	for table, id := range map[string]string{
		"conversations":       "shared-conv",
		"projects":            "shared-project",
		"knowledge_bases":     "shared-kb",
		"files":               "shared-committed",
		"conversation_shares": "shared-link",
	} {
		var ownerID string
		if err := db.QueryRowContext(ctx, `SELECT user_id FROM `+table+` WHERE id=?`, id).Scan(&ownerID); err != nil || ownerID != "owner" {
			t.Fatalf("MarkUserDeleting did not transfer %s %s: owner=%q err=%v", table, id, ownerID, err)
		}
	}
	var draftOwner string
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM files WHERE id='shared-draft'`).Scan(&draftOwner); err != nil || draftOwner != "member" {
		t.Fatalf("MarkUserDeleting transferred private draft: owner=%q err=%v", draftOwner, err)
	}
	postMarkPlan, err := BuildUserCleanupPlan(ctx, db, "member")
	if err != nil {
		t.Fatalf("BuildUserCleanupPlan after mark: %v", err)
	}
	if has(postMarkPlan.ConversationIDs, "shared-conv") || has(postMarkPlan.DocumentIDs, "shared-committed-doc") || !has(postMarkPlan.DocumentIDs, "shared-draft-doc") {
		t.Fatalf("post-mark cleanup scope is unsafe: %+v", postMarkPlan)
	}

	if err := DeleteUser(ctx, db, "member", root); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	assertMissing(t, db, `SELECT id FROM users WHERE id='member'`)
	for _, query := range []string{
		`SELECT id FROM workspaces WHERE id='ws-delete'`,
		`SELECT id FROM conversations WHERE id='shared-conv'`,
		`SELECT id FROM projects WHERE id='shared-project'`,
		`SELECT id FROM knowledge_bases WHERE id='shared-kb'`,
		`SELECT id FROM messages WHERE id='shared-user-message'`,
		`SELECT id FROM artifacts WHERE id='shared-artifact'`,
		`SELECT id FROM files WHERE id='shared-committed'`,
		`SELECT id FROM documents WHERE id='shared-committed-doc'`,
		`SELECT id FROM chunks WHERE id='shared-committed-chunk'`,
		`SELECT id FROM conversation_shares WHERE id='shared-link'`,
	} {
		assertRowPresent(t, db, query)
	}
	for _, query := range []string{
		`SELECT id FROM conversations WHERE id='personal-conv'`,
		`SELECT id FROM projects WHERE id='personal-project'`,
		`SELECT id FROM knowledge_bases WHERE id='personal-kb'`,
		`SELECT id FROM files WHERE id='shared-draft'`,
		`SELECT id FROM documents WHERE id='shared-draft-doc'`,
		`SELECT id FROM chunks WHERE id='shared-draft-chunk'`,
	} {
		assertMissing(t, db, query)
	}
	for table, id := range map[string]string{
		"conversations":       "shared-conv",
		"projects":            "shared-project",
		"knowledge_bases":     "shared-kb",
		"files":               "shared-committed",
		"conversation_shares": "shared-link",
	} {
		var ownerID string
		if err := db.QueryRowContext(ctx, `SELECT user_id FROM `+table+` WHERE id=?`, id).Scan(&ownerID); err != nil || ownerID != "owner" {
			t.Fatalf("%s %s owner=%q err=%v, want owner", table, id, ownerID, err)
		}
	}
	var authorID string
	if err := db.QueryRowContext(ctx, `SELECT author_id FROM messages WHERE id='shared-user-message'`).Scan(&authorID); err != nil || authorID != "collab" {
		t.Fatalf("shared author=%q err=%v, want collab", authorID, err)
	}
	for _, path := range []string{committedPath, sharedArtifactPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved path %s: %v", path, err)
		}
	}
	for _, path := range []string{draftPath, personalPath, personalKBPath, personalArtifactPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted path %s stat err=%v", path, err)
		}
	}
}

func TestDeleteUserRenamesConflictingWorkspaceResourcesWithoutDroppingEither(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-transfer-conflict.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('member','member@x.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('conflict-ch','Conflict','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('conflict-emb','conflict-ch','embedding','embed','Embed')`,
		`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-conflict','Workspace','owner','invite-conflict')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-conflict','owner','owner')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-conflict','member','member')`,
		`INSERT INTO projects(id,user_id,name,workspace_id) VALUES('project-owner','owner','Same Name','ws-conflict')`,
		`INSERT INTO projects(id,user_id,name,workspace_id) VALUES('project-member','member',' same name ','ws-conflict')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('kb-owner','owner','Same KB','conflict-emb',3,'ws-conflict')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('kb-member','member',' same kb ','conflict-emb',3,'ws-conflict')`,
	} {
		exec(t, db, query)
	}
	if err := DeleteUser(ctx, db, "member"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	for _, table := range []string{"projects", "knowledge_bases"} {
		var total, ownerTotal, distinctNames int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN user_id='owner' THEN 1 ELSE 0 END), COUNT(DISTINCT lower(trim(name))) FROM `+table+` WHERE workspace_id='ws-conflict'`).Scan(&total, &ownerTotal, &distinctNames); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if total != 2 || ownerTotal != 2 || distinctNames != 2 {
			t.Fatalf("%s total=%d owner=%d distinct names=%d, want 2/2/2", table, total, ownerTotal, distinctNames)
		}
	}
}

func TestUserDeletionRefusesWorkspaceOwnerWithOtherMembers(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "workspace-owner-guard.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('member','member@x.test','h','user')`,
		`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-guard','Workspace','owner','invite-guard')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-guard','owner','owner')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-guard','member','member')`,
		`INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('guard-conv','owner','Shared','ws-guard')`,
	} {
		exec(t, db, query)
	}
	if _, err := MarkUserDeleting(ctx, db, "owner"); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatalf("MarkUserDeleting err=%v, want ErrWorkspaceOwnership", err)
	}
	if _, err := BuildUserCleanupPlan(ctx, db, "owner"); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatalf("BuildUserCleanupPlan err=%v, want ErrWorkspaceOwnership", err)
	}
	if _, err := ConversationIDsByUser(ctx, db, "owner", 10); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatalf("ConversationIDsByUser err=%v, want ErrWorkspaceOwnership", err)
	}
	if err := DeleteUser(ctx, db, "owner"); !errors.Is(err, ErrWorkspaceOwnership) {
		t.Fatalf("DeleteUser err=%v, want ErrWorkspaceOwnership", err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id='owner'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("owner status=%q err=%v, want active", status, err)
	}
	assertRowPresent(t, db, `SELECT id FROM workspaces WHERE id='ws-guard'`)
	assertRowPresent(t, db, `SELECT id FROM conversations WHERE id='guard-conv'`)
}

func TestDeleteUserRemovesEntireSoleOwnerWorkspaceIncludingFormerMemberResources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "workspace-sole-owner-delete.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	filePath := filepath.Join(root, "former-upload.txt")
	artifactPath := filepath.Join(root, "former-artifact.txt")
	for path, body := range map[string]string{filePath: "upload", artifactPath: "artifact"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('former','former@x.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('sole-ch','Sole','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('sole-emb','sole-ch','embedding','embed','Embed')`,
		`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws-sole','Workspace','owner','invite-sole')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-sole','owner','owner')`,
		`INSERT INTO projects(id,user_id,name,workspace_id) VALUES('former-project','former','Former Project','ws-sole')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('former-kb','former','Former KB','sole-emb',3,'ws-sole')`,
		`INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('former-conv','former','Former','ws-sole')`,
		`INSERT INTO messages(id,conversation_id,role,blocks,author_id) VALUES('former-message','former-conv','assistant','[]','former')`,
	} {
		exec(t, db, query)
	}
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft)
		VALUES('former-file','former','former-conv','upload.txt','text/plain',6,?,'text',0)`, filePath)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path)
		VALUES('former-doc','former-kb','upload.txt','text/plain',6,'ready',?)`, filePath)
	exec(t, db, `INSERT INTO chunks(id,document_id,kb_id,seq,content,embedding_model)
		VALUES('former-chunk','former-doc','former-kb',0,'former','sole-emb')`)
	exec(t, db, `INSERT INTO artifacts(id,message_id,filename,storage_path,mime_type,size_bytes)
		VALUES('former-artifact','former-message','artifact.txt',?,'text/plain',8)`, artifactPath)

	plan, err := BuildUserCleanupPlan(ctx, db, "owner")
	if err != nil {
		t.Fatalf("BuildUserCleanupPlan: %v", err)
	}
	if !has(plan.ConversationIDs, "former-conv") || !has(plan.KBIDs, "former-kb") ||
		!has(plan.DocumentIDs, "former-doc") || !has(plan.StoragePaths, filePath) || !has(plan.StoragePaths, artifactPath) {
		t.Fatalf("sole-owner cleanup plan missed workspace state: %+v", plan)
	}
	if err := DeleteUser(ctx, db, "owner", root); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	assertRowPresent(t, db, `SELECT id FROM users WHERE id='former'`)
	for _, query := range []string{
		`SELECT id FROM users WHERE id='owner'`,
		`SELECT id FROM workspaces WHERE id='ws-sole'`,
		`SELECT id FROM conversations WHERE id='former-conv'`,
		`SELECT id FROM projects WHERE id='former-project'`,
		`SELECT id FROM knowledge_bases WHERE id='former-kb'`,
		`SELECT id FROM files WHERE id='former-file'`,
		`SELECT id FROM documents WHERE id='former-doc'`,
		`SELECT id FROM chunks WHERE id='former-chunk'`,
		`SELECT id FROM artifacts WHERE id='former-artifact'`,
	} {
		assertMissing(t, db, query)
	}
	for _, path := range []string{filePath, artifactPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("workspace path %s stat err=%v", path, err)
		}
	}
}

func TestMarkUserDeletingRejectsStaleVerifiedPasswordHash(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "delete-password-cas.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('user','user@x.test','old-hash','user')`)
	exec(t, db, `UPDATE users SET password_hash='new-hash', password_changed_at=123 WHERE id='user'`)
	if changed, err := MarkUserDeleting(ctx, db, "user", "old-hash"); !errors.Is(err, ErrUserCredentialsChanged) || changed {
		t.Fatalf("stale password mark changed=%v err=%v, want false/ErrUserCredentialsChanged", changed, err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id='user'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v, want active", status, err)
	}
	if changed, err := MarkUserDeleting(ctx, db, "user", "new-hash"); err != nil || !changed {
		t.Fatalf("current password mark changed=%v err=%v", changed, err)
	}
}

func TestMarkUserDeletingSerializesWithConcurrentPasswordChange(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "delete-password-race.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for iteration := 0; iteration < 20; iteration++ {
		userID := fmt.Sprintf("race-user-%d", iteration)
		exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES(?,?,?,'user')`,
			userID, fmt.Sprintf("race-%d@x.test", iteration), "old-hash")
		start := make(chan struct{})
		var wg sync.WaitGroup
		var completion atomic.Int32
		var passwordOrder, deletionOrder int32
		var passwordErr, deletionErr error
		var deletionChanged bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			passwordErr = UpdateUserPassword(ctx, db, userID, "new-hash")
			passwordOrder = completion.Add(1)
		}()
		go func() {
			defer wg.Done()
			<-start
			deletionChanged, deletionErr = MarkUserDeleting(ctx, db, userID, "old-hash")
			deletionOrder = completion.Add(1)
		}()
		close(start)
		wg.Wait()
		if passwordErr != nil {
			t.Fatalf("iteration %d password update: %v", iteration, passwordErr)
		}
		if passwordOrder < deletionOrder {
			if deletionChanged || !errors.Is(deletionErr, ErrUserCredentialsChanged) {
				t.Fatalf("iteration %d password completed first, deletion changed=%v err=%v", iteration, deletionChanged, deletionErr)
			}
			var status string
			if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, userID).Scan(&status); err != nil || status != "active" {
				t.Fatalf("iteration %d status=%q err=%v, want active", iteration, status, err)
			}
			continue
		}
		if deletionErr != nil || !deletionChanged {
			t.Fatalf("iteration %d deletion completed first, changed=%v err=%v", iteration, deletionChanged, deletionErr)
		}
	}
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func assertMissing(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	var id string
	err := db.QueryRowContext(context.Background(), q).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing row for %q, got id=%q err=%v", q, id, err)
	}
}

func assertRowPresent(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), q).Scan(&id); err != nil {
		t.Fatalf("expected row for %q: %v", q, err)
	}
}
