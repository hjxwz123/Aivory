import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ReasoningItem } from '@/types/chat'
import { ReasoningTrace } from '@/components/chat/reasoning-trace'

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

  it('does not show a fabricated zero-second duration for a reloaded tool', () => {
    const reasoning: ReasoningItem[] = [
      {
        kind: 'tool',
        id: 'step-reloaded',
        tool: {
          id: 'tool-reloaded',
          name: 'python_execute',
          label: 'Running Python',
          status: 'error',
          startedAt: 1_700_000_000_000,
          endedAt: 1_700_000_000_000,
          timingKnown: false,
          output: 'Tool execution failed. Please try again.',
        },
      },
    ]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, streaming: false, settled: true }),
    )

    expect(html).toContain('Running Python')
    expect(html).not.toContain('>0s<')
    expect(html).toContain('grid-cols-[auto_auto_minmax(0,1fr)_auto]')
  })
})
