import type { TFunction } from 'i18next'
import { ApiError } from '@/api'

// Admin surfaces map the embedding-model guard 409s (server
// api/embedding_guard.go + errors.go) to localized copy instead of toasting the
// raw snake_case machine code. Both codes share the "why did this delete/edit
// fail" story: a live vector space depends on the row being touched.
const EMBEDDING_GUARD_MESSAGES: Record<string, string> = {
  embedding_model_locked: 'admin:documents.embeddingModelLockedError',
  embedding_model_in_use: 'admin:documents.embeddingModelInUseError',
}

/** Localized text for an embedding-guard ApiError, or '' when `error` carries
 *  a different code (caller keeps its own fallback). */
export function embeddingGuardErrorText(t: TFunction, error: unknown): string {
  if (!(error instanceof ApiError)) return ''
  const key = EMBEDDING_GUARD_MESSAGES[error.message]
  return key ? t(key) : ''
}
