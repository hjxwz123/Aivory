import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const LOCALES = ['en', 'zh', 'zh-Hant', 'ja', 'fr'] as const
const REFERENCE = 'en'
const DIR = fileURLToPath(new URL('../../../src/i18n/locales', import.meta.url))

/**
 * i18next selects a plural form by suffixing the key. Locales with plural rules
 * store `key_one` / `key_other`; locales without them (zh, ja, …) store the bare
 * `key`. Both spellings carry the same message, so compare base names —
 * otherwise every pluralised string reads as a false mismatch.
 */
const PLURAL_SUFFIXES = ['_zero', '_one', '_two', '_few', '_many', '_other'] as const

function baseName(key: string): string {
  for (const suffix of PLURAL_SUFFIXES) {
    if (key.endsWith(suffix)) return key.slice(0, -suffix.length)
  }
  return key
}

function leafKeys(value: unknown, prefix = '', out = new Set<string>()): Set<string> {
  if (value && typeof value === 'object') {
    for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
      const path = prefix ? `${prefix}.${key}` : key
      if (child && typeof child === 'object') leafKeys(child, path, out)
      else out.add(baseName(path))
    }
  }
  return out
}

function load(locale: string, file: string): Set<string> {
  return leafKeys(JSON.parse(readFileSync(`${DIR}/${locale}/${file}`, 'utf8')))
}

/**
 * Pre-existing divergence, frozen so the list cannot grow silently.
 *
 * These four admin-only keys have real translations in `zh` but are absent from
 * the other four locales, whose users therefore see the caller's English
 * `defaultValue` instead of a translation. The fix is to translate them and
 * delete the entries here — deliberately not done as part of the document
 * preview/edit work, since it is an unrelated admin screen.
 */
const KNOWN_UNTRANSLATED = new Set([
  'models.fastCleared',
  'models.fastMarked',
  'models.fields.fastModel',
  'models.fields.fastModelHint',
])

const namespaces = readdirSync(`${DIR}/${REFERENCE}`).filter((file) => file.endsWith('.json'))

function comparable(locale: string, file: string): string[] {
  return [...load(locale, file)].filter((key) => !KNOWN_UNTRANSLATED.has(key)).sort()
}

describe('i18n locale parity', () => {
  it('finds every namespace of the reference locale', () => {
    expect(namespaces.length).toBeGreaterThan(0)
  })

  it('keeps the known-untranslated list from growing', () => {
    // Guards the escape hatch itself: if this fails, a real divergence was
    // added to the list instead of being translated.
    expect([...KNOWN_UNTRANSLATED].sort()).toEqual([
      'models.fastCleared',
      'models.fastMarked',
      'models.fields.fastModel',
      'models.fields.fastModelHint',
    ])
  })

  it.each(namespaces)('%s carries the same messages in all five locales', (file) => {
    const namespace = file.replace(/\.json$/, '')
    const reference = comparable(REFERENCE, file)

    for (const locale of LOCALES) {
      if (locale === REFERENCE) continue
      const actual = new Set(comparable(locale, file))
      // Reported one locale at a time so a failure names the exact gap.
      expect({ locale, namespace, missing: reference.filter((key) => !actual.has(key)) }).toEqual({
        locale,
        namespace,
        missing: [],
      })
    }
  })
})
