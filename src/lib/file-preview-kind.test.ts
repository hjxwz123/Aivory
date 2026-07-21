import { describe, expect, it } from 'vitest'
import {
  documentPreviewByteLimit,
  documentPreviewKind,
  extensionOf,
  fileTypeFilterFor,
  isPreviewSupported,
  MAX_OFFICE_PREVIEW_BYTES,
  MAX_PDF_PREVIEW_BYTES,
  MAX_TEXT_PREVIEW_BYTES,
  type DocumentPreviewKind,
  type FileTypeFilter,
} from './file-preview-kind'

describe('extensionOf', () => {
  it('normalizes case and ignores paths, query strings, and fragments', () => {
    expect(extensionOf('/exports/Quarterly.Report.PDF?download=1#page=2')).toBe('pdf')
    expect(extensionOf('C:\\documents\\NOTES.MD')).toBe('md')
  })

  it('handles empty extensions and dotfiles', () => {
    expect(extensionOf('README')).toBe('')
    expect(extensionOf('archive.')).toBe('')
    expect(extensionOf('.env')).toBe('env')
  })
})

describe('documentPreviewKind', () => {
  it.each<[string, string | undefined, string | undefined, DocumentPreviewKind]>([
    ['paper.PDF', '', undefined, 'pdf'],
    ['download', 'application/pdf; charset=binary', undefined, 'pdf'],
    ['download', '', 'pdf', 'pdf'],
    ['proposal.DOCX?version=2', '', 'doc', 'docx'],
    ['', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', undefined, 'docx'],
    ['deck.PPTX', '', undefined, 'pptx'],
    ['', 'application/vnd.openxmlformats-officedocument.presentationml.presentation', undefined, 'pptx'],
    ['budget.XLSX', '', undefined, 'xlsx'],
    ['forecast.XLSM', '', undefined, 'xlsx'],
    ['', 'application/vnd.ms-excel.sheet.macroEnabled.12', undefined, 'xlsx'],
    ['photo.PNG', '', undefined, 'image'],
    ['asset', 'image/webp', undefined, 'image'],
    ['asset', '', 'image', 'image'],
    ['notes.TXT', '', undefined, 'text'],
    ['component.TSX?raw=1', '', undefined, 'text'],
    ['', 'application/json; charset=utf-8', undefined, 'text'],
    ['', '', 'code', 'text'],
    ['', '', 'docx', 'docx'],
    ['', '', 'pptx', 'pptx'],
    ['', '', 'xlsx', 'xlsx'],
    ['data.csv', 'text/csv', undefined, 'text'],
    ['data.TSV', '', 'sheet', 'text'],
  ])('classifies %s (%s, %s) as %s', (name, mime, backendKind, expected) => {
    expect(documentPreviewKind(name, mime, backendKind)).toBe(expected)
  })

  it.each([
    ['legacy.doc', 'application/msword'],
    ['legacy.PPT', 'application/vnd.ms-powerpoint'],
    ['legacy.xls', 'application/vnd.ms-excel'],
    ['scan.tiff', 'image/tiff'],
    ['archive.bin', 'application/octet-stream'],
  ])('does not claim native preview support for %s', (name, mime) => {
    expect(documentPreviewKind(name, mime)).toBe('unsupported')
    expect(isPreviewSupported(name, mime)).toBe(false)
  })
})

describe('fileTypeFilterFor', () => {
  it.each<[string, string | undefined, string | undefined, Exclude<FileTypeFilter, 'all'>]>([
    ['report.pdf', '', undefined, 'pdf'],
    ['contract.doc', '', undefined, 'document'],
    ['contract.docx', '', undefined, 'document'],
    ['slides.ppt', '', undefined, 'presentation'],
    ['slides.pptx', '', undefined, 'presentation'],
    ['ledger.xls', '', undefined, 'spreadsheet'],
    ['ledger.xlsx', '', undefined, 'spreadsheet'],
    ['ledger.xlsm', '', undefined, 'spreadsheet'],
    ['rows.csv', 'text/csv', undefined, 'spreadsheet'],
    ['rows.tsv', '', undefined, 'spreadsheet'],
    ['photo.jpeg', '', undefined, 'image'],
    ['script.py', '', undefined, 'text'],
    ['', 'text/plain; charset=utf-8', undefined, 'text'],
    ['', 'application/json; charset=utf-8', undefined, 'text'],
    ['', 'application/vnd.ms-powerpoint.template.macroEnabled.12', undefined, 'presentation'],
    ['', '', 'sheet', 'spreadsheet'],
    ['', '', 'docx', 'document'],
    ['', '', 'pptx', 'presentation'],
    ['', '', 'xlsx', 'spreadsheet'],
    ['archive.zip', 'application/zip', 'other', 'other'],
  ])('maps %s (%s, %s) to the %s filter', (name, mime, backendKind, expected) => {
    expect(fileTypeFilterFor(name, mime, backendKind)).toBe(expected)
  })

  it('keeps CSV and TSV previewable as text while grouping them with spreadsheets', () => {
    for (const name of ['data.csv', 'DATA.TSV?download=1']) {
      expect(documentPreviewKind(name, '')).toBe('text')
      expect(fileTypeFilterFor(name, '')).toBe('spreadsheet')
      expect(isPreviewSupported(name, '')).toBe(true)
    }
  })

  it('groups legacy Office files correctly without marking them previewable', () => {
    expect(fileTypeFilterFor('old.doc', '')).toBe('document')
    expect(fileTypeFilterFor('old.ppt', '')).toBe('presentation')
    expect(fileTypeFilterFor('old.xls', '')).toBe('spreadsheet')
    expect(isPreviewSupported('old.doc', '')).toBe(false)
    expect(isPreviewSupported('old.ppt', '')).toBe(false)
    expect(isPreviewSupported('old.xls', '')).toBe(false)
  })
})

describe('documentPreviewByteLimit', () => {
  it.each<[DocumentPreviewKind, number | null]>([
    ['text', MAX_TEXT_PREVIEW_BYTES],
    ['docx', MAX_OFFICE_PREVIEW_BYTES],
    ['pptx', MAX_OFFICE_PREVIEW_BYTES],
    ['xlsx', MAX_OFFICE_PREVIEW_BYTES],
    ['pdf', MAX_PDF_PREVIEW_BYTES],
    ['unsupported', 0],
    ['image', null],
  ])('returns the download-before-preview limit for %s', (kind, expected) => {
    expect(documentPreviewByteLimit(kind)).toBe(expected)
  })
})
