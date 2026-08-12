import { describe, expect, it } from 'vitest'
import {
  boundedCitationSnippet,
  citationsInDisplayOrder,
  documentCitationContentUrl,
  documentIdFromCitationUrl,
  isDocumentCitation,
  isKnowledgeBaseCitation,
} from '@/lib/citations'
import { linkifyCitations } from '@/lib/markdown'

describe('knowledge-base citations', () => {
  it('turns a doc citation into the authenticated content endpoint', () => {
    expect(documentIdFromCitationUrl('doc://doc_123')).toBe('doc_123')
    expect(documentIdFromCitationUrl('kbdoc://doc_123')).toBe('doc_123')
    expect(documentCitationContentUrl({ source: 'kb', url: 'kbdoc://report%20one' })).toBe(
      '/api/documents/report%20one/content',
    )
    expect(
      documentCitationContentUrl({ source: 'document', url: 'doc://report%20one' }),
    ).toBeUndefined()
  })

  it('rejects malformed document URLs instead of constructing an unsafe path', () => {
    expect(documentIdFromCitationUrl('https://example.com/document')).toBeUndefined()
    expect(documentIdFromCitationUrl('doc://')).toBeUndefined()
    expect(documentIdFromCitationUrl('doc://doc_123?download=1')).toBeUndefined()
    expect(documentIdFromCitationUrl('doc://folder%2Fdoc_123')).toBeUndefined()
    expect(documentIdFromCitationUrl('doc://%E0%A4%A')).toBeUndefined()
  })

  it('recognizes explicit KB URLs without changing legacy chat citations', () => {
    expect(isDocumentCitation({ source: 'kb', url: '' })).toBe(true)
    expect(isDocumentCitation({ source: 'document', url: '' })).toBe(true)
    expect(isDocumentCitation({ source: 'web', url: 'DOC://doc_123' })).toBe(true)
    expect(isDocumentCitation({ source: 'web', url: 'KBDOC://doc_123' })).toBe(true)
    expect(isDocumentCitation({ source: 'web', url: 'https://example.com' })).toBe(false)
    expect(isKnowledgeBaseCitation({ source: 'kb', url: 'kbdoc://doc_123' })).toBe(true)
    expect(isKnowledgeBaseCitation({ source: 'kb', url: 'doc://legacy-chat-upload' })).toBe(false)
    expect(isKnowledgeBaseCitation({ source: 'document', url: 'kbdoc://doc_123' })).toBe(false)
  })

  it('normalizes and bounds snippets before rendering them in the source list', () => {
    expect(boundedCitationSnippet('  first\n\nsecond   third  ')).toBe('first second third')
    expect(boundedCitationSnippet('1234567890', 8)).toBe('12345...')
  })

  it('orders mixed live sources by citation index without mutating event order', () => {
    const citations = [
      { id: 'web-5', index: 5, title: 'Web 5', url: 'https://example.com/5', domain: 'example.com' },
      { id: 'web-6', index: 6, title: 'Web 6', url: 'https://example.com/6', domain: 'example.com' },
      { id: 'kb-2', index: 2, title: 'KB 2', url: 'kbdoc://doc-2', domain: '', source: 'kb' as const },
    ]

    expect(citationsInDisplayOrder(citations).map((citation) => citation.index)).toEqual([2, 5, 6])
    expect(citations.map((citation) => citation.index)).toEqual([5, 6, 2])
  })

  it('renders an inline document citation as a button only when preview is available', () => {
    const openable = linkifyCitations('Supported [1].', [
      {
        index: 1,
        url: 'kbdoc://doc_123',
        title: 'Paper.pdf',
        domain: '',
        isDoc: true,
        documentID: 'doc_123',
      },
    ])
    const staticMarker = linkifyCitations('Supported [1].', [
      { index: 1, url: 'doc://doc_123', title: 'Paper.pdf', domain: '', isDoc: true },
    ])

    expect(openable).toContain('button type="button" data-doc-citation-index="1"')
    expect(staticMarker).not.toContain('<button')
  })
})
