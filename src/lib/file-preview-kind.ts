export type DocumentPreviewKind =
  | 'image'
  | 'pdf'
  | 'text'
  | 'docx'
  | 'pptx'
  | 'xlsx'
  | 'unsupported'

export type FileTypeFilter =
  | 'all'
  | 'pdf'
  | 'document'
  | 'presentation'
  | 'spreadsheet'
  | 'image'
  | 'text'
  | 'other'

export const MAX_TEXT_PREVIEW_BYTES = 2 * 1024 * 1024
export const MAX_OFFICE_PREVIEW_BYTES = 20 * 1024 * 1024
export const MAX_PDF_PREVIEW_BYTES = 40 * 1024 * 1024

const PREVIEW_IMAGE_EXTENSIONS = new Set([
  'avif',
  'bmp',
  'gif',
  'ico',
  'jfif',
  'jpeg',
  'jpg',
  'png',
  'svg',
  'webp',
])

const IMAGE_EXTENSIONS = new Set([
  ...PREVIEW_IMAGE_EXTENSIONS,
  'apng',
  'cur',
  'heic',
  'heif',
  'jpe',
  'jxl',
  'psd',
  'tif',
  'tiff',
])

const TEXT_EXTENSIONS = new Set([
  'bash',
  'c',
  'cc',
  'cfg',
  'cjs',
  'clj',
  'conf',
  'cpp',
  'css',
  'cs',
  'cxx',
  'dart',
  'env',
  'erl',
  'ex',
  'exs',
  'fish',
  'fs',
  'gql',
  'go',
  'graphql',
  'h',
  'hs',
  'hpp',
  'htm',
  'html',
  'ini',
  'java',
  'js',
  'json',
  'jsonl',
  'jsx',
  'kt',
  'kts',
  'less',
  'log',
  'lua',
  'markdown',
  'md',
  'mjs',
  'pl',
  'php',
  'proto',
  'ps1',
  'properties',
  'py',
  'pyw',
  'r',
  'rb',
  'rs',
  'rst',
  'sass',
  'scala',
  'scss',
  'sh',
  'sql',
  'swift',
  'svelte',
  'bat',
  'tex',
  'toml',
  'ts',
  'tsx',
  'txt',
  'vue',
  'xml',
  'yaml',
  'yml',
  'zsh',
])

const DELIMITED_TEXT_EXTENSIONS = new Set(['csv', 'tsv'])
const DOCX_EXTENSIONS = new Set(['docx'])
const PPTX_EXTENSIONS = new Set(['pptx'])
const XLSX_EXTENSIONS = new Set(['xlsx', 'xlsm'])

const DOCUMENT_EXTENSIONS = new Set([
  ...DOCX_EXTENSIONS,
  'doc',
  'docm',
  'dot',
  'dotm',
  'dotx',
  'odt',
  'rtf',
])

const PRESENTATION_EXTENSIONS = new Set([
  ...PPTX_EXTENSIONS,
  'odp',
  'pot',
  'potm',
  'potx',
  'pps',
  'ppsm',
  'ppsx',
  'ppt',
  'pptm',
])

const SPREADSHEET_EXTENSIONS = new Set([
  ...XLSX_EXTENSIONS,
  ...DELIMITED_TEXT_EXTENSIONS,
  'ods',
  'xls',
  'xlsb',
  'xlt',
  'xltm',
  'xltx',
])

const DOCX_MIME_TYPES = new Set([
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
])

const DOCUMENT_MIME_TYPES = new Set([
  ...DOCX_MIME_TYPES,
  'application/msword',
  'application/rtf',
  'application/vnd.ms-word.document.macroenabled.12',
  'application/vnd.ms-word.template.macroenabled.12',
  'application/vnd.oasis.opendocument.text',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.template',
  'text/rtf',
])

const PPTX_MIME_TYPES = new Set([
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
])

const PRESENTATION_MIME_TYPES = new Set([
  ...PPTX_MIME_TYPES,
  'application/vnd.ms-powerpoint',
  'application/vnd.ms-powerpoint.presentation.macroenabled.12',
  'application/vnd.ms-powerpoint.slideshow.macroenabled.12',
  'application/vnd.ms-powerpoint.template.macroenabled.12',
  'application/vnd.oasis.opendocument.presentation',
  'application/vnd.openxmlformats-officedocument.presentationml.slideshow',
  'application/vnd.openxmlformats-officedocument.presentationml.template',
])

const XLSX_MIME_TYPES = new Set([
  'application/vnd.ms-excel.sheet.macroenabled.12',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
])

const SPREADSHEET_MIME_TYPES = new Set([
  ...XLSX_MIME_TYPES,
  'application/csv',
  'application/vnd.ms-excel',
  'application/vnd.ms-excel.sheet.binary.macroenabled.12',
  'application/vnd.ms-excel.template.macroenabled.12',
  'application/vnd.oasis.opendocument.spreadsheet',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.template',
  'text/csv',
  'text/tab-separated-values',
])

const TEXT_MIME_TYPES = new Set([
  'application/ecmascript',
  'application/javascript',
  'application/json',
  'application/ld+json',
  'application/sql',
  'application/toml',
  'application/x-httpd-php',
  'application/x-javascript',
  'application/x-sh',
  'application/x-yaml',
  'application/xhtml+xml',
  'application/xml',
])

function normalizedMime(mime?: string): string {
  return (mime ?? '').split(';', 1)[0]?.trim().toLowerCase() ?? ''
}

