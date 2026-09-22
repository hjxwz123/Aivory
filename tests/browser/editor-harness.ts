/**
 * Dev/test-only mount harness for the four document editors.
 *
 * Exists because typecheck + pure-logic tests prove nothing about whether a
 * component actually MOUNTS: an AG Grid theme option that the installed major
 * rejects, a missing stylesheet, or an editor that throws on first render would
 * all pass tsc and the unit suite while leaving the feature unusable.
 *
 * `tests/browser/run-editors.mjs` drives this page in real headless Chrome.
 * Not reachable from index.html, so it never ships.
 */
import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import '@/i18n'
import '@/styles/globals.css'
import { DocumentEditor } from '@/components/files/editors/document-editor'
import type { EditedContent, EditorId } from '@/components/files/editors/editor-types'

interface HarnessState {
  phase: 'loading' | 'mounted' | 'error'
  error: string | null
  editor: string
  fixture: string
  /** Sizes/format of every payload the editor reported, in order. */
  edits: Array<{ bytes: number; magic: string }>
  /**
   * Base64 of the most recent payload, so the runner can write it to disk and
   * have openpyxl / python-pptx / python-docx re-open what the BROWSER produced.
   */
  lastBase64: string | null
}

declare global {
  interface Window {
    __HARNESS__: HarnessState
  }
}

const params = new URLSearchParams(window.location.search)
const editor = (params.get('editor') ?? 'code') as EditorId
const fixture = params.get('file') ?? 'sample.xlsx'
const filename = params.get('name') ?? fixture
/** Fixtures live under tests/fixtures/; OOXML round-trip samples in `ooxml`. */
const fixtureDir = params.get('dir') ?? 'ooxml'

window.__HARNESS__ = { phase: 'loading', error: null, editor, fixture, edits: [], lastBase64: null }

/** First two bytes identify a ZIP ("PK") versus text. */
function magicOf(bytes: Uint8Array): string {
  return Array.from(bytes.slice(0, 2), (byte) => String.fromCharCode(byte)).join('')
}

/** Chunked: a spread over a 100 KB array would blow the argument limit. */
function base64Of(bytes: Uint8Array): string {
  const CHUNK = 0x8000
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + CHUNK))
  }
  return btoa(binary)
}

async function main(): Promise<void> {
  const response = await fetch(`/tests/fixtures/${fixtureDir}/${fixture}`)
  if (!response.ok) throw new Error(`fixture ${fixture} -> HTTP ${response.status}`)
  const data = await response.arrayBuffer()

  // Expose the original bytes so the runner can prove an edit actually changed
  // the payload rather than re-emitting the input.
  Reflect.set(window, '__ORIGINAL__', data.byteLength)

  const container = document.getElementById('root')
  if (!container) throw new Error('#root missing')

  createRoot(container).render(
    createElement(DocumentEditor, {
      editor,
      name: filename,
      data,
      onChange: (content: EditedContent | null) => {
        if (!content) return
        void content.bytes.arrayBuffer().then((buffer) => {
          const bytes = new Uint8Array(buffer)
          window.__HARNESS__.edits.push({ bytes: bytes.length, magic: magicOf(bytes) })
          window.__HARNESS__.lastBase64 = base64Of(bytes)
        })
      },
    }),
  )

  window.__HARNESS__.phase = 'mounted'
}

main().catch((error: unknown) => {
  window.__HARNESS__.phase = 'error'
  window.__HARNESS__.error = error instanceof Error ? error.message : String(error)
})
