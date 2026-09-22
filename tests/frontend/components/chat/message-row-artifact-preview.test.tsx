/**
 * Generated files (sandbox outputs, rendered HTML, exported Office documents)
 * are delivered as artifact cards. Those cards used to offer a single download
 * link, so seeing what the model produced meant round-tripping through the
 * browser's download folder. These tests pin the delivery contract the card now
 * has: download stays, and a preview control opens the SAME bytes in the shared
 * right-edge Artifact panel (the app's existing preview implementation) instead
 * of a new tab.
 *
 * Static-markup assertion matches the sibling suites in this directory; the
 * panel's own rendering of these bytes is covered by `document-preview.test.tsx`
 * and `tests/browser/run-panel.mjs`.
 */
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { MessageRow } from '@/components/chat/message-row'
import { TooltipProvider } from '@/components/ui/tooltip'
import { documentPreviewByteLimit, documentPreviewKind } from '@/lib/file-preview-kind'
import { artifactBackendKind } from '@/lib/artifact-preview-kind'
import type { ArtifactRef, Message } from '@/types/chat'

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
    i18n: { language: 'en', resolvedLanguage: 'en' },
  }),
}))

const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'

const PPTX_ARTIFACT: ArtifactRef = {
  id: 'artifact-deck',
  filename: '季度汇报.pptx',
  url: '/api/artifacts/artifact-deck',
  mimeType: PPTX_MIME,
}

const HTML_ARTIFACT: ArtifactRef = {
  id: 'artifact-site',
  filename: 'index.html',
  url: '/api/artifacts/artifact-site',
  mimeType: 'text/html',
}

function messageWithArtifacts(...artifacts: ArtifactRef[]): Message {
  return {
    id: 'message-artifact',
    role: 'assistant',
    content: '已生成文件。',
    createdAt: 1,
    artifacts,
  }
}

function renderRow(message: Message): string {
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(
        TooltipProvider,
        null,
        createElement(MessageRow, { message }),
      ),
    ),
  )
}

function artifactCard(html: string, filename: string): string {
  const cards = html.split('<div class="inline-flex max-w-full items-center gap-1.5')
  return cards.find((card) => card.includes(filename)) ?? ''
}

describe('MessageRow generated-file delivery card', () => {
  it('offers download and preview side by side', () => {
    const html = renderRow(messageWithArtifacts(PPTX_ARTIFACT))
    const card = artifactCard(html, PPTX_ARTIFACT.filename)

    expect(card).toContain(PPTX_ARTIFACT.filename)
    // Download: still the same bytes and the same saved filename as before.
    expect(card).toContain('download="季度汇报.pptx"')
    expect(card).toContain(`href="${PPTX_ARTIFACT.url}"`)
    // Preview: a real control beside it, not the whole card acting as a link.
    expect(card).toContain('data-artifact-preview="true"')
    expect(card).toContain('aria-label="Preview file"')
  })

  it('renders one card per generated file', () => {
    const html = renderRow(messageWithArtifacts(PPTX_ARTIFACT, HTML_ARTIFACT))

    expect(artifactCard(html, PPTX_ARTIFACT.filename)).toContain('data-artifact-preview="true"')
    expect(artifactCard(html, HTML_ARTIFACT.filename)).toContain('data-artifact-preview="true"')
    expect(html.match(/data-artifact-preview/g)).toHaveLength(2)
  })

  it('keeps an unsafe artifact URL non-interactive', () => {
    const html = renderRow(
      messageWithArtifacts({ ...PPTX_ARTIFACT, id: 'artifact-unsafe', url: 'javascript:alert(1)' }),
    )
    const card = artifactCard(html, PPTX_ARTIFACT.filename)

    // The name stays as a historical reference, but neither action can fire on
    // a scheme the client refuses to hand to the browser.
    expect(card).toContain(PPTX_ARTIFACT.filename)
    expect(card).not.toContain('data-artifact-preview')
    expect(card).not.toContain('download=')
    expect(card.toLowerCase()).not.toContain('javascript:')
  })

  it('still delivers images through the lightbox, not the file panel', () => {
    const html = renderRow(
      messageWithArtifacts({ ...PPTX_ARTIFACT, id: 'artifact-png', filename: 'chart.png', mimeType: 'image/png' }),
    )

    expect(html).toContain('aria-label="View image"')
    expect(html).not.toContain('data-artifact-preview')
  })
})

describe('previewing a generated artifact', () => {
  it('narrows an artifact MIME type into the token the preview kind reader wants', () => {
    // `documentPreviewKind` reads backend kinds as short tokens, not MIME
    // strings — handing it the raw `a.mimeType` would be a silent no-op.
    expect(artifactBackendKind(PPTX_ARTIFACT)).toBe('pptx')
    expect(artifactBackendKind({ mimeType: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' })).toBe('docx')
    expect(artifactBackendKind({ mimeType: 'application/pdf' })).toBe('pdf')
    expect(artifactBackendKind({ mimeType: 'image/webp' })).toBe('image')
    expect(artifactBackendKind({ mimeType: 'text/csv' })).toBe('csv')
    expect(artifactBackendKind({ mimeType: 'text/html; charset=utf-8' })).toBe('code')
    // Nothing to say → no backend kind, and the extension/MIME path decides.
    expect(artifactBackendKind({ mimeType: '' })).toBe('')
    expect(artifactBackendKind({ mimeType: 'application/octet-stream' })).toBe('')
  })

  it('resolves the artifact MIME type to a real renderer, so the panel previews it', () => {
    // The card hands the panel the narrowed backend kind because an artifact has
    // no upload `kind`. This is the contract that makes the clicked control open
    // a rendered document rather than "unsupported" — including for a stored
    // WebP whose name lost its extension and whose Content-Type is generic.
    expect(documentPreviewKind(PPTX_ARTIFACT.filename, PPTX_MIME, artifactBackendKind(PPTX_ARTIFACT))).toBe('pptx')
    expect(documentPreviewByteLimit('pptx')).toBeGreaterThan(0)

    // A sandbox-written HTML page previews live in the opaque-origin frame.
    expect(documentPreviewKind(HTML_ARTIFACT.filename, HTML_ARTIFACT.mimeType, artifactBackendKind(HTML_ARTIFACT))).toBe('html')
    expect(documentPreviewByteLimit('html')).toBeGreaterThan(0)

    const storedWebp = { mimeType: 'image/webp' }
    expect(documentPreviewKind('generated-3', 'application/octet-stream', artifactBackendKind(storedWebp))).toBe('image')
  })
})
