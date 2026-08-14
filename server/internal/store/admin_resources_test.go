package store

import (
	"context"
	"testing"
)

func TestAdminKnowledgeBaseInventoryFiltersStatsAndExcludesProjectLibraries(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	exec(t, db, `UPDATE users SET name='Alice Creator', settings='{"avatar_url":"/avatars/alice.png"}' WHERE id='creator'`)
	exec(t, db, `UPDATE users SET name='Workspace Owner' WHERE id='owner'`)
	exec(t, db, `UPDATE knowledge_bases SET name='Personal Research', description='Primary library', created_at=1 WHERE id='personal-kb'`)
	exec(t, db, `UPDATE documents SET size_bytes=10, chunk_count=2, ingest_updated_at=12, created_at=5 WHERE id='personal-document'`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,error,chunk_count,storage_path,ingest_updated_at,created_at) VALUES
		('personal-failed','personal-kb','failed.txt','text/plain',20,'failed','parse failed',0,'',11,6),
		('personal-pending','personal-kb','pending.txt','text/plain',30,'pending','',0,'',10,7)`)
	exec(t, db, `INSERT INTO knowledge_base_shares(kb_id,user_id,role) VALUES('personal-kb','member','read')`)

	// Both the durable project_id marker and the legacy projects.kb_id reverse
	// link must keep internal project libraries out of the standalone inventory.
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id,workspace_id) VALUES
		('tagged-project-kb','creator','Tagged project library','emb-a',3,'tagged-project','')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id) VALUES
		('tagged-project','creator','Tagged project','tagged-project-kb','')`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES
		('legacy-project-kb','creator','Legacy project library','emb-a',3,'')`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,kb_id,workspace_id) VALUES
		('legacy-project','creator','Legacy project','legacy-project-kb','')`)

	filter := AdminResourceFilter{Search: "RESEARCH", User: "CREATOR@EXAMPLE"}
	total, err := CountAdminKnowledgeBases(ctx, db, filter)
	if err != nil {
		t.Fatalf("count knowledge bases: %v", err)
	}
	items, err := ListAdminKnowledgeBases(ctx, db, filter, 10, 0)
	if err != nil {
		t.Fatalf("list knowledge bases: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "personal-kb" {
		t.Fatalf("filtered knowledge bases total=%d items=%+v", total, items)
	}
	item := items[0]
	if item.CreatorName != "Alice Creator" || item.CreatorAvatarURL != "/avatars/alice.png" {
		t.Fatalf("creator identity=%+v", item)
	}
	if item.DocumentCount != 3 || item.ReadyDocumentCount != 1 || item.FailedDocumentCount != 1 || item.ProcessingDocumentCount != 1 {
		t.Fatalf("document status stats=%+v", item)
	}
	if item.TotalSizeBytes != 60 || item.ChunkCount != 2 || item.ShareCount != 1 || item.LastActivityAt != 12 {
		t.Fatalf("knowledge-base aggregates=%+v", item)
	}

	all, err := ListAdminKnowledgeBases(ctx, db, AdminResourceFilter{}, 50, 0)
	if err != nil {
		t.Fatalf("list all knowledge bases: %v", err)
	}
	seen := map[string]AdminKnowledgeBaseResource{}
	for _, row := range all {
		seen[row.ID] = row
	}
	if _, ok := seen["tagged-project-kb"]; ok {
		t.Fatal("tagged project library appeared in standalone inventory")
	}
	if _, ok := seen["legacy-project-kb"]; ok {
		t.Fatal("legacy project library appeared in standalone inventory")
	}
	workspace := seen["workspace-kb"]
	if workspace.WorkspaceID != "ws1" || workspace.WorkspaceName != "Shared" || workspace.WorkspaceOwnerName != "Workspace Owner" {
		t.Fatalf("workspace metadata=%+v", workspace)
	}

	detail, err := GetAdminKnowledgeBase(ctx, db, "personal-kb")
	if err != nil {
		t.Fatalf("get knowledge base: %v", err)
	}
	if len(detail.Shares) != 1 || detail.Shares[0].UserID != "member" || detail.Shares[0].Role != "read" {
		t.Fatalf("knowledge-base shares=%+v", detail.Shares)
	}
}

