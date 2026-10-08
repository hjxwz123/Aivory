package llm

import "aivory/server/internal/store"

func selectedAdminSkillIDs(skills []store.UserSkill) map[string]bool {
	ids := map[string]bool{}
	for _, skill := range skills {
		if skill.SourceSkillID != "" {
			ids[skill.SourceSkillID] = true
		}
	}
	return ids
}

func selectedSkillNames(skills []store.UserSkill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}
