package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aivory/server/internal/store"
	"aivory/server/internal/vector"
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
	for _, snippet := range got {
		if snippet.Source != "document" {
			t.Fatalf("conversation citation source=%q, want document: %+v", snippet.Source, snippet)
		}
	}
}

func TestRetrieveDocumentsScopesCurrentTurnToAttachedDocument(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('doc2','c1','B.txt','text/plain',100,'ready')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch-b','doc2','c1',0,'text','content from document B','')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed B: %v", err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.RetrieveDocuments(ctx, "u1", "c1", nil, []string{"doc2"}, "summarize this article", 8)
	if err != nil {
		t.Fatalf("retrieve current document: %v", err)
	}
	if len(got) != 1 || got[0].Snippet != "content from document B" || got[0].URL != "doc://doc2" {
		t.Fatalf("current-document retrieval leaked another upload: %+v", got)
	}
}

func TestRetrieveDocumentsEmptyScopeDoesNotFallBackToConversation(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.RetrieveDocuments(ctx, "u1", "c1", nil, nil, "anything", 8)
	if err != nil {
		t.Fatalf("retrieve empty document scope: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty allowed scope leaked conversation documents: %+v", got)
	}
}

func TestRetrieveDocumentsEmptyConversationScopeKeepsKnowledgeBase(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO channels(id,name,type,api_format,base_url,api_key,enabled) VALUES('kb-channel','Emb','openai','chat','https://api.example','sk',1)`,
		`INSERT INTO models(id,channel_id,kind,request_id,label,enabled,dim) VALUES('kb-embedding','kb-channel','embedding','embedding','Embedding',1,256)`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Allowed KB','kb-embedding',256)`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES('kb-doc','kb1','kb.txt','text/plain',100,'ready')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content,embedding_model) VALUES('kb-chunk','kb-doc','kb1',0,'text','knowledge base evidence','')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed KB: %v", err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.RetrieveDocuments(ctx, "u1", "c1", []string{"kb1"}, nil, "anything", 8)
	if err != nil {
		t.Fatalf("retrieve KB with empty conversation scope: %v", err)
	}
	if len(got) != 1 || got[0].Snippet != "knowledge base evidence" || got[0].Source != "kb" {
		t.Fatalf("KB scope was lost or conversation documents leaked: %+v", got)
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
	if got[0].ID != "ch1" || got[0].Snippet != "first full chunk" ||
		got[0].URL != "doc://d1" || got[0].Source != "document" {
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

func TestRetrieveUsesDBLexicalFallbackWhenQdrantKeywordLegMisses(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙4：前一段内容' WHERE id='ch1'`); err != nil {
		t.Fatalf("update first reference: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙5：后一段完整内容' WHERE id='ch2'`); err != nil {
		t.Fatalf("update second reference: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	// Simulate an existing/healthy vector collection whose text index returns
	// no matches, as with an older collection without the multilingual payload
	// index. The relational chunk text must still recall the exact reference.
	svc.SetVectorStore(testVectorStore{
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
	})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "甲乙5", 8)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) == 0 || !strings.Contains(got[0].Snippet, "甲乙5") {
		t.Fatalf("lexical fallback missed the exact reference: %+v", got)
	}
}

func TestRetrieveKeepsLexicalFallbackWithDynamicTopK(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	// GetSetting uses a process-local cache keyed by setting name. Clear it
	// after this test so the per-test database's dynamic flag cannot leak into
	// the following route-merge tests.
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "rag_dynamic_topk", true); err != nil {
		t.Fatalf("enable dynamic top k: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙4：前一段内容' WHERE id='ch1'`); err != nil {
		t.Fatalf("update first reference: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙5：后一段完整内容' WHERE id='ch2'`); err != nil {
		t.Fatalf("update second reference: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	// No dense or Qdrant keyword hit: the DB lexical fallback is the only
	// evidence. Dynamic mode must retain it despite its synthetic sim=0.
	svc.SetVectorStore(testVectorStore{
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
	})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "甲乙5", 8)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) == 0 || got[0].ID != "ch2" || !strings.Contains(got[0].Snippet, "甲乙5") {
		t.Fatalf("dynamic top-k dropped the exact lexical hit: %+v", got)
	}
}

func TestRetrieveKeepsLexicalEvidenceOnAWeakDenseHit(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "rag_dynamic_topk", true); err != nil {
		t.Fatalf("enable dynamic top k: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙5：目标内容' WHERE id='ch2'`); err != nil {
		t.Fatalf("update target chunk: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{
		hits:        []vector.Hit{{Score: 0.01, Payload: vector.Payload{ChunkID: "ch2", DocumentID: "d1"}}},
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
	})
	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "甲乙5", 8)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) == 0 || got[0].ID != "ch2" {
		t.Fatalf("weak dense hit with lexical evidence was filtered: %+v", got)
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

func TestRetrieveKeepsSameParentHitsAndIncludesAdjacentChildren(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()

	for _, q := range []string{
		`UPDATE chunks SET parent_id='p1', content='far matching segment' WHERE id='ch1'`,
		`UPDATE chunks SET parent_id='p1', content='left boundary context' WHERE id='ch2'`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,parent_id,chunk_type,content,embedding_model) VALUES('ch3','d1','c1',3,'p1','text','focused target segment','aivory-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,parent_id,chunk_type,content,embedding_model) VALUES('ch4','d1','c1',4,'p1','text','right boundary context','aivory-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("prepare sibling chunks: %v", err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{
			{Score: 0.99, Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"}},
			{Score: 0.98, Payload: vector.Payload{ChunkID: "ch3", DocumentID: "d1"}},
		},
		existingIDs: map[string]bool{"ch1": true, "ch2": true, "ch3": true, "ch4": true},
	})

	got, err := svc.Retrieve(ctx, "u1", "c1", nil, "focused target", 8)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("same-parent hits collapsed: got %+v", got)
	}

	joined := got[0].Snippet + "\n" + got[1].Snippet
	for _, want := range []string{
		"far matching segment",
		"left boundary context",
		"focused target segment",
		"right boundary context",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("retrieval window omitted %q: %+v", want, got)
		}
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

func TestRouteAndRetrieveConversationRouterQueriesRemainUnbounded(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	longQuery := "  " + strings.Repeat("长", iterativeMaxQueryRunes+25) + "  "
	routerQueries := []string{longQuery, "second", "third", "fourth"}
	router := &recordingRouter{decision: RouteDecision{Strategy: "retrieve", Queries: routerQueries}}
	queries := []string{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
		queryLog:    &queries,
	})

	_, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, longQuery, nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if !equalStrings(decision.Queries, routerQueries) {
		t.Fatalf("conversation router queries=%q, want unchanged %q", decision.Queries, routerQueries)
	}
	if !equalStrings(queries, routerQueries) {
		t.Fatalf("executed conversation queries=%q, want all unchanged %q", queries, routerQueries)
	}
}

func TestRouteAndRetrieveConversationFullDocSummarisesWholeOverBudgetDocument(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{Strategy: "full_doc"}, summary: "summary covering the complete document"}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)

	got, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "summarize everything", nil, 8)
	if err != nil {
		t.Fatalf("route full doc: %v", err)
	}
	if router.calls != 2 {
		t.Fatalf("conversation full_doc made %d task calls, want router + map-reduce", router.calls)
	}
	if decision.Strategy != "full_doc" || len(got) != 1 {
		t.Fatalf("conversation full_doc decision=%+v snippets=%+v", decision, got)
	}
	if got[0].Snippet != router.summary || got[0].Title != "f.txt (摘要)" {
		t.Fatalf("conversation full_doc did not use whole-document summary: %+v", got)
	}
	for _, snippet := range got {
		if snippet.Source != "document" {
			t.Fatalf("conversation full_doc source=%q, want document", snippet.Source)
		}
	}
}

