import { describe, expect, it } from 'vitest'
import { generativeLanguage, MAX_GENERATIVE_SOURCE, parseUIDocument } from '@/lib/generative-ui'

const parse = (blocks: unknown[]) => parseUIDocument(JSON.stringify({ version: 1, blocks }))
describe('generative UI protocol', () => {
  it('supports themed components and nested interactive sections', () => {
    expect(parse([{ type: 'tabs', items: [{ title: 'Usage', blocks: [{ type: 'chart', kind: 'line', labels: ['A', 'B'], series: [{ name: 'Tokens', values: [12, -2] }] }] }] }])?.blocks[0].type).toBe('tabs')
    expect(parse([{ type: 'table', columns: ['Name', 'Value'], rows: [['A', 1], [null, true]] }])?.blocks).toHaveLength(1)
  })
  it('rejects malformed, incomplete, oversized and unsupported data', () => {
    for (const source of ['{', '{"version":2,"blocks":[]}', 'a'.repeat(MAX_GENERATIVE_SOURCE + 1)]) expect(parseUIDocument(source)).toBeNull()
    expect(parse([{ type: 'script', code: 'alert(1)' }])).toBeNull()
    expect(parse([{ type: 'chart', kind: 'line', labels: ['A'], series: [{ name: 'X', values: [1, 2] }] }])).toBeNull()
    expect(parse([{ type: 'table', columns: ['A'], rows: [['a', 'b']] }])).toBeNull()
    expect(parse([{ type: 'metrics', items: [{ label: 'A', value: { html: '<img>' } }] }])).toBeNull()
  })
  it('bounds recursion and total rendering work', () => {
    let blocks: unknown[] = [{ type: 'text', text: 'leaf' }]
    for (let i = 0; i < 6; i++) blocks = [{ type: 'accordion', items: [{ title: 'Nested', blocks }] }]
    expect(parse(blocks)).toBeNull()
    expect(parse(Array.from({ length: 33 }, () => ({ type: 'text', text: 'a' })))).toBeNull()
    expect(parse([{ type: 'chart', kind: 'bar', labels: ['A'], series: [{ name: 'X', values: [1e20] }] }])).toBeNull()
  })
  it('only recognizes explicit generative fences', () => {
    expect(generativeLanguage('AIVORY-UI extra')).toBe('aivory-ui')
    expect(generativeLanguage('aivory-html')).toBe('aivory-html')
    expect(generativeLanguage('html')).toBeNull()
    expect(generativeLanguage('json')).toBeNull()
  })
})
