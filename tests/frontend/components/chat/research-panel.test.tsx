import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import type { ResearchState } from '@/types/chat'
import { ResearchPanel } from '@/components/chat/research-panel'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { count?: number; round?: number; defaultValue?: string }) => {
      if (key === 'research.round') return `Round ${options?.round}`
      if (key === 'research.phase.reading') return `Reading ${options?.count} sources`
      return options?.defaultValue ?? key
    },
  }),
}))

const research: ResearchState = {
  title: 'Alpha study',
  tasks: [{ id: 'q1', question: 'What is alpha?', status: 'researching', round: 2 }],
  sources: [
    { id: 'src_1', url: 'https://a.org', title: 'Useful page', domain: 'a.org', status: 'kept', verdict: 'A' },
    { id: 'src_2', url: 'https://b.org', title: 'Login wall', domain: 'b.org', status: 'read', verdict: 'C' },
  ],
  notes: [{ id: 'note_1', round: 1, text: 'Alpha basics are established; beta lacks primary data.' }],
  phase: { name: 'reading', round: 2, count: 8 },
}

describe('ResearchPanel', () => {
  it('shows the live phase, the research log and off-topic sources while researching', () => {
    const html = renderToStaticMarkup(createElement(ResearchPanel, { research, streaming: true }))

    expect(html).toContain('Round 2 · Reading 8 sources')
    expect(html).toContain('research.notesLabel')
    expect(html).toContain('Round 1')
    expect(html).toContain('Alpha basics are established; beta lacks primary data.')
    expect(html).toMatch(/title="research\.offTopic"[^>]*class="[^"]*opacity-60/)
  })

  it('hides the phase once the turn has settled but keeps the persisted log', () => {
    const html = renderToStaticMarkup(createElement(ResearchPanel, { research, streaming: false, settled: true }))

    expect(html).not.toContain('Reading 8 sources')
    expect(html).toContain('Alpha basics are established; beta lacks primary data.')
  })

  it('ignores a phase name it does not know', () => {
    const html = renderToStaticMarkup(
      createElement(ResearchPanel, { research: { ...research, phase: { name: 'teleporting' } }, streaming: true }),
    )

    expect(html).not.toContain('research.phase.teleporting')
  })
})
