import { describe, expect, it } from 'vitest'
import { ragInjectionFromEvent } from '@/lib/rag-injection'

describe('RAG injection event mapping', () => {
  it('preserves a finite source count for localized rendering', () => {
    expect(
      ragInjectionFromEvent(
        { type: 'rag', status: 'found', summary: 'server-formatted text', source_count: 4 },
        123,
      ),
    ).toEqual({
      strategy: 'found',
      summary: 'server-formatted text',
      sourceCount: 4,
      at: 123,
    })
  })

  it('normalizes invalid counts without changing legacy summaries', () => {
    expect(
      ragInjectionFromEvent(
        { type: 'rag', status: 'full_doc', summary: 'Injected full document' },
        456,
      ),
    ).toEqual({
      strategy: 'full_doc',
      summary: 'Injected full document',
      sourceCount: undefined,
      at: 456,
    })
  })
})
