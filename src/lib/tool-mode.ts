export type ToolMode = 'auto' | 'disabled' | 'enabled'
export type ModelToolMode = 'native' | 'prompt' | 'none'

export function isToolMode(value: unknown): value is ToolMode {
  return value === 'auto' || value === 'disabled' || value === 'enabled'
}

export interface ToolModeCapabilities {
  available: boolean
  /** A workspace-wide tool policy can force the only legal mode to disabled. */
  forcedDisabled?: boolean
}

/** Stable order for tool modes that the current model actually supports. */
export const TOOL_MODE_MENU_ORDER: readonly ToolMode[] = ['auto', 'enabled', 'disabled']

export function toolModeAvailable(mode: ToolMode, capabilities: ToolModeCapabilities): boolean {
  if (capabilities.forcedDisabled) return mode === 'disabled'
  if (mode === 'enabled') return capabilities.available
  return true
}

/** Unsupported policies are omitted from user menus instead of being rendered
 * as disabled rows. This keeps the control limited to actions that can work. */
export function visibleToolModes(capabilities: ToolModeCapabilities): ToolMode[] {
  return TOOL_MODE_MENU_ORDER.filter((mode) => toolModeAvailable(mode, capabilities))
}

/** A persisted per-conversation choice can outlive a model switch. Concrete
 * modes that the new model cannot provide fall back to automatic. */
export function normalizeToolModeForCapabilities(
  mode: ToolMode,
  capabilities: ToolModeCapabilities,
): ToolMode {
  return toolModeAvailable(mode, capabilities) ? mode : capabilities.forcedDisabled ? 'disabled' : 'auto'
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
  return modelAllowsToolModeSelection(modelToolMode)
    ? capabilities
    : { available: false, forcedDisabled: capabilities.forcedDisabled }
}

/**
 * Resolves the administrator-controlled deployment default supplied by the
 * authenticated profile. User settings are deliberately not accepted here:
 * users may override tool mode only inside an individual conversation.
 */
export function resolveDefaultToolMode(inheritedDefault?: unknown): ToolMode {
  if (isToolMode(inheritedDefault)) return inheritedDefault
  return 'auto'
}
