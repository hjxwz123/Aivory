package rag

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestRetrieveExcludesDraftBackedConversationAndKBDocuments(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "draft-rag.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('member','member@example.test','h','user')`,
		`INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','Shared','owner','invite')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`,
		`INSERT INTO conversations(id,user_id,title,workspace_id,is_public) VALUES('c1','owner','Shared','ws1',1)`,
		`INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES('emb1','ch1','embedding','emb','Embedding',3)`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,workspace_id) VALUES('kb1','owner','Project KB','emb1',3,'ws1')`,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,draft) VALUES('f1','owner','c1','private.txt','text/plain',10,'/tmp/draft-rag','text',1)`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('doc-conv','c1','private.txt','text/plain',10,'ready','/tmp/draft-rag')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('chunk-conv','doc-conv','c1',0,'text','conversation draft secret','')`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES('doc-kb','kb1','private.txt','text/plain',10,'ready','/tmp/draft-rag')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES('chunk-kb','doc-kb','kb1',0,'text','project KB draft secret','')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.Retrieve(ctx, "member", "c1", []string{"kb1"}, "secret", 8)
	if err != nil {
		t.Fatalf("retrieve draft scope: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("member retrieved draft-backed content: %+v", got)
	}
	if got, _, err := svc.RouteAndRetrieve(ctx, "member", "c1", []string{"kb1"}, "secret", nil, 8); err != nil || len(got) != 0 {
		t.Fatalf("routed retrieve draft scope: snippets=%+v err=%v; want empty", got, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE files SET draft=0 WHERE id='f1'`); err != nil {
		t.Fatalf("commit file: %v", err)
	}
	got, err = svc.Retrieve(ctx, "member", "c1", []string{"kb1"}, "secret", 8)
	if err != nil {
		t.Fatalf("retrieve committed scope: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("committed conversation/KB twins not shared: %+v", got)
	}
	seen := map[string]bool{}
	for _, snippet := range got {
		seen[snippet.Snippet] = true
	}
	if !seen["conversation draft secret"] || !seen["project KB draft secret"] {
		t.Fatalf("committed snippets = %+v; want both conversation and KB content", got)
	}
}
