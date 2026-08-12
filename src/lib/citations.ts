import type { Citation } from '@/types/chat'

const DOCUMENT_CITATION_PREFIX = 'doc://'
const KNOWLEDGE_BASE_CITATION_PREFIX = 'kbdoc://'

function hasCitationPrefix(url: string, prefix: string): boolean {
  return url.trim().toLowerCase().startsWith(prefix)
}

export function isDocumentCitation(citation: Pick<Citation, 'source' | 'url'>): boolean {
  return (
    citation.source === 'kb' ||
    citation.source === 'document' ||
    hasCitationPrefix(citation.url, DOCUMENT_CITATION_PREFIX) ||
    hasCitationPrefix(citation.url, KNOWLEDGE_BASE_CITATION_PREFIX)
  )
}

/**
 * Only citations emitted with the explicit KB URL scheme may use the new
 * authenticated preview and snippet UI. Legacy chat-upload citations were also
 * stored with source="kb", so source alone would change old no-KB conversations.
 */
export function isKnowledgeBaseCitation(
  citation: Pick<Citation, 'source' | 'url'>,
): boolean {
  return (
    citation.source === 'kb' &&
    hasCitationPrefix(citation.url, KNOWLEDGE_BASE_CITATION_PREFIX)
  )
}

export function documentIdFromCitationUrl(url: string): string | undefined {
  const trimmed = url.trim()
  const normalized = trimmed.toLowerCase()
  const prefix = normalized.startsWith(KNOWLEDGE_BASE_CITATION_PREFIX)
    ? KNOWLEDGE_BASE_CITATION_PREFIX
    : normalized.startsWith(DOCUMENT_CITATION_PREFIX)
      ? DOCUMENT_CITATION_PREFIX
      : undefined
  if (!prefix) return undefined

  const encodedID = trimmed.slice(prefix.length)
  if (!encodedID || /[/?#]/.test(encodedID)) return undefined

  try {
    const id = decodeURIComponent(encodedID).trim()
    return id && !/[/?#]/.test(id) ? id : undefined
  } catch {
    return undefined
  }
}

export function documentCitationContentUrl(
  citation: Pick<Citation, 'source' | 'url'>,
): string | undefined {
  if (!isKnowledgeBaseCitation(citation)) return undefined
  const documentID = documentIdFromCitationUrl(citation.url)
  return documentID ? `/api/documents/${encodeURIComponent(documentID)}/content` : undefined
}

export function boundedCitationSnippet(snippet?: string, maxCharacters = 280): string {
  const normalized = (snippet ?? '').replace(/\s+/g, ' ').trim()
  if (!normalized || maxCharacters <= 0) return ''

  const characters = Array.from(normalized)
  if (characters.length <= maxCharacters) return normalized
  if (maxCharacters <= 3) return '.'.repeat(maxCharacters)
  return `${characters.slice(0, maxCharacters - 3).join('').trimEnd()}...`
}

/**
 * Citation events can arrive out of order while a KB answer is streaming:
 * tool/web sources are emitted immediately, while KB sources are held until
 * the final answer identifies which ones it actually used. Keep the stored
 * event order untouched and normalize only the list presented to the user.
 */
export function citationsInDisplayOrder(citations: readonly Citation[]): Citation[] {
  return citations
    .map((citation, position) => ({ citation, position }))
    .sort((left, right) => {
      const leftIndex = Number.isFinite(left.citation.index)
        ? left.citation.index
        : Number.MAX_SAFE_INTEGER
      const rightIndex = Number.isFinite(right.citation.index)
        ? right.citation.index
        : Number.MAX_SAFE_INTEGER
      return leftIndex - rightIndex || left.position - right.position
    })
    .map(({ citation }) => citation)
}
