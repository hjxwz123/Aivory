import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import {
  SANDBOXED_HTML_FRAME_SANDBOX,
  SandboxedHtmlFrame,
} from '@/components/html/sandboxed-html-frame'

/**
 * SandboxedHtmlFrame is the single place in the app that renders untrusted
 * HTML, so its sandbox flags are a security boundary rather than a detail.
 * These assertions exist to fail loudly if a future change widens them.
 */
describe('SandboxedHtmlFrame', () => {
  it('never grants allow-same-origin, which would void the sandbox', () => {
    // With allow-scripts present, allow-same-origin lets the frame read our
    // cookies, storage, and DOM — i.e. there is no sandbox left at all.
    expect(SANDBOXED_HTML_FRAME_SANDBOX).not.toContain('allow-same-origin')
  })

  it('grants scripts and user-initiated popups only', () => {
    expect(SANDBOXED_HTML_FRAME_SANDBOX).toBe(
      'allow-scripts allow-popups allow-popups-to-escape-sandbox',
    )
    // These would let the page submit forms, throw native dialogs, or drop files.
    expect(SANDBOXED_HTML_FRAME_SANDBOX).not.toContain('allow-forms')
    expect(SANDBOXED_HTML_FRAME_SANDBOX).not.toContain('allow-modals')
    expect(SANDBOXED_HTML_FRAME_SANDBOX).not.toContain('allow-downloads')
  })

  it('renders a no-referrer sandboxed frame wrapping the source', () => {
    const html = renderToStaticMarkup(
      createElement(SandboxedHtmlFrame, { doc: '<p>hi</p>', title: 'page.html' }),
    )

    expect(html).toContain(`sandbox="${SANDBOXED_HTML_FRAME_SANDBOX}"`)
    // react-dom/server keeps the camelCase prop name in its markup; only the
    // browser DOM lowercases it to `referrerpolicy`. Match either.
    expect(html).toMatch(/referrerpolicy="no-referrer"/i)
    expect(html).toContain('page.html')
    // The preview document's own hardening travels with the markup. The
    // `upgrade-insecure-requests` directive is intentionally absent here: it is
    // injected only on an HTTPS origin, and that branch is pinned by
    // tests/frontend/lib/html-preview-document{,-https}.test.ts.
    expect(html).not.toContain('upgrade-insecure-requests')
    expect(html).toContain('noopener')
    expect(html).toContain('hi')
  })
})
