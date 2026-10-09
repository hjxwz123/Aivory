// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { buildInlineHTML, inlineHTMLSnapshot } from '@/lib/inline-html-document'

describe('inline HTML streaming', () => {
  it('keeps the existing preview while JavaScript or CSS is incomplete', () => {
    expect(inlineHTMLSnapshot('<p>Ready</p><script>const result =')).toBeNull()
    expect(inlineHTMLSnapshot('<style>body { color:')).toBeNull()
    expect(inlineHTMLSnapshot('<p>Ready</p><script')).toBeNull()
    const source = '<p>Ready</p><script>const result = 1;</script>'
    expect(inlineHTMLSnapshot(source)).toBe(source)
    expect(inlineHTMLSnapshot('<!-- <script>comment --> <p>Ready</p>')).not.toBeNull()
  })
  it('reports readiness and errors through isolated frames without granting network access', () => {
    const source = buildInlineHTML('<p>Hello</p>', 'channel', { '--color-surface': '#fff' })
    expect(source).toContain("connect-src 'none'")
    expect(source).toContain('unhandledrejection')
    expect(source).toContain('MutationObserver')
    expect(source).toContain('data.ready')
    expect(source).toContain('sandbox="allow-scripts"')
    expect(source).not.toContain('allow-same-origin')
  })
})
