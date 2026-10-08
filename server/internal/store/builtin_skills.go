package store

import (
	"database/sql"
	_ "embed"
	"time"
)

//go:embed skills/generative-ui.md
var generativeUIInstructions string

const GenerativeUISkillID = "sk_generative_ui"
const CatalogSkillCommandPrefix = "catalog:"

// Insert once; upgrades preserve administrator edits, enablement and bindings.
func seedBuiltinSkills(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO skills(id,name,description,display_description,icon,instructions,assets,enabled,sort_order,updated_at)
		VALUES(?,?,?,?,?,?,'[]',1,0,?) ON CONFLICT DO NOTHING`, GenerativeUISkillID, "generative-ui",
		"Render useful visual answers, data comparisons, charts, steps, or self-contained interactive calculators directly in chat.",
		"Visual answers with charts, tables and interactive interfaces.", "PanelsTopLeft", generativeUIInstructions, time.Now().Unix())
	return err
}
