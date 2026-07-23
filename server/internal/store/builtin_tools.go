package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrBuiltinToolsInvalid is returned when a model's local-tool allowlist is
// not a JSON array of non-empty tool names.
var ErrBuiltinToolsInvalid = errors.New("builtin_tools must be null or a JSON array of tool names")

// ParseBuiltinTools preserves the policy distinction needed for backwards
// compatibility: an absent/null value means every registered tool is allowed,
// while an explicit empty array means no local tools are allowed.
func ParseBuiltinTools(raw json.RawMessage) (names []string, configured bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, true, ErrBuiltinToolsInvalid
	}
	names = make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			return nil, true, fmt.Errorf("%w: item %d is empty", ErrBuiltinToolsInvalid, index+1)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, true, nil
}

// NormalizeBuiltinTools validates and compacts a model's local-tool policy.
// nil is deliberately retained for the default-all policy; [] remains a
// non-nil JSON array so it can represent an explicit deny-all policy.
func NormalizeBuiltinTools(raw json.RawMessage) (json.RawMessage, error) {
	names, configured, err := ParseBuiltinTools(raw)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}
	normalized, err := json.Marshal(names)
	if err != nil {
		return nil, ErrBuiltinToolsInvalid
	}
	return normalized, nil
}

// BuiltinToolAllowed applies one persisted policy. Invalid non-null data fails
// closed; only genuinely absent/null data receives the backwards-compatible
// default-all behavior.
func BuiltinToolAllowed(raw json.RawMessage, name string) bool {
	names, configured, err := ParseBuiltinTools(raw)
	if err != nil {
		return false
	}
	if !configured {
		return true
	}
	name = strings.TrimSpace(name)
	for _, allowed := range names {
		if allowed == name {
			return true
		}
	}
	return false
}