func TestRetrieveDocumentsBoundsSingleLargeCurrentDocumentWithoutVectorStore(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	t.Cleanup(store.InvalidateConfig)
	if err := store.SetSetting(db, "rag_full_text_threshold", 4); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	got, err := svc.RetrieveDocuments(ctx, "u1", "c1", nil, []string{"d1"}, "second", 8)
	if err != nil {
		t.Fatalf("retrieve current large document: %v", err)
	}
	total := 0
	for _, snippet := range got {
		total += estimateTokens(snippet.Snippet)
		if snippet.URL != "" && snippet.URL != "doc://d1" {
			t.Fatalf("retrieval escaped current document: %+v", got)
		}
	}
	if total > 4 {
		t.Fatalf("single large document fallback injected %d tokens, want <= 4: %+v", total, got)
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

func TestRouteAndRetrieveHonorsRouterNoneWithoutLexicalOverride(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{Strategy: "none"}}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		hits: []vector.Hit{{
			Score:   0.99,
			Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"},
		}},
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
	})

	got, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "first full chunk", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if decision.Strategy != "none" || len(got) != 0 {
		t.Fatalf("router none was overridden = decision %q snippets %+v", decision.Strategy, got)
	}
}

func TestRouteAndRetrieveRunsAllRewrittenQueriesBeforeTopKCap(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1); err != nil {
		t.Fatalf("set threshold: %v", err)
	}
	if err := store.SetSetting(db, "rag_top_k", 2); err != nil {
		t.Fatalf("set top k: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch3','d1','c1',3,'text','third exact reference','aivory-local-embed')`); err != nil {
		t.Fatalf("insert third chunk: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{
		Strategy: "retrieve",
		Queries:  []string{"full chunk", "exact reference query"},
	}}
	queries := []string{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		existingIDs: map[string]bool{"ch1": true, "ch2": true, "ch3": true},
		keywordHitsByQuery: map[string][]vector.Hit{
			"full chunk": {
				{Score: 2, Payload: vector.Payload{ChunkID: "ch1", DocumentID: "d1"}},
				{Score: 1, Payload: vector.Payload{ChunkID: "ch2", DocumentID: "d1"}},
			},
			"exact reference query": {
				{Score: 2, Payload: vector.Payload{ChunkID: "ch3", DocumentID: "d1"}},
			},
		},
		queryLog: &queries,
	})

	got, _, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "unseen source identifier", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want two capped results", got)
	}
	if got[0].ID == "ch3" || got[1].ID != "ch3" {
		t.Fatalf("round-robin merge should retain the later exact-query hit: %+v", got)
	}
	if len(queries) != 2 || queries[0] != "full chunk" || queries[1] != "exact reference query" {
		t.Fatalf("retrieval queries = %v, want all rewritten queries in order", queries)
	}
}

