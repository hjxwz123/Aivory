package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"aivory/server/internal/systemprompts"
)

var systemPromptWriteMu sync.Mutex

func adminSystemPromptsGet(d Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"prompts":          systemprompts.Catalog(),
		"overrides":        systemprompts.Load(d.DB),
		"max_prompt_bytes": systemprompts.MaxPromptBytes,
	})
}

// This endpoint accepts only changed IDs. Clearing a value removes its override;
// unrelated prompt edits are retained. Validation finishes before any writes.
func adminSystemPromptsSet(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Overrides json.RawMessage `json:"overrides"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	patch, err := systemprompts.Normalize(body.Overrides)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var provided map[string]string
	_ = json.Unmarshal(body.Overrides, &provided)
	systemPromptWriteMu.Lock()
	defer systemPromptWriteMu.Unlock()
	merged := systemprompts.Load(d.DB)
	for id := range provided {
		delete(merged, id)
		if text, ok := patch[id]; ok {
			merged[id] = text
		}
	}
	settingsPatch := map[string]json.RawMessage{}
	for _, entry := range systemprompts.Catalog() {
		if entry.SettingKey == "" {
			continue
		}
		if _, ok := provided[entry.ID]; ok {
			settingsPatch[entry.SettingKey], _ = json.Marshal(patch[entry.ID])
		}
		delete(merged, entry.ID)
	}
	raw, _ := json.Marshal(merged)
	if _, err := normalizeSystemPromptOverrides(raw); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settingsPatch[systemprompts.SettingKey] = raw
	if _, err := applyAdminSettingsPatch(r.Context(), d, settingsPatch, false, ""); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalidInput) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	broadcastConfigInvalidate(d)
	adminSystemPromptsGet(d, w, r)
}

func normalizeSystemPromptOverrides(raw json.RawMessage) (json.RawMessage, error) {
	values, err := systemprompts.Normalize(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidInput, err)
	}
	for _, entry := range systemprompts.Catalog() {
		if entry.SettingKey != "" {
			if _, ok := values[entry.ID]; ok {
				return nil, fmt.Errorf("%w: use %s for %s", errInvalidInput, entry.SettingKey, entry.ID)
			}
		}
	}
	// Empty prompts have already been removed by Normalize. Literal whitespace in
	// nonempty fragments is intentionally preserved.
	encoded, err := json.Marshal(values)
	if err != nil || strings.TrimSpace(string(encoded)) == "null" {
		return nil, errInvalidInput
	}
	return encoded, nil
}
