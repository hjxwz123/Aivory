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

func effectiveDefaultToolMode(db *sql.DB) string {
	return deploymentDefaultToolMode(db)
}
