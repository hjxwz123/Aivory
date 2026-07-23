import type { ApiModel, ApiOfficialToolDefinition } from '@/api/types'

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Sanitize a user-controlled tool-name array while preserving selection order. */
export function sanitizeOfficialToolNames(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const names: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') continue
    const name = item.trim()
    if (!name || seen.has(name)) continue
    seen.add(name)
    names.push(name)
  }
  return names
}

/**
 * Read the public model definition defensively. The string branch keeps the UI
 * usable during a rolling deploy where an older backend still returns the
 * retired `string[]` representation.
 */
export function officialToolsForModel(
  model: Pick<ApiModel, 'official_tools'> | null | undefined,
): ApiOfficialToolDefinition[] {
  const raw = model?.official_tools as unknown
  if (!Array.isArray(raw)) return []

  const tools: ApiOfficialToolDefinition[] = []
  const seen = new Set<string>()
  for (const item of raw) {
    const name = typeof item === 'string' ? item.trim() : isRecord(item) && typeof item.name === 'string' ? item.name.trim() : ''
    if (!name || seen.has(name)) continue
    const icon = isRecord(item) && typeof item.icon === 'string' ? item.icon.trim() : ''
    seen.add(name)
    tools.push({ name, icon })
  }
  return tools
}

/** Keep only names still allowed by the model, in administrator-defined order. */
export function filterOfficialToolNames(
  model: Pick<ApiModel, 'official_tools'> | null | undefined,
  selected: unknown,
): string[] {
  const wanted = new Set(sanitizeOfficialToolNames(selected))
  return officialToolsForModel(model)
    .map((tool) => tool.name)
    .filter((name) => wanted.has(name))
}

export function resolveDefaultOfficialToolNames(
  settings: Record<string, unknown> | null | undefined,
): string[] {
  return sanitizeOfficialToolNames(settings?.official_tool_names_default)
}

export function humanizeOfficialToolName(name: string): string {
  return name.trim().replace(/[._-]+/g, ' ').replace(/\s+/g, ' ')
}
