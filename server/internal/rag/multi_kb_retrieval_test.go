package rag

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/vector"
)

func TestRouteRejectsIncompatibleKnowledgeBasesBeforeFullTextShortcut(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "multi-kb-compatibility.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
			('kb-a','u1','A','model-a',256),
			('kb-different-model','u1','Different model','model-b',256),
			('kb-different-dimension','u1','Different dimension','model-a',512)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	for _, ids := range [][]string{
		{"kb-a", "kb-different-model"},
		{"kb-a", "kb-different-dimension"},
	} {
		_, _, err := svc.RouteAndRetrieve(ctx, "u1", "", ids, "small query", nil, 8)
		if !errors.Is(err, store.ErrMixedKBEmbeddingModels) {
			t.Fatalf("ids=%v error=%v, want ErrMixedKBEmbeddingModels before full-text shortcut", ids, err)
		}
	}
}

func TestRetrieveFixedTopKInterleavesCompatibleKnowledgeBases(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "multi-kb-fairness.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		store.InvalidateConfig()
	})
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	if err := store.SetSetting(db, "rag_top_k", 2); err != nil {
		t.Fatalf("set top-k: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES
			('kb-a','u1','A','',256),
			('kb-b','u1','B','',256)`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES
			('doc-a1','kb-a','a-one.txt','text/plain',1,'ready'),
			('doc-a2','kb-a','a-two.txt','text/plain',1,'ready'),
			('doc-b','kb-b','b-one.txt','text/plain',1,'ready')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES
			('chunk-a1','doc-a1','kb-a',0,'text','shared shared shared','aivory-local-embed'),
			('chunk-a2','doc-a2','kb-a',0,'text','shared shared','aivory-local-embed'),
			('chunk-b','doc-b','kb-b',0,'text','shared','aivory-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	vec := &iterativeVectorStore{
		enabled: true,
		statuses: map[string]vector.ChunkVectorStatus{
			"chunk-a1": {Exists: true, HasVector: true},
			"chunk-a2": {Exists: true, HasVector: true},
			"chunk-b":  {Exists: true, HasVector: true},
		},
		keywordHitsByQuery: map[string][]vector.Hit{
			"shared": {
				{Score: 3, Payload: vector.Payload{ChunkID: "chunk-a1"}},
				{Score: 2, Payload: vector.Payload{ChunkID: "chunk-a2"}},
				{Score: 1, Payload: vector.Payload{ChunkID: "chunk-b"}},
			},
		},
	}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(vec)
	got, err := svc.Retrieve(ctx, "u1", "", []string{"kb-a", "kb-b"}, "shared", 8)
	if err != nil {
		t.Fatalf("retrieve compatible KBs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want fixed top-k results from both KBs", got)
	}
	seen := map[string]Snippet{}
	for _, snippet := range got {
		seen[snippet.ID] = snippet
		if snippet.Source != "kb" || !strings.HasPrefix(snippet.URL, "kbdoc://") {
			t.Fatalf("KB provenance was not reconstructed from the live row: %+v", snippet)
		}
	}
	if _, ok := seen["chunk-b"]; !ok {
		t.Fatalf("second compatible KB was crowded out by the first: %+v", got)
	}
	if _, ok := seen["chunk-a1"]; !ok {
		t.Fatalf("best candidate from first KB was lost: %+v", got)
	}
	for _, scope := range vec.scopes {
		if scope.ConversationID != "" || !equalStrings(scope.KBIDs, []string{"kb-a", "kb-b"}) {
			t.Fatalf("vector scope=%+v, want both selected KBs in order", scope)
		}
	}
}

func TestIterativeRetrievalMergesCompatibleKnowledgeBasesWithDocumentProvenance(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "multi-kb-retrieval.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Empty model IDs exercise the local legacy embedder without an external
	// embedding service. KB-B is also the implicit project library; RAG receives
	// the project KB and the optional KB as one already-authorized scope. All
	// selected KBs still share the same locked dimension.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim,project_id) VALUES
			('kb-a','u1','Library A','',256,NULL),
			('kb-b','u1','Project Library B','',256,'project-b'),
			('kb-out','u1','Not selected','',256,NULL)`,
		`INSERT INTO projects(id,user_id,name,kb_id) VALUES('project-b','u1','Project B','kb-b')`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES
			('doc-a','kb-a','alpha.txt','text/plain',100,'ready'),
			('doc-b','kb-b','beta.txt','text/plain',100,'ready'),
			('doc-out','kb-out','outside.txt','text/plain',100,'ready')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES
			('chunk-a','doc-a','kb-a',0,'text','shared evidence from alpha','aivory-local-embed'),
			('chunk-b','doc-b','kb-b',0,'text','shared evidence from beta','aivory-local-embed'),
			('chunk-out','doc-out','kb-out',0,'text','shared evidence outside scope','aivory-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}

	statuses := map[string]vector.ChunkVectorStatus{
		"chunk-a": {Exists: true, HasVector: true},
		"chunk-b": {Exists: true, HasVector: true},
	}
	vec := &iterativeVectorStore{
		enabled:  true,
		statuses: statuses,
		keywordHitsByQuery: map[string][]vector.Hit{
			"shared": {
				// Payload provenance is deliberately wrong. Retrieval must accept
				// only live in-scope chunk IDs and rebuild citations from SQL rows.
				{Score: 3, Payload: vector.Payload{ChunkID: "chunk-a", DocumentID: "forged-a", KBID: "kb-out"}},
				{Score: 2, Payload: vector.Payload{ChunkID: "chunk-b", DocumentID: "forged-b", KBID: "kb-out"}},
				{Score: 1, Payload: vector.Payload{ChunkID: "chunk-out", DocumentID: "doc-out", KBID: "kb-out"}},
			},
		},
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(vec)
	result, err := svc.RouteAndRetrieveIterative(
		ctx,
		"u1",
		"",
		[]string{"kb-b", "kb-a", "kb-b"},
		"shared",
		nil,
		8,
		IterativeRetrievalOptions{ForceRetrieve: true},
	)
	if err != nil {
		t.Fatalf("retrieve two KBs: %v", err)
	}
	if result.Status != IterativeRetrievalPartial {
		t.Fatalf("status=%q, want partial when no evidence judge is configured", result.Status)
	}
	if len(result.Snippets) != 2 {
		t.Fatalf("snippets=%+v, want one hit from each selected KB", result.Snippets)
	}

	byID := make(map[string]Snippet, len(result.Snippets))
	for index, snippet := range result.Snippets {
		if snippet.Index != index+1 {
			t.Fatalf("non-contiguous citation indexes: %+v", result.Snippets)
		}
		byID[snippet.ID] = snippet
	}
	for _, want := range []struct {
		chunkID string
		title   string
		url     string
	}{
		{chunkID: "chunk-a", title: "alpha.txt", url: "kbdoc://doc-a"},
		{chunkID: "chunk-b", title: "beta.txt", url: "kbdoc://doc-b"},
	} {
		got, ok := byID[want.chunkID]
		if !ok || got.Title != want.title || got.URL != want.url || got.Source != "kb" {
			t.Fatalf("citation for %s=%+v, want title=%q url=%q source=kb", want.chunkID, got, want.title, want.url)
		}
	}
	if _, leaked := byID["chunk-out"]; leaked {
		t.Fatalf("unselected knowledge-base hit escaped the fixed scope: %+v", result.Snippets)
	}
	for _, scope := range vec.scopes {
		if scope.ConversationID != "" || !equalStrings(scope.KBIDs, []string{"kb-b", "kb-a"}) {
			t.Fatalf("vector scope=%+v, want deduplicated selected KBs only", scope)
		}
	}
	if len(vec.scopes) == 0 {
		t.Fatal("vector backend was not queried")
	}

	if len(result.Decision.Queries) != 1 || result.Decision.Queries[0] != "shared" {
		t.Fatalf("initial query changed unexpectedly: %v", result.Decision.Queries)
	}
}
