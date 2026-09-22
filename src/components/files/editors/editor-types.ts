import type { MutableRefObject } from 'react'
import type { DocumentPreviewKind } from '@/lib/file-preview-kind'

/**
 * Editors that can be mounted inside the artifact panel. Each identifier is also
 * a lazy-load boundary, so an editor's parser never enters the route chunk of a
 * user who only reads documents.
 */
export type EditorId = 'code' | 'sheet' | 'docx' | 'pptx'

/**
 * What an editor reports to the panel.
 *
 * `bytes` is always the exact payload to persist — the editor owns its own
 * serialization (text encoding today, OOXML repacking in later phases).
 * `previewHtml` is present only when the edited content is markup the panel can
 * render live beside the editor.
 */
export interface EditedContent {
  bytes: Blob
  previewHtml?: string
}

export interface DocumentEditorProps {
  name: string
  mimeType?: string
  /** Bytes as loaded from the server — the editor's starting content. */
  data: ArrayBuffer
  /**
   * Called on every change. `null` means the content still matches the original,
   * which is what keeps "save a copy" honest about there being nothing to save.
   */
  onChange: (content: EditedContent | null) => void
  /**
   * Serializing a whole OOXML package is expensive, so the editors that do it
   * (sheet, docx, pptx) debounce and `onChange` therefore lags the last
   * keystroke. They register a flush here; the panel calls it before saving or
   * downloading, so editing and immediately saving persists the edit rather
   * than the package as it was on load.
   *
   * Synchronous editors (the code editor) leave this null.
   */
  flushRef?: MutableRefObject<(() => Promise<Blob | null>) | null>
}

/**
 * Which editor, if any, can open this file.
 *
 * Returns `null` for everything without an editor yet — the panel then simply
 * shows no edit affordance rather than a button that cannot work. `docx` and
 * `pptx` cases are deliberately absent until those editors exist.
 */
export function documentEditorFor(_name: string, kind: DocumentPreviewKind): EditorId | null {
  switch (kind) {
    case 'html':
    case 'text':
      return 'code'
    case 'xlsx':
      return 'sheet'
    case 'docx':
      return 'docx'
    case 'pptx':
      return 'pptx'
    default:
      return null
  }
}
