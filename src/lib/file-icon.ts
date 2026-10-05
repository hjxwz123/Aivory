import {
  File,
  FileText,
  FileType2,
  FileSpreadsheet,
  FileImage,
  FileCode,
  Presentation,
  type LucideIcon,
} from 'lucide-react'
import type { Attachment } from '@/types/chat'

/**
 * fileIconFor — maps an attachment to a lucide icon by file type. PDF, Word,
 * PowerPoint and Excel get a recognisable glyph; everything else falls back to
 * the generic file icon. Kept monochrome (callers colour with tokens) so the
 * one-accent rule holds.
 *
 * The backend `kind` lumps Office docs into "doc", so we look at the extension
 * to tell Word from PowerPoint.
 */
export function fileIconFor(name?: string, kind?: string): LucideIcon {
  const ext = extOf(name)

  // Extension first — it's the most specific signal.
  switch (ext) {
    case 'pdf':
      return FileText
    case 'doc':
    case 'docx':
    case 'rtf':
    case 'odt':
      return FileType2
    case 'ppt':
    case 'pptx':
    case 'odp':
      return Presentation
    case 'xls':
    case 'xlsx':
    case 'xlsm':
    case 'csv':
    case 'tsv':
    case 'ods':
      return FileSpreadsheet
  }

  switch (kind) {
    case 'pdf':
      return FileText
    case 'doc':
      return FileType2
    case 'sheet':
      return FileSpreadsheet
    case 'image':
      return FileImage
    case 'code':
      return FileCode
  }

  return File
}

/** Compact type label shared by composer and sent-message attachment cards. */
export function attachmentKindLabel(attachment: Pick<Attachment, 'kind' | 'name'>): string {
  const ext = extOf(attachment.name).toUpperCase()
  if (ext) return ext
  switch (attachment.kind) {
    case 'pdf':
      return 'PDF'
    case 'doc':
      return 'DOC'
    case 'sheet':
      return 'SHEET'
    case 'code':
      return 'CODE'
    case 'image':
      return 'IMAGE'
    default:
      return 'FILE'
  }
}

/** Semantic file-tile colour shared by composer and sent-message attachments. */
export function attachmentTileClass(attachment: Pick<Attachment, 'kind' | 'name'>): string {
  const ext = extOf(attachment.name)
  if (attachment.kind === 'pdf' || ext === 'pdf') {
    return 'bg-[var(--color-danger)] text-[var(--color-fg-inverted)]'
  }
  if (attachment.kind === 'sheet' || ['xls', 'xlsx', 'csv', 'tsv'].includes(ext)) {
    return 'bg-[var(--color-success)] text-[var(--color-fg-inverted)]'
  }
  if (attachment.kind === 'doc' || ['doc', 'docx', 'ppt', 'pptx'].includes(ext)) {
    return 'bg-[var(--color-info)] text-[var(--color-fg-inverted)]'
  }
  return 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
}

const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'heic', 'avif'])
const CODE_EXTENSIONS = new Set([
  'js', 'jsx', 'ts', 'tsx', 'py', 'go', 'rs', 'java', 'kt', 'swift', 'c', 'cc', 'cpp', 'h', 'hpp',
  'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css', 'vue',
])

/**
 * Soft tinted tile for file rows in browsing lists (Files, knowledge bases).
 * Same hue families as attachmentTileClass, but at "-soft" strength so a long
 * list stays calm while PDFs, sheets, slides and images remain scannable.
 */
export function fileTypeTileClass(name?: string, mimeType?: string): string {
  const ext = extOf(name)
  const mime = (mimeType ?? '').toLowerCase()
  if (ext === 'pdf' || mime === 'application/pdf') {
    return 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
  }
  if (['xls', 'xlsx', 'xlsm', 'csv', 'tsv', 'ods'].includes(ext)) {
    return 'bg-[var(--color-success-soft)] text-[var(--color-success)]'
  }
  if (['ppt', 'pptx', 'odp', 'key'].includes(ext)) {
    return 'bg-[var(--color-warning-soft)] text-[var(--color-warning)]'
  }
  if (['doc', 'docx', 'rtf', 'odt', 'pages'].includes(ext)) {
    return 'bg-[var(--color-info-soft)] text-[var(--color-info)]'
  }
  if (IMAGE_EXTENSIONS.has(ext) || mime.startsWith('image/')) {
    return 'bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
  }
  if (CODE_EXTENSIONS.has(ext)) {
    return 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
  }
  return 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]'
}

/** Icon for a browsing-list row: extension first, then the MIME family. */
export function fileTypeIcon(name?: string, mimeType?: string): LucideIcon {
  const ext = extOf(name)
  if (IMAGE_EXTENSIONS.has(ext) || (mimeType ?? '').toLowerCase().startsWith('image/')) return FileImage
  if (CODE_EXTENSIONS.has(ext)) return FileCode
  return fileIconFor(name)
}

function extOf(name?: string): string {
  if (!name) return ''
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1).toLowerCase() : ''
}
