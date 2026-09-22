package api

import "testing"

// validateUploadRelPath is the boundary that stops a client-supplied folder path
// from becoming a sandbox escape or a malformed staged path.
func TestValidateUploadRelPath(t *testing.T) {
	accepted := []struct {
		raw  string
		want string
	}{
		{"a.txt", "a.txt"},
		{"src/a.txt", "src/a.txt"},
		{"my-project/src/app/main.ts", "my-project/src/app/main.ts"},
		// Windows separators are folded, which is what a desktop picker reports.
		{`my-project\src\main.ts`, "my-project/src/main.ts"},
		{"src/deep/nested/tree/file.txt", "src/deep/nested/tree/file.txt"},
		// A single leading "./" is normalised away rather than rejected.
		{"./src/a.txt", "src/a.txt"},
	}
	for _, tc := range accepted {
		got, ok := validateUploadRelPath(tc.raw)
		if !ok || got != tc.want {
			t.Errorf("validateUploadRelPath(%q) = (%q, %v); want (%q, true)", tc.raw, got, ok, tc.want)
		}
	}

	rejected := []string{
		"",
		"../evil.txt",
		"a/../../evil.txt",
		"/etc/passwd",
		"a//b.txt",
		"a/./b.txt",
		"a/b.txt/",
		"a/../b.txt",
		"..",
		".",
		"nul\x00byte.txt",
		"ctrl\x01char.txt",
		// A segment longer than the filename cap.
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.txt",
	}
	for _, raw := range rejected {
		if got, ok := validateUploadRelPath(raw); ok {
			t.Errorf("validateUploadRelPath(%q) = (%q, true); want rejected", raw, got)
		}
	}
}

// A deep-but-legal tree is accepted; an absurdly deep one is not.
func TestValidateUploadRelPathDepth(t *testing.T) {
	// Exactly the segment limit: maxFolderRelPathSegments segments.
	deep := "a"
	for i := 1; i < maxFolderRelPathSegments; i++ {
		deep += "/a"
	}
	if got, ok := validateUploadRelPath(deep); !ok || got != deep {
		t.Errorf("segment-limit path rejected: (%q, %v)", got, ok)
	}
	if _, ok := validateUploadRelPath(deep + "/a"); ok {
		t.Error("path beyond the segment limit was accepted")
	}
}

// folderRelativeName turns "folder + file rel_path" into the durable staged path,
// and must never double the folder prefix the picker already includes.
func TestFolderRelativeName(t *testing.T) {
	cases := []struct {
		folder, relPath, filename, want string
	}{
		{"", "", "a.txt", ""},
		{"p", "p/a.txt", "a.txt", "p/a.txt"},
		{"p", "a.txt", "a.txt", "p/a.txt"},
		{"p", "p/src/a.txt", "a.txt", "p/src/a.txt"},
		{"my-project", "my-project/src/main.ts", "main.ts", "my-project/src/main.ts"},
		// A folder upload whose picker reported no rel_path still lands inside
		// the folder rather than at the uploads root.
		{"p", "", "a.txt", "p/a.txt"},
	}
	for _, tc := range cases {
		if got := folderRelativeName(tc.folder, tc.relPath, tc.filename); got != tc.want {
			t.Errorf("folderRelativeName(%q, %q, %q) = %q; want %q", tc.folder, tc.relPath, tc.filename, got, tc.want)
		}
	}
}
