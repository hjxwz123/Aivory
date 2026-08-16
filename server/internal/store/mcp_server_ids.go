package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrMCPServerIDsInvalid is returned when a model's default MCP selection is
// not a nullable JSON array of non-empty server ids.
var ErrMCPServerIDsInvalid = errors.New("mcp_server_ids must be null or a JSON array of MCP server ids")

// ParseMCPServerIDs preserves whether an administrator explicitly configured a
// model's MCP defaults. Absent/null and explicit [] both select no MCP service;
// the distinction is retained so the editor can display the default-off state.
func ParseMCPServerIDs(raw json.RawMessage) (ids []string, configured bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, true, ErrMCPServerIDsInvalid
	}
	ids = make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			return nil, true, fmt.Errorf("%w: item %d is empty", ErrMCPServerIDsInvalid, index+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, true, nil
}

// NormalizeMCPServerIDs validates and compacts a model's default MCP
// selection. Unknown or currently disabled ids are deliberately retained so a
// temporary service outage or administrative disable does not destroy model
// configuration.
func NormalizeMCPServerIDs(raw json.RawMessage) (json.RawMessage, error) {
	ids, configured, err := ParseMCPServerIDs(raw)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}
	normalized, err := json.Marshal(ids)
	if err != nil {
		return nil, ErrMCPServerIDsInvalid
	}
	return normalized, nil
}
