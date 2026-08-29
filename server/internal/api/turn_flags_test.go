package api

import (
	"encoding/json"
	"reflect"
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

func TestParseSelectedToolIDsOmittedEmptyAndValidation(t *testing.T) {
	tests := []struct {
		name           string
		raw            json.RawMessage
		wantIDs        []string
		wantConfigured bool
		wantErr        bool
	}{
		{name: "omitted means all", raw: nil, wantConfigured: false},
		{name: "explicit empty means none", raw: json.RawMessage(`[]`), wantConfigured: true},
		{
			name: "normalizes whitespace and duplicates",
			raw:  json.RawMessage(`[" builtin:aivory_web_search ","mcp:rail","mcp:rail","hosted:web_search"]`),
			wantIDs: []string{
				"builtin:aivory_web_search", "mcp:rail", "hosted:web_search",
			},
			wantConfigured: true,
		},
		{name: "null is not omission", raw: json.RawMessage(`null`), wantConfigured: true, wantErr: true},
		{name: "object is invalid", raw: json.RawMessage(`{}`), wantConfigured: true, wantErr: true},
		{name: "unknown namespace is invalid", raw: json.RawMessage(`["plugin:test"]`), wantConfigured: true, wantErr: true},
		{name: "missing key is invalid", raw: json.RawMessage(`["mcp:"]`), wantConfigured: true, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotIDs, gotConfigured, err := parseSelectedToolIDs(test.raw)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, test.wantErr)
			}
			if gotConfigured != test.wantConfigured {
				t.Fatalf("configured=%v, want=%v", gotConfigured, test.wantConfigured)
			}
			if !test.wantErr && !reflect.DeepEqual(gotIDs, test.wantIDs) {
				t.Fatalf("ids=%v, want=%v", gotIDs, test.wantIDs)
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
		fallback string
		want     string
		wantErr  bool
	}{
		{"omitted uses administrator fallback", nil, false, llm.ToolModeDisabled, llm.ToolModeDisabled, false},
		{"omitted invalid fallback uses auto", nil, false, "sometimes", llm.ToolModeAuto, false},
		{"legacy true disables", nil, true, llm.ToolModeEnabled, llm.ToolModeDisabled, false},
		{"explicit auto", raw(llm.ToolModeAuto), false, llm.ToolModeDisabled, llm.ToolModeAuto, false},
		{"explicit disabled wins over fallback", raw(llm.ToolModeDisabled), false, llm.ToolModeEnabled, llm.ToolModeDisabled, false},
		{"explicit enabled wins over legacy true", raw(llm.ToolModeEnabled), true, llm.ToolModeDisabled, llm.ToolModeEnabled, false},
		{"legacy official maps to enabled", raw(llm.ToolModeOfficial), true, llm.ToolModeDisabled, llm.ToolModeEnabled, false},
		{"explicit empty is invalid", raw(""), false, llm.ToolModeAuto, "", true},
		{"unknown is invalid", raw("sometimes"), false, llm.ToolModeAuto, "", true},
		{"explicit null is invalid", json.RawMessage("null"), false, llm.ToolModeAuto, "", true},
		{"explicit boolean is invalid", json.RawMessage("true"), false, llm.ToolModeAuto, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTurnToolMode(tc.explicit, tc.legacy, tc.fallback)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}
