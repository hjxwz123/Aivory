export type ToolMode = 'auto' | 'disabled' | 'enabled'
export type ModelToolMode = 'native' | 'prompt' | 'none'

export function isToolMode(value: unknown): value is ToolMode {
  return value === 'auto' || value === 'disabled' || value === 'enabled'
}

export interface ToolModeCapabilities {
  available: boolean
}

/** Stable order for tool modes that the current model actually supports. */
export const TOOL_MODE_MENU_ORDER: readonly ToolMode[] = ['auto', 'enabled', 'disabled']

export function toolModeAvailable(mode: ToolMode, capabilities: ToolModeCapabilities): boolean {
  if (mode === 'enabled') return capabilities.available
  return true
}

/** Unsupported policies are omitted from user menus instead of being rendered
 * as disabled rows. This keeps the control limited to actions that can work. */
export function visibleToolModes(capabilities: ToolModeCapabilities): ToolMode[] {
  return TOOL_MODE_MENU_ORDER.filter((mode) => toolModeAvailable(mode, capabilities))
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

/** A model-level `none` policy is authoritative for every tool family,
 * including provider-native tools that may remain in an old saved definition. */
export function resolveModelToolModeCapabilities(
  modelToolMode: ModelToolMode | null | undefined,
  capabilities: ToolModeCapabilities,
): ToolModeCapabilities {
  return modelAllowsToolModeSelection(modelToolMode) ? capabilities : { available: false }
}

/**
 * Resolves the account-level default while preserving choices made by clients
 * that predate the three-state tool mode. A missing legacy value was the old
 * implicit default, so it becomes the new default (`auto`); explicit legacy
 * booleans remain explicit user choices. Accounts without either setting use
 * the deployment default supplied by the authenticated profile.
 */
export function resolveDefaultToolMode(
  settings: Record<string, unknown> | null | undefined,
  inheritedDefault?: unknown,
): ToolMode {
  if (isToolMode(settings?.tool_mode_default)) return settings.tool_mode_default
  // Retired hosted-only mode now means the complete administrator-configured
  // collection. This also migrates existing account settings on hydration.
  if (settings?.tool_mode_default === 'official') return 'enabled'
  if (settings?.disable_tools_default === true) return 'disabled'
  if (settings?.disable_tools_default === false) return 'enabled'
  if (isToolMode(inheritedDefault)) return inheritedDefault
  if (inheritedDefault === 'official') return 'enabled'
  return 'auto'
}
