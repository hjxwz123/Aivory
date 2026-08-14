/** Preserve the wire contract: undefined means the serving model's current
 * defaults, while an explicit empty array means no candidate tools. */
export function committedToolSelection(
  availableIds: readonly string[],
  defaultIds: readonly string[],
  selectedIds: ReadonlySet<string>,
): string[] | undefined {
  const selected = availableIds.filter((id) => selectedIds.has(id))
  const defaults = availableIds.filter((id) => defaultIds.includes(id))
  if (selected.length === defaults.length && selected.every((id, index) => id === defaults[index])) {
    return undefined
  }
  return selected
}
