package tools

import (
	"strings"
	"testing"

	"aivory/server/internal/store"
)

// sandboxUploadPath decides where a staged upload lands, and it is the single
// place where a folder upload keeps — or loses — its shape.
func TestSandboxUploadPath(t *testing.T) {
	t.Run("legacy flat upload keeps its filename", func(t *testing.T) {
		seen := map[string]bool{}
		if got := sandboxUploadPath(store.File{Filename: "photo.png"}, seen); got != "/workspace/uploads/photo.png" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("folder upload keeps its tree", func(t *testing.T) {
		seen := map[string]bool{}
		got := sandboxUploadPath(store.File{Filename: "main.ts", RelPath: "my-project/src/main.ts"}, seen)
		if got != "/workspace/uploads/my-project/src/main.ts" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("same basename in different directories both survive", func(t *testing.T) {
		seen := map[string]bool{}
		first := sandboxUploadPath(store.File{Filename: "index.ts", RelPath: "p/src/index.ts"}, seen)
		second := sandboxUploadPath(store.File{Filename: "index.ts", RelPath: "p/test/index.ts"}, seen)
		if first == second {
			t.Fatalf("both files staged at %q; the second overwrote the first", first)
		}
	})

	t.Run("a genuine same-path collision is renamed, not overwritten", func(t *testing.T) {
		seen := map[string]bool{}
		first := sandboxUploadPath(store.File{Filename: "a.txt", RelPath: "p/a.txt"}, seen)
		second := sandboxUploadPath(store.File{Filename: "a.txt", RelPath: "p/a.txt"}, seen)
		if first != "/workspace/uploads/p/a.txt" {
			t.Fatalf("first = %q", first)
		}
		if second != "/workspace/uploads/p/a-2.txt" {
			t.Fatalf("second = %q; want a -2 suffix", second)
		}
	})

	t.Run("a hostile rel_path degrades to the basename instead of escaping", func(t *testing.T) {
		for _, rel := range []string{"../evil.txt", "/etc/passwd", "../../../../etc/passwd"} {
			seen := map[string]bool{}
			got := sandboxUploadPath(store.File{Filename: "evil.txt", RelPath: rel}, seen)
			if !strings.HasPrefix(got, "/workspace/uploads/") {
				t.Fatalf("rel_path %q staged at %q", rel, got)
			}
			if strings.Contains(got, "..") {
				t.Fatalf("rel_path %q produced a traversal path %q", rel, got)
			}
		}
	})
}

// The folder manifest is what the model reads instead of walking a tree, so it
// must list only folder files, in a stable order, and be silent for a
// conversation with no folder upload at all.
func TestBuildFolderManifest(t *testing.T) {
	t.Run("empty for flat uploads", func(t *testing.T) {
		if got := buildFolderManifest([]store.File{{Filename: "a.pdf"}, {Filename: "b.png"}}); got != "" {
			t.Fatalf("got manifest for flat uploads: %q", got)
		}
		if got := buildFolderManifest(nil); got != "" {
			t.Fatalf("got manifest for no files: %q", got)
		}
	})

	t.Run("lists only folder files, sorted, with staged paths and sizes", func(t *testing.T) {
		manifest := buildFolderManifest([]store.File{
			{Filename: "loose.pdf"},
			{Filename: "main.ts", RelPath: "p/src/main.ts", SizeBytes: 12},
			{Filename: "readme.md", RelPath: "p/readme.md", SizeBytes: 340},
		})
		if !strings.Contains(manifest, "/workspace/uploads/p/readme.md") {
			t.Fatalf("manifest missing readme: %s", manifest)
		}
		if !strings.Contains(manifest, "/workspace/uploads/p/src/main.ts") {
			t.Fatalf("manifest missing nested file: %s", manifest)
		}
		if strings.Contains(manifest, "loose.pdf") {
			t.Fatalf("manifest listed a flat upload: %s", manifest)
		}
		if strings.Index(manifest, "p/readme.md") > strings.Index(manifest, "p/src/main.ts") {
			t.Fatalf("manifest is not sorted: %s", manifest)
		}
		if !strings.Contains(manifest, "340") {
			t.Fatalf("manifest omitted the file size: %s", manifest)
		}
	})

	t.Run("caps a huge folder and tells the model to enumerate", func(t *testing.T) {
		files := make([]store.File, 0, maxFolderManifestEntries+10)
		for i := 0; i < maxFolderManifestEntries+10; i++ {
			files = append(files, store.File{
				Filename: "f.txt", RelPath: "big/f.txt", SizeBytes: 1,
			})
		}
		// Distinct paths, or the map-based dedupe would collapse them.
		for i := range files {
			files[i].RelPath = "big/" + string(rune('a'+i%26)) + "-" + itoa(i) + ".txt"
		}
		manifest := buildFolderManifest(files)
		if !strings.Contains(manifest, "os.walk") {
			t.Fatalf("capped manifest does not tell the model to enumerate: %s", manifest[:200])
		}
		// Count FILE lines, not occurrences of the path prefix — the header
		// comment names the directory too.
		listed := 0
		for _, line := range strings.Split(manifest, "\n") {
			// Each entry is "<size>  <path>", so the path is not at column 0.
			if strings.Contains(line, "/workspace/uploads/") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				listed++
			}
		}
		if listed != maxFolderManifestEntries {
			t.Fatalf("listed %d entries; want %d", listed, maxFolderManifestEntries)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
