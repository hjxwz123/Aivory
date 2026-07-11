package api

import (
	"errors"
	"testing"
)

func TestValidateSVG(t *testing.T) {
	clean := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M4 4h16v16H4z" fill="#0f172a"/></svg>`
	if err := validateSVG([]byte(clean)); err != nil {
		t.Errorf("clean svg should pass, got %v", err)
	}

	bad := map[string]string{
		"script":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"onload":        `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><path/></svg>`,
		"onclick path":  `<svg xmlns="http://www.w3.org/2000/svg"><path onclick="x()" d="M0 0"/></svg>`,
		"foreignObject": `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body>x</body></foreignObject></svg>`,
		"doctype/xxe":   `<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"js href":       `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><path/></a></svg>`,
	}
	for name, s := range bad {
		if err := validateSVG([]byte(s)); !errors.Is(err, errIconUnsafeSVG) {
			t.Errorf("%s: expected errIconUnsafeSVG, got %v", name, err)
		}
	}

	notSVG := map[string]string{
		"empty": ``,
		"plain": `hello world`,
		"html":  `<html><body>x</body></html>`,
	}
	for name, s := range notSVG {
		if err := validateSVG([]byte(s)); !errors.Is(err, errIconBadBytes) {
			t.Errorf("%s: expected errIconBadBytes, got %v", name, err)
		}
	}
}
