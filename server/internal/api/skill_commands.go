package api

import (
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

// Composer-only metadata: direct catalog commands must never become editable
// fake rows in the personal/workspace library API.
func listSkillCommandsHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	workspaceID := libraryWorkspaceID(r)
	if !authorizeLibraryWorkspaceUse(d, w, r, workspaceID, libraryCapabilitySkill) {
		return
	}
	permissions, err := requestPermissions(d, r)
	if err != nil {
		writeError(w, http.StatusForbidden, errSkillGroupPermission)
		return
	}
	personal, err := store.ListUserSkillsScoped(r.Context(), d.DB, authUser(r).ID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	catalog, err := store.ListSkills(r.Context(), d.DB, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	commands := make([]store.UserSkill, 0, len(personal)+len(catalog))
	sources := map[string]bool{}
	for _, skill := range personal {
		if workspaceID == "" && !store.UserSkillPolicyAllows(permissions.Skills, skill) {
			continue
		}
		skill.Instructions = ""
		commands = append(commands, skill)
		if skill.SourceSkillID != "" {
			sources[skill.SourceSkillID] = true
		}
	}
	for _, skill := range catalog {
		if sources[skill.ID] || !store.ResourcePolicyAllows(permissions.Skills, skill.ID) {
			continue
		}
		commands = append(commands, store.UserSkill{ID: store.CatalogSkillCommandPrefix + skill.ID,
			Name: skill.Name, Description: strings.TrimSpace(skill.DisplayDescription), Icon: skill.Icon, SourceSkillID: skill.ID})
	}
	writeJSON(w, http.StatusOK, commands)
}
