package rag

import (
	"context"
	"database/sql"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aurelia/server/internal/store"
	"aurelia/server/internal/vector"
)

func TestRetrieveWithoutVectorStoreInjectsFullContext(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want full child context (2): %+v", len(got), got)
	}
	if got[0].Snippet != "first full chunk" || got[1].Snippet != "second full chunk" {
		t.Fatalf("unexpected full-context snippets: %+v", got)
	}
}

func TestRetrieveFullContextFallbackDoesNotTruncate(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	long := strings.Repeat("长上下文不应该被截断-", 700)
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content=? WHERE id='ch1'`, long); err != nil {
		t.Fatalf("update long chunk: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("got no snippets, want full long chunk")
	}
	if got[0].Snippet != long {
		t.Fatalf("full-context fallback truncated or changed content: len got=%d want=%d", len([]rune(got[0].Snippet)), len([]rune(long)))
	}
}

func TestRetrieveWithEmptyVectorStoreInjectsFullContext(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want full child context after empty vector search: %+v", len(got), got)
	}
	if got[0].Snippet != "first full chunk" || got[1].Snippet != "second full chunk" {
		t.Fatalf("unexpected full-context snippets: %+v", got)
	}
}

func TestRetrieveWithStaleVectorHitsInjectsFullContext(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{hits: []vector.Hit{{
		Score:   0.99,
		Payload: vector.Payload{ChunkID: "old-chunk", DocumentID: "old-doc", Content: "stale qdrant text"},
	}}, existingIDs: map[string]bool{"ch1": true, "ch2": true}})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want full child context after stale vector hit: %+v", len(got), got)
	}
	if got[0].Snippet != "first full chunk" || got[1].Snippet != "second full chunk" {
		t.Fatalf("unexpected full-context snippets: %+v", got)
	}
}

func TestRetrieveWithLiveVectorHitUsesCurrentDBChunk(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{hits: []vector.Hit{{
		Score:   0.99,
		Payload: vector.Payload{ChunkID: "ch1", DocumentID: "stale-doc", Content: "stale qdrant text"},
	}}, existingIDs: map[string]bool{"ch1": true, "ch2": true}})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d snippets, want one live vector hit: %+v", len(got), got)
	}
	if got[0].ID != "ch1" || got[0].Snippet != "first full chunk" || got[0].URL != "doc://d1" {
		t.Fatalf("retrieval should render the current DB chunk, not stale Qdrant payload: %+v", got)
	}
}

func TestRetrieveWithPartialVectorIndexInjectsFullContext(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{hits: []vector.Hit{{
		Score:   0.99,
		Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1", Content: "stale qdrant text"},
	}}, existingIDs: map[string]bool{"ch1": true}})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want full child context when qdrant misses a DB chunk: %+v", len(got), got)
	}
	if got[0].Snippet != "first full chunk" || got[1].Snippet != "second full chunk" {
		t.Fatalf("unexpected full-context snippets: %+v", got)
	}
}

func TestRetrieveWithEmptyQdrantVectorInjectsFullContext(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{{
			Score:   0.99,
			Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1", Content: "qdrant payload without vector"},
		}},
		statuses: map[string]vector.ChunkVectorStatus{
			"ch1": {Exists: true, HasVector: true},
			"ch2": {Exists: true, HasVector: false},
		},
	})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want full child context when qdrant has an empty vector: %+v", len(got), got)
	}
	if got[0].Snippet != "first full chunk" || got[1].Snippet != "second full chunk" {
		t.Fatalf("unexpected full-context snippets: %+v", got)
	}
}

func TestRetrieveWithParentHitIncludesMatchedChild(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	child := "needle child content that must be visible"
	parent := strings.Repeat("parent opening text ", 220) + child + strings.Repeat(" parent tail", 80)
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content=? WHERE id='p1'`, parent); err != nil {
		t.Fatalf("update parent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET parent_id=?, content=? WHERE id='ch1'`, "p1", child); err != nil {
		t.Fatalf("update child: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{hits: []vector.Hit{{
		Score:   0.99,
		Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1", Content: "stale qdrant text"},
	}}, existingIDs: map[string]bool{"ch1": true, "ch2": true}})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "anything", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d snippets, want one live vector hit: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Snippet, child) {
		t.Fatalf("snippet lost matched child: %q", got[0].Snippet)
	}
	if got[0].Snippet == parent {
		t.Fatalf("snippet should be a focused parent window, not the full stored parent")
	}
}

