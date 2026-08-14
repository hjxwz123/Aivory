interface MCPServerAvailability {
  id: string
  enabled: boolean
  discovered_tools?: readonly unknown[]
}

function uniqueIDs(ids: readonly string[]): string[] {
  const seen = new Set<string>()
  const result: string[] = []
  for (const value of ids) {
    const id = value.trim()
    if (!id || seen.has(id)) continue
    seen.add(id)
    result.push(id)
  }
  return result
}

/** The runtime can only declare tools from globally enabled services with a
 * persisted discovery snapshot. A previous sync error does not invalidate the
 * last successful snapshot. */
export function isSelectableMCPServer(server: MCPServerAvailability): boolean {
  return server.enabled && Array.isArray(server.discovered_tools) && server.discovered_tools.length > 0
}

/** Resolve the model policy without discarding explicitly saved unavailable or
 * deleted IDs. `null` follows the live available-service catalog. */
export function resolveModelMCPServerIDs(
  configured: readonly string[] | null | undefined,
  availableIDs: readonly string[],
): string[] {
  return configured == null ? uniqueIDs(availableIDs) : uniqueIDs(configured)
}

/** Convert default-all into an explicit snapshot before the administrator
 * starts customizing it. */
export function materializeModelMCPServerIDs(
  configured: readonly string[] | null | undefined,
  availableIDs: readonly string[],
): string[] {
  return resolveModelMCPServerIDs(configured, availableIDs)
}

/** Toggle one service while keeping available services in catalog order and
 * preserving saved unavailable IDs until the administrator removes them. */
export function toggleModelMCPServerID(
  configured: readonly string[] | null | undefined,
  availableIDs: readonly string[],
  id: string,
): string[] {
  const normalizedID = id.trim()
  if (!normalizedID) return resolveModelMCPServerIDs(configured, availableIDs)

  const resolved = resolveModelMCPServerIDs(configured, availableIDs)
  const selected = new Set(resolved)
  if (selected.has(normalizedID)) selected.delete(normalizedID)
  else selected.add(normalizedID)

  const order = uniqueIDs([
    ...availableIDs,
    ...(configured ?? []).filter((savedID) => !availableIDs.includes(savedID)),
    normalizedID,
  ])
  return order.filter((candidate) => selected.has(candidate))
}

/** Replace only the currently available subset. Explicitly saved unavailable
 * IDs survive bulk "select all" operations; a deliberate clear should submit
 * `[]` directly. */
export function replaceAvailableModelMCPServerIDs(
  configured: readonly string[] | null | undefined,
  availableIDs: readonly string[],
  selectedAvailableIDs: readonly string[],
): string[] {
  const available = new Set(uniqueIDs(availableIDs))
  const selectedAvailable = new Set(uniqueIDs(selectedAvailableIDs))
  const preservedUnavailable = configured == null ? [] : uniqueIDs(configured).filter((id) => !available.has(id))

  return [...uniqueIDs(availableIDs).filter((id) => selectedAvailable.has(id)), ...preservedUnavailable]
}
