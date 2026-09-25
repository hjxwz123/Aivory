import { describe, expect, it } from 'vitest'

import {
  docmeeEnabledPatch,
  docmeeKeyProvided,
  docmeeSettingsPatch,
  SETTING_MASK,
  storedDocmeeEnabled,
} from '@/lib/aippt-admin-settings'

describe('storedDocmeeEnabled', () => {
  it('reads booleans, tolerates legacy string booleans, and reports unset', () => {
    expect(storedDocmeeEnabled({ docmee_enabled: true })).toBe(true)
    expect(storedDocmeeEnabled({ docmee_enabled: false })).toBe(false)
    expect(storedDocmeeEnabled({ docmee_enabled: 'true' })).toBe(true)
    expect(storedDocmeeEnabled({ docmee_enabled: 'false' })).toBe(false)
    expect(storedDocmeeEnabled({ docmee_enabled: null })).toBeNull()
    expect(storedDocmeeEnabled({})).toBeNull()
  })
})

describe('docmeeSettingsPatch', () => {
  it('saves only AI PPT fields and leaves the platform credit rate untouched', () => {
    const patch = docmeeSettingsPatch({
      docmee_api_key: 'sk-live',
      docmee_credits_per_ppt: 12,
      credits_per_usd: 20,
      settlement_currency: 'USD',
    }, false)
    expect(patch).toMatchObject({ docmee_api_key: 'sk-live', docmee_credits_per_ppt: 12, docmee_enabled: true })
    expect(patch).not.toHaveProperty('credits_per_usd')
    expect(patch).not.toHaveProperty('settlement_currency')
  })

  it('preserves a masked key without accidentally changing the enable switch', () => {
    const patch = docmeeSettingsPatch({ docmee_api_key: SETTING_MASK, docmee_enabled: false }, false)
    expect(patch.docmee_api_key).toBe(SETTING_MASK)
    expect(patch).not.toHaveProperty('docmee_enabled')
  })

  it('normalizes prices and integer limits without writing unrelated settings', () => {
    const patch = docmeeSettingsPatch({
      docmee_credits_per_ppt: -5,
      docmee_edit_credits: '2.5',
      docmee_max_upload_mb: '40.9',
      docmee_token_hours: -2,
      daily_message_limit: 200,
    }, false)
    expect(patch).toMatchObject({
      docmee_credits_per_ppt: 0,
      docmee_edit_credits: 2.5,
      docmee_max_upload_mb: 40,
      docmee_token_hours: 0,
    })
    expect(patch).not.toHaveProperty('daily_message_limit')
  })
})

describe('docmeeKeyProvided', () => {
  it('treats the display mask and blank fields as "no new key"', () => {
    expect(docmeeKeyProvided(SETTING_MASK)).toBe(false)
    expect(docmeeKeyProvided('')).toBe(false)
    expect(docmeeKeyProvided('   ')).toBe(false)
    expect(docmeeKeyProvided('sk-live-123')).toBe(true)
  })
})

describe('docmeeEnabledPatch', () => {
  it('activates the integration when the admin supplies a key and saves', () => {
    // The reported flow: paste the API key, click Save, refresh → must stay on.
    expect(docmeeEnabledPatch({ stored: null, keyInput: 'sk-live', touched: false })).toBe(true)
    // …even when an earlier save had frozen an explicit false into the database.
    expect(docmeeEnabledPatch({ stored: false, keyInput: 'sk-live', touched: false })).toBe(true)
  })

  it('never writes a derived false, so a save cannot switch the feature off', () => {
    // A save with an empty key field must not persist an explicit false: that is
    // what made an enabled integration report "disabled" after a refresh.
    expect(docmeeEnabledPatch({ stored: null, keyInput: '', touched: false })).toBeUndefined()
    // Same for echoing the mask back (the key is stored, the field is masked).
    expect(docmeeEnabledPatch({ stored: false, keyInput: SETTING_MASK, touched: false })).toBeUndefined()
    expect(docmeeEnabledPatch({ stored: true, keyInput: SETTING_MASK, touched: false })).toBeUndefined()
    // A deployment whose key comes from DOCMEE_API_KEY has a blank field: saving
    // unrelated settings must leave the flag (and the working feature) alone.
    expect(docmeeEnabledPatch({ stored: null, keyInput: '', touched: false })).toBeUndefined()
  })

  it('honours the switch the admin actually flipped', () => {
    expect(docmeeEnabledPatch({ stored: true, keyInput: SETTING_MASK, touched: true })).toBe(true)
    // Deliberately parking the integration must survive a later save.
    expect(docmeeEnabledPatch({ stored: false, keyInput: '', touched: true })).toBe(false)
    // Flipping the switch always writes a boolean into the draft first, so a
    // touched-but-null flag is a defensive case: fail to "off" rather than guess.
    expect(docmeeEnabledPatch({ stored: null, keyInput: '', touched: true })).toBe(false)
  })
})
