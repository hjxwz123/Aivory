package api

import "testing"

// TestUploadPolicyExtAliases locks in that equivalent extension spellings are
// accepted regardless of which one the admin listed — most importantly that a
// .jpeg photo passes when the allowlist only says "jpg" (a common config typo
// that previously blocked ordinary JPEG uploads).
func TestUploadPolicyExtAliases(t *testing.T) {
	exts := parseExtensionList("png,jpg,pdf,yml,md") // admin listed only "jpg"
	applyExtAliases(exts)
	p := uploadPolicy{AllowedExt: exts}

	allowed := []string{
		"photo.jpeg", "photo.jpg", "shot.JPE", "scan.jfif", // jpg family
		"a.png", "doc.pdf", "conf.yaml", "notes.markdown", // other aliases
	}
	for _, name := range allowed {
		if _, _, err := p.validateUpload(name); err != nil {
			t.Errorf("%q should be allowed, got %v", name, err)
		}
	}

	// Genuinely disallowed types still fail (aliases don't widen the surface).
	for _, name := range []string{"evil.exe", "x.svg", "a.zip"} {
		if _, _, err := p.validateUpload(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}
