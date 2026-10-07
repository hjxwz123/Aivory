export function matchesAdminSearch(query: string, fields: (string | number | undefined | null)[]): boolean {
  const normalize = (value: string) => value.normalize('NFKC').toLocaleLowerCase()
  const tokens = normalize(query).trim().split(/\s+/).filter(Boolean)
  if (!tokens.length) return true
  const text = normalize(fields.filter((field) => field != null).join(' '))
  return tokens.every((token) => text.includes(token))
}

// Filtered sorting moves only visible slots and keeps the latest row objects.
export function mergeVisibleAdminOrder<T extends { id: string }>(allItems: T[], visibleOrder: T[]): T[] {
  const byId = new Map(allItems.map((item) => [item.id, item]))
  const ids = [...new Set(visibleOrder.map((item) => item.id))].filter((id) => byId.has(id))
  const visibleIds = new Set(ids)
  let position = 0
  return allItems.map((item) => visibleIds.has(item.id) ? byId.get(ids[position++])! : item)
}
