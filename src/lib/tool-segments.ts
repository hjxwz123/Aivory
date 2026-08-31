/** Official / my-tools segment. The "mine" segment is exactly the `usermcp:`
 * id namespace, parsed on the first colon so ids cannot spoof their source. */
export type ToolSegment = 'official' | 'mine'

const USER_MCP_SOURCE = 'usermcp'

export function toolSegmentOf(id: string): ToolSegment {
  const separator = id.indexOf(':')
  return separator > 0 && id.slice(0, separator) === USER_MCP_SOURCE ? 'mine' : 'official'
}

export function toolsInSegment<T extends { id: string }>(tools: readonly T[], segment: ToolSegment): T[] {
  return tools.filter((tool) => toolSegmentOf(tool.id) === segment)
}

/** Count against the global allowed set, never the visible segment, so changing
 * segments cannot hide a chosen id from the selection summary. */
export function countSelectedTools(selectedIds: ReadonlySet<string>, allowedIds: readonly string[]): number {
  return allowedIds.filter((id) => selectedIds.has(id)).length
}
