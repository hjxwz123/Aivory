export type MathContentSegment =
  | { type: 'text'; value: string; raw: string }
  | { type: 'inline-math' | 'block-math'; value: string; raw: string }

function isEscaped(source: string, index: number): boolean {
  let slashCount = 0
  for (let i = index - 1; i >= 0 && source[i] === '\\'; i -= 1) slashCount += 1
  return slashCount % 2 === 1
}

function findUnescaped(source: string, needle: string, from: number): number {
  let index = source.indexOf(needle, from)
  while (index >= 0) {
    if (!isEscaped(source, index)) return index
    index = source.indexOf(needle, index + needle.length)
  }
  return -1
}

function backtickRunLength(source: string, start: number): number {
  let length = 0
  while (source[start + length] === '`') length += 1
  return length
}

function isIndentedCodeLine(source: string, index: number): boolean {
  if (index > 0 && source[index - 1] !== '\n') return false
  return source[index] === '\t' || source.startsWith('    ', index)
}

function markerRunLength(source: string, start: number, marker: '`' | '~'): number {
  let length = 0
  while (source[start + length] === marker) length += 1
  return length
}

function isFenceMarker(source: string, index: number, marker: '`' | '~'): boolean {
  const lineStart = source.lastIndexOf('\n', index - 1) + 1
  return /^ {0,3}$/.test(source.slice(lineStart, index)) && source.startsWith(marker.repeat(3), index)
}

function isWordCharacter(value: string): boolean {
  return Boolean(value) && /[\p{L}\p{N}_]/u.test(value)
}

function pushText(segments: MathContentSegment[], value: string): void {
  if (!value) return
  const previous = segments.at(-1)
  if (previous?.type === 'text') {
    previous.value += value
    previous.raw += value
    return
  }
  segments.push({ type: 'text', value, raw: value })
}

/**
 * Split a message into literal text and math without interpreting the rest of
 * Markdown. Code spans/fences stay literal, and currency-like dollar pairs are
 * deliberately ignored. The returned `raw` values always reconstruct source.
 */
export function splitMathContent(source: string): MathContentSegment[] {
  const segments: MathContentSegment[] = []
  let textStart = 0
  let index = 0
  let inlineSlashCloseExhausted = false
  let blockSlashCloseExhausted = false

  const flushText = (end: number) => {
    pushText(segments, source.slice(textStart, end))
  }

  while (index < source.length) {
    if (isIndentedCodeLine(source, index)) {
      const lineEnd = source.indexOf('\n', index)
      index = lineEnd < 0 ? source.length : lineEnd + 1
      continue
    }

    if (source[index] === '~' && isFenceMarker(source, index, '~')) {
      const run = markerRunLength(source, index, '~')
      const delimiter = '~'.repeat(run)
      const close = source.indexOf(delimiter, index + run)
      if (close >= 0) {
        index = close + run
        continue
      }
    }

    if (source[index] === '`' && !isEscaped(source, index)) {
      const ticks = backtickRunLength(source, index)
      const delimiter = '`'.repeat(ticks)
      const close = source.indexOf(delimiter, index + ticks)
      if (close >= 0) {
        index = close + ticks
        continue
      }
    }

    const slashDelimiter =
      source.startsWith('\\(', index) && !isEscaped(source, index)
        ? { close: '\\)', type: 'inline-math' as const }
        : source.startsWith('\\[', index) && !isEscaped(source, index)
          ? { close: '\\]', type: 'block-math' as const }
          : null

    if (slashDelimiter) {
      const closeExhausted = slashDelimiter.type === 'inline-math'
        ? inlineSlashCloseExhausted
        : blockSlashCloseExhausted
      if (closeExhausted) {
        index += 2
        continue
      }
      const close = findUnescaped(source, slashDelimiter.close, index + 2)
      if (close < 0) {
        if (slashDelimiter.type === 'inline-math') inlineSlashCloseExhausted = true
        else blockSlashCloseExhausted = true
      }
      const opener = source.slice(index, index + 2)
      let innermostOpen = index
      if (close >= 0) {
        let nestedOpen = findUnescaped(source, opener, innermostOpen + 2)
        while (nestedOpen >= 0 && nestedOpen < close) {
          innermostOpen = nestedOpen
          nestedOpen = findUnescaped(source, opener, innermostOpen + 2)
        }
      }
      if (innermostOpen !== index) {
        index = innermostOpen
        continue
      }
      if (close >= 0) {
        const value = source.slice(index + 2, close).trim()
        if (value) {
          flushText(index)
          const end = close + 2
          segments.push({ type: slashDelimiter.type, value, raw: source.slice(index, end) })
          index = end
          textStart = end
          continue
        }
      }
    }

    if (source.startsWith('$$', index) && !isEscaped(source, index)) {
      const close = findUnescaped(source, '$$', index + 2)
      if (close >= 0) {
        const value = source.slice(index + 2, close).trim()
        if (value) {
          flushText(index)
          const end = close + 2
          segments.push({ type: 'block-math', value, raw: source.slice(index, end) })
          index = end
          textStart = end
          continue
        }
      }
    }

    if (source[index] === '$' && !isEscaped(source, index) && source[index + 1] !== '$') {
      const previous = source[index - 1] ?? ''
      const next = source[index + 1] ?? ''
      // Single-dollar math is a legacy input format, so require Markdown-like
      // word boundaries. Without the closing boundary, common shell text such
      // as `Use $PATH:$HOME` is misread as a formula and gets rewritten.
      if (!isWordCharacter(previous) && next && !/\s|\d/.test(next)) {
        let close = index + 1
        while ((close = source.indexOf('$', close)) >= 0) {
          if (!isEscaped(source, close) && source[close + 1] !== '$') break
          close += 1
        }
        if (close >= 0) {
          const beforeClose = source[close - 1] ?? ''
          const afterClose = source[close + 1] ?? ''
          const value = source.slice(index + 1, close).trim()
          if (value && !/\s/.test(beforeClose) && !isWordCharacter(afterClose)) {
            flushText(index)
            const end = close + 1
            segments.push({ type: 'inline-math', value, raw: source.slice(index, end) })
            index = end
            textStart = end
            continue
          }
        }
      }
    }

    index += 1
  }

  flushText(source.length)
  return segments
}

export function hasMathContent(source: string): boolean {
  return splitMathContent(source).some((segment) => segment.type !== 'text')
}

export function mathContentToPlainText(source: string): string {
  return splitMathContent(source)
    .map((segment) => segment.value)
    .join('')
}
