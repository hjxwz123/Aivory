export function workspaceSwitchDestination(
  previousWorkspaceId: string | null,
  nextWorkspaceId: string | null,
): '/' | null {
  return previousWorkspaceId === nextWorkspaceId ? null : '/'
}
