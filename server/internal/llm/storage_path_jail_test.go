package llm

import (
	"os"
	"path/filepath"
	"testing"

	"aivory/server/internal/store"
)

func TestProviderImageReadRejectsOutOfRootAndSymlinkPaths(t *testing.T) {
	uploads := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	inside := filepath.Join(uploads, "inside.png")
	if err := os.WriteFile(inside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if data, mime, state := readVerifiedProviderImage(&store.File{
		Filename: "inside.png", Kind: "image", MimeType: "image/png", SizeBytes: int64(len(png)), StoragePath: inside,
	}, uploads); state != verifiedAttachmentImage || mime != "image/png" || len(data) != len(png) {
		t.Fatalf("valid in-root image state=%v mime=%q bytes=%d", state, mime, len(data))
	}

	if _, _, state := readVerifiedProviderImage(&store.File{
		Filename: "proc.png", Kind: "image", MimeType: "image/png", SizeBytes: 1, StoragePath: "/proc/self/environ",
	}, uploads); state != notAttachmentImage {
		t.Fatalf("/proc path state=%v, want notAttachmentImage", state)
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(uploads, "linked.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, state := readVerifiedProviderImage(&store.File{
		Filename: "linked.png", Kind: "image", MimeType: "image/png", SizeBytes: int64(len(png)), StoragePath: link,
	}, uploads); state != notAttachmentImage {
		t.Fatalf("symlink escape state=%v, want notAttachmentImage", state)
	}
}
