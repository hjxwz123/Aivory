package api

import "testing"

// normalizeTurnFlags enforces composer feature mutual-exclusion server-side:
// deep-research wins over no-tools; web search only applies inside a no-tools
// turn.
func TestNormalizeTurnFlags(t *testing.T) {
	cases := []struct {
		name              string
		mode              string
		noTools, webSrch  bool
		wantNoTools, want bool
	}{
		{"plain", "", false, false, false, false},
		{"no-tools only", "", true, false, true, false},
		{"no-tools + web", "", true, true, true, true},
		{"web without no-tools is dropped", "", false, true, false, false},
		{"deep-research wins over no-tools", "deep-research", true, true, false, false},
		{"deep-research plain", "deep-research", false, false, false, false},
	}
	for _, c := range cases {
		gotNoTools, gotWeb := normalizeTurnFlags(c.mode, c.noTools, c.webSrch)
		if gotNoTools != c.wantNoTools || gotWeb != c.want {
			t.Errorf("%s: normalizeTurnFlags(%q,%v,%v) = (%v,%v), want (%v,%v)",
				c.name, c.mode, c.noTools, c.webSrch, gotNoTools, gotWeb, c.wantNoTools, c.want)
		}
	}
}
