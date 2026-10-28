import { describe, expect, it } from 'vitest'
import { reasoningPreview } from '@/components/chat/reasoning-preview'

const toolLabel = (tool: { label?: string; name: string }) => tool.label || tool.name

describe('live reasoning preview', () => {
  it('shows the latest sentence without Markdown syntax or link destinations', () => {
    expect(reasoningPreview([{ kind: 'thinking', id: '1', text: '先比较路线。\n\n**查看[开放时间](https://example.test)**，再安排返程。' }], toolLabel))
      .toBe('查看开放时间，再安排返程。')
  })

  it('keeps the last actual text when an empty thinking item arrives', () => {
    expect(reasoningPreview([
      { kind: 'thinking', id: '1', text: 'Check the train timetable. Then compare journey times.' },
      { kind: 'thinking', id: '2', text: '  ' },
    ], toolLabel)).toBe('Then compare journey times.')
  })

  it('uses the newest tool label without falling back to stale reasoning', () => {
    expect(reasoningPreview([
      { kind: 'thinking', id: '1', text: 'Compare routes.' },
      { kind: 'tool', id: '2', tool: { id: 'search', name: 'web_search', label: '查询列车时刻', status: 'running', startedAt: 1 } },
    ], toolLabel)).toBe('查询列车时刻')
  })

  it('keeps the tail of a long streaming sentence and preserves Unicode characters', () => {
    const text = '🚆'.repeat(130) + '比较最新的交通方案'
    const result = reasoningPreview([{ kind: 'narration', id: '1', text }], toolLabel)
    expect(Array.from(result)).toHaveLength(121)
    expect(result).toMatch(/^…🚆/)
    expect(result.endsWith('比较最新的交通方案')).toBe(true)
  })
})