func TestRouteAndRetrievePrioritizesExactUserQueryForTopKOne(t *testing.T) {
	ctx := context.Background()
	db := seedEmbeddedConversationDoc(t, ctx)
	defer db.Close()
	for _, setting := range []struct {
		key   string
		value any
	}{
		{"rag_full_text_threshold", 1},
		{"rag_top_k", 1},
	} {
		if err := store.SetSetting(db, setting.key, setting.value); err != nil {
			t.Fatalf("set %s: %v", setting.key, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙4：前一段内容' WHERE id='ch1'`); err != nil {
		t.Fatalf("update first reference: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chunks SET content='甲乙5：精确命中的内容' WHERE id='ch2'`); err != nil {
		t.Fatalf("update second reference: %v", err)
	}

	router := &recordingRouter{decision: RouteDecision{
		Strategy: "retrieve",
		Queries:  []string{"broad rewritten query", "甲乙5"},
	}}
	queries := []string{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetTaskLLM(router)
	svc.SetVectorStore(testVectorStore{
		existingIDs: map[string]bool{"ch1": true, "ch2": true},
		keywordHitsByQuery: map[string][]vector.Hit{
			"甲乙5": {{Score: 2, Payload: vector.Payload{ChunkID: "ch2", DocumentID: "d1"}}},
		},
		queryLog: &queries,
	})

	got, _, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "甲乙5", nil, 8)
	if err != nil {
		t.Fatalf("route retrieve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ch2" {
		t.Fatalf("topK=1 lost exact user query: %+v", got)
	}
	if len(queries) == 0 || queries[0] != "甲乙5" {
		t.Fatalf("exact user query was not prioritized: %v", queries)
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
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('p1','d1','c1',0,'parent','parent text','aivory-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch1','d1','c1',1,'text','first full chunk','aivory-local-embed')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('ch2','d1','c1',2,'text','second full chunk','aivory-local-embed')`,
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
	summary  string
	err      error
}

func (r *recordingRouter) RunJSON(_ context.Context, kind string, _ string, out any, _ RouterOpts) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if kind == "task.rag_map_reduce" {
		return json.Unmarshal([]byte(`{"summary":`+strconv.Quote(r.summary)+`}`), out)
	}
	if d, ok := out.(*RouteDecision); ok {
		*d = r.decision
	}
	return nil
}

type testVectorStore struct {
	hits               []vector.Hit
	keywordHits        []vector.Hit
	keywordHitsByQuery map[string][]vector.Hit
	existingIDs        map[string]bool
	statuses           map[string]vector.ChunkVectorStatus
	queryLog           *[]string
}

func (testVectorStore) Enabled() bool { return true }
func (testVectorStore) Upsert(context.Context, int, []vector.Point) error {
	return nil
}
func (v testVectorStore) Search(context.Context, int, []float32, vector.Scope, int) ([]vector.Hit, error) {
	return v.hits, nil
}
func (v testVectorStore) SearchKeyword(_ context.Context, _ int, query string, _ vector.Scope, _ int) ([]vector.Hit, error) {
	if v.queryLog != nil {
		*v.queryLog = append(*v.queryLog, query)
	}
	if v.keywordHitsByQuery != nil {
		return v.keywordHitsByQuery[query], nil
	}
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
	return v.allVectorChunkStatuses(), nil
}
func (v testVectorStore) AllVectorChunkStatuses(context.Context, int) (map[string]vector.ChunkVectorStatus, error) {
	return v.allVectorChunkStatuses(), nil
}
func (v testVectorStore) allVectorChunkStatuses() map[string]vector.ChunkVectorStatus {
	if v.statuses != nil {
		out := map[string]vector.ChunkVectorStatus{}
		for id, status := range v.statuses {
			out[id] = status
		}
		return out
	}
	out := map[string]vector.ChunkVectorStatus{}
	for id, ok := range v.existingIDs {
		out[id] = vector.ChunkVectorStatus{Exists: ok, HasVector: ok}
	}
	return out
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
