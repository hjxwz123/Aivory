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
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(
        TooltipProvider,
        null,
        createElement(MessageRow, { message: knowledgeBaseMessage, readOnly }),
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
