import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ReasoningItem } from '@/types/chat'
import { ReasoningTrace } from '@/components/chat/reasoning-trace'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string; count?: number; duration?: string }) => {
      if (key === 'reasoning.durationSeconds') return `${options?.count} seconds`
      if (key === 'reasoning.thoughtFor') return `Thought for ${options?.duration}`
      return options?.defaultValue ?? key
    },
  }),
}))

describe('ReasoningTrace tool layout', () => {
  it('removes a collapsed long trace from layout instead of clipping it in a zero-row grid', () => {
    const reasoning: ReasoningItem[] = [
      {
        kind: 'thinking',
        id: 'long-thought',
        text: 'A long reasoning paragraph.\n\n'.repeat(200),
      },
    ]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, streaming: false, settled: true }),
    )

    expect(html).toContain('aria-expanded="false"')
    expect(html).toMatch(/aria-controls="([^"]+)"[^>]*>[\s\S]*<div id="\1" hidden="">/)
    expect(html).not.toContain('grid-rows-[0fr]')
  })

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

  it('uses a generic tool label when persisted data has no tool name', () => {
    const reasoning: ReasoningItem[] = [
      {
        kind: 'tool',
        id: 'step-missing-name',
        tool: {
          id: 'tool-missing-name',
          name: undefined as unknown as string,
          label: '',
          status: 'error',
          startedAt: Date.now(),
        },
      },
    ]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, streaming: false, settled: true }),
    )

    expect(html).toContain('>Tool<')
    expect(html).not.toContain('tools.undefined')
  })

  it('shows the persisted thought duration with a neutral lightbulb icon', () => {
    const reasoning: ReasoningItem[] = [{ kind: 'thinking', id: 'thought-1', text: 'I considered the options.' }]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, thinkingMs: 3200, streaming: false, settled: true }),
    )

    expect(html).toContain('Thought for 3 seconds')
    expect(html).toContain('lucide-lightbulb')
    expect(html).toContain('text-[var(--color-fg-muted)]')
    expect(html).not.toContain('text-[var(--color-secondary)]')
    expect(html).not.toContain('thinking-shimmer')
  })

  it('renders a section title glued after a sentence as its own thought paragraph', () => {
    const reasoning: ReasoningItem[] = [
      {
        kind: 'thinking',
        id: 'thought-title-boundary',
        text: 'The prior thought ends at the lower right.**Updating hardware visuals**\n\nThe next thought begins.',
      },
    ]

    const html = renderToStaticMarkup(
      createElement(ReasoningTrace, { reasoning, streaming: false, settled: true }),
    )

    expect(html).toMatch(/lower right\.<\/p><p[^>]*><strong>Updating hardware visuals<\/strong><\/p>/)
  })
})
