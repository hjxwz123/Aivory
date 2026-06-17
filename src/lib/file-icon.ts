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

function extOf(name?: string): string {
  if (!name) return ''
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1).toLowerCase() : ''
}
