/**
 * Quick helper for "initials" usage — derives 1-2 letters from a name.
 */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).slice(0, 2)
  return parts.map((p) => p[0]?.toUpperCase() ?? '').join('') || '?'
}
