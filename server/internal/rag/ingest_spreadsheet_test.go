package rag

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestRunPipelineKeepsConversationSpreadsheetForSandbox(t *testing.T) {
	ctx := context.Background()
	db := openSpreadsheetIngestTestDB(t, ctx)
	defer db.Close()

	path := filepath.Join(t.TempDir(), "conversation.csv")
	content := "name,score\nAlice,95\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write conversation csv: %v", err)
	}
	doc := createSpreadsheetIngestDocument(t, ctx, db, store.Document{
		ConversationID: "c1",
		Filename:       "conversation.csv",
		MimeType:       "text/csv",
		SizeBytes:      int64(len(content)),
		StoragePath:    path,
	})

	vec := &adminVectorStore{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(vec)
	if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
		t.Fatalf("runPipeline: %v", err)
	}

	got := getSpreadsheetIngestDocument(t, ctx, db, doc.ID)
	if got.Status != "ready" || got.ChunkCount != 0 || got.Error != "" {
		t.Fatalf("conversation spreadsheet state = status %q chunks %d error %q, want ready/0/empty", got.Status, got.ChunkCount, got.Error)
	}
	if count := spreadsheetIngestChunkCount(t, ctx, db, doc.ID); count != 0 {
		t.Fatalf("conversation spreadsheet stored %d chunks, want 0", count)
	}
	if len(vec.points) != 0 {
		t.Fatalf("conversation spreadsheet upserted %d vectors, want 0", len(vec.points))
	}
}

func TestRunPipelineIndexesKnowledgeBaseSpreadsheets(t *testing.T) {
	ctx := context.Background()
	db := openSpreadsheetIngestTestDB(t, ctx)
	defer db.Close()

	cases := []struct {
		name     string
		filename string
		mime     string
		write    func(*testing.T, string) int64
		want     []string
	}{
		{
			name: "csv", filename: "records.csv", mime: "text/csv",
			write: func(t *testing.T, path string) int64 {
				t.Helper()
				content := []byte("name,score\nAlice\x00,95\nBob,88\n")
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatalf("write csv: %v", err)
				}
				return int64(len(content))
			},
			want: []string{"records.csv", "name\tscore", "Alice\t95", "Bob\t88"},
		},
		{
			name: "tsv", filename: "records.tsv", mime: "text/tab-separated-values",
			write: func(t *testing.T, path string) int64 {
				t.Helper()
				content := []byte("name\tscore\nAlice\t95\n")
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatalf("write tsv: %v", err)
				}
				return int64(len(content))
			},
			want: []string{"records.tsv", "name\tscore", "Alice\t95"},
		},
		{
			name: "xlsx", filename: "book.xlsx", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			write: func(t *testing.T, path string) int64 {
				t.Helper()
				writeMinimalXLSX(t, path)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat xlsx: %v", err)
				}
				return info.Size()
			},
			want: []string{"book.xlsx › Data", "Name\tScore\tJoined", "Alice\t95", "2023-03-15"},
		},
		{
			name: "xlsm", filename: "book.xlsm", mime: "application/vnd.ms-excel.sheet.macroEnabled.12",
			write: func(t *testing.T, path string) int64 {
				t.Helper()
				writeMinimalXLSX(t, path)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat xlsm: %v", err)
				}
				return info.Size()
			},
			want: []string{"book.xlsm › Data", "Alice\t95", "2023-03-15"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.filename)
			size := tc.write(t, path)
			doc := createSpreadsheetIngestDocument(t, ctx, db, store.Document{
				KBID:        "kb1",
				Filename:    tc.filename,
				MimeType:    tc.mime,
				SizeBytes:   size,
				StoragePath: path,
			})

			vec := &adminVectorStore{}
			svc := New(db, nil, log.New(io.Discard, "", 0))
			svc.SetVectorStore(vec)
			if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
				t.Fatalf("runPipeline(%s): %v", tc.filename, err)
			}

			got := getSpreadsheetIngestDocument(t, ctx, db, doc.ID)
			if got.Status != "ready" || got.Error != "" || got.ChunkCount <= 0 {
				t.Fatalf("KB spreadsheet state = status %q chunks %d error %q, want ready/>0/empty", got.Status, got.ChunkCount, got.Error)
			}

			chunks, err := store.ListChunksInScope(ctx, db, []string{"kb1"}, "")
			if err != nil {
				t.Fatalf("list KB chunks: %v", err)
			}
			var children []store.Chunk
			var indexed strings.Builder
			for _, chunk := range chunks {
				if chunk.DocumentID != doc.ID || chunk.ChunkType == "parent" {
					continue
				}
				children = append(children, chunk)
				indexed.WriteString(chunk.Content)
				indexed.WriteByte('\n')
				if chunk.EmbeddingModel != "aivory-local-embed" {
					t.Fatalf("chunk embedding model = %q, want local embedder", chunk.EmbeddingModel)
				}
			}
			if len(children) != got.ChunkCount {
				t.Fatalf("child chunks = %d, document chunk_count = %d", len(children), got.ChunkCount)
			}
			for _, want := range tc.want {
				if !strings.Contains(indexed.String(), want) {
					t.Fatalf("indexed content missing %q:\n%s", want, indexed.String())
				}
			}
			if strings.IndexByte(indexed.String(), 0) >= 0 {
				t.Fatalf("indexed content retained a NUL byte: %q", indexed.String())
			}

			if len(vec.points) != got.ChunkCount {
				t.Fatalf("vector points = %d, document chunk_count = %d", len(vec.points), got.ChunkCount)
			}
			for _, point := range vec.points {
				if point.Payload.DocumentID != doc.ID || point.Payload.KBID != "kb1" || point.Payload.ConversationID != "" {
					t.Fatalf("unexpected vector payload: %+v", point.Payload)
				}
				if len(point.Vector) != localEmbedDim {
					t.Fatalf("vector dimension = %d, want %d", len(point.Vector), localEmbedDim)
				}
				if strings.IndexByte(point.Payload.Content, 0) >= 0 {
					t.Fatalf("vector payload retained a NUL byte: %q", point.Payload.Content)
				}
			}
		})
	}
}

