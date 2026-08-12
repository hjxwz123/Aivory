package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestResolveTurnKnowledgeBaseSelectionDistinguishesOmittedAndEmpty(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-kb-snapshot.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('u1','u1@example.test','h','user'),
		('u2','u2@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES
		('emb-a','ch1','embedding','emb-a','Embedding A',3),
		('emb-other','ch1','embedding','emb-other','Embedding Other',99)`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,kb_ids) VALUES
		('c1','u1','Conversation','["kb-a"]')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('kb-a','u1','A','emb-a',3),
		('kb-b','u1','B','emb-a',3),
		('kb-other','u2','Other','emb-other',99)`)

	conv, err := store.GetConversation(ctx, db, "c1", "u1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}

	ids, configured, err := resolveTurnKnowledgeBaseSelection(ctx, db, "u1", conv, nil)
	if err != nil || configured || ids != nil {
		t.Fatalf("omitted selection ids=%v configured=%v err=%v", ids, configured, err)
	}

	ids, configured, err = resolveTurnKnowledgeBaseSelection(ctx, db, "u1", conv, json.RawMessage(`[]`))
	if err != nil || !configured || len(ids) != 0 {
		t.Fatalf("empty selection ids=%v configured=%v err=%v", ids, configured, err)
	}

	ids, configured, err = resolveTurnKnowledgeBaseSelection(
		ctx,
		db,
		"u1",
		conv,
		json.RawMessage(`["kb-b","kb-a","kb-b"]`),
	)
	if err != nil || !configured || len(ids) != 2 || ids[0] != "kb-b" || ids[1] != "kb-a" {
		t.Fatalf("ordered selection ids=%v configured=%v err=%v", ids, configured, err)
	}

	if _, configured, err := resolveTurnKnowledgeBaseSelection(
		ctx,
		db,
		"u1",
		conv,
		json.RawMessage(`["kb-b","kb-other"]`),
	); !configured || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unauthorized selection configured=%v err=%v, want not found", configured, err)
	}

	if _, _, err := resolveTurnKnowledgeBaseSelection(ctx, db, "u1", conv, json.RawMessage(`null`)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("null selection err=%v, want invalid input", err)
	}
}

func TestResolveTurnKnowledgeBaseSelectionIncludesProjectLibraryInCompatibility(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-project-kb-snapshot.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES
		('u1','u1@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','Embedding','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label,dim) VALUES
		('emb-a','ch1','embedding','emb-a','Embedding A',3),
		('emb-b','ch1','embedding','emb-b','Embedding B',3)`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
		('kb-explicit','u1','Explicit','emb-a',3),
		('kb-project','u1','Project','emb-b',3)`)
	mustExec(t, db, `INSERT INTO projects(id,user_id,name,kb_id) VALUES
		('project-1','u1','Project','kb-project')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title,project_id) VALUES
		('c1','u1','Conversation','project-1')`)

	conv, err := store.GetConversation(ctx, db, "c1", "u1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	_, configured, err := resolveTurnKnowledgeBaseSelection(
		ctx,
		db,
		"u1",
		conv,
		json.RawMessage(`["kb-explicit"]`),
	)
	if !configured || !errors.Is(err, store.ErrMixedKBEmbeddingModels) {
		t.Fatalf("configured=%v err=%v, want mixed-model conflict", configured, err)
	}

	ids, configured, err := resolveTurnKnowledgeBaseSelection(ctx, db, "u1", conv, json.RawMessage(`[]`))
	if err != nil || !configured || len(ids) != 0 {
		t.Fatalf("project-only selection ids=%v configured=%v err=%v", ids, configured, err)
	}

	if _, configured, err := resolveTurnKnowledgeBaseSelection(
		ctx,
		db,
		"u1",
		conv,
		json.RawMessage(`["kb-project"]`),
	); !configured || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("explicit project library configured=%v err=%v, want not found", configured, err)
	}
}

func TestResolveTurnKnowledgeBaseSelectionSurfacesDatabaseErrors(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "message-kb-database-error.db"))
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	_, configured, err := resolveTurnKnowledgeBaseSelection(
		context.Background(),
		db,
		"u1",
		&store.Conversation{},
		json.RawMessage(`["kb-a"]`),
	)
	if !configured || err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("configured=%v err=%v, want database error", configured, err)
	}
}
