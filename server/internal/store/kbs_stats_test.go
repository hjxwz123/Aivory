package store

import (
	"context"
	"testing"
)

func TestAttachKnowledgeBaseStatsSummarizesDocumentsPerLibrary(t *testing.T) {
	db := openKBPermissionTestDB(t)
	ctx := context.Background()
	exec(t, db, `UPDATE knowledge_bases SET created_at=100`)
	exec(t, db, `UPDATE documents SET size_bytes=10, created_at=110, ingest_updated_at=0 WHERE id='personal-document'`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,created_at,ingest_updated_at) VALUES
		('personal-failed','personal-kb','failed.txt','text/plain',20,'failed','',120,130),
		('personal-embedding','personal-kb','embedding.txt','text/plain',30,'embedding','',140,0)`)

	rows, err := ListKBs(ctx, db, "creator")
	if err != nil {
		t.Fatalf("list knowledge bases: %v", err)
	}
	if err := AttachKnowledgeBaseStats(ctx, db, rows); err != nil {
		t.Fatalf("attach stats: %v", err)
	}
	byID := map[string]KnowledgeBase{}
	for _, row := range rows {
		byID[row.ID] = row
	}

	personal := byID["personal-kb"].Stats
	if personal == nil {
		t.Fatal("personal-kb has no stats")
	}
	want := KnowledgeBaseStats{
		DocumentCount:           3,
		ReadyDocumentCount:      1,
		FailedDocumentCount:     1,
		ProcessingDocumentCount: 1,
		TotalSizeBytes:          60,
		UpdatedAt:               140,
	}
	if *personal != want {
		t.Fatalf("personal-kb stats=%+v want %+v", *personal, want)
	}

	// A library without documents still reports zero totals and its own
	// creation time, so the client never has to special-case a nil block.
	empty := byID["compatible-kb"].Stats
	if empty == nil || *empty != (KnowledgeBaseStats{UpdatedAt: 100}) {
		t.Fatalf("empty library stats=%+v", empty)
	}

	if err := AttachKnowledgeBaseStats(ctx, db, nil); err != nil {
		t.Fatalf("attach stats to no rows: %v", err)
	}
}
