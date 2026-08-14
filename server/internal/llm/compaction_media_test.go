package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestCollectCompactionMediaRefsKeepsRecentUniqueImages(t *testing.T) {
	message := func(id string, attachments []Attachment, blocks []UnifiedBlock) store.Message {
		t.Helper()
		attachmentJSON, err := json.Marshal(attachments)
		if err != nil {
			t.Fatal(err)
		}
		blockJSON, err := json.Marshal(blocks)
		if err != nil {
			t.Fatal(err)
		}
		return store.Message{ID: id, Role: "user", Attachments: attachmentJSON, Blocks: blockJSON}
	}

	refs := collectCompactionMediaRefs([]store.Message{
		message("m1", []Attachment{{ID: "file-1", Kind: "image"}, {ID: "document", Kind: "pdf"}}, nil),
		message("m2", []Attachment{{ID: "file-2", MimeType: "image/webp"}}, nil),
		message("m3", []Attachment{{ID: "file-1", Kind: "image"}}, nil),
		message("m4", nil, []UnifiedBlock{{
			Kind: "artifact", FileRef: "artifact-1", Summary: "application/octet-stream",
		}}),
		message("m5", []Attachment{{ID: "file-3", Kind: "image"}}, nil),
		message("m6", []Attachment{{ID: "file-4", Kind: "image"}}, nil),
	})

	if len(refs) != 5 {
		t.Fatalf("media refs = %+v, want all 5 unique image references", refs)
	}
	want := []string{"file-2", "file-1", "artifact-1", "file-3", "file-4"}
	for i := range want {
		if refs[i].ID != want[i] {
			t.Fatalf("media refs = %+v, want ids %v", refs, want)
		}
	}
	encoded, err := json.Marshal(SummaryBlock{Media: refs})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 {
		t.Fatalf("metadata-only media refs unexpectedly large: %d bytes", len(encoded))
	}
}

