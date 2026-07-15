package api

import (
	"encoding/json"
	"testing"

	"aivory/server/internal/llm"
)

// normalizeTurnFlags enforces composer feature mutual-exclusion server-side:
// deep-research wins over disabled tools; web search only applies inside an
// explicitly disabled turn.
func TestNormalizeTurnFlags(t *testing.T) {
	cases := []struct {
		name           string
		mode, toolMode string
		webSearch      bool
		wantMode       string
		wantWebSearch  bool
	}{
		{"auto", "", llm.ToolModeAuto, false, llm.ToolModeAuto, false},
		{"enabled", "", llm.ToolModeEnabled, false, llm.ToolModeEnabled, false},
		{"disabled", "", llm.ToolModeDisabled, false, llm.ToolModeDisabled, false},
		{"disabled plus web", "", llm.ToolModeDisabled, true, llm.ToolModeDisabled, true},
		{"web with auto is dropped", "", llm.ToolModeAuto, true, llm.ToolModeAuto, false},
		{"web with enabled is dropped", "", llm.ToolModeEnabled, true, llm.ToolModeEnabled, false},
		{"deep-research wins over disabled", "deep-research", llm.ToolModeDisabled, true, llm.ToolModeEnabled, false},
		{"deep-research wins over auto", "deep-research", llm.ToolModeAuto, false, llm.ToolModeEnabled, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMode, gotWeb := normalizeTurnFlags(c.mode, c.toolMode, c.webSearch)
			if gotMode != c.wantMode || gotWeb != c.wantWebSearch {
				t.Fatalf("normalizeTurnFlags(%q,%q,%v) = (%q,%v), want (%q,%v)",
					c.mode, c.toolMode, c.webSearch, gotMode, gotWeb, c.wantMode, c.wantWebSearch)
			}
		})
	}
}

func TestResolveTurnToolModeCompatibilityAndPrecedence(t *testing.T) {
	raw := func(value string) json.RawMessage {
		encoded, _ := json.Marshal(value)
		return encoded
	}
	cases := []struct {
		name     string
		explicit json.RawMessage
		legacy   bool
		want     string
		wantErr  bool
	}{
		{"legacy omitted defaults enabled", nil, false, llm.ToolModeEnabled, false},
		{"legacy true disables", nil, true, llm.ToolModeDisabled, false},
		{"explicit auto", raw(llm.ToolModeAuto), false, llm.ToolModeAuto, false},
		{"explicit disabled wins over legacy false", raw(llm.ToolModeDisabled), false, llm.ToolModeDisabled, false},
		{"explicit enabled wins over legacy true", raw(llm.ToolModeEnabled), true, llm.ToolModeEnabled, false},
		{"explicit empty is invalid", raw(""), false, "", true},
		{"unknown is invalid", raw("sometimes"), false, "", true},
		{"explicit null is invalid", json.RawMessage("null"), false, "", true},
		{"explicit boolean is invalid", json.RawMessage("true"), false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTurnToolMode(tc.explicit, tc.legacy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}
