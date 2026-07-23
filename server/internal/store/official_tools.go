package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aivory/server/internal/envcfg"
)

// ErrOfficialToolsInvalid is returned when a model's hosted-tool configuration
// is not an array of definitions. Each definition is provider-agnostic: request
// is a complete JSON body fragment merged into the provider's upstream request.
var ErrOfficialToolsInvalid = errors.New("official_tools must be a JSON array of tool definitions")

// OfficialToolDefinition describes one provider-hosted tool exposed by a model.
// Icon is a symbolic UI icon name. Request deliberately remains raw JSON because
// hosted-tool schemas are owned by each upstream provider, not by the store.
type OfficialToolDefinition struct {
	Name    string          `json:"name"`
	Icon    string          `json:"icon"`
	Request json.RawMessage `json:"request"`
}

// DefaultOpenAIResponsesOfficialTools returns the hosted tools that were
// historically hard-coded by the OpenAI Responses provider. A fresh slice and
// fresh request buffers are returned so callers may safely customize them.
func DefaultOpenAIResponsesOfficialTools() []OfficialToolDefinition {
	searchContextSize := envcfg.Str("AIVORY_LLM_OFFICIAL_TOOL_SPEC", "medium")
	webSearchRequest, _ := json.Marshal(map[string]any{
		"tools": []map[string]any{{
			"type":                "web_search",
			"search_context_size": searchContextSize,
		}},
	})
	return []OfficialToolDefinition{
		{
			Name:    "web_search",
			Icon:    "search",
			Request: webSearchRequest,
		},
		{
			Name:    "code_interpreter",
			Icon:    "terminal",
			Request: json.RawMessage(`{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`),
		},
		{
			Name:    "image_generation",
			Icon:    "image",
			Request: json.RawMessage(`{"tools":[{"type":"image_generation"}]}`),
		},
	}
}

// DefaultOpenAIResponsesOfficialToolsJSON returns the canonical persisted form
// of DefaultOpenAIResponsesOfficialTools.
func DefaultOpenAIResponsesOfficialToolsJSON() json.RawMessage {
	raw, _ := json.Marshal(DefaultOpenAIResponsesOfficialTools())
	return json.RawMessage(raw)
}

// ParseOfficialTools accepts both the current definition array and the legacy
// string array. Legacy known OpenAI names expand to their historical request
// objects; any other legacy name receives a generic tools-array body fragment.
func ParseOfficialTools(raw json.RawMessage) ([]OfficialToolDefinition, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return []OfficialToolDefinition{}, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, ErrOfficialToolsInvalid
	}

	definitions := make([]OfficialToolDefinition, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		definition, err := parseOfficialToolDefinition(item)
		if err != nil {
			return nil, fmt.Errorf("%w: item %d: %v", ErrOfficialToolsInvalid, index+1, err)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate name %q", ErrOfficialToolsInvalid, definition.Name)
		}
		seen[definition.Name] = struct{}{}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

// NormalizeOfficialTools validates and compacts a model's hosted-tool config.
// An omitted value means no hosted tools. Legacy string arrays are upgraded to
// the canonical object representation as part of normalization.
func NormalizeOfficialTools(raw json.RawMessage) (json.RawMessage, error) {
	definitions, err := ParseOfficialTools(raw)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(definitions)
	if err != nil {
		return nil, ErrOfficialToolsInvalid
	}
	return json.RawMessage(normalized), nil
}

func parseOfficialToolDefinition(raw json.RawMessage) (OfficialToolDefinition, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return OfficialToolDefinition{}, errors.New("definition is empty")
	}
	if raw[0] == '"' {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return OfficialToolDefinition{}, errors.New("legacy name must be a string")
		}
		return legacyOfficialToolDefinition(name)
	}

	var definition OfficialToolDefinition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return OfficialToolDefinition{}, errors.New("definition must contain only name, icon, and request")
	}
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Icon = strings.TrimSpace(definition.Icon)
	if definition.Name == "" {
		return OfficialToolDefinition{}, errors.New("name is required")
	}
	request, err := normalizeOfficialToolRequest(definition.Request)
	if err != nil {
		return OfficialToolDefinition{}, err
	}
	definition.Request = request
	return definition, nil
}

func legacyOfficialToolDefinition(name string) (OfficialToolDefinition, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return OfficialToolDefinition{}, errors.New("name is required")
	}
	for _, definition := range DefaultOpenAIResponsesOfficialTools() {
		if definition.Name == name {
			definition.Request = append(json.RawMessage(nil), definition.Request...)
			return definition, nil
		}
	}
	request, _ := json.Marshal(map[string]any{"tools": []map[string]string{{"type": name}}})
	return OfficialToolDefinition{Name: name, Icon: "wrench", Request: request}, nil
}

func normalizeOfficialToolRequest(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("request is required")
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(raw, &request); err != nil || request == nil {
		return nil, errors.New("request must be a JSON object")
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("request must be a JSON object")
	}
	return json.RawMessage(normalized), nil
}

// migrateOfficialToolDefinitions upgrades valid legacy string arrays in place.
// Malformed historical rows are left untouched so a bad admin-only value never
// prevents the server from starting; subsequent admin writes validate strictly.
func migrateOfficialToolDefinitions(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, official_tools FROM models`)
	if err != nil {
		if isMissingTableErr(err) || isMissingColumnErr(err) {
			return nil
		}
		return err
	}
	type update struct {
		id, raw string
	}
	updates := []update{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		normalized, err := NormalizeOfficialTools(json.RawMessage(raw))
		if err == nil && !bytes.Equal(bytes.TrimSpace([]byte(raw)), normalized) {
			updates = append(updates, update{id: id, raw: string(normalized)})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := db.Exec(`UPDATE models SET official_tools=? WHERE id=?`, item.raw, item.id); err != nil {
			return err
		}
	}
	return nil
}
