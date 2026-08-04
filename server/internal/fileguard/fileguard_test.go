package fileguard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExistingConfinesRealPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nested", "inside.txt")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExisting(inside, root)
	if err != nil || resolved == "" {
		t.Fatalf("inside path rejected: resolved=%q err=%v", resolved, err)
	}

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveExisting(outside, root); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("outside path error = %v, want ErrOutsideRoot", err)
	}

	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveExisting(link, root); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("symlink escape error = %v, want ErrOutsideRoot", err)
	}
}

func TestRemapRejectsTraversalAndEmptySource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	good := filepath.Join(source, "user", "file.txt")
	got, err := Remap(source, target, good)
	if err != nil {
		t.Fatalf("remap valid path: %v", err)
	}
	want := filepath.Join(target, "user", "file.txt")
	if got != want {
		t.Fatalf("remapped path = %q, want %q", got, want)
	}
	if _, err := Remap(source, target, filepath.Join(source, "..", "secret")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("traversal error = %v, want ErrOutsideRoot", err)
	}
	if _, err := Remap("", target, "/proc/self/environ"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("empty-source error = %v, want ErrOutsideRoot", err)
	}
}

func TestPrepareWriteRejectsSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PrepareWrite(root, filepath.Join(root, "linked-dir", "escape.txt")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("symlink parent error=%v, want ErrOutsideRoot", err)
	}
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "target-link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWrite(root, link); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("symlink target error=%v, want ErrOutsideRoot", err)
	}
}
