import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PrivateMarkdown } from '@/components/chat/private-markdown'

describe('private markdown', () => {
  it('never mounts remote images, HTML, iframes or code execution controls', () => {
    const html = renderToStaticMarkup(createElement(PrivateMarkdown, {
      text: '![tracker](https://tracker.example/secret)\n\n<script>alert(1)</script>\n\n<iframe src="https://tracker.example"></iframe>\n\n```html\n<img src="https://tracker.example">\n```',
    }))
    expect(html).not.toContain('<img')
    expect(html).not.toContain('<script')
    expect(html).not.toContain('<iframe')
    expect(html).not.toContain('<button')
    expect(html).toContain('&lt;script&gt;')
  })

  it('preserves safe formatting and uses referrer-free links', () => {
    const html = renderToStaticMarkup(createElement(PrivateMarkdown, { text: '**bold** [safe](https://example.com) [unsafe](javascript:alert(1))\n\n- list item' }))
    expect(html).toContain('<strong>bold</strong>')
    expect(html).toContain('rel="noreferrer noopener"')
    expect(html).not.toContain('href="javascript:')
    expect(html).toContain('<li>')
  })
})
