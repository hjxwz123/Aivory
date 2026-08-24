import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { MessageRow } from '@/components/chat/message-row'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { Message } from '@/types/chat'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
    i18n: { language: 'en' },
  }),
}))

const knowledgeBaseMessage: Message = {
  id: 'message-kb',
  role: 'assistant',
  content: 'Grounded answer [1].',
  createdAt: 1,
  citations: [
    {
      id: 'citation-kb',
      index: 1,
      title: 'Knowledge.pdf',
      url: 'kbdoc://document-kb',
      domain: '',
      source: 'kb',
    },
  ],
}

function renderMessage(readOnly: boolean): string {
  return renderRow(knowledgeBaseMessage, readOnly)
}

function renderRow(message: Message, readOnly = false): string {
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(
        TooltipProvider,
        null,
        createElement(MessageRow, { message, readOnly }),
      ),
    ),
  )
}

describe('MessageRow knowledge-base citation preview', () => {
  it('keeps document preview available in the owning user conversation', () => {
    const html = renderMessage(false)

    expect(html).toContain('data-doc-citation-index="1"')
  })

  it('does not expose the user-scoped preview action in a read-only transcript', () => {
    const html = renderMessage(true)

    expect(html).not.toContain('data-doc-citation-index')
    expect(html).toContain('<sup class="cite-marker cite-doc" title="Knowledge.pdf">1</sup>')
  })
})

describe('MessageRow image attachment containment', () => {
  it('uses the message column as a definite width and reflows a three-image grid', () => {
    const html = renderRow({
      id: 'message-images',
      role: 'user',
      content: 'Use the third image as a hardware reference.',
      createdAt: 1,
      attachments: [
        { id: 'image-1', name: 'poster.png', kind: 'image', size: 1, previewUrl: '/api/files/image-1' },
        { id: 'image-2', name: 'website.png', kind: 'image', size: 1, previewUrl: '/api/files/image-2' },
        { id: 'image-3', name: 'hardware.png', kind: 'image', size: 1, previewUrl: '/api/files/image-3' },
      ],
    })

    expect(html).toContain('flex w-full min-w-0 flex-col [container-type:inline-size]')
    expect(html).toContain('w-fit min-w-0 max-w-full overflow-hidden')
    expect(html).toContain('data-image-attachment-grid="multiple"')
    expect(html).toContain('grid-cols-[repeat(auto-fit,minmax(min(6rem,100%),1fr))]')
    expect(html).toContain('w-[min(22rem,calc(100cqw-2.125rem))]')
    expect(html).toContain('aspect-square w-full')
  })
})