function normalizedBackendKind(backendKind?: string): string {
  return (backendKind ?? '').trim().toLowerCase()
}

export function extensionOf(name: string): string {
  const withoutQuery = name.trim().split(/[?#]/, 1)[0]?.replace(/\\/g, '/') ?? ''
  const basename = withoutQuery.slice(withoutQuery.lastIndexOf('/') + 1)
  const dot = basename.lastIndexOf('.')
  if (dot < 0 || dot === basename.length - 1) return ''
  return basename.slice(dot + 1).toLowerCase()
}

export function documentPreviewKind(
  name: string,
  mime?: string,
  backendKind?: string,
): DocumentPreviewKind {
  const extension = extensionOf(name)

  if (PREVIEW_IMAGE_EXTENSIONS.has(extension)) return 'image'
  if (extension === 'pdf') return 'pdf'
  if (DOCX_EXTENSIONS.has(extension)) return 'docx'
  if (PPTX_EXTENSIONS.has(extension)) return 'pptx'
  if (XLSX_EXTENSIONS.has(extension)) return 'xlsx'
  if (TEXT_EXTENSIONS.has(extension) || DELIMITED_TEXT_EXTENSIONS.has(extension)) return 'text'
  if (
    IMAGE_EXTENSIONS.has(extension) ||
    DOCUMENT_EXTENSIONS.has(extension) ||
    PRESENTATION_EXTENSIONS.has(extension) ||
    SPREADSHEET_EXTENSIONS.has(extension)
  ) {
    return 'unsupported'
  }

  const normalized = normalizedMime(mime)
  if (normalized.startsWith('image/')) return 'image'
  if (normalized === 'application/pdf') return 'pdf'
  if (DOCX_MIME_TYPES.has(normalized)) return 'docx'
  if (PPTX_MIME_TYPES.has(normalized)) return 'pptx'
  if (XLSX_MIME_TYPES.has(normalized)) return 'xlsx'
  if (
    normalized.startsWith('text/') ||
    TEXT_MIME_TYPES.has(normalized) ||
    normalized === 'application/csv'
  ) {
    return 'text'
  }
  if (
    DOCUMENT_MIME_TYPES.has(normalized) ||
    PRESENTATION_MIME_TYPES.has(normalized) ||
    SPREADSHEET_MIME_TYPES.has(normalized)
  ) {
    return 'unsupported'
  }

  switch (normalizedBackendKind(backendKind)) {
    case 'image':
      return 'image'
    case 'pdf':
      return 'pdf'
    case 'docx':
      return 'docx'
    case 'pptx':
      return 'pptx'
    case 'xlsm':
    case 'xlsx':
      return 'xlsx'
    case 'csv':
    case 'tsv':
    case 'code':
    case 'text':
      return 'text'
    default:
      return 'unsupported'
  }
}

export function fileTypeFilterFor(
  name: string,
  mime?: string,
  backendKind?: string,
): Exclude<FileTypeFilter, 'all'> {
  const extension = extensionOf(name)

  if (extension === 'pdf') return 'pdf'
  if (DOCUMENT_EXTENSIONS.has(extension)) return 'document'
  if (PRESENTATION_EXTENSIONS.has(extension)) return 'presentation'
  if (SPREADSHEET_EXTENSIONS.has(extension)) return 'spreadsheet'
  if (IMAGE_EXTENSIONS.has(extension)) return 'image'
  if (TEXT_EXTENSIONS.has(extension)) return 'text'

  const normalized = normalizedMime(mime)
  if (normalized === 'application/pdf') return 'pdf'
  if (DOCUMENT_MIME_TYPES.has(normalized)) return 'document'
  if (PRESENTATION_MIME_TYPES.has(normalized)) return 'presentation'
  if (SPREADSHEET_MIME_TYPES.has(normalized)) return 'spreadsheet'
  if (normalized.startsWith('image/')) return 'image'
  if (normalized.startsWith('text/') || TEXT_MIME_TYPES.has(normalized)) return 'text'

  switch (normalizedBackendKind(backendKind)) {
    case 'pdf':
      return 'pdf'
    case 'doc':
    case 'docx':
    case 'document':
      return 'document'
    case 'ppt':
    case 'pptx':
    case 'presentation':
      return 'presentation'
    case 'csv':
    case 'tsv':
    case 'xls':
    case 'xlsm':
    case 'xlsx':
    case 'sheet':
    case 'spreadsheet':
      return 'spreadsheet'
    case 'image':
      return 'image'
    case 'code':
    case 'text':
      return 'text'
    default:
      return 'other'
  }
}

export function isPreviewSupported(name: string, mime?: string, backendKind?: string): boolean {
  return documentPreviewKind(name, mime, backendKind) !== 'unsupported'
}

/**
 * Returns the largest payload the in-app renderer will parse. `null` means the
 * format has no additional preview cap beyond the server upload policy, while
 * `0` means it is download-only. Callers can use this before fetching bytes so
 * an automatically selected file does not download content that cannot render.
 */
export function documentPreviewByteLimit(kind: DocumentPreviewKind): number | null {
  switch (kind) {
    case 'text':
      return MAX_TEXT_PREVIEW_BYTES
    case 'docx':
    case 'pptx':
    case 'xlsx':
      return MAX_OFFICE_PREVIEW_BYTES
    case 'pdf':
      return MAX_PDF_PREVIEW_BYTES
    case 'unsupported':
      return 0
    default:
      return null
  }
}
