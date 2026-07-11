package rag

import (
	"context"
	"database/sql"
	"io"
	"log"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aivory/server/internal/queue"
	"aivory/server/internal/store"
)

func TestCanceledIngestFinalizesDocumentWithFreshContext(t *testing.T) {
	ctx := context.Background()
	db := openIngestRecoveryDB(t)
	defer db.Close()
	doc := createRecoveryDocument(t, ctx, db, "cancelled", "pending")

	svc := New(db, &captureIngestQueue{}, log.New(io.Discard, "", 0))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.runIngestWithRetries(cancelled, doc.ID); err == nil {
		t.Fatal("runIngestWithRetries returned nil for cancelled context")
	}

	got, err := store.GetDocument(ctx, db, doc.ID)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Fatal("failed document has no terminal error")
	}
}

func TestRequeueStaleIngestsClaimsOnlyAbandonedActiveTask(t *testing.T) {
	ctx := context.Background()
	db := openIngestRecoveryDB(t)
	defer db.Close()
	stale := createRecoveryDocument(t, ctx, db, "stale", "embedding")
	_ = createRecoveryDocument(t, ctx, db, "live", "parsing")
	if _, err := db.ExecContext(ctx,
		`UPDATE documents SET ingest_updated_at=? WHERE id=?`,
		time.Now().Add(-10*time.Minute).Unix(), stale.ID); err != nil {
		t.Fatalf("age stale document: %v", err)
	}

	q := &captureIngestQueue{}
	svc := New(db, q, log.New(io.Discard, "", 0))
	svc.RequeueStaleIngests(ctx)
	if names := q.Names(); len(names) != 1 || names[0] != ragIngestTaskType {
		t.Fatalf("queued jobs = %v, want one %s", names, ragIngestTaskType)
	}
	got, err := store.GetDocument(ctx, db, stale.ID)
	if err != nil {
		t.Fatalf("get stale document: %v", err)
	}
	if got.Status != "pending" {
		t.Fatalf("reclaimed status = %q, want pending", got.Status)
	}

	svc.RequeueStaleIngests(ctx)
	if names := q.Names(); len(names) != 1 {
		t.Fatalf("second watchdog pass queued duplicate jobs: %v", names)
	}
}

func TestIngestQueueNameForDocumentReservesFastLaneForLocalFiles(t *testing.T) {
	cases := []struct {
		name string
		doc  store.Document
		want string
	}{
		{name: "plain text", doc: store.Document{Filename: "notes.txt", MimeType: "text/plain"}, want: ragFastQueueName},
		{name: "markdown by extension", doc: store.Document{Filename: "README.md", MimeType: "application/octet-stream"}, want: ragFastQueueName},
		{name: "spreadsheet", doc: store.Document{Filename: "data.xlsx", MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}, want: ragFastQueueName},
		{name: "pdf", doc: store.Document{Filename: "report.pdf", MimeType: "application/pdf"}, want: ragSlowQueueName},
		{name: "office", doc: store.Document{Filename: "slides.pptx", MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}, want: ragSlowQueueName},
		{name: "image", doc: store.Document{Filename: "scan.png", MimeType: "image/png"}, want: ragSlowQueueName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ingestQueueNameForDocument(&tc.doc); got != tc.want {
				t.Fatalf("queue = %q, want %q", got, tc.want)
			}
		})
	}
}

func openIngestRecoveryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "ingest-recovery.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`); err != nil {
		_ = db.Close()
		t.Fatalf("seed user: %v", err)
	}
	if _, err := store.CreateConversation(context.Background(), db, store.Conversation{ID: "c1", UserID: "u1", Title: "t"}); err != nil {
		_ = db.Close()
		t.Fatalf("seed conversation: %v", err)
	}
	return db
}

func createRecoveryDocument(t *testing.T, ctx context.Context, db *sql.DB, id, status string) *store.Document {
	t.Helper()
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ID: id, ConversationID: "c1", Filename: id + ".txt",
		MimeType: "text/plain", Status: status,
	})
	if err != nil {
		t.Fatalf("create document %s: %v", id, err)
	}
	return doc
}

type captureIngestQueue struct {
	mu    sync.Mutex
	names []string
}

func (q *captureIngestQueue) Enqueue(name string, _ queue.Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.names = append(q.names, name)
}

func (*captureIngestQueue) Close() {}

func (q *captureIngestQueue) Names() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.names...)
}
