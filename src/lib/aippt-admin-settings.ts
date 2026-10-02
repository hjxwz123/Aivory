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

/** USD price settings and the legacy credit-denominated settings they replace. */
export const DOCMEE_PRICES = [
  { usdKey: 'docmee_price_per_ppt_usd', legacyKey: 'docmee_credits_per_ppt', legacyDefault: 10 },
  { usdKey: 'docmee_edit_price_usd', legacyKey: 'docmee_edit_credits', legacyDefault: 0 },
] as const

function finiteNumber(raw: unknown): number | undefined {
  const parsed = typeof raw === 'number' ? raw : typeof raw === 'string' && raw.trim() ? Number(raw) : NaN
  return Number.isFinite(parsed) ? parsed : undefined
}

/**
 * The USD price to show for an AI PPT price setting, mirroring the server: a
 * saved USD price wins; until one exists the legacy credit amount keeps
 * charging, shown as its USD equivalent at the current credit rate. Undefined
 * when there is nothing to show (no USD price and credits are off).
 */
export function docmeeDisplayPriceUSD(
  draft: Record<string, unknown>,
  price: (typeof DOCMEE_PRICES)[number],
): number | undefined {
  const usd = finiteNumber(draft[price.usdKey])
  if (usd !== undefined) return Math.max(0, usd)
  const ratio = finiteNumber(draft.credits_per_usd) ?? 0
  if (ratio <= 0) return undefined
  const credits = Math.max(0, finiteNumber(draft[price.legacyKey]) ?? price.legacyDefault)
  return credits / ratio
}

export function docmeeSettingsPatch(draft: Record<string, unknown>, touched: boolean): Record<string, unknown> {
  const readString = (key: string) => typeof draft[key] === 'string' ? draft[key] as string : ''
  const readNumber = (key: string, fallback: number, integer = false) => {
    const value = finiteNumber(draft[key]) ?? fallback
    return Math.max(0, integer ? Math.floor(value) : value)
  }
  const patch: Record<string, unknown> = {
    docmee_api_key: readString('docmee_api_key'),
    docmee_api_base_url: readString('docmee_api_base_url').trim(),
    docmee_default_template_id: readString('docmee_default_template_id').trim(),
    docmee_max_upload_mb: readNumber('docmee_max_upload_mb', 50, true),
    docmee_sdk_url: readString('docmee_sdk_url').trim(),
    docmee_domain: readString('docmee_domain').trim(),
    docmee_token_hours: readNumber('docmee_token_hours', 2, true),
  }
  // Prices are written only once they exist in the draft (saved before, or
  // edited now). Writing a derived $0 for an untouched legacy deployment would
  // silently make its decks free after a save while credits are off.
  for (const { usdKey } of DOCMEE_PRICES) {
    const raw = draft[usdKey]
    if (raw === undefined || raw === null || raw === '') continue
    patch[usdKey] = readNumber(usdKey, 0)
  }
  const enabled = docmeeEnabledPatch({
    stored: storedDocmeeEnabled(draft),
    keyInput: readString('docmee_api_key'),
    touched,
  })
  if (enabled !== undefined) patch.docmee_enabled = enabled
  return patch
}
