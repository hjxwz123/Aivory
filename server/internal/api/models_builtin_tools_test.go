package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"aivory/server/internal/store"
)

func TestEffectivePublicBuiltinTools(t *testing.T) {
	registered := []string{"image_generate", "python_execute", "web_search"}
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
			want:     []string{"image_generate", "web_search"},
		},
		{
			name:  "custom policy keeps registry order and drops stale names",
			model: store.Model{Kind: "chat", ToolMode: "native", BuiltinTools: json.RawMessage(`["web_search","removed","image_generate"]`)},
			want:  []string{"image_generate", "web_search"},
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