func TestRouteAndRetrieveUsesRouterForConversationUploads(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{Strategy: "retrieve", Queries: []string{"anything"}}}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{{
			Score:   0.99,
			Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"},
		}},
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
	})

	got, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "anything", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if router.calls != 1 {
		t.Fatalf("conversation uploads in auto mode should call task router, got %d calls", router.calls)
	}
	if decision.Strategy != "retrieve" {
		t.Fatalf("decision=%q, want router retrieve decision", decision.Strategy)
	}
	if len(got) != 1 || got[0].ID != "ch1" {
		t.Fatalf("got %+v, want direct vector retrieval result ch1", got)
	}
}

func TestRouteAndRetrievePrependsPinnedConversationDocs(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET embedding_model='' WHERE id='ch2'`); err != nil {
		t.Fatalf("make ch2 pinned: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{Strategy: "retrieve", Queries: []string{"anything"}}}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{{
			Score:   0.99,
			Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"},
		}},
		existingIDs: map[string]bool{"ch1": true},
	})

	got, _, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "anything", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if router.calls != 1 {
		t.Fatalf("conversation uploads in auto mode should call task router, got %d calls", router.calls)
	}
	if len(got) != 2 {
		t.Fatalf("got %d snippets, want pinned full text + retrieved hit: %+v", len(got), got)
	}
	if got[0].ID != "ch2" || got[0].Snippet != "second full chunk" {
		t.Fatalf("first snippet should be pinned full-text chunk ch2, got %+v", got[0])
	}
	if got[1].ID != "ch1" || got[1].Snippet != "first full chunk" {
		t.Fatalf("second snippet should be retrieved hit ch1, got %+v", got[1])
	}
}

func TestRouteAndRetrieveKeepsRouterForKBOnlyScope(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedKBDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{Strategy: "none"}}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{existingIDs: map[string]bool{"kbch1": true}})

	got, decision, err := svc.RouteAndRetrieve(ctx, "u1", "", []string{"kb1"}, "anything", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if router.calls != 1 {
		t.Fatalf("KB-only auto mode should still call task router, got %d calls", router.calls)
	}
	if decision.Strategy != "none" {
		t.Fatalf("decision=%q, want router decision none", decision.Strategy)
	}
	if len(got) != 0 {
		t.Fatalf("got snippets despite router none: %+v", got)
	}
}

func seedEmbeddedConversationDoc(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d1','c1','f.txt','text/plain',10,'ready')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('p1','d1','c1',0,'parent','parent text','aurelia-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch1','d1','c1',1,'text','first full chunk','aurelia-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch2','d1','c1',2,'text','second full chunk','aurelia-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return db
}

func seedEmbeddedKBDoc(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`,
		`INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('ch1','Emb','openai','chat','https://api.example','sk',1)`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('emb1','ch1','embedding','text-embedding-3-small','Emb',1,256)`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','emb1',256)`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES('kbd1','kb1','kb.txt','text/plain',10,'ready')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES('kbch1','kbd1','kb1',0,'text','knowledge base full chunk','emb:emb1')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return db
}

type recordingRouter struct {
	calls    int
	decision RouteDecision
	err      error
}

func (r *recordingRouter) RunJSON(_ context.Context, _ string, _ string, out any, _ RouterOpts) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if d, ok := out.(*RouteDecision); ok {
		*d = r.decision
	}
	return nil
}

type testVectorStore struct {
	hits        []vector.Hit
	keywordHits []vector.Hit
	existingIDs map[string]bool
	statuses    map[string]vector.ChunkVectorStatus
}

func (testVectorStore) Enabled() bool { return true }
func (testVectorStore) Upsert(context.Context, int, []vector.Point) error {
	return nil
}
func (v testVectorStore) Search(context.Context, int, []float32, vector.Scope, int) ([]vector.Hit, error) {
	return v.hits, nil
}
func (v testVectorStore) SearchKeyword(context.Context, int, string, vector.Scope, int) ([]vector.Hit, error) {
	return v.keywordHits, nil
}
func (v testVectorStore) ExistingChunkIDs(context.Context, int, vector.Scope) (map[string]bool, error) {
	out := map[string]bool{}
	for id, ok := range v.existingIDs {
		out[id] = ok
	}
	return out, nil
}
func (v testVectorStore) VectorChunkStatuses(context.Context, int, vector.Scope) (map[string]vector.ChunkVectorStatus, error) {
	if v.statuses != nil {
		out := map[string]vector.ChunkVectorStatus{}
		for id, status := range v.statuses {
			out[id] = status
		}
		return out, nil
	}
	out := map[string]vector.ChunkVectorStatus{}
	for id, ok := range v.existingIDs {
		out[id] = vector.ChunkVectorStatus{Exists: ok, HasVector: ok}
	}
	return out, nil
}
func (testVectorStore) DeleteByDocument(context.Context, string) error {
	return nil
}
func (testVectorStore) DeleteByKB(context.Context, string) error {
	return nil
}
func (testVectorStore) DeleteByConversation(context.Context, string) error {
	return nil
}
