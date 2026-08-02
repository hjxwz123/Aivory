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

function extOf(name?: string): string {
  if (!name) return ''
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1).toLowerCase() : ''
}
