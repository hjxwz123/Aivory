package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestLogChatRunErrorIncludesMetadataWithoutRequestContent(t *testing.T) {
	var out bytes.Buffer
	logger := log.New(&out, "", 0)
	underlying := errors.New(`ERROR: insert or update on table "messages" violates foreign key constraint "messages_parent_id_fkey"`)

	logChatRunError(logger, chatRunErrorMetadata{
		Operation:       "post_message",
		UserID:          "user-1",
		ConversationID:  "conversation-1",
		Fast:            true,
		Branch:          false,
		ParentID:        "missing-parent",
		ReferenceID:     "assistant-1",
		AttachmentCount: 1,
	}, underlying)

	got := out.String()
	for _, want := range []string{
		`operation="post_message"`,
		`user_id="user-1"`,
		`conversation_id="conversation-1"`,
		`fast=true`,
		`branch=false`,
		`parent_id="missing-parent"`,
		`reference_id="assistant-1"`,
		`attachment_count=1`,
		`messages_parent_id_fkey`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output %q does not contain %q", got, want)
		}
	}
	for _, secret := range []string{
		"private message text",
		"quarterly-results.xlsx",
		"uploaded spreadsheet contents",
		"assembled system prompt",
	} {
		if strings.Contains(got, secret) {
			t.Errorf("log output leaked request content %q: %s", secret, got)
		}
	}
}

func TestLogChatRunErrorAllowsNilLogger(t *testing.T) {
	logChatRunError(nil, chatRunErrorMetadata{Operation: "post_message"}, errors.New("database unavailable"))
}

func TestEnsureAttachedDocumentsReadyRequiresReadyStatus(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "attached-docs.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c2','u1','Other')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_pending','c1','p.pdf','application/pdf',10,'embedding')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_ready','c1','r.pdf','application/pdf',10,'ready')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_other','c2','o.pdf','application/pdf',10,'ready')`)

	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_ready"}}); err != nil {
		t.Fatalf("ready document rejected: %v", err)
	}
	err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_pending"}})
	if err == nil || !strings.Contains(err.Error(), "still indexing") {
		t.Fatalf("pending document error = %v, want still indexing", err)
	}
	err = ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_other"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("foreign document error = %v, want not found", err)
	}
}

func TestEnsureAttachedDocumentsReadyFallsBackToFileID(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "attached-files.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f_ready','u1','c1','r.pdf','application/pdf',10,'pdf','/tmp/r.pdf')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f_pending','u1','c1','p.pdf','application/pdf',10,'pdf','/tmp/p.pdf')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_ready','c1','r.pdf','application/pdf',10,'ready','/tmp/r.pdf')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_pending','c1','p.pdf','application/pdf',10,'embedding','/tmp/p.pdf')`)

	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_ready", Kind: "pdf"}}); err != nil {
		t.Fatalf("ready file-backed document rejected: %v", err)
	}
	err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_pending", Kind: "pdf"}})
	if err == nil || !strings.Contains(err.Error(), "still indexing") {
		t.Fatalf("pending file-backed document error = %v, want still indexing", err)
	}
	err = ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_missing", Kind: "pdf"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing file-backed document error = %v, want not found", err)
	}
}

// A spreadsheet (kind='sheet') is staged to the code sandbox and never creates a
// RAG document row. When a browser mislabels its OOXML MIME as 'doc' (the
// "officedocument" substring), the send must still go through — the preflight
// must trust the SERVER's file kind, not the client's, or every xlsx upload 409s
// with "attached document not found".
func TestEnsureAttachedDocumentsReadyAllowsSandboxSheet(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "attached-sheet.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f_sheet','u1','c1','data.xlsx','application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',10,'sheet','/tmp/data.xlsx')`)

	// Client mislabels the sheet as 'doc'; the server filed it as 'sheet' with no
	// document — the send must NOT be rejected.
	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_sheet", Kind: "doc"}}); err != nil {
		t.Fatalf("sandbox spreadsheet rejected: %v", err)
	}
}

// §fast-mode: redactCost is the single user-facing chokepoint that must blank a
// fast turn's real model identity (model_id / model_label / provider) while
// keeping `fast:true` so the client renders 快速. A normal turn's identity is
// untouched.
func TestRedactCostMasksFastModelIdentity(t *testing.T) {
	ems := []enrichedMessage{
		{Message: store.Message{ID: "m1", Fast: true, ModelID: "secret_id", ModelLabel: "SecretModel", Provider: "anthropic", Cost: 1.23, Currency: "USD"}},
		{Message: store.Message{ID: "m2", Fast: false, ModelID: "gpt", ModelLabel: "GPT", Provider: "openai", Cost: 0.5, Currency: "USD"}},
	}
	out := redactCost(ems)
	if out[0].ModelID != "" || out[0].ModelLabel != "" || out[0].Provider != "" {
		t.Fatalf("fast turn's model identity leaked: %+v", out[0])
	}
	if !out[0].Fast {
		t.Fatal("fast flag must survive redaction so the client can render 快速")
	}
	if out[1].ModelID != "gpt" || out[1].ModelLabel != "GPT" || out[1].Provider != "openai" {
		t.Fatalf("a normal turn's model identity must be untouched: %+v", out[1])
	}
	// Cost is redacted for both regardless (pre-existing behaviour).
	if out[0].Cost != 0 || out[1].Cost != 0 {
		t.Fatalf("cost not redacted: %v %v", out[0].Cost, out[1].Cost)
	}
}

func TestEnsureAttachedDocumentsReadyRejectsOmittedServerDraft(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "draft-preflight.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path,draft) VALUES('f_draft','u1','c1','ready.pdf','application/pdf',10,'pdf','/tmp/ready.pdf',1)`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_ready','c1','ready.pdf','application/pdf',10,'ready','/tmp/ready.pdf')`)

	err := ensureAttachedDocumentsReady(ctx, db, "c1", nil)
	if err == nil || !strings.Contains(err.Error(), "unsent attachments") {
		t.Fatalf("omitted draft error = %v, want unsent attachments", err)
	}
	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_draft", Kind: "pdf"}}); err != nil {
		t.Fatalf("included ready draft rejected: %v", err)
	}
}
