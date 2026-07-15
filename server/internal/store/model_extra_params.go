package store

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrModelExtraParamsInvalid rejects anything other than a JSON object. Model
// extra_params are deliberately unrestricted by key, but their object shape is
// required so they can be safely deep-merged with provider-native fields.
var ErrModelExtraParamsInvalid = errors.New("extra_params must be a JSON object")

// NormalizeModelExtraParams validates a model's extra upstream parameters and
// returns a compact object representation. An omitted value means no extras.
func NormalizeModelExtraParams(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, ErrModelExtraParamsInvalid
	}
	normalized, err := json.Marshal(obj)
	if err != nil {
		return nil, ErrModelExtraParamsInvalid
	}
	return json.RawMessage(normalized), nil
}

// MergeModelExtraParams deep-merges a validated model's extra_params into
// target. Stored rows should always be valid; malformed legacy data is ignored
// rather than making a live request fail before reaching its provider.
func MergeModelExtraParams(target map[string]any, raw json.RawMessage) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return target
	}
	return DeepMergeJSONObjects(target, obj)
}

// DeepMergeJSONObjects overlays source onto target recursively for object
// values. Scalars and arrays replace their target values. Map/slice values are
// copied so a later provider-side adjustment cannot mutate a reusable source
// request template across tool-loop iterations.
func DeepMergeJSONObjects(target, source map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for key, value := range source {
		if sourceObj, ok := value.(map[string]any); ok {
			if targetObj, ok := target[key].(map[string]any); ok {
				DeepMergeJSONObjects(targetObj, sourceObj)
				continue
			}
			target[key] = cloneJSONObject(sourceObj)
			continue
		}
		target[key] = cloneJSONValue(value)
	}
	return target
}

func cloneJSONObject(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = cloneJSONValue(value)
	}
	return copy
}

func cloneJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneJSONObject(v)
	case []any:
		copy := make([]any, len(v))
		for i, item := range v {
			copy[i] = cloneJSONValue(item)
		}
		return copy
	case []map[string]any:
		copy := make([]map[string]any, len(v))
		for i, item := range v {
			copy[i] = cloneJSONObject(item)
		}
		return copy
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}
