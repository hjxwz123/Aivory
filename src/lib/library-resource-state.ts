export type LibraryResourceKind = 'skill' | 'prompt' | 'mcp'

export type LibraryResourceAvailability = Record<LibraryResourceKind, boolean>

export function revokedLibraryResourceKinds(
  previous: LibraryResourceAvailability,
  current: LibraryResourceAvailability,
): LibraryResourceKind[] {
  return (['skill', 'prompt', 'mcp'] as const).filter((kind) => previous[kind] && !current[kind])
}
