import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ReasoningItem } from '@/types/chat'
import { ReasoningTrace } from './reasoning-trace'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
  }),
}))

describe('ReasoningTrace tool layout', () => {
  it('contains long tool descriptions inside a wrapping, width-bounded row', () => {
    const description =
      'Precisely edit the terminal screenshot and preserve every other detail while replacing several timestamps without changing the background or typography.'
    const reasoning: ReasoningItem[] = [
      {
        kind: 'tool',
        id: 'step-1',
        tool: {
          id: 'tool-1',
          name: 'image_generate',
          label: 'Generating an image',
          input: { prompt: description },
          status: 'running',
          startedAt: Date.now(),
        },
      },
    ]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, streaming: true, settled: false }),
    )

    expect(html).toContain(description)
    expect(html).toContain(`title="${description}"`)
    expect(html).toContain('minmax(0,1fr)')
    expect(html).toContain('line-clamp-2')
    expect(html).toContain('overflow-wrap:anywhere')
  })
})
