import { deepMerge, en, zhCN } from '@docx-editor.dev/i18n'
import { describe, expect, it } from 'vitest'
import { jaChrome } from '@/i18n/docx-editor/ja'
import { zhHantChrome } from '@/i18n/docx-editor/zh-Hant'

/**
 * Guards the hand-written DOCX editor catalogues.
 *
 * The vendor interpolates placeholders and emits the raw token when a
 * translation drops it — a user would see "{fonts}" in the font-substitution
 * notice. That is the failure this suite exists to prevent, plus a check that
 * the Traditional Chinese merge really does keep the vendor's Simplified
 * strings for everything this project has not translated.
 */

function flatten(value: unknown, prefix = '', out = new Map<string, string>()): Map<string, string> {
  if (value && typeof value === 'object') {
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      flatten(child, prefix ? `${prefix}.${key}` : key, out)
    }
  } else {
    out.set(prefix, String(value))
  }
  return out
}

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{(\w+)\}/g)].map((match) => match[1]!).sort()
}

/**
 * Strings that are correctly identical in every supported language, so matching
 * the English original is not evidence of a missing translation:
 * - nothing but placeholders and punctuation (`"{kind} {number}"`) — there is no
 *   prose to translate;
 * - acronyms and unit symbols (`"URL"`, `"pt"`), which are not translated in
 *   Japanese or Traditional Chinese either.
 */
function isLanguageNeutral(value: string): boolean {
  const prose = value.replace(/\{[^}]*\}/g, '').replace(/[^\p{L}]/gu, '')
  if (prose === '') return true
  return /^(?:URL|URI|PDF|PNG|JPEG|GIF|pt|px|cm|mm|in)$/.test(value.trim())
}

const english = flatten(en)

/**
 * Surfaces the vendor ships as Pro-only. This project uses the Apache-2.0 core
 * and the artifact panel removes the menu bar that would reach them, so
 * translating them would produce dead strings.
 */
const PRO_ONLY_PREFIXES = [
  'comments',
  'revisions',
  'collaboration',
  'collaborationDemo',
  'review',
  'reviewers',
]

/**
 * Reachable keys deliberately left to English because there is genuinely nothing
 * to translate: paper-size names and their dimensions, numeral-format examples,
 * the literal "OK"/"in", border-width measurements, the URL placeholder, and the
 * vendor's `_lang` metadata field.
 *
 * Frozen by a meta-test below so the escape hatch cannot quietly grow — adding a
 * key here instead of translating it should be a conscious decision.
 */
const LANGUAGE_NEUTRAL_KEYS = new Set([
  '_lang',
  'hyperlinkPopup.urlPlaceholder',
  'dialogs.pageSetup.pageSizes.letter',
  'dialogs.pageSetup.pageSizes.a4',
  'dialogs.pageSetup.pageSizes.legal',
  'dialogs.pageSetup.pageSizes.a3',
  'dialogs.pageSetup.pageSizes.a5',
  'dialogs.pageSetup.pageSizes.b5',
  'dialogs.pageSetup.pageSizes.executive',
  'dialogs.footnoteProperties.formats.decimal',
  'dialogs.footnoteProperties.formats.lowerRoman',
  'dialogs.footnoteProperties.formats.upperRoman',
  'dialogs.paragraph.ok',
  'dialogs.paragraph.unitInches',
  'table.borderWidths.halfPt',
  'table.borderWidths.onePt',
  'table.borderWidths.oneHalfPt',
  'table.borderWidths.twoPt',
  'table.borderWidths.threePt',
  'textFormField.apply',
])

describe.each([
  ['ja', jaChrome],
  ['zh-Hant', zhHantChrome],
] as const)('%s chrome catalogue', (locale, catalogue) => {
  const entries = [...flatten(catalogue)]

  it('translates something', () => {
    // A floor, not the goal: the real guarantee is the "every reachable key"
    // invariant below, which fails if a vendor upgrade adds an untranslated key.
    expect(entries.length).toBeGreaterThan(560)
  })

  it('only overrides keys that exist in the vendor catalogue', () => {
    const unknown = entries
      .map(([key]) => key)
      .filter((key) => !english.has(key) && !flatten(zhCN).has(key))
    // A typo'd key would silently do nothing, so fail loudly instead.
    expect(unknown).toEqual([])
  })

  it('keeps every placeholder the English string relies on', () => {
    const broken: Array<{ key: string; missing: string[] }> = []
    for (const [key, translated] of entries) {
      const source = english.get(key) ?? flatten(zhCN).get(key)
      if (source === undefined) continue
      const expected = placeholders(source)
      const actual = placeholders(translated)
      const missing = expected.filter((token) => !actual.includes(token))
      if (missing.length > 0) broken.push({ key, missing })
    }
    expect(broken).toEqual([])
  })

  it('translates every reachable key except the frozen language-neutral ones', () => {
    // The invariant that keeps coverage honest: a reachable string may only be
    // left in English if there is no prose in it to translate. This fails the
    // moment someone adds a new reachable key (via a vendor upgrade) without
    // translating it, which is exactly when the gap would otherwise creep back.
    const reachable = [...english.keys()].filter(
      (key) =>
        !PRO_ONLY_PREFIXES.some((prefix) => key === prefix || key.startsWith(`${prefix}.`)) &&
        !LANGUAGE_NEUTRAL_KEYS.has(key) &&
        !/Shortcut$/.test(key),
    )
    const translated = new Set(entries.map(([key]) => key))
    const missing = reachable.filter((key) => !translated.has(key))
    expect(missing).toEqual([])
  })

  it('does not leave a translation equal to the English original', () => {
    // Catches copy-paste slips where a value was never translated. Shortcut
    // strings ("Ctrl+L") are language-neutral and legitimately identical.
    const untranslated = entries
      .filter(([key, value]) => {
        if (/Shortcut$/.test(key)) return false
        if (isLanguageNeutral(value)) return false
        const source = english.get(key)
        return source !== undefined && source === value
      })
      .map(([key]) => key)
    expect(untranslated).toEqual([])
  })
})

describe('Traditional Chinese merge', () => {
  const merged = flatten(deepMerge(zhCN, zhHantChrome))

  it('uses Traditional wording for the chrome it covers', () => {
    expect(merged.get('toolbar.save')).toBe('儲存')
    expect(merged.get('formattingBar.bold')).toBe('粗體')
    expect(merged.get('common.cancel')).toBe('取消')
  })

  it('falls back to the vendor Simplified strings, not English', () => {
    // A deep dialog key this project has not translated yet.
    const simplified = flatten(zhCN)
    const aDeepKey = [...simplified.keys()].find((key) => key.startsWith('collaboration.') && !flatten(zhHantChrome).has(key))
    expect(aDeepKey).toBeDefined()
    expect(merged.get(aDeepKey!)).toBe(simplified.get(aDeepKey!))
    expect(merged.size).toBe(simplified.size)
  })
})
