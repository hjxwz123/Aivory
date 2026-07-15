export type ToolMode = 'auto' | 'disabled' | 'enabled'

export function isToolMode(value: unknown): value is ToolMode {
  return value === 'auto' || value === 'disabled' || value === 'enabled'
}

/**
 * Resolves the account-level default while preserving choices made by clients
 * that predate the three-state tool mode. A missing legacy value was the old
 * implicit default, so it becomes the new default (`auto`); explicit legacy
 * booleans remain explicit user choices.
 */
export function resolveDefaultToolMode(settings: Record<string, unknown> | null | undefined): ToolMode {
  if (isToolMode(settings?.tool_mode_default)) return settings.tool_mode_default
  if (settings?.disable_tools_default === true) return 'disabled'
  if (settings?.disable_tools_default === false) return 'enabled'
  return 'auto'
}
