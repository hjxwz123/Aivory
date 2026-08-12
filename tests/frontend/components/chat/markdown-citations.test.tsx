import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { Markdown } from '@/components/chat/markdown'
import type { Citation } from '@/types/chat'

function renderHeadingCitation(citation: Citation): string {
  return renderToStaticMarkup(
    createElement(Markdown, {
      content: '# Evidence [1]',
      citations: [citation],
      onOpenDocumentCitation: vi.fn(),
    }),
  )
}

describe('Markdown document citations', () => {
  it('renders a KB citation in a heading as a preview button', () => {
    const html = renderHeadingCitation({
      id: 'cite-kb',
      index: 1,
      title: 'Knowledge.pdf',
      url: 'kbdoc://doc-kb',
      domain: '',
      source: 'kb',
    })

    expect(html).toContain('<h1')
    expect(html).toContain('button type="button" data-doc-citation-index="1"')
  })

  it('keeps a chat-uploaded document citation static even with the same doc URL', () => {
    const html = renderHeadingCitation({
      id: 'cite-document',
      index: 1,
      title: 'Upload.pdf',
      url: 'doc://doc-upload',
      domain: '',
      source: 'document',
    })

    expect(html).toContain('cite-marker cite-doc')
    expect(html).not.toContain('<button')
  })

  it('keeps legacy source=kb chat citations static without an explicit KB URL', () => {
    const html = renderHeadingCitation({
      id: 'cite-legacy-upload',
      index: 1,
      title: 'Legacy upload.pdf',
      url: 'doc://doc-legacy-upload',
      domain: '',
      source: 'kb',
    })

    expect(html).toContain('cite-marker cite-doc')
    expect(html).not.toContain('<button')
  })
})
