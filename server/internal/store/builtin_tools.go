package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrBuiltinToolsInvalid is returned when a model's local-tool allowlist is
// not a JSON array of non-empty tool names.
var ErrBuiltinToolsInvalid = errors.New("builtin_tools must be null or a JSON array of tool names")

const retiredKnowledgeBaseSearchTool = "search_knowledge_base"

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
		if name == retiredKnowledgeBaseSearchTool {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, true, nil
}

// migrateRetiredKnowledgeBaseSearchTool removes the former model-driven RAG
// tool from persisted policies. It is deliberately idempotent so old database
// files and restored backups are cleaned on every startup.
func migrateRetiredKnowledgeBaseSearchTool(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE conversations SET rag_mode='auto' WHERE lower(trim(rag_mode))=?`, "tool"); err != nil {
		return err
	}

	type modelPolicy struct {
		id  string
		raw string
	}
	rows, err := db.Query(`SELECT id, builtin_tools FROM models WHERE builtin_tools IS NOT NULL AND builtin_tools LIKE ?`, "%"+retiredKnowledgeBaseSearchTool+"%")
	if err != nil {
		return err
	}
	policies := []modelPolicy{}
	for rows.Next() {
		var policy modelPolicy
		if err := rows.Scan(&policy.id, &policy.raw); err != nil {
			_ = rows.Close()
			return err
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, policy := range policies {
		normalized, err := NormalizeBuiltinTools(json.RawMessage(policy.raw))
		if err != nil {
			continue
		}
		if string(normalized) == policy.raw {
			continue
		}
		if _, err := db.Exec(`UPDATE models SET builtin_tools=? WHERE id=?`, string(normalized), policy.id); err != nil {
			return err
		}
	}

	var raw string
	err = db.QueryRow(`SELECT value FROM settings WHERE key=?`, "disabled_tools").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var disabled []string
	if json.Unmarshal([]byte(raw), &disabled) != nil {
		return nil
	}
	filtered := make([]string, 0, len(disabled))
	changed := false
	for _, name := range disabled {
		if strings.TrimSpace(name) == retiredKnowledgeBaseSearchTool {
			changed = true
			continue
		}
		filtered = append(filtered, name)
	}
	if changed {
		return SetSetting(db, "disabled_tools", filtered)
	}
	return nil
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