func TestRunIngestWithRetriesFailsMalformedKnowledgeBaseSpreadsheet(t *testing.T) {
	ctx := context.Background()
	db := openSpreadsheetIngestTestDB(t, ctx)
	defer db.Close()

	path := filepath.Join(t.TempDir(), "broken.xlsx")
	content := []byte("this is not an xlsx zip")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write malformed xlsx: %v", err)
	}
	doc := createSpreadsheetIngestDocument(t, ctx, db, store.Document{
		KBID:        "kb1",
		Filename:    "broken.xlsx",
		MimeType:    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		SizeBytes:   int64(len(content)),
		StoragePath: path,
	})

	vec := &adminVectorStore{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(vec)
	err := svc.runIngestWithRetries(ctx, doc.ID)
	if err == nil {
		t.Fatal("malformed KB spreadsheet ingest unexpectedly succeeded")
	}
	if !isNonRetryableIngestError(err) {
		t.Fatalf("malformed spreadsheet error = %T %v, want non-retryable", err, err)
	}

	got := getSpreadsheetIngestDocument(t, ctx, db, doc.ID)
	if got.Status != "failed" || got.ChunkCount != 0 {
		t.Fatalf("malformed spreadsheet state = status %q chunks %d, want failed/0", got.Status, got.ChunkCount)
	}
	if !strings.Contains(got.Error, "knowledge-base spreadsheet parse failed") {
		t.Fatalf("malformed spreadsheet failure reason = %q", got.Error)
	}
	if count := spreadsheetIngestChunkCount(t, ctx, db, doc.ID); count != 0 {
		t.Fatalf("malformed spreadsheet retained %d chunks, want 0", count)
	}
	if len(vec.points) != 0 {
		t.Fatalf("malformed spreadsheet upserted %d vectors, want 0", len(vec.points))
	}
}

func TestRunPipelineFailsUnextractableLegacyKnowledgeBaseXLS(t *testing.T) {
	ctx := context.Background()
	db := openSpreadsheetIngestTestDB(t, ctx)
	defer db.Close()

	path := filepath.Join(t.TempDir(), "legacy.xls")
	content := []byte("\xd0\xcf\x11\xe0legacy BIFF placeholder")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write legacy xls: %v", err)
	}
	doc := createSpreadsheetIngestDocument(t, ctx, db, store.Document{
		KBID:        "kb1",
		Filename:    "legacy.xls",
		MimeType:    "application/vnd.ms-excel",
		SizeBytes:   int64(len(content)),
		StoragePath: path,
	})

	vec := &adminVectorStore{}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	svc.SetVectorStore(vec)
	if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
		t.Fatalf("legacy xls generic fallback returned an error: %v", err)
	}

	got := getSpreadsheetIngestDocument(t, ctx, db, doc.ID)
	if got.Status != "failed" || got.ChunkCount != 0 {
		t.Fatalf("legacy xls state = status %q chunks %d, want failed/0", got.Status, got.ChunkCount)
	}
	if !strings.Contains(got.Error, "could not extract text") && !strings.Contains(got.Error, "MinerU") {
		t.Fatalf("legacy xls failure reason does not explain extraction failure: %q", got.Error)
	}
	if count := spreadsheetIngestChunkCount(t, ctx, db, doc.ID); count != 0 {
		t.Fatalf("legacy xls retained %d chunks, want 0", count)
	}
	if len(vec.points) != 0 {
		t.Fatalf("legacy xls upserted %d vectors, want 0", len(vec.points))
	}
}

func openSpreadsheetIngestTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "spreadsheet-ingest.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate database: %v", err)
	}
	// A blank model id exercises the supported legacy-KB fallback to the bundled
	// deterministic embedder without introducing an HTTP dependency in this test.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		_ = db.Close()
		t.Fatalf("disable test foreign keys: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','spreadsheet@example.test','h','User','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Conversation')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Spreadsheet KB','',256)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			_ = db.Close()
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	return db
}

func createSpreadsheetIngestDocument(t *testing.T, ctx context.Context, db *sql.DB, input store.Document) *store.Document {
	t.Helper()
	doc, err := store.CreateDocument(ctx, db, input)
	if err != nil {
		t.Fatalf("create document %q: %v", input.Filename, err)
	}
	return doc
}

func getSpreadsheetIngestDocument(t *testing.T, ctx context.Context, db *sql.DB, docID string) *store.Document {
	t.Helper()
	doc, err := store.GetDocument(ctx, db, docID)
	if err != nil {
		t.Fatalf("get document %s: %v", docID, err)
	}
	return doc
}

func spreadsheetIngestChunkCount(t *testing.T, ctx context.Context, db *sql.DB, docID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE document_id=?`, docID).Scan(&count); err != nil {
		t.Fatalf("count chunks for %s: %v", docID, err)
	}
	return count
}
