export interface SkillDescriptionSource {
  description?: string | null
  display_description?: string | null
}

/** User-facing skill copy: administrator catalog text wins, with the standard
 * Agent Skill description (the "when to use" metadata) as the fallback. */
export function skillDisplayDescription(skill: SkillDescriptionSource): string {
  return skill.display_description?.trim() || skill.description?.trim() || ''
}
