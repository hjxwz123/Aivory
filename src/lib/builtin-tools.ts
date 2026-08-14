/** Resolve the administrator's default built-in tool selection. `null`/omitted
 * follows the live registry so newly registered tools default to selected. */
export function resolveBuiltinToolNames(
  configured: string[] | null | undefined,
  availableNames: string[],
): string[] {
  if (configured == null) return [...availableNames]
  const selected = new Set(configured)
  return availableNames.filter((name) => selected.has(name))
}

/** Toggle one tool while preserving registry order. The result is always an
 * explicit custom default selection; only "default all" emits null. */
export function toggleBuiltinToolName(
  configured: string[] | null | undefined,
  availableNames: string[],
  name: string,
): string[] {
  const selected = new Set(resolveBuiltinToolNames(configured, availableNames))
  if (selected.has(name)) selected.delete(name)
  else selected.add(name)
  return availableNames.filter((toolName) => selected.has(toolName))
}

/** Replace the selection for the currently visible subset without changing
 * the saved choice for registered tools hidden by a global availability rule. */
export function replaceVisibleBuiltinToolNames(
  configured: string[] | null | undefined,
  availableNames: string[],
  visibleNames: string[],
  selectedVisibleNames: string[],
): string[] {
  const selected = new Set(resolveBuiltinToolNames(configured, availableNames))
  const visible = new Set(visibleNames)
  const nextVisible = new Set(selectedVisibleNames)

  for (const name of visible) selected.delete(name)
  for (const name of nextVisible) {
    if (visible.has(name)) selected.add(name)
  }
  return availableNames.filter((name) => selected.has(name))
}

interface BuiltinToolCapabilityModel {
  tool_mode?: string | null
  builtin_tools?: string[] | null
}

/** Public model responses carry an exact default array. `null`/omitted is
 * retained as a compatibility fallback for older servers and admin model data,
 * where it means the registry-wide default. */
export function modelHasBuiltinTools(model: BuiltinToolCapabilityModel | null | undefined): boolean {
  if (!model || model.tool_mode === 'none') return false
  return Array.isArray(model.builtin_tools) ? model.builtin_tools.length > 0 : true
}

/** Whether one local tool is selected by the model default. Public responses
 * already account for global and group ceilings; older responses default all. */
export function modelSupportsBuiltinTool(
  model: BuiltinToolCapabilityModel | null | undefined,
  name: string,
): boolean {
  if (!modelHasBuiltinTools(model)) return false
  return Array.isArray(model?.builtin_tools) ? model.builtin_tools.includes(name) : true
}