func TestInjectCompactionMediaReauthorizesAndSniffsBytes(t *testing.T) {
	oldLimit := attachmentImageInlineBytes
	attachmentImageInlineBytes = 128
	t.Cleanup(func() { attachmentImageInlineBytes = oldLimit })

	db, err := store.Open(filepath.Join(t.TempDir(), "compaction-media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','owner@example.test','hash','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('u2','other@example.test','hash','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Owner')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c2','u2','Other')`,
		`INSERT INTO messages(id,conversation_id,role,author_id,status) VALUES('a1','c1','assistant','u1','complete')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	root := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, png, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, file := range []store.File{
		{
			ID: "owned-file", UserID: "u1", ConversationID: "c1", Filename: "legacy.bin",
			MimeType: "text/plain", Kind: "other", StoragePath: write("owned.bin"), SizeBytes: int64(len(png)),
		},
		{
			ID: "foreign-file", UserID: "u2", ConversationID: "c2", Filename: "foreign.png",
			MimeType: "image/png", Kind: "image", StoragePath: write("foreign.png"), SizeBytes: int64(len(png)),
		},
	} {
		if _, err := store.CreateFile(ctx, db, file); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := store.CreateArtifact(ctx, db, store.Artifact{
		MessageID: "a1", Filename: "generated.bin", StoragePath: write("artifact.bin"),
		MimeType: "text/plain", SizeBytes: int64(len(png)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetArtifact(ctx, db, artifact.ID, "u1"); err != nil {
		t.Fatalf("owned artifact is not readable by fixture user: %v", err)
	}
	if data, mimeType := readVerifiedProviderArtifactImage(artifact, root); len(data) == 0 || mimeType != "image/png" {
		t.Fatalf("artifact fixture bytes are not readable: bytes=%d mime=%q path=%q", len(data), mimeType, artifact.StoragePath)
	}
	if source, err := store.GetMessage(ctx, db, "a1"); err != nil || source.ConversationID != "c1" {
		t.Fatalf("artifact source message is not readable: source=%+v err=%v", source, err)
	}

	blocks := []SummaryBlock{{Media: []CompactionMediaRef{
		{Kind: "attachment", ID: "owned-file"},
		{Kind: "attachment", ID: "foreign-file"},
		{Kind: "artifact", ID: artifact.ID, MessageID: "a1"},
	}}}
	history := []UnifiedMessage{
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "summary marker"}}},
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "continue"}}},
	}
	orchestrator := &Orchestrator{db: db, uploadDir: root, artifactDir: root}
	orchestrator.injectCompactionMedia(ctx, "u1", "c1", history, blocks, true)

	images := make([]UnifiedBlock, 0)
	for _, block := range history[1].Blocks {
		if block.Kind == "image" {
			images = append(images, block)
		}
	}
	if len(images) != 2 {
		t.Fatalf("hydrated images = %+v, want owned attachment + owned artifact", images)
	}
	for _, image := range images {
		if image.MimeType != "image/png" || image.Data != base64.StdEncoding.EncodeToString(png) {
			t.Fatalf("image was not byte-sniffed and hydrated: %+v", image)
		}
	}

	forgedHistory := []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "continue"}}}}
	orchestrator.injectCompactionMedia(ctx, "u1", "c1", forgedHistory, []SummaryBlock{{Media: []CompactionMediaRef{
		{Kind: "artifact", ID: artifact.ID, MessageID: "wrong-message"},
	}}}, true)
	if len(forgedHistory[0].Blocks) != 2 || !strings.Contains(forgedHistory[0].Blocks[0].Text, "omitted") {
		t.Fatalf("artifact with a forged source message was hydrated: %+v", forgedHistory[0].Blocks)
	}

	nonVision := []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "continue"}}}}
	orchestrator.injectCompactionMedia(ctx, "u1", "c1", nonVision, blocks, false)
	if len(nonVision[0].Blocks) != 2 || !strings.Contains(nonVision[0].Blocks[0].Text, "compacted_media") {
		t.Fatalf("text-only model did not receive compacted media metadata: %+v", nonVision[0].Blocks)
	}
}

func TestMergeCompactionMediaRefsKeepsAllUniqueReferences(t *testing.T) {
	blocks := []SummaryBlock{
		{Media: []CompactionMediaRef{{Kind: "attachment", ID: "1"}, {Kind: "attachment", ID: "2"}}},
		{Media: []CompactionMediaRef{{Kind: "attachment", ID: "1"}, {Kind: "artifact", ID: "3"}, {Kind: "attachment", ID: "4"}, {Kind: "attachment", ID: "5"}}},
	}
	got := mergeCompactionMediaRefs(blocks)
	want := []string{"2", "1", "3", "4", "5"}
	if len(got) != len(want) {
		t.Fatalf("merged media = %+v, want ids %v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("merged media = %+v, want ids %v", got, want)
		}
	}
}

func TestSelectCompactionMediaRefsPreservesOrderAndDoesNotDropMetadata(t *testing.T) {
	refs := []CompactionMediaRef{
		{Kind: "attachment", ID: "old"},
		{Kind: "attachment", ID: "new"},
	}
	got := selectCompactionMediaRefsForHydration(refs, 1)
	if len(got) != len(refs) || got[0].ID != "old" || got[1].ID != "new" {
		t.Fatalf("hydration selection dropped persisted references: %+v", got)
	}
}

func TestCompactionMediaMetadataIsBoundedPerField(t *testing.T) {
	previous := compactionMetadataTokens
	compactionMetadataTokens = 32
	t.Cleanup(func() { compactionMetadataTokens = previous })

	oversized := strings.Repeat("very-long-image-name ", 400) + "UNBOUNDED_MEDIA_SUFFIX"
	text := compactionMediaMetadataText([]CompactionMediaRef{{
		Kind: "attachment", ID: "image-1", MessageID: "message-1",
		Filename: oversized, MimeType: "image/png",
	}}, true)
	if !strings.Contains(text, "compacted_media omitted") || !strings.Contains(text, "very-long-image-name") {
		t.Fatalf("media metadata lost useful fields: %q", text)
	}
	if strings.Contains(text, "UNBOUNDED_MEDIA_SUFFIX") {
		t.Fatalf("media metadata exceeded its per-field cap: %q", text)
	}
	if estimateTokens(text) > compactionMetadataLimit()*4 {
		t.Fatalf("media metadata exceeded aggregate cap: %d", estimateTokens(text))
	}
}

func TestCompactionMediaMetadataHonorsSmallAggregateBudget(t *testing.T) {
	previous := compactionMetadataTokens
	compactionMetadataTokens = 2
	t.Cleanup(func() { compactionMetadataTokens = previous })

	refs := make([]CompactionMediaRef, 0, 16)
	for i := 0; i < 16; i++ {
		refs = append(refs, CompactionMediaRef{
			Kind: "attachment", ID: fmt.Sprintf("image-%d", i), MessageID: fmt.Sprintf("message-%d", i),
		})
	}
	text := compactionMediaMetadataText(refs, true)
	if got := estimateTokens(text); got > compactionMetadataLimit()*4 {
		t.Fatalf("media metadata exceeded configured small aggregate cap: got %d, cap %d, text=%q", got, compactionMetadataLimit()*4, text)
	}
}

func TestCompactionMediaMetadataKeepsNewestReferenceWhenMarkerNeedsRoom(t *testing.T) {
	previous := compactionMetadataTokens
	compactionMetadataTokens = 16
	t.Cleanup(func() { compactionMetadataTokens = previous })

	text := compactionMediaMetadataText([]CompactionMediaRef{
		{Kind: "attachment", ID: "old-image", MessageID: "old-message"},
		{Kind: "attachment", ID: "middle-image", MessageID: "middle-message"},
		{Kind: "attachment", ID: "new-image", MessageID: "new-message"},
	}, false)
	if !strings.Contains(text, "new-image") {
		t.Fatalf("newest media reference was evicted before marker: %q", text)
	}
	if strings.Contains(text, "old-image") || !strings.Contains(text, "omitted=1") {
		t.Fatalf("old media reference should be the one omitted: %q", text)
	}
}
