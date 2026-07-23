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
// User-facing controls remain declarative: users can only select among the
// model's declared mappings. Admin-only extra_params is applied separately by
// MergeRequestParams and never comes from a user request.
package llm

import (
	"encoding/json"
	"reflect"
	"strings"

	"aivory/server/internal/store"
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
	if len(controls) == 0 {
		return target
	}
	var defs []paramControl
	if err := json.Unmarshal(controls, &defs); err != nil {
		// Malformed — drop silently and return target as-is.
		return target
	}
	// Defaults are request semantics, not merely a frontend convenience. Resolve
	// them before evaluating show_if so mobile/API clients that never mount the
	// visual control produce the same request (and image quota projection) as the
	// desktop composer. Explicit client values always win over defaults.
	effectivePicks := make(map[string]any, len(picks)+len(defs))
	for key, value := range picks {
		effectivePicks[key] = value
	}
	for _, c := range defs {
		if _, exists := effectivePicks[c.Key]; !exists && c.Default != nil {
			effectivePicks[c.Key] = c.Default
		}
	}
	for _, c := range defs {
		if !paramControlVisible(c.ShowIf, effectivePicks) {
			continue
		}
		raw, ok := effectivePicks[c.Key]
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

func paramControlVisible(showIf map[string]any, picks map[string]any) bool {
	for key, expected := range showIf {
		if actual, exists := picks[key]; !exists || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

// MergeRequestParams builds an upstream request body with the required
// precedence: native provider fields > selected param-control fragments >
// admin extra_params. Every merge is recursive for object values, which keeps
// provider-owned nested fields such as Gemini generationConfig authoritative
// without discarding unrelated admin defaults.
func MergeRequestParams(native map[string]any, extraParams, controls json.RawMessage, picks map[string]any) map[string]any {
	body := store.MergeModelExtraParams(nil, extraParams)
	body = MergeParamControls(body, controls, picks)
	return store.DeepMergeJSONObjects(body, native)
}

func officialToolModeEnabled(req UnifiedChatRequest) bool {
	return req.ToolModeOfficial || len(req.OfficialToolNames) > 0 || len(req.OfficialToolRequests) > 0
}

// MergeOfficialToolRequests overlays the selected official-tool request
// fragments in order. Objects recurse, scalar/type conflicts use the later
// fragment, and arrays append. Array concatenation is essential for top-level
// provider `tools` arrays: selecting multiple hosted tools must retain every
// declaration instead of letting the last definition replace the earlier ones.
// Malformed legacy rows are ignored at runtime; admin normalization rejects
// them on write.
func MergeOfficialToolRequests(target map[string]any, requests []json.RawMessage) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for _, raw := range requests {
		var fragment map[string]any
		if err := json.Unmarshal(raw, &fragment); err != nil || fragment == nil {
			continue
		}
		deepMergeAppendingArrays(target, fragment)
	}
	return target
}

func deepMergeAppendingArrays(dst, src map[string]any) {
	for key, value := range src {
		if sourceObject, ok := value.(map[string]any); ok {
			if targetObject, ok := dst[key].(map[string]any); ok {
				deepMergeAppendingArrays(targetObject, sourceObject)
				continue
			}
			dst[key] = cloneOfficialRequestValue(sourceObject)
			continue
		}
		if sourceArray, ok := jsonArrayItems(value); ok {
			if targetArray, ok := jsonArrayItems(dst[key]); ok {
				combined := make([]any, 0, len(targetArray)+len(sourceArray))
				for _, item := range targetArray {
					combined = append(combined, cloneOfficialRequestValue(item))
				}
				for _, item := range sourceArray {
					combined = append(combined, cloneOfficialRequestValue(item))
				}
				dst[key] = combined
				continue
			}
			dst[key] = cloneOfficialRequestValue(sourceArray)
			continue
		}
		dst[key] = cloneOfficialRequestValue(value)
	}
}

// jsonArrayItems accepts the concrete slice shapes used by provider-native
// bodies as well as []any produced by JSON decoding. Raw JSON bytes are scalar
// payloads here, not arrays to concatenate.
func jsonArrayItems(value any) ([]any, bool) {
	switch value.(type) {
	case nil, json.RawMessage, []byte:
		return nil, false
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	items := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		items[i] = rv.Index(i).Interface()
	}
	return items, true
}

func cloneOfficialRequestValue(value any) any {
	if object, ok := value.(map[string]any); ok {
		copy := make(map[string]any, len(object))
		for key, item := range object {
			copy[key] = cloneOfficialRequestValue(item)
		}
		return copy
	}
	if items, ok := jsonArrayItems(value); ok {
		copy := make([]any, len(items))
		for i, item := range items {
			copy[i] = cloneOfficialRequestValue(item)
		}
		return copy
	}
	switch raw := value.(type) {
	case json.RawMessage:
		return append(json.RawMessage(nil), raw...)
	case []byte:
		return append([]byte(nil), raw...)
	default:
		return value
	}
}

// StripToolFields removes every provider tool declaration/control when native
// tools are disabled for a turn. Admin extra_params and param-control fragments
// must never resurrect tool calling in a no-tools or prompt-tool request.
func StripToolFields(body map[string]any, nativeToolsEnabled bool) map[string]any {
	if nativeToolsEnabled {
		return body
	}
	for _, key := range []string{
		"tools", "tool_choice", "toolChoice", "functions", "function_call", "functionCall",
		"parallel_tool_calls", "tool_config", "toolConfig",
	} {
		delete(body, key)
	}
	return body
}

// deepMerge writes every key from src into dst. When both sides hold a map at
// the same key it recurses; otherwise src replaces dst.
func deepMerge(dst, src map[string]any) {
	store.DeepMergeJSONObjects(dst, src)
}
