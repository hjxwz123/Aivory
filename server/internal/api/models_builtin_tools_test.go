package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"aivory/server/internal/store"
)

func TestResearchToolsAvailableIgnoresModelBuiltinSelection(t *testing.T) {
	searchAllowed := map[string]bool{"aivory_web_search": true}
	tests := []struct {
		name      string
		model     store.Model
		available map[string]bool
		want      bool
	}{
		{
			name:      "default selection",
			model:     store.Model{Kind: "chat", ToolMode: "native"},
			available: searchAllowed,
			want:      true,
		},
		{
			name:      "custom selection without web search still offers research",
			model:     store.Model{Kind: "chat", ToolMode: "native", BuiltinTools: json.RawMessage(`["python_execute"]`)},
			available: searchAllowed,
			want:      true,
		},
		{
			name:      "explicit empty selection still offers research",
			model:     store.Model{Kind: "chat", ToolMode: "prompt", BuiltinTools: json.RawMessage(`[]`)},
			available: searchAllowed,
			want:      true,
		},
		{
			name:      "global group or workspace ceiling removes it",
			model:     store.Model{Kind: "chat", ToolMode: "native"},
			available: map[string]bool{"python_execute": true},
			want:      false,
		},
		{
			name:      "model tool mode none removes it",
			model:     store.Model{Kind: "chat", ToolMode: "none"},
			available: searchAllowed,
			want:      false,
		},
		{
			name:      "non chat models never offer it",
			model:     store.Model{Kind: "image", ToolMode: "native"},
			available: searchAllowed,
			want:      false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := researchToolsAvailable(test.model, test.available); got != test.want {
				t.Fatalf("researchToolsAvailable() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEffectivePublicBuiltinTools(t *testing.T) {
	registered := []string{"aivory_web_search", "fetch_image", "image_generate", "python_execute"}
	tests := []struct {
		name     string
		model    store.Model
		disabled map[string]bool
		want     []string
	}{
		{
			name:  "default all follows live registry",
			model: store.Model{Kind: "chat", ToolMode: "native"},
			want:  registered,
		},
		{
			name:     "global disable is removed",
			model:    store.Model{Kind: "chat", ToolMode: "prompt"},
			disabled: map[string]bool{"python_execute": true},
			want:     []string{"aivory_web_search", "fetch_image", "image_generate"},
		},
		{
			name:  "custom policy keeps registry order and drops stale names",
			model: store.Model{Kind: "chat", ToolMode: "native", BuiltinTools: json.RawMessage(`["web_search","removed","image_generate"]`)},
			want:  []string{"aivory_web_search", "image_generate"},
		},
		{
			name:  "explicit empty policy disables all",
			model: store.Model{Kind: "chat", ToolMode: "native", BuiltinTools: json.RawMessage(`[]`)},
			want:  []string{},
		},
		{
			name:  "malformed policy fails closed",
			model: store.Model{Kind: "chat", ToolMode: "native", BuiltinTools: json.RawMessage(`{}`)},
			want:  []string{},
		},
		{
			name:  "model tool mode none exposes no local capability",
			model: store.Model{Kind: "chat", ToolMode: "none"},
			want:  []string{},
		},
		{
			name:  "non chat models expose no local capability",
			model: store.Model{Kind: "image", ToolMode: "native"},
			want:  []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := effectivePublicBuiltinTools(test.model, registered, test.disabled)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("effectivePublicBuiltinTools() = %v, want %v", got, test.want)
			}
		})
	}
}
