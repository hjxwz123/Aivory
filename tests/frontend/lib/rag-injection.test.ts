import { describe, expect, it } from 'vitest'
import { ragInjectionFromEvent, visibleRagInjection } from '@/lib/rag-injection'

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


describe('attachment retrieval progress visibility', () => {
  it.each(['document_searching', 'document_found', 'document_no_hit'])(
    'shows %s while waiting and clears it on text or stop', (strategy) => {
      const ragInjection = ragInjectionFromEvent({ type: 'rag', status: strategy }, 123)
      expect(visibleRagInjection({ ragInjection, streaming: true, content: '' })).toEqual(ragInjection)
      expect(visibleRagInjection({ ragInjection, streaming: true, content: 'Answer begins' })).toBeUndefined()
      expect(visibleRagInjection({ ragInjection, streaming: false, content: '' })).toBeUndefined()
    },
  )

  it('clears a skipped retrieval and preserves an explicit failure with the answer', () => {
    expect(visibleRagInjection({
      ragInjection: ragInjectionFromEvent({ type: 'rag', status: 'document_skipped' }, 123),
      streaming: true, content: '',
    })).toBeUndefined()
    const ragInjection = ragInjectionFromEvent({ type: 'rag', status: 'document_error' }, 123)
    expect(visibleRagInjection({ ragInjection, streaming: false, content: 'Answer' })).toEqual(ragInjection)
  })

  it('preserves existing knowledge-base statuses after text begins', () => {
    const ragInjection = ragInjectionFromEvent({ type: 'rag', status: 'partial', source_count: 2 }, 123)
    expect(visibleRagInjection({ ragInjection, streaming: true, content: 'Answer' })).toEqual(ragInjection)
  })
})
