// Package systemprompts owns the catalog and administrator overrides for authored
// prompts. Runtime data (messages, tool schemas, candidates) stays at call sites.
package systemprompts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"aivory/server/internal/store"
)

const SettingKey = "system_prompt_overrides"
const MaxPromptBytes = 64 * 1024
const MaxTotalBytes = 2 * 1024 * 1024

type Entry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Engine  string `json:"engine"`
	Locale  string `json:"locale,omitempty"`
	Default string `json:"default"`
	Format  string `json:"format"`
	Notes   string `json:"notes"`
	// Required placeholders are validated without interpreting arbitrary text as
	// a Go format string. Substitution uses literal strings.ReplaceAll only.
	Variables  []string `json:"variables,omitempty"`
	SettingKey string   `json:"-"` // legacy setting shared with its original page
	MaxBytes   int      `json:"max_bytes,omitempty"`
}

// Registration happens only during package initialization. Catalog returns
// copies so a response cannot mutate runtime defaults.
var entries = map[string]Entry{}

func Register(e Entry) {
	if e.ID == "" {
		panic("empty system prompt ID")
	}
	if _, exists := entries[e.ID]; exists {
		panic("duplicate system prompt: " + e.ID)
	}
	entries[e.ID] = e
}

func Catalog() []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		e.Variables = append([]string(nil), e.Variables...)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].ID < out[j].ID
	})
	return out
}

type Overrides map[string]string

func Normalize(raw json.RawMessage) (Overrides, error) {
	if len(raw) > MaxTotalBytes || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return nil, fmt.Errorf("prompt overrides must be a JSON object (max %d bytes)", MaxTotalBytes)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("invalid prompt overrides")
	}
	out := Overrides{}
	for id, value := range values {
		e, ok := entries[id]
		if !ok {
			return nil, fmt.Errorf("unknown prompt: %s", id)
		}
		var text string
		if string(value) == "null" || json.Unmarshal(value, &text) != nil {
			return nil, fmt.Errorf("%s must be a string", id)
		}
		limit := MaxPromptBytes
		if e.MaxBytes > 0 {
			limit = e.MaxBytes
		}
		if len(text) > limit {
			return nil, fmt.Errorf("%s exceeds %d bytes", id, limit)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		for _, variable := range e.Variables {
			if !strings.Contains(text, variable) {
				return nil, fmt.Errorf("%s must include %s", id, variable)
			}
		}
		// Preserve significant whitespace in fragments and translated headings.
		out[id] = text
	}
	return out, nil
}

func Load(db *sql.DB) Overrides {
	if db == nil {
		return nil
	}
	values := Overrides{}
	if raw, err := store.GetSetting(db, SettingKey); err == nil {
		_ = json.Unmarshal(raw, &values)
	}
	if values == nil {
		values = Overrides{}
	}
	for id, entry := range entries {
		if entry.SettingKey == "" {
			continue
		}
		delete(values, id)
		if raw, err := store.GetSetting(db, entry.SettingKey); err == nil {
			var text string
			if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
				values[id] = text
			}
		}
	}
	return values
}

func (o Overrides) Text(id string) string {
	if value := o[id]; strings.TrimSpace(value) != "" && len(value) <= MaxPromptBytes {
		return value
	}
	return entries[id].Default
}

func Text(db *sql.DB, id string) string { return Load(db).Text(id) }

type contextKey struct{}

func WithDB(ctx context.Context, db *sql.DB) context.Context {
	return context.WithValue(ctx, contextKey{}, Load(db))
}

func FromContext(ctx context.Context, id string) string {
	o, _ := ctx.Value(contextKey{}).(Overrides)
	return o.Text(id)
}

// Render performs literal replacement. Administrator text is never evaluated
// as code, a template program, or a printf format string.
func Render(text string, variables ...string) string {
	return strings.NewReplacer(variables...).Replace(text)
}
