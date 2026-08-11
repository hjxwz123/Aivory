/** Preserve the wire contract: undefined means the complete available catalog,
 * while an explicit empty array means no candidate tools. An empty catalog
 * therefore must not make those two states collapse into each other. */
export function committedToolSelection(
  availableIds: readonly string[],
  selectedIds: ReadonlySet<string>,
  allSelected: boolean,
): string[] | undefined {
  const selected = availableIds.filter((id) => selectedIds.has(id))
  if (allSelected || (availableIds.length > 0 && selected.length === availableIds.length)) {
    return undefined
  }
  return selected
}
