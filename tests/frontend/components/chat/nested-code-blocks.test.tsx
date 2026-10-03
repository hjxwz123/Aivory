// @vitest-environment jsdom
import { createElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { Markdown } from '@/components/chat/markdown'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('@/store/theme', () => ({ useTheme: () => 'light' }))

function render(content: string) {
  return renderToStaticMarkup(createElement(Markdown, { content, blockKeyPrefix: 'message' }))
}

describe('nested code blocks', () => {
  it('adds wrapping to code in lists and blockquotes without execution or preview controls', () => {
    const html = render('- item\n\n  ```python\n  print("hello")\n  ```\n\n> quote\n>\n> ```html\n> <img src="https://tracker.example">\n> ```')
    expect(html.match(/data-code-body/g)).toHaveLength(2)
    expect(html.match(/aria-label="code.wrap"/g)).toHaveLength(2)
    expect(html).toContain('<blockquote')
    expect(html).not.toContain('aria-label="code.run"')
    expect(html).not.toContain('aria-label="code.preview"')
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('preserves ordered starts, task checks, inline formatting and math in nested containers', () => {
    const html = render('3. **third** with $x^2$\n4. fourth\n\n- [x] done\n- [ ] pending\n\n> **quoted** $y$')
    expect(html).toContain('<ol start="3"')
    expect(html).toContain('<strong>third</strong>')
    expect(html).toContain('type="checkbox"')
    expect(html).toContain('checked=""')
    expect(html).toContain('katex')
    expect(html).toContain('<strong>quoted</strong>')
  })

  it('keeps unsafe HTML and links inert in nested content', () => {
    const html = render('- [unsafe](javascript:alert(1))\n\n  <script>alert(1)</script>\n\n> <iframe src="https://tracker.example"></iframe>')
    expect(html).not.toContain('<script')
    expect(html).not.toContain('<iframe')
    expect(html).not.toContain('href="javascript:')
  })
})
