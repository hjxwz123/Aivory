import type { ArtifactRef } from '@/types/chat'

/**
 * Artifacts (tool-generated files — sandbox output, rendered HTML, exported
 * Office documents) carry no upload `kind`; the server tells us the MIME type it
 * stored instead. `documentPreviewKind` reads its `backendKind` argument as a
 * short token (`'pdf'`, `'docx'`, `'pptx'`, `'xlsx'`, `'csv'`, `'code'`,
 * `'image'`), so a MIME string has to be narrowed before it can widen the
 * preview kind: it is the last-resort signal for a file whose name has no
 * extension AND whose served Content-Type is inconclusive (`application/
 * octet-stream` for a stored WebP, for example).
 */
const ARTIFACT_MIME_KINDS: ReadonlyArray<readonly [string, string]> = [
  ['application/pdf', 'pdf'],
  ['application/vnd.openxmlformats-officedocument.wordprocessingml.document', 'docx'],
  ['application/vnd.openxmlformats-officedocument.presentationml.presentation', 'pptx'],
  ['application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'xlsx'],
  ['application/vnd.ms-excel.sheet.macroenabled.12', 'xlsm'],
  ['text/csv', 'csv'],
  ['text/tab-separated-values', 'tsv'],
]

export function artifactBackendKind(artifact: Pick<ArtifactRef, 'mimeType'>): string {
  const mime = (artifact.mimeType ?? '').split(';', 1)[0]?.trim().toLowerCase() ?? ''
  if (!mime) return ''
  if (mime.startsWith('image/')) return 'image'
  const known = ARTIFACT_MIME_KINDS.find(([candidate]) => candidate === mime)
  if (known) return known[1]
  if (mime.startsWith('text/') || mime === 'application/json' || mime === 'application/xml') return 'code'
  return ''
}
