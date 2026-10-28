import type { ReasoningItem, ToolCall } from '@/types/chat'

/** Use only text reported by the model or its current tool, never invented progress. */
export function reasoningPreview(items: ReasoningItem[], toolLabel: (tool: ToolCall) => string): string {
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index]
    if (item.kind === 'tool') {
      const label = toolLabel(item.tool).trim()
      if (label) return label
      continue
    }
    const plain = item.text
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/<[^>]*>/g, '')
      .replace(/^\s*(?:#{1,6}\s+|>\s*|[-*+]\s+)/gm, '')
      .replace(/[*_`~]/g, '')
      .trim()
    if (!plain) continue
    const segments = plain.split(/\n+|(?<=[。！？!?])\s*|(?<=\.)\s+/u).map((part) => part.trim()).filter(Boolean)
    const latest = (segments.at(-1) ?? plain).replace(/\s+/g, ' ')
    const characters = Array.from(latest)
    return characters.length > 120 ? `…${characters.slice(-120).join('')}` : latest
  }
  return ''
}
