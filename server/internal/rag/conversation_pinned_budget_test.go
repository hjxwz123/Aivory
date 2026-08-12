package rag

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aivory/server/internal/store"
)

func TestConversationDocumentsShareCumulativePinnedBudget(t *testing.T) {
	ctx := context.Background()
	db := openPinnedBudgetTestDB(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 12); err != nil {
		t.Fatalf("set full-text threshold: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	first := createPinnedBudgetDocument(t, ctx, db, "c1", "", "first.md", strings.Repeat("alpha ", 6))
	second := createPinnedBudgetDocument(t, ctx, db, "c1", "", "second.md", strings.Repeat("bravo ", 6))
	if err := svc.runPipeline(ctx, first.ID, nil); err != nil {
		t.Fatalf("ingest first document: %v", err)
	}
	if err := svc.runPipeline(ctx, second.ID, nil); err != nil {
		t.Fatalf("ingest second document: %v", err)
	}

	if model := childEmbeddingModel(t, ctx, db, first.ID); model != "" {
		t.Fatalf("first document embedding model = %q, want pinned", model)
	}
	if model := childEmbeddingModel(t, ctx, db, second.ID); model == "" {
		t.Fatal("second document remained pinned after cumulative budget was exhausted")
	}
	assertConversationPinnedTokensAtMost(t, ctx, db, "c1", 12)
}

func TestConcurrentConversationIngestDoesNotOversubscribePinnedBudget(t *testing.T) {
	ctx := context.Background()
	db := openPinnedBudgetTestDB(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 12); err != nil {
		t.Fatalf("set full-text threshold: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	docs := []*store.Document{
		createPinnedBudgetDocument(t, ctx, db, "c1", "", "one.md", strings.Repeat("alpha ", 6)),
		createPinnedBudgetDocument(t, ctx, db, "c1", "", "two.md", strings.Repeat("bravo ", 6)),
		createPinnedBudgetDocument(t, ctx, db, "c1", "", "three.md", strings.Repeat("charlie ", 6)),
	}
	start := make(chan struct{})
	errs := make(chan error, len(docs))
	var wg sync.WaitGroup
	for _, doc := range docs {
		doc := doc
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.runPipeline(ctx, doc.ID, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	assertConversationPinnedTokensAtMost(t, ctx, db, "c1", 12)
}

func TestKnowledgeBaseSmallDocumentAlwaysEmbeds(t *testing.T) {
	ctx := context.Background()
	db := openPinnedBudgetTestDB(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 1000); err != nil {
		t.Fatalf("set full-text threshold: %v", err)
	}

	doc := createPinnedBudgetDocument(t, ctx, db, "", "kb1", "small.md", "short knowledge base document")
	svc := New(db, nil, log.New(io.Discard, "", 0))
	if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
		t.Fatalf("ingest KB document: %v", err)
	}
	if model := childEmbeddingModel(t, ctx, db, doc.ID); model == "" {
		t.Fatal("small knowledge-base document was pinned; KB documents must always embed")
	}
}

func TestRouteBoundsLegacyPinnedOverflowAndExplainsOmission(t *testing.T) {
	ctx := context.Background()
	db := openPinnedBudgetTestDB(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 100); err != nil {
		t.Fatalf("set full-text threshold: %v", err)
	}

	for i, content := range []string{strings.Repeat("a", 320), strings.Repeat("b", 320)} {
		doc, err := store.CreateDocument(ctx, db, store.Document{
			ConversationID: "c1",
			Filename:       "legacy-" + string(rune('1'+i)) + ".md",
			MimeType:       "text/markdown",
			StoragePath:    filepath.Join(t.TempDir(), "legacy.md"),
		})
		if err != nil {
			t.Fatalf("create legacy document: %v", err)
		}
		if err := store.CreateChunk(ctx, db, doc.ID, "", "c1", 0, content, ""); err != nil {
			t.Fatalf("create legacy chunk: %v", err)
		}
		if err := store.UpdateDocumentStatus(ctx, db, doc.ID, "ready", "", 1); err != nil {
			t.Fatalf("mark legacy document ready: %v", err)
		}
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	snippets, decision, err := svc.RouteAndRetrieve(ctx, "u1", "c1", nil, "unrelated question", nil, 5)
	if err != nil {
		t.Fatalf("route legacy overflow: %v", err)
	}
	if decision.Strategy != "retrieve" {
		t.Fatalf("strategy = %q, want bounded relational retrieval", decision.Strategy)
	}
	total := 0
	var combined strings.Builder
	for _, snippet := range snippets {
		total += estimateTokens(snippet.Snippet)
		combined.WriteString(snippet.Snippet)
	}
	if total > 100 {
		t.Fatalf("legacy pinned output = %d estimated tokens, want <= 100", total)
	}
	if !strings.Contains(combined.String(), "历史附件") || !strings.Contains(combined.String(), "关键词检索") {
		t.Fatalf("legacy overflow omission was not explained: %q", combined.String())
	}

	direct, err := svc.Retrieve(ctx, "u1", "c1", nil, "unrelated question", 5)
	if err != nil {
		t.Fatalf("direct retrieve legacy overflow: %v", err)
	}
	directTokens := 0
	combined.Reset()
	for _, snippet := range direct {
		directTokens += estimateTokens(snippet.Snippet)
		combined.WriteString(snippet.Snippet)
	}
	if directTokens > 100 {
		t.Fatalf("direct legacy pinned output = %d estimated tokens, want <= 100", directTokens)
	}
	if !strings.Contains(combined.String(), "历史附件") {
		t.Fatalf("direct legacy overflow omission was not explained: %q", combined.String())
	}
}

func TestConversationAggregateOverflowUsesBoundedRelationalFallback(t *testing.T) {
	ctx := context.Background()
	db := openPinnedBudgetTestDB(t, ctx)
	defer db.Close()
	if err := store.SetSetting(db, "rag_full_text_threshold", 100); err != nil {
		t.Fatalf("set full-text threshold: %v", err)
	}

	pinnedContent := strings.Repeat("alpha ", 45)
	embeddedContent := "late_document_target " + strings.Repeat("bravo ", 25)
	for _, query := range []string{
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('pinned-doc','c1','pinned.md','text/markdown',1,'ready')`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('embedded-doc','c1','embedded.md','text/markdown',1,'ready')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('pinned-chunk','pinned-doc','c1',0,'text','` + pinnedContent + `','')`,
		`INSERT INTO chunks(id,document_id,conversation_id,seq,chunk_type,content,embedding_model) VALUES('embedded-chunk','embedded-doc','c1',0,'text','` + embeddedContent + `','aivory-local-embed')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed aggregate overflow %q: %v", query, err)
		}
	}

	assertBoundedHit := func(label string, snippets []Snippet) {
		t.Helper()
		total := 0
		var combined strings.Builder
		foundEmbedded := false
		for _, snippet := range snippets {
			total += estimateTokens(snippet.Snippet)
			combined.WriteString(snippet.Snippet)
			if snippet.ID == "embedded-chunk" {
				foundEmbedded = true
			}
		}
		if total > 100 {
			t.Fatalf("%s returned %d estimated tokens, want <= 100: %+v", label, total, snippets)
		}
		if !foundEmbedded || !strings.Contains(combined.String(), "late_document_target") {
			t.Fatalf("%s missed the relevant embedded document: %+v", label, snippets)
		}
		if strings.Contains(combined.String(), pinnedContent) && strings.Contains(combined.String(), embeddedContent) {
			t.Fatalf("%s failed open to both full documents: %+v", label, snippets)
		}
	}

	directService := New(db, nil, log.New(io.Discard, "", 0))
	direct, err := directService.Retrieve(ctx, "u1", "c1", nil, "late_document_target", 8)
	if err != nil {
		t.Fatalf("direct retrieve: %v", err)
	}
	assertBoundedHit("direct retrieve", direct)

	autoService := New(db, nil, log.New(io.Discard, "", 0))
	auto, decision, err := autoService.RouteAndRetrieve(ctx, "u1", "c1", nil, "late_document_target", nil, 8)
	if err != nil {
		t.Fatalf("auto route: %v", err)
	}
	if decision.Strategy != "retrieve" {
		t.Fatalf("auto strategy = %q, want retrieve", decision.Strategy)
	}
	assertBoundedHit("auto route", auto)

	router := &recordingRouter{decision: RouteDecision{Strategy: "full_doc"}}
	fullDocService := New(db, nil, log.New(io.Discard, "", 0))
	fullDocService.SetTaskLLM(router)
	fullDoc, decision, err := fullDocService.RouteAndRetrieve(ctx, "u1", "c1", nil, "late_document_target", nil, 8)
	if err != nil {
		t.Fatalf("full_doc route: %v", err)
	}
	if router.calls != 1 || decision.Strategy != "full_doc" {
		t.Fatalf("full_doc routing = calls %d, decision %+v", router.calls, decision)
	}
	assertBoundedHit("full_doc route", fullDoc)
}

func openPinnedBudgetTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "pinned-budget.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		_ = db.Close()
		t.Fatalf("disable test foreign keys: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','pinned@example.test','h','User','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Conversation')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','',256)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	return db
}

func createPinnedBudgetDocument(t *testing.T, ctx context.Context, db *sql.DB, conversationID, kbID, filename, content string) *store.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ConversationID: conversationID,
		KBID:           kbID,
		Filename:       filename,
		MimeType:       "text/markdown",
		SizeBytes:      int64(len(content)),
		StoragePath:    path,
	})
	if err != nil {
		t.Fatalf("create %s: %v", filename, err)
	}
	return doc
}

func childEmbeddingModel(t *testing.T, ctx context.Context, db *sql.DB, documentID string) string {
	t.Helper()
	var model string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(embedding_model,'') FROM chunks WHERE document_id=? AND chunk_type!='parent' LIMIT 1`,
		documentID,
	).Scan(&model); err != nil {
		t.Fatalf("read child embedding model for %s: %v", documentID, err)
	}
	return model
}

func assertConversationPinnedTokensAtMost(t *testing.T, ctx context.Context, db *sql.DB, conversationID string, max int) {
	t.Helper()
	chunks, err := store.ListChunksInScope(ctx, db, nil, conversationID)
	if err != nil {
		t.Fatalf("list conversation chunks: %v", err)
	}
	total := 0
	for _, chunk := range chunks {
		if chunk.ChunkType != "parent" && strings.TrimSpace(chunk.EmbeddingModel) == "" {
			total += estimateTokens(chunk.Content)
		}
	}
	if total > max {
		t.Fatalf("conversation pinned tokens = %d, want <= %d", total, max)
	}
}
