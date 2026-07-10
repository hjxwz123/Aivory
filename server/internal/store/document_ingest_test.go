package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimStaleIncompleteDocumentsIsAtomicAndSkipsLiveRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "stale-ingest.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	if _, err := CreateConversation(ctx, db, Conversation{ID: "c1", UserID: "u1", Title: "t"}); err != nil {
		t.Fatalf("conversation: %v", err)
	}
	for _, d := range []Document{
		{ID: "stale", ConversationID: "c1", Filename: "stale.txt", MimeType: "text/plain", Status: "embedding"},
		{ID: "live", ConversationID: "c1", Filename: "live.txt", MimeType: "text/plain", Status: "parsing"},
		{ID: "queued", ConversationID: "c1", Filename: "queued.txt", MimeType: "text/plain", Status: "pending"},
		{ID: "ready", ConversationID: "c1", Filename: "ready.txt", MimeType: "text/plain", Status: "ready"},
	} {
		if _, err := CreateDocument(ctx, db, d); err != nil {
			t.Fatalf("create %s: %v", d.ID, err)
		}
	}
	old := time.Now().Add(-10 * time.Minute).Unix()
	exec(t, db, `UPDATE documents SET ingest_updated_at=? WHERE id IN ('stale','queued','ready')`, old)

	claimed, err := ClaimStaleIncompleteDocuments(
		ctx, db,
		time.Now().Add(-90*time.Minute).Unix(),
		time.Now().Add(-2*time.Minute).Unix(),
	)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "stale" || claimed[0].Status != "embedding" {
		t.Fatalf("claimed = %+v, want stale embedding document", claimed)
	}
	var status, errorText string
	var heartbeat int64
	if err := db.QueryRow(`SELECT status, error, ingest_updated_at FROM documents WHERE id='stale'`).Scan(&status, &errorText, &heartbeat); err != nil {
		t.Fatalf("read claimed row: %v", err)
	}
	if status != "pending" || errorText != "" || heartbeat <= old {
		t.Fatalf("claimed row = status %q error %q heartbeat %d", status, errorText, heartbeat)
	}

	again, err := ClaimStaleIncompleteDocuments(
		ctx, db,
		time.Now().Add(-90*time.Minute).Unix(),
		time.Now().Add(-2*time.Minute).Unix(),
	)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second claim = %+v, want none", again)
	}
}

func TestMigrateAddsIngestHeartbeatToLegacyDocumentsTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-ingest.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("seed current schema: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE documents DROP COLUMN ingest_updated_at`); err != nil {
		t.Fatalf("simulate legacy documents table: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	if _, err := db.Exec(`SELECT ingest_updated_at FROM documents WHERE 1=0`); err != nil {
		t.Fatalf("heartbeat column missing after migration: %v", err)
	}
	var indexName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_docs_ingest_state'`).Scan(&indexName); err != nil {
		t.Fatalf("heartbeat index missing after migration: %v", err)
	}
}
