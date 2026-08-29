package api

import (
	"database/sql"
	"encoding/json"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func normalizedDefaultToolMode(value string) (string, bool) {
	if value == llm.ToolModeOfficial {
		return llm.ToolModeEnabled, true
	}
	if validTurnToolMode(value) {
		return value, true
	}
	return "", false
}

func userDefaultToolMode(settings json.RawMessage) (string, bool) {
	values := map[string]json.RawMessage{}
	if len(settings) == 0 || json.Unmarshal(settings, &values) != nil {
		return "", false
	}
	if raw, ok := values["tool_mode_default"]; ok {
		var mode string
		if json.Unmarshal(raw, &mode) == nil {
			if normalized, valid := normalizedDefaultToolMode(mode); valid {
				return normalized, true
			}
		}
	}
	if raw, ok := values["disable_tools_default"]; ok {
		var disabled bool
		if json.Unmarshal(raw, &disabled) == nil {
			if disabled {
				return llm.ToolModeDisabled, true
			}
			return llm.ToolModeEnabled, true
		}
	}
	return "", false
}

func deploymentDefaultToolMode(db *sql.DB) string {
	if db != nil {
		if raw, err := store.GetSetting(db, "tool_mode_default"); err == nil {
			var mode string
			if json.Unmarshal(raw, &mode) == nil {
				if normalized, valid := normalizedDefaultToolMode(mode); valid {
					return normalized
				}
			}
		}
	}
	return llm.ToolModeAuto
}

func effectiveDefaultToolMode(db *sql.DB, settings json.RawMessage) string {
	if mode, configured := userDefaultToolMode(settings); configured {
		return mode
	}
	return deploymentDefaultToolMode(db)
}
