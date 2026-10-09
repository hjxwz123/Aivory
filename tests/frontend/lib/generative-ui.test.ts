import { describe, expect, it } from 'vitest'
import { generativeLanguage, MAX_GENERATIVE_SOURCE, parseUIDocument } from '@/lib/generative-ui'

const parse = (blocks: unknown[]) => parseUIDocument(JSON.stringify({ version: 1, blocks }))
describe('generative UI protocol', () => {
  it('supports themed components and nested interactive sections', () => {
    expect(parse([{ type: 'tabs', items: [{ title: 'Usage', blocks: [{ type: 'chart', kind: 'line', labels: ['A', 'B'], series: [{ name: 'Tokens', values: [12, -2] }] }] }] }])?.blocks[0].type).toBe('tabs')
    expect(parse([{ type: 'table', columns: ['Name', 'Value'], rows: [['A', 1], [null, true]] }])?.blocks).toHaveLength(1)
  })
  it('supports rich table columns and mixed cell types without changing legacy tables', () => {
    const table = {
      type: 'table', title: 'Product comparison',
      columns: ['Product', { label: 'Photo', width: 'compact', align: 'center' }, { label: 'Details', width: 'wide' }, { label: 'Price', align: 'right' }, 'Link'],
      rows: [[
        { type: 'text', text: 'Camera', description: 'Travel kit' },
        { type: 'image', url: 'https://cdn.example.test/camera.jpg?size=large&signature=abc', alt: 'Camera', caption: 'Front view' },
        { type: 'list', items: ['Lightweight', 'Weather sealed'] },
        120,
        { type: 'link', url: 'https://example.test/product', text: 'Product page' },
      ]],
    }
    const result = parse([table])?.blocks[0]
    expect(result).toEqual(table)
    expect(parse([{ type: 'table', columns: ['A', 'B'], rows: [[null, false], ['Plain text', 0]] }])).not.toBeNull()
    expect(parse([{ type: 'tabs', items: [{ title: 'Products', blocks: [table] }] }])).not.toBeNull()
  })
  it('allows only absolute HTTP(S) URLs for table images and links', () => {
    for (const type of ['image', 'link']) {
      for (const url of ['http://images.example.test/a.png', 'https://images.example.test/a.png']) {
        expect(parse([{ type: 'table', columns: ['A'], rows: [[{ type, url, text: 'Open' }]] }])).not.toBeNull()
      }
      for (const url of ['javascript:alert(1)', 'data:image/svg+xml,<svg/>', 'file:///etc/passwd', 'blob:https://example.test/id', '//example.test/image.png', '/api/admin/delete', 'https://user:password@example.test/image.png', 'not a url']) {
        expect(parse([{ type: 'table', columns: ['A'], rows: [[{ type, url, text: 'Open' }]] }])).toBeNull()
      }
    }
  })
  it('rejects malformed rich cells, column styles and unbounded cell lists', () => {
    for (const value of [{ type: 'image', url: 42 }, { type: 'html', html: '<img>' }, { type: 'link', url: 'https://example.test' }, { type: 'text', text: [] }, { type: 'list', items: [1] }, { type: 'list', items: Array(21).fill('Item') }]) {
      expect(parse([{ type: 'table', columns: ['A'], rows: [[value]] }])).toBeNull()
    }
    for (const column of [{ label: 'A', align: 'justify' }, { label: 'A', width: 1000 }, { label: 'A', width: '100vw' }, { label: [] }]) {
      expect(parse([{ type: 'table', columns: [column], rows: [['A']] }])).toBeNull()
    }
    expect(parse([{ type: 'metrics', items: [{ label: 'Image', value: { type: 'image', url: 'https://example.test/a.png' } }] }])).toBeNull()
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
