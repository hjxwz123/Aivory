let revision = 0

export function getUserSettingsRevision(): number {
  return revision
}

export function advanceUserSettingsRevision(): void {
  revision += 1
}
