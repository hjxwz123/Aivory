package llm

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestResolveAttachmentsUsesConversationFileBytesNotClientMetadata(t *testing.T) {
	oldLimit := attachmentImageInlineBytes
	attachmentImageInlineBytes = 64
	t.Cleanup(func() { attachmentImageInlineBytes = oldLimit })

	db, err := store.Open(filepath.Join(t.TempDir(), "attachments.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES(?, 'u1', 'Test')`, id); err != nil {
			t.Fatal(err)
		}
	}

	root := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	pdf := []byte("%PDF-1.7\nnot an image")
	paths := map[string]string{
		"legacy": write("legacy.bin", png),
		"pdf":    write("report.pdf", pdf),
		"cross":  write("cross.png", png),
		"large":  write("large.png", png),
	}
	if err := os.Truncate(paths["large"], attachmentImageInlineBytes+1); err != nil {
		t.Fatal(err)
	}

	for _, file := range []store.File{
		{ID: "f_legacy", UserID: "u1", ConversationID: "c1", Filename: "legacy.bin", MimeType: "text/plain", Kind: "text", SizeBytes: int64(len(png)), StoragePath: paths["legacy"]},
		{ID: "f_pdf", UserID: "u1", ConversationID: "c1", Filename: "report.pdf", MimeType: "application/pdf", Kind: "pdf", SizeBytes: int64(len(pdf)), StoragePath: paths["pdf"]},
		{ID: "f_cross", UserID: "u1", ConversationID: "c2", Filename: "cross.png", MimeType: "image/png", Kind: "image", SizeBytes: int64(len(png)), StoragePath: paths["cross"]},
		{ID: "f_large", UserID: "u1", ConversationID: "c1", Filename: "large.png", MimeType: "image/png", Kind: "image", SizeBytes: attachmentImageInlineBytes + 1, StoragePath: paths["large"]},
	} {
		if _, err := store.CreateFile(context.Background(), db, file); err != nil {
			t.Fatalf("create %s: %v", file.ID, err)
		}
	}

	history := []UnifiedMessage{{
		Role:   "user",
		Blocks: []UnifiedBlock{{Kind: "text", Text: "inspect these"}},
		Attachments: []Attachment{
			// Every client field is wrong. The DB bytes still identify this image.
			{ID: "f_legacy", Filename: "notes.txt", MimeType: "text/plain", Kind: "other"},
			// A forged image claim cannot turn a real PDF into base64 image input.
			{ID: "f_pdf", Filename: "fake.png", MimeType: "image/png", Kind: "image"},
			// A valid file owned by the same user but from another conversation is inaccessible.
			{ID: "f_cross", Filename: "cross.png", MimeType: "image/png", Kind: "image"},
			// Even a real image cannot exceed the independent provider inline cap.
			{ID: "f_large", Filename: "large.png", MimeType: "image/png", Kind: "image"},
		},
	}}
	var events []SseEvent
	(&Orchestrator{db: db}).resolveAttachments(
		context.Background(), "u1", "c1", history, &store.Model{Vision: true},
		func(event SseEvent) { events = append(events, event) },
	)

	var images []UnifiedBlock
	for _, block := range history[0].Blocks {
		if block.Kind == "image" {
			images = append(images, block)
		}
	}
	if len(images) != 1 {
		t.Fatalf("provider image blocks = %+v, want only verified legacy image", images)
	}
	if images[0].Title != "legacy.bin" || images[0].MimeType != "image/png" || images[0].Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("verified image block = %+v", images[0])
	}
	for _, block := range history[0].Blocks {
		if strings.Contains(block.Data, base64.StdEncoding.EncodeToString(pdf)) {
			t.Fatal("forged PDF reached provider image data")
		}
	}
	foundPDFNote := false
	foundOversizeWarning := false
	for _, block := range history[0].Blocks {
		foundPDFNote = foundPDFNote || strings.Contains(block.Text, "PDF attachment")
	}
	for _, event := range events {
		foundOversizeWarning = foundOversizeWarning || strings.Contains(event.Summary, "inline limit")
	}
	if !foundPDFNote || !foundOversizeWarning {
		t.Fatalf("missing server-derived PDF note or oversize warning: blocks=%+v events=%+v", history[0].Blocks, events)
	}
}

func TestResolveAttachmentsStripsLegacyImageBytesForNonVisionModel(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "non-vision-attachments.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Test')`); err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	path := filepath.Join(t.TempDir(), "legacy.bin")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFile(context.Background(), db, store.File{
		ID: "f1", UserID: "u1", ConversationID: "c1", Filename: "legacy.bin",
		MimeType: "text/plain", Kind: "text", SizeBytes: int64(len(png)), StoragePath: path,
	}); err != nil {
		t.Fatal(err)
	}
	history := []UnifiedMessage{{Role: "user", Attachments: []Attachment{{ID: "f1", Kind: "other"}}}}
	(&Orchestrator{db: db}).resolveAttachments(context.Background(), "u1", "c1", history, &store.Model{Vision: false}, nil)
	if len(history[0].Blocks) != 1 || history[0].Blocks[0].Kind != "text" || !strings.Contains(history[0].Blocks[0].Text, "lacks vision") {
		t.Fatalf("legacy image was not replaced by non-vision placeholder: %+v", history[0].Blocks)
	}
}