func TestAdminProjectInventoryDetailAndConversationPagination(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	exec(t, db, `UPDATE users SET name='Alice Creator', settings='{"avatar_url":"/avatars/alice.png"}' WHERE id='creator'`)
	exec(t, db, `UPDATE users SET name='Member User' WHERE id='member'`)
	exec(t, db, `UPDATE users SET name='Workspace Owner' WHERE id='owner'`)
	exec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,description,embedding_model_id,embedding_dim,project_id,workspace_id,created_at) VALUES
		('omega-kb','creator','Omega Library','Project documents','emb-a',3,'omega-project','ws1',90)`)
	exec(t, db, `INSERT INTO projects(id,user_id,name,description,instructions,accent,emoji,pinned,kb_id,auto_add_uploads,workspace_id,created_at,updated_at) VALUES
		('omega-project','creator','Omega Project','Project description','Keep the answer concise','green','O',1,'omega-kb',1,'ws1',100,110)`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,error,chunk_count,storage_path,ingest_updated_at,created_at) VALUES
		('omega-ready','omega-kb','ready.txt','text/plain',10,'ready','',2,'',200,120),
		('omega-failed','omega-kb','failed.txt','text/plain',20,'failed','embedding failed',0,'',190,130)`)
	exec(t, db, `INSERT INTO conversations(id,user_id,project_id,title,provider,model_id,workspace_id,is_public,archived,updated_at,created_at) VALUES
		('omega-new','creator','omega-project','Newest conversation','openai','emb-a','ws1',0,0,300,210),
		('omega-old','member','omega-project','Archived conversation','openai','emb-a','ws1',1,1,250,205)`)
	exec(t, db, `INSERT INTO conversations(id,user_id,project_id,title,workspace_id,inline_source_conv,updated_at,created_at) VALUES
		('omega-inline','creator','omega-project','Inline thread','ws1','omega-new',400,220)`)

	filter := AdminResourceFilter{Search: "OMEGA", User: "creator@example"}
	total, err := CountAdminProjects(ctx, db, filter)
	if err != nil {
		t.Fatalf("count projects: %v", err)
	}
	items, err := ListAdminProjects(ctx, db, filter, 10, 0)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "omega-project" {
		t.Fatalf("filtered projects total=%d items=%+v", total, items)
	}
	item := items[0]
	if item.WorkspaceName != "Shared" || item.WorkspaceOwnerName != "Workspace Owner" || item.KBID != "omega-kb" {
		t.Fatalf("project linkage=%+v", item)
	}
	if item.DocumentCount != 2 || item.ReadyDocumentCount != 1 || item.FailedDocumentCount != 1 || item.TotalSizeBytes != 30 || item.ChunkCount != 2 {
		t.Fatalf("project document stats=%+v", item)
	}
	if item.ConversationCount != 2 || item.ActiveConversationCount != 1 || item.ArchivedConversationCount != 1 || item.LastActivityAt != 300 {
		t.Fatalf("project conversation stats=%+v", item)
	}

	detail, err := GetAdminProject(ctx, db, "omega-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if detail.Instructions != "Keep the answer concise" || !detail.Pinned || !detail.AutoAddUploads {
		t.Fatalf("project detail=%+v", detail)
	}

	conversationTotal, err := CountAdminProjectConversations(ctx, db, "omega-project")
	if err != nil {
		t.Fatalf("count project conversations: %v", err)
	}
	first, err := ListAdminProjectConversations(ctx, db, "omega-project", 1, 0)
	if err != nil {
		t.Fatalf("first conversation page: %v", err)
	}
	second, err := ListAdminProjectConversations(ctx, db, "omega-project", 1, 1)
	if err != nil {
		t.Fatalf("second conversation page: %v", err)
	}
	if conversationTotal != 2 || len(first) != 1 || len(second) != 1 || first[0].ID != "omega-new" || second[0].ID != "omega-old" {
		t.Fatalf("conversation pagination total=%d first=%+v second=%+v", conversationTotal, first, second)
	}
	if first[0].CreatorName != "Alice Creator" || second[0].CreatorName != "Member User" || !second[0].Archived {
		t.Fatalf("conversation identities first=%+v second=%+v", first[0], second[0])
	}
}
