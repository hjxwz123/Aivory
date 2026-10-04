import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('../../../../src', import.meta.url))
const ADMIN_DIRS = ['pages/admin', 'components/admin']

function adminSources(): Array<{ file: string; text: string }> {
  return ADMIN_DIRS.flatMap((dir) =>
    readdirSync(join(SRC, dir))
      .filter((name) => name.endsWith('.tsx'))
      .map((name) => ({ file: `${dir}/${name}`, text: readFileSync(join(SRC, dir, name), 'utf8') })),
  )
}

/** Every `[^\s"'`]*-[Npx]` arbitrary value of one utility, with its file. */
function arbitraryPx(utility: 'rounded' | 'text'): Array<{ file: string; value: number }> {
  const pattern = new RegExp(`\\b${utility}-\\[(\\d+(?:\\.\\d+)?)px\\]`, 'g')
  return adminSources().flatMap(({ file, text }) =>
    Array.from(text.matchAll(pattern), (match) => ({ file, value: Number(match[1]) })),
  )
}

describe('admin console typography and shape', () => {
  it('keeps the editorial serif out of the console so it follows the Appearance font', () => {
    // Fraunces has no CJK glyphs, so serif headings fall back to Songti in
    // Chinese while the body is a sans face. The serif is for brand pages only.
    const offenders = adminSources().filter(({ text }) => /\bfont-serif\b/.test(text)).map(({ file }) => file)
    expect(offenders).toEqual([])
  })

  it('does not set console text below 12px', () => {
    const tooSmall = arbitraryPx('text').filter(({ value }) => value < 12)
    expect(tooSmall).toEqual([])
  })

  it('uses the shared radius scale: 6px detail, 8px control, 12px container', () => {
    const offScale = arbitraryPx('rounded').filter(({ value }) => ![6, 8, 12].includes(value))
    expect(offScale).toEqual([])
  })
})
