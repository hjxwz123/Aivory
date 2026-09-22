import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { DocumentPreview } from '@/components/files/document-preview'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
    i18n: { language: 'en' },
  }),
}))

function render(name: string, source: string): string {
  const data = new TextEncoder().encode(source).buffer
  return renderToStaticMarkup(createElement(DocumentPreview, { name, data }))
}

const HTML_SANDBOX = 'sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"'

/**
 * The user-facing requirement is "an .html file previews as a document". These
 * tests pin the difference between the rendered branch and the source branch,
 * because the source branch is exactly what used to happen.
 */
describe('DocumentPreview — HTML files', () => {
  it('renders markup as a sandboxed document instead of escaped source', () => {
    const html = render('page.html', '<h1>Hello</h1>')

    expect(html).toContain(HTML_SANDBOX)
    // The discriminator between the two branches is the element, not escaping:
    // the srcdoc attribute legitimately entity-escapes its markup either way.
    expect(html).toContain('<iframe')
    // react-dom/server keeps the camelCase `srcDoc` prop name in its markup.
    expect(html).toMatch(/srcdoc=/i)
    expect(html).not.toContain('<pre')
    expect(html).toContain('Hello')
  })

  it('treats .htm and .xhtml the same way', () => {
    for (const name of ['page.htm', 'doc.xhtml']) {
      expect(render(name, '<p>body</p>')).toContain(HTML_SANDBOX)
    }
  })

  it('still shows non-markup source as plain text', () => {
    const html = render('index.ts', 'const answer = 42')

    expect(html).toContain('<pre')
    expect(html).toContain('const answer = 42')
    expect(html).not.toContain('<iframe')
    expect(html).not.toContain('sandbox=')
  })
})
