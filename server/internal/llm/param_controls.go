// Package llm — paramControls deep-merge implements design.md §2.3-G.
//
// A model can declare `param_controls` (JSON array) describing UI controls
// (toggle / select). Each control declares a `map` from the user-picked value
// to a fragment of upstream request body. The orchestrator captures the
// user's choices as `params: {key: value, ...}` and the provider — before
// firing the upstream request — must deep-merge the matching fragments
// into its request body.
//
// Security:
//   - Keys outside the declared param_controls are silently dropped.
//   - Values not declared in the control's `options` (for select) or `map`
//     (for toggle) are silently dropped.
//   - The merge is shallow-recursive on JSON objects; arrays and scalars are
//     replaced (not concatenated) to keep behaviour deterministic.
//
// This is declarative — admins cannot inject arbitrary upstream parameters
// because the user-side only picks among declared keys.
package llm

import (
	"encoding/json"
	"strings"
)

// paramControl is the wire shape of one item in models.param_controls
// (§2.3-G). Label/Icon are UI-only (rendered by the frontend); the backend
// receives them for struct parity but only uses Key/Type/Map/Options.
type paramControl struct {
	Key     string                    `json:"key"`
	Type    string                    `json:"type"` // toggle | select
	Label   string                    `json:"label,omitempty"`
	Icon    string                    `json:"icon,omitempty"`
	Default any                       `json:"default,omitempty"`
	Map     map[string]map[string]any `json:"map,omitempty"`
	Options []paramControlOption      `json:"options,omitempty"`
	ShowIf  map[string]any            `json:"show_if,omitempty"`
}

type paramControlOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

// MergeParamControls deep-merges fragments from the declared param_controls
// (matching the user's picked values) into `target`. Returns target for
// convenience. Unknown keys or values are dropped.
func MergeParamControls(target map[string]any, controls json.RawMessage, picks map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	if len(controls) == 0 || len(picks) == 0 {
		return target
	}
	var defs []paramControl
	if err := json.Unmarshal(controls, &defs); err != nil {
		// Malformed — drop silently and return target as-is.
		return target
	}
	for _, c := range defs {
		raw, ok := picks[c.Key]
		if !ok {
			continue
		}
		var key string
		switch v := raw.(type) {
		case bool:
			if v {
				key = "on"
			} else {
				key = "off"
			}
		case string:
			key = v
		default:
			// Try to format other JSON scalars as string.
			b, _ := json.Marshal(v)
			key = strings.Trim(string(b), `"`)
		}
		switch c.Type {
		case "toggle":
			fragment := c.Map[key]
			if fragment != nil {
				deepMerge(target, fragment)
			}
		case "select":
			// For select, only honour values that are declared in either
			// options or directly in map.
			allowed := false
			for _, o := range c.Options {
				if o.Value == key {
					allowed = true
					break
				}
			}
			if !allowed {
				if _, ok := c.Map[key]; ok {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
			fragment := c.Map[key]
			if fragment != nil {
				deepMerge(target, fragment)
			}
		}
	}
	return target
}

// deepMerge writes every key from src into dst. When both sides hold a map at
// the same key it recurses; otherwise src replaces dst.
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		existing, has := dst[k]
		if !has {
			dst[k] = v
			continue
		}
		switch nv := v.(type) {
		case map[string]any:
			if em, ok := existing.(map[string]any); ok {
				deepMerge(em, nv)
				continue
			}
		}
		dst[k] = v
	}
}
