export interface ParsedSkillDocument {
  name?: string
  description?: string
  instructions: string
}

export const SKILL_NAME_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

function unquoteYamlScalar(value: string): string {
  const trimmed = value.trim()
  if (trimmed.length >= 2) {
    const first = trimmed[0]
    const last = trimmed[trimmed.length - 1]
    if ((first === '"' && last === '"') || (first === "'" && last === "'")) {
      return trimmed.slice(1, -1).trim()
    }
  }
  return trimmed
}

/** Parse the instruction-only subset of the Agent Skills SKILL.md format. */
export function parseSkillDocument(markdown: string): ParsedSkillDocument {
  const match = markdown.match(/^\s*---\s*\r?\n([\s\S]*?)\r?\n---\s*(?:\r?\n)?([\s\S]*)$/)
  if (!match) return { instructions: markdown.trim() }

  const frontmatter = match[1]
  const name = frontmatter.match(/^\s*name:\s*(.+)$/m)?.[1]
  const description = frontmatter.match(/^\s*description:\s*(.+)$/m)?.[1]
  return {
    name: name ? unquoteYamlScalar(name) : undefined,
    description: description ? unquoteYamlScalar(description) : undefined,
    instructions: match[2].trim(),
  }
}

export function isValidSkillName(name: string): boolean {
  const normalized = name.trim()
  return normalized.length > 0 && normalized.length <= 64 && SKILL_NAME_PATTERN.test(normalized)
}
