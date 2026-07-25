export interface ComposerCommandQuery {
  query: string
  from: number
  to: number
}

export const MAX_SELECTED_USER_SKILLS = 5

/**
 * Finds the slash token immediately before a collapsed cursor. Positions are
 * ProseMirror document positions; textBeforeCursor is the current textblock's
 * text, with inline atoms represented by one replacement character.
 */
export function findComposerCommandQuery(
  textBeforeCursor: string,
  textblockStart: number,
  cursorPosition: number,
): ComposerCommandQuery | null {
  if (!Number.isInteger(textblockStart) || !Number.isInteger(cursorPosition)) return null
  if (textblockStart < 0 || cursorPosition < textblockStart) return null
  const match = textBeforeCursor.match(/(?:^|\s)\/([^\s/]*)$/)
  if (!match) return null
  const query = match[1] ?? ''
  const slashOffset = textBeforeCursor.length - query.length - 1
  return {
    query,
    from: textblockStart + slashOffset,
    to: cursorPosition,
  }
}

interface UserSkillIdentity {
  id: string
}

/** Add one skill while preserving order, uniqueness, and the server's cap. */
export function addSelectedUserSkill<T extends UserSkillIdentity>(current: T[], skill: T): T[] {
  const id = skill.id.trim()
  if (!id || current.some((item) => item.id.trim() === id)) return current
  if (current.length >= MAX_SELECTED_USER_SKILLS) return current
  return [...current, skill]
}

/** Build the optional per-turn wire field from selected skill records. */
export function selectedUserSkillIdsForRequest(
  skills: readonly UserSkillIdentity[],
): string[] | undefined {
  return normalizeSelectedUserSkillIds(skills.map((skill) => skill.id))
}

/** Normalize the final wire field for every caller, including non-UI sends. */
export function normalizeSelectedUserSkillIds(
  selected: readonly string[] | undefined,
): string[] | undefined {
  const ids: string[] = []
  const seen = new Set<string>()
  for (const rawID of selected ?? []) {
    const id = rawID.trim()
    if (!id || seen.has(id)) continue
    seen.add(id)
    ids.push(id)
    if (ids.length === MAX_SELECTED_USER_SKILLS) break
  }
  return ids.length > 0 ? ids : undefined
}
