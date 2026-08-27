package vector

import (
	"bytes"
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestSQLiteVectorStoreSearchScopesAndPersistence(t *testing.T) {
	db := openSQLiteVectorTestDB(t)
	defer db.Close()
	seedSQLiteVectorChunks(t, db)

	ctx := context.Background()
	first := NewSQLite(db)
	if err := first.Upsert(ctx, 3, []Point{
		{ChunkID: "ch-kb", Vector: []float32{1, 0, 0}},
		{ChunkID: "ch-a", Vector: []float32{0.9, 0.1, 0}},
		{ChunkID: "ch-b", Vector: []float32{0, 1, 0}},
		{ChunkID: "ch-other", Vector: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// A new adapter over the same DB proves vectors are persisted, not held only
	// in an in-process cache.
	persisted := NewSQLite(db)
	hits, err := persisted.Search(ctx, 3, []float32{1, 0, 0}, Scope{ConversationID: "conv-1"}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := hitIDs(hits); strings.Join(got, ",") != "ch-a,ch-b" {
		t.Fatalf("conversation hits = %v, want [ch-a ch-b]", got)
	}
	if hits[0].Payload.Filename != "a.txt" || hits[0].Payload.Content != "alpha banana" {
		t.Fatalf("payload = %+v", hits[0].Payload)
	}
	if math.Abs(float64(hits[0].Score-0.9938837)) > 1e-5 {
		t.Fatalf("cosine score = %f", hits[0].Score)
	}

	hits, err = persisted.Search(ctx, 3, []float32{1, 0, 0}, Scope{
		ConversationID: "conv-1",
		DocumentIDs:    []string{"doc-b"},
	}, 10)
	if err != nil {
		t.Fatalf("document-scoped Search: %v", err)
	}
	if got := hitIDs(hits); len(got) != 1 || got[0] != "ch-b" {
		t.Fatalf("document-scoped hits = %v, want [ch-b]", got)
	}

	hits, err = persisted.Search(ctx, 3, []float32{1, 0, 0}, Scope{KBIDs: []string{"kb-1"}}, 10)
	if err != nil {
		t.Fatalf("KB-scoped Search: %v", err)
	}
	if got := hitIDs(hits); len(got) != 1 || got[0] != "ch-kb" {
		t.Fatalf("KB-scoped hits = %v, want [ch-kb]", got)
	}

	if hits, err := persisted.Search(ctx, 3, []float32{1, 0, 0}, Scope{}, 10); err != nil || len(hits) != 0 {
		t.Fatalf("empty-scope Search hits=%v err=%v, want empty", hits, err)
	}
	if hits, err := persisted.Search(ctx, 3, []float32{1, 0, 0}, Scope{
		ConversationID: "conv-1",
		DocumentIDs:    []string{""},
	}, 10); err != nil || len(hits) != 0 {
		t.Fatalf("empty document-id Search hits=%v err=%v, want fail-closed empty scope", hits, err)
	}
}

func TestSQLiteVectorStoreKeywordStatusAndDeletes(t *testing.T) {
	db := openSQLiteVectorTestDB(t)
	defer db.Close()
	seedSQLiteVectorChunks(t, db)
	ctx := context.Background()
	vec := NewSQLite(db)
	if err := vec.Upsert(ctx, 3, []Point{
		{ChunkID: "ch-kb", Vector: []float32{1, 0, 0}},
		{ChunkID: "ch-a", Vector: []float32{0.9, 0.1, 0}},
		{ChunkID: "ch-b", Vector: []float32{0, 1, 0}},
		{ChunkID: "ch-other", Vector: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert dim=3: %v", err)
	}
	if err := vec.Upsert(ctx, 2, []Point{{ChunkID: "ch-a", Vector: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert dim=2: %v", err)
	}

	keywordHits, err := vec.SearchKeyword(ctx, 3, "banana", Scope{ConversationID: "conv-1"}, 10)
	if err != nil {
		t.Fatalf("SearchKeyword: %v", err)
	}
	if got := hitIDs(keywordHits); len(got) != 1 || got[0] != "ch-a" {
		t.Fatalf("keyword hits = %v, want [ch-a]", got)
	}

	ids, err := vec.ExistingChunkIDs(ctx, 3, Scope{ConversationID: "conv-1"})
	if err != nil {
		t.Fatalf("ExistingChunkIDs: %v", err)
	}
	if len(ids) != 2 || !ids["ch-a"] || !ids["ch-b"] {
		t.Fatalf("existing ids = %#v", ids)
	}
	status, err := vec.VectorChunkStatuses(ctx, 3, Scope{ConversationID: "conv-1"})
	if err != nil {
		t.Fatalf("VectorChunkStatuses: %v", err)
	}
	if len(status) != 2 || !status["ch-a"].HasVector || !status["ch-b"].HasVector {
		t.Fatalf("scoped status = %#v", status)
	}
	if empty, err := vec.VectorChunkStatuses(ctx, 3, Scope{}); err != nil || len(empty) != 0 {
		t.Fatalf("empty-scope status=%#v err=%v", empty, err)
	}
	all, err := vec.AllVectorChunkStatuses(ctx, 3)
	if err != nil {
		t.Fatalf("AllVectorChunkStatuses: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("all status count = %d, want 4", len(all))
	}

	if err := vec.DeleteByDocument(ctx, "doc-b"); err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}
	if err := vec.DeleteByKB(ctx, "kb-1"); err != nil {
		t.Fatalf("DeleteByKB: %v", err)
	}
	if err := vec.DeleteByConversation(ctx, "conv-1"); err != nil {
		t.Fatalf("DeleteByConversation: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vector_points`).Scan(&remaining); err != nil {
		t.Fatalf("count vectors: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining vectors = %d, want only ch-other", remaining)
	}
}

func TestSQLiteVectorStoreValidationAndLogicalBackup(t *testing.T) {
	db := openSQLiteVectorTestDB(t)
	defer db.Close()
	seedSQLiteVectorChunks(t, db)
	ctx := context.Background()
	vec := NewSQLite(db)

	if err := vec.Upsert(ctx, 3, []Point{{ChunkID: "ch-a", Vector: []float32{1, 0}}}); err == nil {
		t.Fatal("dimension mismatch was accepted")
	}
	if err := vec.Upsert(ctx, 3, []Point{{ChunkID: "ch-a", Vector: []float32{0, 0, 0}}}); err == nil {
		t.Fatal("zero vector was accepted")
	}
	if err := vec.Upsert(ctx, 3, []Point{{ChunkID: "ch-a", Vector: []float32{1, float32(math.NaN()), 0}}}); err == nil {
		t.Fatal("non-finite vector was accepted")
	}
	if err := vec.Upsert(ctx, 3, []Point{{ChunkID: "ch-a", Vector: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("valid Upsert: %v", err)
	}

	var dump bytes.Buffer
	count, err := store.ExportTable(ctx, db, "vector_points", &dump)
	if err != nil {
		t.Fatalf("ExportTable(vector_points): %v", err)
	}
	if count != 1 || !strings.Contains(dump.String(), `"__b64__"`) {
		t.Fatalf("vector backup count=%d dump=%s", count, dump.String())
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM vector_points`); err != nil {
		t.Fatalf("clear vectors before restore: %v", err)
	}
	restored, err := store.RestoreTable(ctx, db, "vector_points", bytes.NewReader(dump.Bytes()))
	if err != nil {
		t.Fatalf("RestoreTable(vector_points): %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored vector rows = %d, want 1", restored)
	}
	hits, err := vec.Search(ctx, 3, []float32{1, 0, 0}, Scope{ConversationID: "conv-1"}, 1)
	if err != nil || len(hits) != 1 || hits[0].Payload.ChunkID != "ch-a" {
		t.Fatalf("search after restore hits=%v err=%v", hits, err)
	}
	found := false
	for _, table := range store.BackupTableOrder() {
		if table == "vector_points" {
			found = true
		}
	}
	if !found {
		t.Fatal("vector_points is missing from logical backup order")
	}
}

func openSQLiteVectorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "vector.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func seedSQLiteVectorChunks(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, doc := range []struct{ id, filename string }{
		{id: "doc-kb", filename: "kb.txt"},
		{id: "doc-a", filename: "a.txt"},
		{id: "doc-b", filename: "b.txt"},
		{id: "doc-other", filename: "other.txt"},
	} {
		if _, err := db.Exec(`INSERT INTO documents(id, filename, mime_type, size_bytes, status) VALUES(?, ?, 'text/plain', 1, 'ready')`, doc.id, doc.filename); err != nil {
			t.Fatalf("insert document %s: %v", doc.id, err)
		}
	}
	for _, chunk := range []struct {
		id, doc, kb, conv, content string
	}{
		{id: "ch-kb", doc: "doc-kb", kb: "kb-1", content: "knowledge base alpha"},
		{id: "ch-a", doc: "doc-a", conv: "conv-1", content: "alpha banana"},
		{id: "ch-b", doc: "doc-b", conv: "conv-1", content: "beta orange"},
		{id: "ch-other", doc: "doc-other", conv: "conv-2", content: "unrelated alpha"},
	} {
		if _, err := db.Exec(`
			INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, chunk_type, content, embedding_model)
			VALUES(?, ?, NULLIF(?, ''), NULLIF(?, ''), 1, 'text', ?, 'test-embedding')`,
			chunk.id, chunk.doc, chunk.kb, chunk.conv, chunk.content); err != nil {
			t.Fatalf("insert chunk %s: %v", chunk.id, err)
		}
	}
}

func hitIDs(hits []Hit) []string {
	ids := make([]string, len(hits))
	for i := range hits {
		ids[i] = hits[i].Payload.ChunkID
	}
	return ids
}
