import type { JSONContent } from '@tiptap/core'
import { splitMathContent, type MathContentSegment } from './math-content'

function textNode(value: string): JSONContent | null {
  return value ? { type: 'text', text: value } : null
}

function emptyParagraph(): JSONContent {
  return { type: 'paragraph' }
}

function inlineNodes(segments: MathContentSegment[]): JSONContent[] {
  return segments.flatMap((segment) => {
    if (segment.type === 'text') {
      const node = textNode(segment.value)
      return node ? [node] : []
    }
    if (segment.type === 'inline-math') {
      return [{ type: 'inlineMath', attrs: { latex: segment.value } }]
    }
    return []
  })
}

function appendInlineRun(
  blocks: JSONContent[],
  segments: MathContentSegment[],
  hasBlockBefore: boolean,
  hasBlockAfter: boolean,
): void {
  const source = segments.map((segment) => segment.raw).join('')

  if (!hasBlockBefore && !hasBlockAfter) {
    const content = inlineNodes(segments)
    blocks.push({ type: 'paragraph', content: content.length > 0 ? content : undefined })
    return
  }

  // Adjacent block nodes have one canonical separator. Extra newlines are
  // user-authored blank lines and therefore become real empty paragraphs.
  if (hasBlockBefore && hasBlockAfter && /^\n?$/.test(source)) return
  if (hasBlockBefore && hasBlockAfter && /^\n+$/.test(source)) {
    for (let index = 1; index < source.length; index += 1) blocks.push(emptyParagraph())
    return
  }

  // A terminal block always needs one empty caret paragraph. Additional empty
  // paragraphs encode trailing newlines without relying on invisible markers.
  if (hasBlockBefore && !hasBlockAfter && /^\n*$/.test(source)) {
    blocks.push(emptyParagraph())
    for (let index = 0; index < source.length; index += 1) blocks.push(emptyParagraph())
    return
  }

  const content = inlineNodes(segments)
  if (content.length > 0) blocks.push({ type: 'paragraph', content })
}

export function composerValueToDocument(value: string): JSONContent {
  const normalizedValue = value.replace(/\r\n?/g, '\n')
  const segments = splitMathContent(normalizedValue)
  const blocks: JSONContent[] = []
  let inlineRun: MathContentSegment[] = []
  let hasBlockBefore = false

  for (const segment of segments) {
    if (segment.type !== 'block-math') {
      inlineRun.push(segment)
      continue
    }

    appendInlineRun(blocks, inlineRun, hasBlockBefore, true)
    blocks.push({ type: 'blockMath', attrs: { latex: segment.value } })
    inlineRun = []
    hasBlockBefore = true
  }

  appendInlineRun(blocks, inlineRun, hasBlockBefore, false)
  return { type: 'doc', content: blocks }
}

function serializeInline(content: JSONContent[] | undefined): string {
  return (content ?? [])
    .map((node) => {
      if (node.type === 'text') return node.text ?? ''
      if (node.type === 'hardBreak') return '\n'
      if (node.type === 'inlineMath') return `\\(${String(node.attrs?.latex ?? '').trim()}\\)`
      return ''
    })
    .join('')
}

function terminalBlockCaretIndex(content: JSONContent[]): number {
  for (let index = 0; index < content.length - 1; index += 1) {
    if (content[index]?.type !== 'blockMath') continue
    if (content[index + 1]?.type !== 'paragraph') continue
    if (serializeInline(content[index + 1]?.content)) continue
    if (content.slice(index + 1).every((node) => (
      node.type === 'paragraph' && !serializeInline(node.content)
    ))) return index + 1
  }
  return -1
}

function emptyParagraphRunEndsAtBlock(content: JSONContent[], start: number): boolean {
  let index = start
  while (content[index]?.type === 'paragraph' && !serializeInline(content[index]?.content)) {
    index += 1
  }
  return content[index]?.type === 'blockMath'
}

function emptyParagraphRunStartsDocumentOrBlock(content: JSONContent[], end: number): boolean {
  let index = end
  while (content[index]?.type === 'paragraph' && !serializeInline(content[index]?.content)) {
    index -= 1
  }
  return index < 0 || content[index]?.type === 'blockMath'
}

function topLevelSeparator(content: JSONContent[], index: number): string {
  const previous = content[index - 1]
  const current = content[index]
  if (previous.type === current.type) return '\n'

  const currentIsEmptyParagraph = current.type === 'paragraph' && !serializeInline(current.content)
  if (
    previous.type === 'blockMath' &&
    currentIsEmptyParagraph &&
    emptyParagraphRunEndsAtBlock(content, index)
  ) return '\n'

  const previousIsEmptyParagraph = previous.type === 'paragraph' && !serializeInline(previous.content)
  if (
    previousIsEmptyParagraph &&
    current.type === 'blockMath' &&
    emptyParagraphRunStartsDocumentOrBlock(content, index - 1)
  ) return '\n'

  return ''
}

export function composerDocumentToValue(document: JSONContent): string {
  const content = document.content ?? []
  const structuralCaretIndex = terminalBlockCaretIndex(content)
  const serializableContent = structuralCaretIndex >= 0
    ? content.slice(0, structuralCaretIndex)
    : content
  let value = ''

  for (let index = 0; index < serializableContent.length; index += 1) {
    const node = serializableContent[index]
    if (index > 0) value += topLevelSeparator(serializableContent, index)
    if (node.type === 'paragraph') {
      value += serializeInline(node.content)
      continue
    }
    if (node.type === 'blockMath') {
      value += `\\[${String(node.attrs?.latex ?? '').trim()}\\]`
    }
  }

  if (structuralCaretIndex >= 0) {
    value += '\n'.repeat(content.length - structuralCaretIndex - 1)
  }
  return value.replace(/\r\n?/g, '\n')
}
