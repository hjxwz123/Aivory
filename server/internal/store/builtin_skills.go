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

//go:embed skills/legacy/generative-ui-v2.md
var previousGenerativeUIInstructions string

const GenerativeUISkillID = "sk_generative_ui"
const CatalogSkillCommandPrefix = "catalog:"

const generativeUIDescription = "Use helpful inline UI alongside prose to improve reading, comparison, and understanding. Choose presentation from the conversation, even when visualization is not explicitly requested."
const legacyGenerativeUIDescription = "Render useful visual answers, data comparisons, charts, steps, or self-contained interactive calculators directly in chat."
const previousGenerativeUIDescription = "Proactively improve answers with useful comparisons, trends, plans, steps, or interactive what-if exploration. Infer the need from the conversation and answer content even when the user does not request visualization. Keep simple answers as prose."

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
		description=CASE WHEN description IN (?,?) THEN ? ELSE description END,
		instructions=CASE WHEN instructions IN (?,?) THEN ? ELSE instructions END,
		updated_at=? WHERE id=? AND (description IN (?,?) OR instructions IN (?,?))`,
		legacyGenerativeUIDescription, previousGenerativeUIDescription, generativeUIDescription,
		legacyGenerativeUIInstructions, previousGenerativeUIInstructions, generativeUIInstructions,
		time.Now().Unix(), GenerativeUISkillID,
		legacyGenerativeUIDescription, previousGenerativeUIDescription, legacyGenerativeUIInstructions, previousGenerativeUIInstructions)
	return err
}
