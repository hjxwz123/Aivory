import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ImageGenerating } from '@/components/chat/image-generating'
import { MessageRow } from '@/components/chat/message-row'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useModels } from '@/store/models'
import type { ApiModel } from '@/api/types'
import type { Message } from '@/types/chat'

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string; duration?: string }) =>
      key === 'image.elapsed' ? `Elapsed time ${options?.duration}` : options?.defaultValue ?? key,
    i18n: { language: 'en' },
  }),
}))

const imageModel: ApiModel = {
  id: 'image-model', channel_id: 'channel', kind: 'image', request_id: 'gpt-image-1',
  label: 'GPT Image', description: '', icon: '', enabled: true, sort_order: 0,
  tool_mode: 'none', vision: true, stream: false, system_prompt: '', param_controls: [],
  price_input: 0, price_output: 0, price_cache_read: 0, price_cache_write: 0, price_per_image: 0, currency: 'USD', dim: 0, updated_at: 0,
}

afterEach(() => {
  vi.useRealTimers()
  useModels.setState({ imageModels: [] })
})

function renderMessage(overrides: Partial<Message> = {}) {
  useModels.setState({ imageModels: [imageModel] })
  const message: Message = {
    id: 'drawing-turn', role: 'assistant', content: '', createdAt: Date.now(),
    streaming: true, modelId: imageModel.id, ...overrides,
  }
  return renderToStaticMarkup(createElement(MemoryRouter, null,
    createElement(TooltipProvider, null, createElement(MessageRow, { message })),
  ))
}

describe('ImageGenerating', () => {
  it.each([
    ['preparing', 'Preparing your image…'],
    ['optimizing', 'Refining your prompt…'],
    ['generating', 'Painting your image…'],
  ] as const)('announces the real %s phase without a fabricated percentage', (phase, label) => {
    const html = renderToStaticMarkup(createElement(ImageGenerating, { phase }))
    expect(html).toContain(`data-image-generating="${phase}"`)
    expect(html).toContain(label)
    expect(html).toContain('role="status"')
    expect(html).toContain('aria-live="polite"')
    expect(html).not.toContain('aria-valuenow')
    expect(html).not.toContain('role="timer"')
  })

  it('shows actual elapsed time outside the live announcement', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-14T12:00:00Z'))
    const html = renderToStaticMarkup(createElement(ImageGenerating, {
      phase: 'generating', startedAt: Date.now() - 65_000,
    }))
    expect(html).toContain('aria-label="Elapsed time 01:05"')
    expect(html).toContain('role="timer" aria-live="off"')
    expect(html).toContain('01:05')
  })

  it('handles invalid and future timestamps without a negative timer', () => {
    for (const startedAt of [0, NaN, Infinity]) {
      expect(renderToStaticMarkup(createElement(ImageGenerating, { phase: 'generating', startedAt }))).not.toContain('role="timer"')
    }
    expect(renderToStaticMarkup(createElement(ImageGenerating, { phase: 'generating', startedAt: Date.now() + 60_000 }))).toContain('00:00')
  })
})

describe('MessageRow image generation lifecycle', () => {
  it('shows the drawing placeholder immediately for an image-model turn', () => {
    const html = renderMessage()
    expect(html).toContain('data-image-generating="preparing"')
    expect(html).not.toContain('thinking-shimmer')
  })

  it('uses server phases for both direct image models and image tool calls', () => {
    expect(renderMessage({ imageStatus: 'optimizing' })).toContain('data-image-generating="optimizing"')
    expect(renderMessage({ modelId: undefined, imageStatus: 'generating' })).toContain('data-image-generating="generating"')
  })

  it('keeps ordinary chat and fast-mode waiting states unchanged', () => {
    expect(renderMessage({ modelId: undefined })).not.toContain('data-image-generating')
    expect(renderMessage({ fast: true })).not.toContain('data-image-generating')
  })

  it('removes the placeholder when an image is delivered', () => {
    const html = renderMessage({
      imageStatus: 'generating',
      artifacts: [{ id: 'result', filename: 'result.png', mimeType: 'image/png', url: '/result.png', source: 'image_generate' }],
    })
    expect(html).not.toContain('data-image-generating')
    expect(html).toContain('alt="result.png"')
    expect(html).not.toContain('thinking-shimmer')
    expect(html).not.toContain('align-text-bottom')
  })

  it.each([
    { streaming: false },
    { streaming: false, stopped: true },
    { streaming: false, error: 'Provider unavailable' },
    { streaming: false, quotaExceeded: true },
    { streaming: false, moderation: true },
    { streaming: false, refused: true },
  ])('does not leave stale drawing activity on a settled turn: %j', (terminal) => {
    const html = renderMessage({ imageStatus: 'generating', ...terminal })
    expect(html).not.toContain('data-image-generating')
    expect(html).not.toContain('role="timer"')
  })
})
