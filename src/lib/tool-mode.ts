export type ToolMode = 'auto' | 'disabled' | 'enabled' | 'official'
export type ModelToolMode = 'native' | 'prompt' | 'none'

export function isToolMode(value: unknown): value is ToolMode {
  return value === 'auto' || value === 'disabled' || value === 'enabled' || value === 'official'
}

export interface ToolModeCapabilities {
  builtin: boolean
  official: boolean
}

/** Stable menu order. Unsupported concrete modes stay visible but disabled so
 * the drill-down hierarchy does not shift between models. */
export const TOOL_MODE_MENU_ORDER: readonly ToolMode[] = ['auto', 'official', 'disabled', 'enabled']

export function toolModeAvailable(mode: ToolMode, capabilities: ToolModeCapabilities): boolean {
  if (mode === 'official') return capabilities.official
  if (mode === 'enabled') return capabilities.builtin
  return true
}

/** A persisted per-turn/default choice can outlive a model switch. Concrete
 * modes that the new model cannot provide fall back to automatic. */
export function normalizeToolModeForCapabilities(
  mode: ToolMode,
  capabilities: ToolModeCapabilities,
): ToolMode {
  return toolModeAvailable(mode, capabilities) ? mode : 'auto'
}

/** Whether a model exposes the per-turn tool policy to users. */
export function modelAllowsToolModeSelection(
  modelToolMode: ModelToolMode | null | undefined,
): boolean {
  // Missing values preserve compatibility with older model-list responses.
  return modelToolMode !== 'none'
}

/**
 * Resolves the account-level default while preserving choices made by clients
 * that predate the four-state tool mode. A missing legacy value was the old
 * implicit default, so it becomes the new default (`auto`); explicit legacy
 * booleans remain explicit user choices.
 */
export function resolveDefaultToolMode(settings: Record<string, unknown> | null | undefined): ToolMode {
  if (isToolMode(settings?.tool_mode_default)) return settings.tool_mode_default
  if (settings?.disable_tools_default === true) return 'disabled'
  if (settings?.disable_tools_default === false) return 'enabled'
  return 'auto'
}
