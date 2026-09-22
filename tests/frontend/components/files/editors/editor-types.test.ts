import { describe, expect, it } from 'vitest'
import { documentEditorFor } from '@/components/files/editors/editor-types'
import { documentPreviewKind } from '@/lib/file-preview-kind'

function editorFor(name: string, kind?: string) {
  return documentEditorFor(name, documentPreviewKind(name, '', kind))
}

/**
 * The resolver decides whether the panel shows an edit affordance at all, so a
 * wrong answer here is a button that cannot work rather than a cosmetic issue.
 */
describe('documentEditorFor', () => {
  it('opens markup in the code editor', () => {
    for (const name of ['page.html', 'PAGE.HTM?raw=1', 'doc.xhtml']) {
      expect(editorFor(name)).toBe('code')
    }
  })

  it('opens text artifacts in the code editor', () => {
    for (const name of ['data.csv', 'DATA.TSV', 'notes.md', 'README.markdown', 'script.py', 'config.json']) {
      expect(editorFor(name)).toBe('code')
    }
  })

  it('opens spreadsheets in the sheet editor', () => {
    expect(editorFor('book.xlsx')).toBe('sheet')
    expect(editorFor('BOOK.XLSM')).toBe('sheet')
  })

  it('opens Word documents in the docx editor', () => {
    expect(editorFor('report.docx')).toBe('docx')
    expect(editorFor('', 'docx')).toBe('docx')
  })

  it('opens presentations in the pptx editor', () => {
    expect(editorFor('deck.pptx')).toBe('pptx')
  })

  it('offers no editor for formats whose editors do not exist yet', () => {
    // Keep this list honest: move an entry out only when its editor ships.
    for (const name of ['paper.pdf', 'photo.png', 'legacy.doc', 'legacy.ppt']) {
      expect(editorFor(name)).toBeNull()
    }
  })

  it('offers no editor for formats that cannot be previewed at all', () => {
    for (const name of ['legacy.doc', 'legacy.ppt', 'legacy.xls', 'scan.tiff']) {
      expect(editorFor(name)).toBeNull()
    }
  })

  it('follows the resolved preview kind, not the filename alone', () => {
    // A backend-declared kind can classify a file the extension cannot.
    expect(documentEditorFor('download', documentPreviewKind('download', 'text/html'))).toBe('code')
    expect(documentEditorFor('download', documentPreviewKind('download', 'application/pdf'))).toBeNull()
  })
})
