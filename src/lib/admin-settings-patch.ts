export type AdminSettings = Record<string, unknown>

/** Build a PATCH body for primitive settings owned by one admin page. */
export function changedAdminSettings(
  draft: AdminSettings,
  saved: AdminSettings,
  keys: readonly string[],
): AdminSettings {
  const patch: AdminSettings = {}
  for (const key of keys) {
    if (key in draft && !Object.is(draft[key], saved[key])) {
      patch[key] = draft[key]
    }
  }
  return patch
}
