package store

import (
	"database/sql"
	_ "embed"
	"time"
)

//go:embed skills/generative-ui.md
var generativeUIInstructions string

//go:embed skills/legacy/generative-ui-v1.md
var legacyGenerativeUIInstructions string

const GenerativeUISkillID = "sk_generative_ui"
const CatalogSkillCommandPrefix = "catalog:"

const generativeUIDescription = "Proactively improve answers with useful comparisons, trends, plans, steps, or interactive what-if exploration. Infer the need from the conversation and answer content even when the user does not request visualization. Keep simple answers as prose."
const legacyGenerativeUIDescription = "Render useful visual answers, data comparisons, charts, steps, or self-contained interactive calculators directly in chat."

// Refresh only untouched shipped fields; preserve administrator customizations.
func seedBuiltinSkills(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO skills(id,name,description,display_description,icon,instructions,assets,enabled,sort_order,updated_at)
		VALUES(?,?,?,?,?,?,'[]',1,0,?) ON CONFLICT DO NOTHING`, GenerativeUISkillID, "generative-ui",
		generativeUIDescription,
		"Visual answers with charts, tables and interactive interfaces.", "PanelsTopLeft", generativeUIInstructions, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE skills SET
		description=CASE WHEN description=? THEN ? ELSE description END,
		instructions=CASE WHEN instructions=? THEN ? ELSE instructions END,
		updated_at=? WHERE id=? AND (description=? OR instructions=?)`,
		legacyGenerativeUIDescription, generativeUIDescription, legacyGenerativeUIInstructions, generativeUIInstructions,
		time.Now().Unix(), GenerativeUISkillID, legacyGenerativeUIDescription, legacyGenerativeUIInstructions)
	return err
}
