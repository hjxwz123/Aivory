import { createElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { PrivateMessageRow, type PrivateDisplayMessage } from '@/components/chat/private-message-row'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}))

vi.mock('@/hooks/use-media-query', () => ({
  useMediaQuery: () => false,
}))

// Radix Tooltip needs a TooltipProvider the global app shell supplies.
vi.mock('@/components/ui/tooltip', () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}))

const base = { createdAt: 1_750_000_000_000 }

describe('private message row', () => {
  it('renders a right-aligned user bubble with in-memory images and edit affordances', () => {
    const message: PrivateDisplayMessage = {
      ...base,
      id: 1,
      role: 'user',
      text: 'look at this',
      images: [{ data: 'aGVsbG8=', mime_type: 'image/png' }],
    }
    const html = renderToStaticMarkup(createElement(PrivateMessageRow, {
      message,
      onEdit: () => undefined,
    }))
    expect(html).toContain('justify-end')
    expect(html).toContain('--color-user-bubble')
    expect(html).toContain('src="data:image/png;base64,aGVsbG8="')
    expect(html).toContain('look at this')
    expect(html).toContain('aria-label="actions.edit"')
  })

  it('renders assistant replies through the private renderer without remote images', () => {
    const message: PrivateDisplayMessage = {
      ...base,
      id: 2,
      role: 'assistant',
      text: 'answer ![tracker](https://tracker.example/secret)',
      reasoning: 'thinking text',
      generatedImages: ['data:image/png;base64,AAAA'],
    }
    const html = renderToStaticMarkup(createElement(PrivateMessageRow, {
      message,
      model: { id: 'm', label: 'Test model', icon: 'sparkles' } as never,
      isLastAssistant: true,
      onRegenerate: () => undefined,
    }))
    expect(html).toContain('Test model')
    expect(html).toContain('[tracker]')
    expect(html).not.toContain('tracker.example')
    expect(html).toContain('data:image/png;base64,AAAA')
    expect(html).toContain('aria-label="actions.regenerate"')
    expect(html).toContain('aria-label="actions.copy"')
  })

  it('shows the in-row error card with the localized private error code', () => {
    const message: PrivateDisplayMessage = {
      ...base,
      id: 3,
      role: 'assistant',
      text: 'partial',
      error: 'private_quota_exceeded',
    }
    const html = renderToStaticMarkup(createElement(PrivateMessageRow, {
      message,
      isLastAssistant: true,
      onRegenerate: () => undefined,
    }))
    expect(html).toContain('role="alert"')
    expect(html).toContain('private.errors.private_quota_exceeded')
    expect(html).toContain('message.error.retry')
  })

  it('shows the stopped marker only for empty stopped turns', () => {
    const stopped: PrivateDisplayMessage = { ...base, id: 4, role: 'assistant', text: '', stopped: true }
    const html = renderToStaticMarkup(createElement(PrivateMessageRow, { message: stopped }))
    expect(html).toContain('message.stopped')
    expect(html).toContain('role="status"')
    const partial: PrivateDisplayMessage = { ...base, id: 5, role: 'assistant', text: 'some answer', stopped: true }
    expect(renderToStaticMarkup(createElement(PrivateMessageRow, { message: partial }))).not.toContain('message.stopped')
  })
})
