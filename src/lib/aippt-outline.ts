/**
 * Pure helpers for the self-built AI PPT UI (§ AI PPT API mode).
 *
 * Kept out of the page so the outline parsing and the input-type contract can be
 * unit-tested: the vendor's Markdown rules are strict (exactly one `#`, `##` for
 * chapters, `###` for pages, `####` for paragraphs), and the UI mirrors them.
 */

/** Heading parsed out of a generated outline, in document order. */
export interface AiPPTHeading {
  /** Heading depth: 1..4 (0 marks body text and is not returned). */
  level: number
  text: string
  /** 1-based source line, for jump-to-line behaviour. */
  line: number
}

/**
 * Extract the heading skeleton of an outline. Fenced code blocks are skipped so
 * a `#` inside a snippet is not mistaken for the deck's title.
 */
export function parseAiPPTHeadings(markdown: string): AiPPTHeading[] {
  const headings: AiPPTHeading[] = []
  let fence: string | null = null
  const lines = markdown.split('\n')
  for (let index = 0; index < lines.length; index++) {
    const raw = lines[index]
    const trimmed = raw.trim()
    const fenceMatch = trimmed.match(/^(```+|~~~+)/)
    if (fenceMatch) {
      if (fence === null) fence = fenceMatch[1][0]
      else if (fenceMatch[1][0] === fence) fence = null
      continue
    }
    if (fence !== null) continue
    const match = trimmed.match(/^(#{1,6})\s+(.*)$/)
    if (!match) continue
    const level = match[1].length
    if (level > 4) continue
    const text = match[2].replace(/\s+#+\s*$/, '').trim()
    if (!text) continue
    headings.push({ level, text, line: index + 1 })
  }
  return headings
}

/** Vendor task types the self-built UI exposes, in menu order. */
export const AI_PPT_INPUT_TYPES = [1, 6, 5, 2, 7] as const
export type AiPPTInputType = (typeof AI_PPT_INPUT_TYPES)[number]

/** i18n key suffix for an input type's label/placeholder. */
export function aiPPTTypeKey(type: number): string {
  switch (type) {
    case 2:
      return 'upload'
    case 5:
      return 'url'
    case 6:
      return 'text'
    case 7:
      return 'markdown'
    default:
      return 'topic'
  }
}

/** Content length preferences the vendor accepts for generateContent. */
export const AI_PPT_LENGTHS = ['short', 'medium', 'long'] as const

/**
 * Structural warnings for an outline, following the vendor's Markdown rules
 * (exactly one `#`, at least one `##`). Empty means "ready to render".
 */
export function aiPPTOutlineWarnings(markdown: string): string[] {
  const headings = parseAiPPTHeadings(markdown)
  const warnings: string[] = []
  const titles = headings.filter((h) => h.level === 1)
  const chapters = headings.filter((h) => h.level === 2)
  const pages = headings.filter((h) => h.level === 3)
  if (titles.length === 0) warnings.push('missing-title')
  if (titles.length > 1) warnings.push('multiple-titles')
  if (chapters.length === 0) warnings.push('missing-chapters')
  if (pages.length === 0) warnings.push('missing-pages')
  return warnings
}

/** First level-1 heading, used as the deck's default display name. */
export function aiPPTOutlineSubject(markdown: string): string {
  const title = parseAiPPTHeadings(markdown).find((h) => h.level === 1)
  return title ? title.text.slice(0, 60) : ''
}

/** Compact page/chapter counters for the outline header. */
export function aiPPTOutlineStats(markdown: string): {
  chapters: number
  pages: number
  paragraphs: number
} {
  const headings = parseAiPPTHeadings(markdown)
  return {
    chapters: headings.filter((h) => h.level === 2).length,
    pages: headings.filter((h) => h.level === 3).length,
    paragraphs: headings.filter((h) => h.level === 4).length,
  }
}
