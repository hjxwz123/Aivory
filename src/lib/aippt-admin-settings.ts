/**
 * Admin-form rules for the AI PPT (Docmee) settings block (§ AI PPT).
 *
 * These live outside the component because the enable flag is the one setting the
 * UI derives rather than reads: getting it wrong silently disables a configured
 * integration after a refresh. Keeping the decision pure makes it testable — see
 * tests/frontend/lib/aippt-admin-settings.test.ts.
 */

/**
 * The settings API answers secrets with this display mask and treats it as "keep
 * the stored value" on write (§ H-1). Re-sending it must never be mistaken for the
 * admin supplying a new key.
 */
export const SETTING_MASK = '••••••'

/** The stored AI PPT enable flag, or null when it has never been written. */
export function storedDocmeeEnabled(draft: Record<string, unknown>): boolean | null {
  const value = draft.docmee_enabled
  if (typeof value === 'boolean') return value
  if (value === 'true') return true
  if (value === 'false') return false
  return null
}

/** True when the form field carries a key the admin typed (the mask is not one). */
export function docmeeKeyProvided(keyInput: string): boolean {
  const key = keyInput.trim()
  return key !== '' && key !== SETTING_MASK
}

/**
 * The value a general Save should send for `docmee_enabled`, or undefined to leave
 * the stored value untouched.
 *
 * Never returns a *derived* `false`. A save performed while the key field was
 * empty used to persist an explicit `false`; because an explicit value outranks
 * the server's "unset follows the key" default, the integration then stayed off
 * across refreshes even after a key was configured. The same rule also protects
 * deployments whose key comes from `DOCMEE_API_KEY`, where the form field is
 * legitimately blank.
 */
export function docmeeEnabledPatch(options: {
  stored: boolean | null
  keyInput: string
  /** True when the admin flipped the enable switch in this session. */
  touched: boolean
}): boolean | undefined {
  if (options.touched) return options.stored ?? false
  if (docmeeKeyProvided(options.keyInput)) return true
  return undefined
}
